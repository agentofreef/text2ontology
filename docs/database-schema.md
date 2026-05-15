# Text2DAX Database Schema

## Overview

```
┌─────────────────────────────────────────────────────────────┐
│                      用户与项目层                             │
│  user  ←──┐                                                 │
│  project ←┼── project_id 贯穿所有核心表                      │
└───────────┼─────────────────────────────────────────────────┘
            │
┌───────────▼─────────────────────────────────────────────────┐
│                  模型元数据层（Semantic Layer 导入）           │
│  semantic_table       表级元数据                              │
│  column_explanation   列级元数据 + 向量 + 语义解释             │
│  measure_explanation  度量值元数据 + DAX表达式 + 向量          │
│  table_relationship   表间关系                                │
└───────────┬─────────────────────────────────────────────────┘
            │
┌───────────▼─────────────────────────────────────────────────┐
│                  知识层（业务语义 + RAG 检索源）               │
│  keyword_explanation  业务关键词 → 字段映射 + 向量             │
│  keyword_split        分词词典/规则（GIN 索引）               │
│  dax_example          QA pairs + 问题向量（few-shot 来源）    │
└───────────┬─────────────────────────────────────────────────┘
            │
┌───────────▼─────────────────────────────────────────────────┐
│                  运行层                                       │
│  prompt_config        System prompt + 业务背景（版本控制）     │
│  execution_history    执行记录（数据飞轮入口）                 │
└─────────────────────────────────────────────────────────────┘

数据飞轮: execution_history → 人工标注 mark=true → 回流到 dax_example / keyword 表
```

## 公共列约定

所有核心表（semantic_table, column_explanation, measure_explanation, table_relationship,
keyword_explanation, keyword_split, dax_example）都包含以下列：

```
project_id     UUID NOT NULL   → 项目隔离
mark           BOOLEAN DEFAULT FALSE  → true 才进入 prompt 组装
note           TEXT            → 标注备注
created_by     UUID            → FK user.id
updated_by     UUID            → FK user.id
created_at     TIMESTAMPTZ DEFAULT now()
updated_at     TIMESTAMPTZ DEFAULT now()
```

---

## 表定义

### 1. user — 用户表

```sql
CREATE TABLE "user" (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username        VARCHAR(100) UNIQUE NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,
    display_name    VARCHAR(100),
    role            VARCHAR(20) DEFAULT 'user',  -- admin / user
    is_active       BOOLEAN DEFAULT TRUE,
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);
```

### 2. project — 项目表（一个 Power BI 模型 = 一个 project）

```sql
CREATE TABLE project (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(200) NOT NULL,
    description     TEXT,
    owner_id        UUID NOT NULL REFERENCES "user"(id),
    -- 模型来源信息
    source_type     VARCHAR(50),          -- vpax / manual / api
    source_file     VARCHAR(500),         -- 导入的文件路径
    compatibility   INTEGER,              -- Power BI 兼容级别
    status          VARCHAR(20) DEFAULT 'active',  -- active / archived
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- 项目成员（多用户协作）
CREATE TABLE project_member (
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    role            VARCHAR(20) DEFAULT 'editor',  -- owner / editor / viewer
    PRIMARY KEY (project_id, user_id)
);
```

### 3. semantic_table — 表级元数据

```sql
CREATE TABLE semantic_table (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    table_name      VARCHAR(200) NOT NULL,
    table_type      VARCHAR(20) DEFAULT 'dimension', -- fact / dimension / bridge / calculated
    description     TEXT,
    row_count       BIGINT,
    is_hidden       BOOLEAN DEFAULT FALSE,
    -- 飞轮
    mark            BOOLEAN DEFAULT FALSE,
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),

    UNIQUE (project_id, table_name)
);
```

### 4. column_explanation — 列级元数据

```sql
CREATE TABLE column_explanation (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    table_id        UUID NOT NULL REFERENCES semantic_table(id) ON DELETE CASCADE,
    column_name     VARCHAR(200) NOT NULL,
    data_type       VARCHAR(50),          -- String / Integer / Decimal / DateTime / Boolean
    is_key          BOOLEAN DEFAULT FALSE,
    is_hidden       BOOLEAN DEFAULT FALSE,
    sort_order      INTEGER,
    -- 语义层
    column_explain  TEXT,                 -- 中文语义解释
    column_vector   vector(1024),         -- 语义向量（pgvector, bge-large-zh 维度）
    -- 飞轮
    mark            BOOLEAN DEFAULT FALSE,
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),

    UNIQUE (project_id, table_id, column_name)
);

CREATE INDEX idx_column_explain_vector ON column_explanation
    USING ivfflat (column_vector vector_cosine_ops) WITH (lists = 100);
```

### 5. measure_explanation — 度量值元数据

```sql
CREATE TABLE measure_explanation (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    table_id        UUID REFERENCES semantic_table(id) ON DELETE SET NULL,  -- 可选归属表
    measure_name    VARCHAR(200) NOT NULL,
    dax_expression  TEXT NOT NULL,        -- 度量值的 DAX 定义
    display_folder  VARCHAR(200),
    format_string   VARCHAR(100),         -- #,##0.00 / 0.00% 等
    is_hidden       BOOLEAN DEFAULT FALSE,
    -- 语义层
    measure_explain TEXT,                 -- 中文语义解释
    measure_vector  vector(1024),         -- 语义向量
    -- 依赖关系
    depends_on      TEXT[],               -- 引用的其他 measure/column 名称列表
    -- 飞轮
    mark            BOOLEAN DEFAULT FALSE,
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),

    UNIQUE (project_id, measure_name)
);

CREATE INDEX idx_measure_explain_vector ON measure_explanation
    USING ivfflat (measure_vector vector_cosine_ops) WITH (lists = 100);
```

### 6. table_relationship — 表间关系

```sql
CREATE TABLE table_relationship (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id              UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    from_table_id           UUID NOT NULL REFERENCES semantic_table(id) ON DELETE CASCADE,
    from_column             VARCHAR(200) NOT NULL,
    to_table_id             UUID NOT NULL REFERENCES semantic_table(id) ON DELETE CASCADE,
    to_column               VARCHAR(200) NOT NULL,
    cardinality             VARCHAR(20) NOT NULL,  -- one-to-many / many-to-one / one-to-one / many-to-many
    cross_filter_direction  VARCHAR(10) DEFAULT 'single',  -- single / both
    is_active               BOOLEAN DEFAULT TRUE,
    -- 飞轮
    mark                    BOOLEAN DEFAULT FALSE,
    note                    TEXT,
    created_by              UUID REFERENCES "user"(id),
    updated_by              UUID REFERENCES "user"(id),
    created_at              TIMESTAMPTZ DEFAULT now(),
    updated_at              TIMESTAMPTZ DEFAULT now()
);
```

### 7. keyword_explanation — 业务关键词

```sql
CREATE TABLE keyword_explanation (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    keyword         VARCHAR(200) NOT NULL,  -- 业务关键词（如"销售额"）
    synonyms        TEXT[],                 -- 同义词列表（如 {"营业额","收入","销售量"}）
    -- 映射目标
    mapped_table    VARCHAR(200),           -- 映射到的表名
    mapped_field    VARCHAR(200),           -- 映射到的列名或度量值名
    mapped_type     VARCHAR(20),            -- column / measure
    category        VARCHAR(50),            -- 度量值 / 维度 / 时间 / 筛选条件
    -- 语义层
    keyword_explain TEXT,                   -- 关键词的业务含义解释
    keyword_vector  vector(1024),           -- 语义向量
    -- 飞轮
    mark            BOOLEAN DEFAULT FALSE,
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),

    UNIQUE (project_id, keyword)
);

CREATE INDEX idx_keyword_vector ON keyword_explanation
    USING ivfflat (keyword_vector vector_cosine_ops) WITH (lists = 100);
CREATE INDEX idx_keyword_synonyms ON keyword_explanation USING GIN (synonyms);
```

### 8. keyword_split — 分词词典

```sql
CREATE TABLE keyword_split (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    original_text   TEXT NOT NULL,          -- 原始用户提问
    tokens          TEXT[] NOT NULL,        -- 分词结果列表
    token_labels    TEXT[],                 -- 每个 token 的类型标注（dimension/measure/filter/time/other）
    -- 飞轮
    mark            BOOLEAN DEFAULT FALSE,  -- true = 作为分词参考样本
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_keyword_split_tokens ON keyword_split USING GIN (tokens);
```

### 9. dax_example — DAX 示例（Few-shot 来源）

```sql
CREATE TABLE dax_example (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    user_question   TEXT NOT NULL,          -- 自然语言提问
    dax_query       TEXT NOT NULL,          -- 对应的 DAX 语句
    description     TEXT,                   -- 补充说明
    tags            TEXT[],                 -- 分类标签
    difficulty      VARCHAR(20),            -- simple / medium / complex
    -- 向量检索
    question_vector vector(1024),           -- 问题的语义向量（用于相似问题召回）
    -- 来源追溯
    source          VARCHAR(20) DEFAULT 'manual', -- manual / generated / imported
    source_id       UUID,                  -- 如果从 execution_history 回流，记录来源 ID
    -- 飞轮
    mark            BOOLEAN DEFAULT FALSE,
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_dax_example_question_vector ON dax_example
    USING ivfflat (question_vector vector_cosine_ops) WITH (lists = 100);
CREATE INDEX idx_dax_example_tags ON dax_example USING GIN (tags);
```

### 10. prompt_config — Prompt 模板管理（版本控制）

```sql
CREATE TABLE prompt_config (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    config_key      VARCHAR(50) NOT NULL,   -- system_instruction / business_context / output_format
    config_value    TEXT NOT NULL,           -- prompt 文本内容
    version         INTEGER NOT NULL DEFAULT 1,
    is_active       BOOLEAN DEFAULT FALSE,  -- 当前生效的版本
    -- 飞轮
    mark            BOOLEAN DEFAULT FALSE,
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),

    UNIQUE (project_id, config_key, version)
);

-- 确保每个 config_key 只有一个 is_active=true
CREATE UNIQUE INDEX idx_prompt_config_active
    ON prompt_config (project_id, config_key)
    WHERE is_active = TRUE;
```

### 11. execution_history — 执行历史（数据飞轮入口）

```sql
CREATE TABLE execution_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    -- 输入
    user_question   TEXT NOT NULL,
    -- 分词阶段
    split_tokens    TEXT[],                 -- LLM 分词结果
    -- 召回阶段
    recalled_keywords   JSONB,             -- 命中的 keyword_explanation IDs + scores
    recalled_examples   JSONB,             -- 命中的 dax_example IDs + scores
    recalled_columns    JSONB,             -- 命中的 column/measure IDs + scores
    -- 生成阶段
    assembled_prompt    TEXT,              -- 最终组装的完整 prompt（可选，调试用）
    generated_dax       TEXT,              -- LLM 生成的 DAX
    confidence          FLOAT,            -- 置信度
    model_name          VARCHAR(100),     -- 使用的 LLM 模型
    -- 执行阶段
    execution_status    VARCHAR(20),      -- success / error / timeout
    execution_result    TEXT,             -- 执行结果预览
    execution_error     TEXT,             -- 错误信息
    execution_duration  FLOAT,           -- 耗时（秒）
    -- 飞轮: 标记好的记录可回流到 dax_example
    mark                BOOLEAN DEFAULT FALSE,
    note                TEXT,
    created_by          UUID REFERENCES "user"(id),
    created_at          TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_exec_history_project ON execution_history (project_id, created_at DESC);
```

---

## 完整查询流程与表的关系

```
用户提问: "北京地区今年销售额同比增长多少"
    │
    ▼ ① LLM 分词
keyword_split (mark=true 的作为分词示例 few-shot)
    │
    ├→ tokens: ["北京", "地区", "今年", "销售额", "同比增长"]
    │
    ▼ ② 逐个 token 召回
    │
    ├─ "销售额" → keyword_explanation 正则匹配 + 向量余弦 + L1
    │              → 命中: mapped_field="Total Sales", mapped_type="measure"
    │
    ├─ "北京"   → keyword_explanation
    │              → 命中: mapped_field="City", mapped_table="Geography"
    │
    ├─ "同比增长" → keyword_explanation
    │              → 命中: mapped_field="Sales YoY%", mapped_type="measure"
    │
    ├─ "地区"   → column_explanation 向量匹配
    │              → 命中: column_name="Region", table="Geography"
    │
    ├─ "今年"   → keyword_explanation
    │              → 命中: category="时间", keyword_explain="当前自然年"
    │
    ▼ ③ 整句问题向量 → dax_example 语义匹配
dax_example (mark=true, question_vector 余弦相似)
    │
    ├→ Top-3 相似 QA pairs 作为 few-shot
    │
    ▼ ④ 组装 Prompt
    │
    ├─ prompt_config[system_instruction] (is_active=true)
    ├─ prompt_config[business_context]   (is_active=true)
    ├─ semantic_table + column_explanation + measure_explanation (mark=true, 召回相关的)
    ├─ keyword_explanation 命中结果（关键词+解释）
    ├─ dax_example Top-3（few-shot）
    ├─ user_question
    │
    ▼ ⑤ LLM 生成 DAX
    │
    ▼ ⑥ 全流程写入 execution_history
    │
    ▼ ⑦ 人工标注 mark=true → 回流到 dax_example (source='generated')
```

---

## 索引策略

| 表 | 索引类型 | 用途 |
|---|---|---|
| column_explanation.column_vector | IVFFlat (cosine) | 列语义检索 |
| measure_explanation.measure_vector | IVFFlat (cosine) | 度量值语义检索 |
| keyword_explanation.keyword_vector | IVFFlat (cosine) | 关键词语义检索 |
| keyword_explanation.synonyms | GIN | 同义词数组匹配 |
| dax_example.question_vector | IVFFlat (cosine) | 相似问题检索（few-shot） |
| dax_example.tags | GIN | 标签筛选 |
| keyword_split.tokens | GIN | 分词结果匹配 |
| 所有核心表.project_id | B-Tree | 项目隔离过滤 |
| 所有核心表.(project_id, mark) | B-Tree 联合 | 快速筛选生效数据 |

---

## ER 关系概要

```
user ──1:N──▶ project (owner)
user ──M:N──▶ project (via project_member)
project ──1:N──▶ semantic_table
project ──1:N──▶ measure_explanation
project ──1:N──▶ keyword_explanation
project ──1:N──▶ keyword_split
project ──1:N──▶ dax_example
project ──1:N──▶ prompt_config
project ──1:N──▶ execution_history
semantic_table ──1:N──▶ column_explanation
semantic_table ──1:N──▶ table_relationship (from/to)
execution_history ──0:1──▶ dax_example (回流 source_id)
```
