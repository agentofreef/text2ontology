# Text2DAX 架构解析：从 Prompt 反推系统设计

> 本文档从"LLM 生成一条 DAX 查询需要什么信息"这个问题出发，反推整个系统为什么要这样设计。适合给技术同僚做架构讲解。

---

## 一、最终送给 LLM 的 Prompt 长什么样？

```
[SYSTEM]
你是一个专业的 DAX 查询生成器。根据用户的自然语言问题，生成正确的 DAX 查询语句。
规则：
1. 只使用提供的表和列信息
2. 使用 EVALUATE 作为查询开头
3. 合理使用 SUMMARIZECOLUMNS, FILTER, CALCULATE 等函数
4. 输出格式化的 DAX 代码

[CONTEXT]
本项目是 AdventureWorks 零售销售分析模型。
包含产品(Product)、销售(Sales)、日期(Date)、客户(Customer)、地理(Geography) 五张核心表。
主要分析维度：时间、产品类别、客户、地区。
核心指标：销售额、利润率、客户数、平均订单金额。

[KEYWORDS]
  销售额 (score=0.95)  →  mapped: Sales[Total Sales], type=measure
  同比   (score=0.93)  →  mapped: Sales YoY%, type=measure

[COLUMNS]
  Sales[SalesAmount]
  Date[Year]
  Date[Month]

[EXAMPLES]
  Q: 今年每个月的销售额是多少 (score=0.95)
  A: EVALUATE SUMMARIZECOLUMNS('Date'[Year], 'Date'[Month], "TotalSales", [Total Sales]) ...

  Q: 销售额同比增长率趋势 (score=0.88)
  A: EVALUATE SUMMARIZECOLUMNS('Date'[Year], 'Date'[Month], "Sales", [Total Sales], "YoY", [Sales YoY%]) ...

[QUESTION]
今年每个月的销售额趋势
```

**这就是全部。** LLM 只需要这些信息就能生成正确的 DAX。现在我们反过来看——每一块信息从哪来，怎么管理。

---

## 二、Prompt 中的 6 块信息 → 反推出 6 个子系统

| Prompt 区块 | 解决什么问题 | 数据来源（DB 表） | 管理界面 |
|---|---|---|---|
| `[SYSTEM]` | 告诉 LLM "你是谁、规则是什么" | `prompt_config` (config_key=system_instruction) | /settings/prompt-config |
| `[CONTEXT]` | 告诉 LLM "这个数据模型的业务背景" | `prompt_config` (config_key=business_context) | /settings/prompt-config |
| `[KEYWORDS]` | 把中文业务术语映射到 DAX 表/列/度量 | `keyword_explanation` | /dax-knowledge/keywords |
| `[COLUMNS]` | 告诉 LLM 有哪些可用的字段 | `semantic_table` + `column_explanation` | /dax-model/tables |
| `[EXAMPLES]` | Few-shot 示例，教 LLM 正确的 DAX 写法 | `dax_example` | /dax-knowledge/questions |
| `[QUESTION]` | 用户当前的自然语言问题 | 实时输入 | /text2dax/query |

**核心洞察：系统的每个页面、每张数据库表，都是为了填充 Prompt 中的某一块。**

---

## 三、查询 Pipeline 的 7 个步骤

```
用户输入                                        系统输出
  │                                               ▲
  ▼                                               │
┌─────────────────────────────────────────────────────┐
│ ① 分词（Tokenize）                                   │
│    "北京地区今年销售额同比增长多少"                       │
│    → ["北京", "地区", "今年", "销售额", "同比增长"]      │
│                                                     │
│    怎么分？→ LLM 分词，用 keyword_split 表中            │
│              mark=true 的记录作为 few-shot 示例          │
├─────────────────────────────────────────────────────┤
│ ② 关键词召回（Keyword Recall）                         │
│    逐个 token 查 keyword_explanation 表                │
│    匹配方式：精确匹配 keyword + 同义词数组(synonyms)      │
│    未来：+ 向量余弦相似度（keyword_vector, 1024维）       │
│                                                     │
│    结果："销售额" → Total Sales (measure)               │
│          "同比增长" → Sales YoY% (measure)              │
│          "北京" → Geography[City] (column)              │
├─────────────────────────────────────────────────────┤
│ ③ 列/度量召回（Column Recall）                         │
│    逐个 token 查 column_explanation + measure 表        │
│    匹配：列名 ILIKE + 中文解释 ILIKE                    │
│    未来：+ column_vector / measure_vector 向量检索       │
│                                                     │
│    结果：Sales[SalesAmount], Date[Year] ...             │
├─────────────────────────────────────────────────────┤
│ ④ 示例召回（Example Recall）                           │
│    整句问题 → 查 dax_example 表                         │
│    当前：文本 ILIKE 匹配                                │
│    未来：question_vector 余弦相似 → Top-3               │
│                                                     │
│    结果：最相似的 3 组 (question, dax_query) 对          │
├─────────────────────────────────────────────────────┤
│ ⑤ Prompt 组装（Assembly）                              │
│    从 prompt_config 读 system + context                │
│    + 上面召回的 keywords / columns / examples           │
│    + 用户原始问题                                      │
│    → 拼成完整 prompt 字符串                             │
├─────────────────────────────────────────────────────┤
│ ⑥ LLM 生成 DAX                                       │
│    调用 llm_config 中 is_active=true 的 chat 模型       │
│    → 返回 DAX 代码 + 置信度                             │
├─────────────────────────────────────────────────────┤
│ ⑦ 全流程记录 → execution_history                       │
│    分词结果、召回结果、组装的prompt、生成的DAX、           │
│    执行状态、耗时 → 全部写入一条记录                      │
│                                                     │
│    人工标注 mark=true → 回流到 dax_example              │
│    这就是「数据飞轮」                                   │
└─────────────────────────────────────────────────────┘
```

---

## 四、`mark` 字段：控制什么进入 Prompt

这是系统设计中最关键的一个布尔值。

**所有核心表都有 `mark` 字段。只有 `mark=true` 的记录才会被召回、进入 Prompt。**

这意味着：
- 导入 100 列的模型，但只有人工审核过、标记了 `mark=true` 的列会出现在 Prompt 中
- 知识库里 50 个关键词，只有标记过的才参与匹配
- DAX 示例库里 200 条，只有标记过的才作为 few-shot

**为什么这样设计？** 因为 Prompt 的 context window 有限，必须精选。宁可少给但准确，不能全给但噪声大。`mark` 就是人工质量门控。

---

## 五、数据飞轮

```
                    ┌──────────────────────────┐
                    │     用户提问              │
                    └────────────┬─────────────┘
                                 ▼
                    ┌──────────────────────────┐
                    │  Pipeline 生成 DAX        │
                    └────────────┬─────────────┘
                                 ▼
                    ┌──────────────────────────┐
                    │  写入 execution_history   │
                    └────────────┬─────────────┘
                                 ▼
                    ┌──────────────────────────┐
                    │  人工审核                 │
                    │  ├─ 结果正确？mark=true   │
                    │  └─ 发现新关键词？         │
                    └────────────┬─────────────┘
                        ┌────────┴────────┐
                        ▼                 ▼
              ┌─────────────────┐  ┌──────────────────┐
              │ 回流到           │  │ 补充到            │
              │ dax_example      │  │ keyword_explain   │
              │ source=generated │  │ 或 column_explain │
              └─────────────────┘  └──────────────────┘
                        │                 │
                        └────────┬────────┘
                                 ▼
                    ┌──────────────────────────┐
                    │  下次查询 Prompt 更准确    │
                    └──────────────────────────┘
```

越用越准。这是整个系统的核心价值。

---

## 六、系统架构总览

```
┌─────────────────────────────────────────────────────────────┐
│                        前端 (Next.js)                        │
│                                                             │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌──────────┐ │
│  │ DAX 模型   │  │ 知识库     │  │ Text2DAX  │  │ 设置     │ │
│  │ 管理       │  │ 管理       │  │ 查询/对话  │  │ LLM/提示 │ │
│  │            │  │            │  │           │  │ 词配置   │ │
│  │ tables     │  │ keywords   │  │ query     │  │ llm-cfg  │ │
│  │ measures   │  │ examples   │  │ chat      │  │ prompt   │ │
│  │ relations  │  │ splitwords │  │ history   │  │          │ │
│  │ overview   │  │ annotation │  │           │  │          │ │
│  └─────┬─────┘  └─────┬─────┘  └─────┬─────┘  └────┬─────┘ │
│        │              │              │              │       │
│        └──────────────┴──────┬───────┴──────────────┘       │
│                              │ REST API                     │
└──────────────────────────────┼───────────────────────────────┘
                               │
┌──────────────────────────────┼───────────────────────────────┐
│                     后端 (Go)│                                │
│                              ▼                               │
│  ┌───────────────────────────────────────────────┐           │
│  │            Query Pipeline                      │           │
│  │  tokenize → recall(kw+col+ex) → assemble → LLM│           │
│  └───────────────────────┬───────────────────────┘           │
│                          │                                   │
│       ┌──────────────────┼──────────────────┐                │
│       ▼                  ▼                  ▼                │
│  ┌─────────┐      ┌───────────┐      ┌───────────┐          │
│  │PostgreSQL│      │  LLM API  │      │  Embedding │          │
│  │+pgvector │      │(chat模型) │      │  API       │          │
│  └─────────┘      └───────────┘      └───────────┘          │
│                                                              │
│  数据库表对应 Prompt 区块:                                     │
│  prompt_config      → [SYSTEM] + [CONTEXT]                   │
│  keyword_explanation→ [KEYWORDS]                             │
│  column_explanation → [COLUMNS]                              │
│  measure_explanation→ [COLUMNS] (度量值部分)                   │
│  dax_example        → [EXAMPLES]                             │
│  execution_history  → 记录一切，数据飞轮入口                    │
│  llm_config         → 决定调用哪个 LLM                        │
│  keyword_split      → 分词阶段的 few-shot 示例                 │
└──────────────────────────────────────────────────────────────┘
```

---

## 七、向量检索（pgvector）的角色

系统中有 4 个 `vector(1024)` 列，全部使用 bge-large-zh 模型生成 1024 维嵌入：

| 向量列 | 在 Pipeline 中的用途 |
|---|---|
| `column_explanation.column_vector` | token → 列语义匹配（"销售额" → Sales[SalesAmount]） |
| `measure_explanation.measure_vector` | token → 度量值语义匹配（"利润率" → [Profit Margin]） |
| `keyword_explanation.keyword_vector` | token → 关键词语义匹配（精确匹配失败时兜底） |
| `dax_example.question_vector` | 整句 → 相似问题检索（few-shot 召回） |

当前实现使用文本 ILIKE 匹配作为占位，向量检索是设计目标。`llm_config` 表中 `config_type=embedding` 的记录配置 Embedding 模型。

---

## 八、一句话总结

**Text2DAX = 一个精心管理 Prompt 内容的系统。** 模型管理、知识库、分词词典、QA 示例——每个功能模块存在的唯一理由，就是往 Prompt 里填入更准确的上下文，让 LLM 生成更好的 DAX。
