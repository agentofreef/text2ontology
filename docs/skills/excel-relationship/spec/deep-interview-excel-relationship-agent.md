# Deep Interview Spec: Excel Relationship Discovery Agent

## Metadata
- Interview ID: excel-relationship-agent-001
- Rounds: 12
- Final Ambiguity Score: 17%
- Type: brownfield (DeerFlow framework extension)
- Generated: 2026-04-07
- Threshold: 20%
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.83 | 35% | 0.291 |
| Constraint Clarity | 0.85 | 25% | 0.213 |
| Success Criteria | 0.80 | 25% | 0.200 |
| Context Clarity (brownfield) | 0.80 | 15% | 0.120 |
| **Total Clarity** | | | **0.824** |
| **Ambiguity** | | | **17%** |

---

## Goal

构建一个基于 DeerFlow 框架的 Excel 关系发现 Agent，当用户说"帮我查找这个文件夹下每个 Excel 之间的关系"时自动触发，完成以下工作：

**分析粒度：Sheet 级别**
- 输入：N 个 Excel 文件，每个文件 K 张 Sheet，共 N×K 张 Sheet
- 分析矩阵：所有 Sheet 对（同文件内 + 跨文件）
- 输出：仅展示**有关系的 Sheet 对**（稀疏图，过滤无关系的）

**三类关系（均在 Sheet 级别发现）：**
1. **JOIN 关联**：Sheet A 的某列 ↔ Sheet B 的某列存在可 JOIN 的实际数据重叠
2. **数据血缘**：Sheet A 的数据是 Sheet B 某列的计算来源（或反之）
3. **语义重叠**：两张 Sheet 描述同一业务实体（即使列名不同）

**每个 Excel/Sheet 还需提供：**
- 业务语义描述：这张 Sheet 在描述什么业务对象
- 表格分类：数据表（有实际业务数据）vs 计算表（临时汇总/派生）

**输出分两部分展示：**
- Part 1：同一文件内的 Sheet 关系（A.xlsx 内部）
- Part 2：跨文件的 Sheet 关系（A.xlsx Sheet1 ↔ B.xlsx Sheet3）

**核心挑战**：真实 Excel 不是标准表格——同一 sheet 里可能有标题行、元信息、空行、合并单元格、小计行、备注行、嵌套表格，需先将"乱糟糟的 Excel"解析为逻辑表，再发现关系。

---

## Constraints

- **规模**：单次任务 20+ 个 Excel 文件，单文件可能达 100MB+
- **精度要求**：生产级关键路径——不确定时宁可不输出，不能乱猜
- **不确定性处理**（三层升级）：
  1. 多 Agent 表决：3个不同角度的分析 Agent 独立评估，投票
  2. 用户澄清：票数不足时主动询问用户
  3. 安全失败：置信度低于阈值则不输出该关系
- **Excel 解析策略**：
  - 程序化规则优先（openpyxl：空行分割、合并单元格检测、格式识别、关键字检测）
  - 边界不确定时询问用户确认
  - 不依赖 MarkItDown（100MB 文件 Markdown 转换会丢失结构且内存爆炸）
- **触发方式**：通过 DeerFlow 对话界面触发，lead_agent 识别意图后委托子 Agent 执行

---

## Non-Goals

- 不处理非 Excel 文件（CSV、数据库连接等）
- 不自动修改或合并原始 Excel 文件
- 不提供实时协作/多用户同时分析
- 不支持 Excel 公式语义解析（如 `=VLOOKUP(A2,Sheet2!A:B,2,0)` 追踪）——这是 v2 的事
- 不构建通用 ETL 管道

---

## Acceptance Criteria

- [ ] 给定包含 20 个 Excel 的文件夹（每个可能含多张 Sheet），Agent 能完成分析并输出报告
- [ ] 对于格式混乱的 Sheet（标题行、空行、小计行穿插），能正确识别逻辑表边界；边界不确定时主动询问用户确认
- [ ] 每张 Sheet 的业务描述，业务专家看后认为"基本符合事实"
- [ ] 数据表 vs 计算表的分类，业务专家认为主要正确
- [ ] 能发现列名不同但语义相同的 JOIN 关联（如"客户ID" ↔ "客户编号"）
- [ ] 同文件内的 Sheet 关系与跨文件的 Sheet 关系**分开展示**
- [ ] 输出仅包含有关系的 Sheet 对，无关系的不出现在报告中
- [ ] 输出的 Mermaid ER 图语法有效，可直接渲染
- [ ] 无法达成 Agent 共识的关系：不输出，标记为"需用户确认"
- [ ] 每条发现的关系附带：关系类型 + 具体关联列 + 置信度分数 + 支撑证据

---

## Assumptions Exposed & Resolved

| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| "关系"是单一概念 | 关系可以是3种完全不同的东西 | 明确为三类：JOIN关联 / 数据血缘 / 语义重叠 |
| 生产级精度可以通过单次LLM调用实现 | LLM本身对结构性问题有幻觉 | 三层机制：多Agent表决 → 用户澄清 → 安全失败 |
| 关系是客观存在的 | 两个业务专家可能对"有没有关联"答案不同 | 结构性关系（字段重叠）客观可测，语义关系需人工确认 |
| MarkItDown可以处理所有Excel | 100MB+文件MarkItDown会丢失结构 | 改用openpyxl直接读取单元格属性 |
| LLM可以直接理解乱Excel | 没有预处理的乱Excel对LLM是噪音 | 程序化规则先跑，再询问用户确认，再LLM分析 |

---

## Implementation Architecture（关键架构决策）

### 1. AI Agent 还是 Workflow？

**决策：在现有 lead_agent 之上，新建一个 LangGraph 子图（Subgraph/Flow）**

```
DeerFlow 现有结构：
  用户 → Nginx(2026) → LangGraph Server(2024) → lead_agent（对话 AI）
                                                      ↓
                                              SubagentExecutor → 子 Agent

新增结构：
  用户 → Nginx → LangGraph Server → lead_agent
                                        ↓ (识别"分析Excel关系"意图)
                                 ExcelRelationshipFlow（新 LangGraph 子图）
                                        ↓
                              ┌─────────────────────┐
                              │  Node1: StructParser  │ (Python Tool via Sandbox)
                              │  Node2: Understand    │ (LLM via lead_agent model)
                              │  Node3: Relationship  │ (3x SubagentExecutor并行)
                              │  Node4: OutputGen     │ (Mermaid + Report)
                              └─────────────────────┘
```

**不是独立的 standalone workflow**，而是通过 DeerFlow 的 subagent 机制委托给专用流程。用户仍然通过现有的 DeerFlow 前端页面交互。

### 2. 本地还是线上？

**决策：本地运行（与现有 DeerFlow 部署保持一致）**

- DeerFlow 本地运行：Frontend(Nginx:2026) + Backend(LangGraph:2024 + Gateway:8001)
- 文件通过 DeerFlow 上传 API 存储到 Sandbox：`/mnt/user-data/{thread_id}/uploads/`
- Excel 解析在 Sandbox 内执行（用户数据不离开本地）
- 不涉及外部服务，除了 LLM API 调用

### 3. 是否涉及 Bash 执行？

**决策：是，通过 DeerFlow Sandbox 执行 Python 脚本**

```
Excel 解析逻辑以 Python 脚本形式存在：
  /mnt/skills/public/excel-relationship/scripts/
    ├── parse_structure.py    ← openpyxl 解析，输出 JSON
    ├── extract_schema.py     ← 提取 ColumnSchema
    └── compute_overlap.py    ← 值重叠率计算（纯算法）

lead_agent 通过 bash_tool 调用：
  python /mnt/skills/.../parse_structure.py --file /mnt/user-data/.../Q3.xlsx
  输出 JSON 到 stdout，lead_agent 读取结果继续处理
```

Python 脚本在 DeerFlow sandbox 内执行，拥有文件访问权限但无网络权限（安全隔离）。

### 4. 有哪些 Skill？如何配合？

**新建 Skill：`excel-relationship`**

```
skills/public/excel-relationship/
├── SKILL.md                    ← Skill 定义（注入 lead_agent system prompt）
├── scripts/
│   ├── parse_structure.py      ← Phase 1: openpyxl 结构解析
│   ├── extract_schema.py       ← Phase 1: Schema 提取
│   ├── compute_overlap.py      ← Phase 3: 值重叠率（结构分析师）
│   └── generate_mermaid.py     ← Phase 4: Mermaid ER 图生成
└── prompts/
    ├── business_understanding.md ← Phase 2: LLM 语义理解 prompt 模板
    ├── semantic_analyst.md      ← Phase 3: 语义分析师 Agent prompt
    └── relationship_voter.md    ← Phase 3: 表决 Agent prompt
```

`SKILL.md` 告诉 lead_agent：
- 何时激活（关键词：分析Excel关系、查找关系、ER图）
- 可用哪些脚本工具
- 整体流程编排指令

### 5. 子 Agent 如何实现和调度？

**使用 DeerFlow 现有的 SubagentExecutor（`subagents/executor.py`）**

```python
# Phase 3: 3-Agent 表决的实现方式
# DeerFlow SubagentExecutor 支持并行任务

async def run_3agent_voting(table_a, table_b):
    # 结构分析师：纯算法，不用 LLM，直接在 sandbox 执行 Python
    structural_result = await run_script("compute_overlap.py", table_a, table_b)
    
    # 语义分析师 + 统计分析师：作为 subagent tasks 并行执行
    semantic_task = SubagentTask(
        prompt=render_prompt("semantic_analyst.md", table_a, table_b),
        model=config.model  # 继承 lead_agent 的模型配置
    )
    statistical_task = SubagentTask(
        prompt=render_prompt("statistical_analyst.md", table_a, table_b),
        model=config.model
    )
    
    # 并行调度（利用 SubagentExecutor 的 _execution_pool，3 workers）
    semantic_result, statistical_result = await asyncio.gather(
        executor.submit(semantic_task),
        executor.submit(statistical_task)
    )
    
    return vote(structural_result, semantic_result, statistical_result)
```

**调度限制**：DeerFlow SubagentExecutor 默认 3 个执行线程。对于 20 文件 × 多 Sheet 对，需要批量处理而非一次性提交所有任务。

### 6. 前端页面如何交互？

**使用现有 DeerFlow 前端，无需新建页面**

```
用户操作流程：
1. 打开 DeerFlow 前端（localhost:2026）
2. 上传 Excel 文件（拖拽或点击上传，已有上传 UI）
3. 发送消息："帮我分析这些 Excel 之间的关系，生成 ER 图"
4. lead_agent 激活 excel-relationship Skill，开始分析
5. 过程中若边界不确定：lead_agent 发 clarification 请求，用户在聊天界面回复
6. 分析完成：Artifacts 区域显示 Mermaid ER 图 + 报告文件
```

用户的确认操作（边界确认、关系审核）通过 DeerFlow 的 `ask_clarification` 内置工具实现，返回聊天界面，无需额外 UI。

### 7. 数据流总览

```
用户上传 N 个 Excel 文件
    ↓ (DeerFlow Upload API → /mnt/user-data/{thread}/uploads/)
lead_agent 收到消息 + 文件引用
    ↓ (激活 excel-relationship Skill)
[Sandbox] parse_structure.py × N 文件（并行，bash_tool）
    ↓ (JSON: LogicalTable 列表，含置信度)
[Clarification] 低置信度的表结构 → ask_clarification → 用户确认
    ↓
[LLM] business_understanding prompt × N×K 张表（分批，避免 context 溢出）
    ↓ (BusinessConcept + 表分类)
[SubagentExecutor] 3-Agent 表决 × 候选 Sheet 对（批量并行）
    ↓ (RelationshipResult 列表)
[Clarification] NEEDS_USER_REVIEW 的关系 → ask_clarification → 用户确认
    ↓
[Sandbox] generate_mermaid.py（Mermaid ER 图）
    ↓ (artifacts: er_diagram.md + relationship_report.md)
DeerFlow Artifacts UI 展示结果
```

---

## Technical Context

### DeerFlow 现有能力（可复用）

| 组件 | 位置 | 与本项目的关系 |
|------|------|--------------|
| openpyxl（通过 MarkItDown 依赖） | `backend/pyproject.toml` | **复用**：直接用 openpyxl 读取单元格属性 |
| DuckDB data-analysis Skill | `skills/public/data-analysis/` | **不适用**：DuckDB 假设 Excel 是标准表格，无法处理乱格式 |
| Subagent 执行框架 | `backend/packages/harness/deerflow/subagents/executor.py` | **复用**：多 Agent 表决用此框架 |
| ThreadState / artifacts | `agents/thread_state.py` | **复用**：ER图等输出通过 artifacts 系统 |
| 文件上传 + Sandbox | `app/gateway/routers/uploads.py` | **复用**：文件访问走 `/mnt/user-data/uploads/` 路径 |
| ask_clarification 工具 | lead_agent 内置工具 | **复用**：边界不确定时用此工具询问用户 |

### 新增需要构建的组件

```
excel-relationship-flow/
├── excel_parser/
│   ├── structure_detector.py     # 程序化规则：空行、合并、格式、关键字
│   ├── logical_table_extractor.py # 从 sheet 提取逻辑表列表
│   └── schema_extractor.py       # 轻量 schema：列名 + 类型 + 100行样本
│
├── understanding/
│   ├── business_concept_agent.py  # LLM：这张表在描述什么业务？
│   └── table_classifier.py       # LLM：数据表 vs 计算表
│
├── relationship/
│   ├── structural_analyzer.py    # 列名相似度 + 值重叠率（算法）
│   ├── semantic_analyzer.py      # LLM：业务语义关系
│   ├── lineage_analyzer.py       # LLM：数据血缘推断
│   └── consensus_voter.py        # 3-Agent 表决机制
│
└── output/
    ├── er_diagram_generator.py   # 生成 Mermaid ER 图
    └── report_generator.py       # 关系报告 + 置信度
```

### 大文件处理策略（100MB+）

- **不将全文件内容注入 LLM context**：只提取 Schema（列名 + 前100行样本值 + 统计）
- **分块处理**：openpyxl `read_only=True` 模式流式读取
- **并行**：多文件并行处理（DeerFlow 的 SubagentExecutor 线程池）
- **向量相似度**（可选 v2）：列名 embedding 用于候选关系快速筛选

---

## Ontology (Key Entities)

| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| ExcelFile | core domain | file_path, file_name, size, sheet_count | contains many SheetContents |
| SheetContent | core domain | sheet_name, cell_matrix, merged_cells | contains many LogicalTables |
| LogicalTable | core domain | start_row, end_row, columns, detected_header_row | classified as DataTable or CalculationTable; has many ColumnSchemas |
| ColumnSchema | supporting | name, inferred_type, sample_values, business_meaning | belongs to LogicalTable |
| BusinessConcept | supporting | description, domain_area, confidence | describes ExcelFile or LogicalTable |
| Relationship | core domain | type(join/lineage/semantic), source_column, target_column, confidence, evidence | between two ColumnSchemas |
| ConsensusVote | supporting | agent_id, vote, reasoning, confidence | votes on one Relationship |
| ERDiagram | supporting | mermaid_code, entity_count, relationship_count | visualizes all confirmed Relationships |
| RelationshipReport | core domain | relationships, unconfirmed_items, metadata | final output artifact |

---

## Ontology Convergence

| Round | Entity Count | New | Changed | Stable | Stability |
|-------|-------------|-----|---------|--------|-----------|
| 2 | 5 | 5 | - | - | N/A |
| 4 | 6 | 1 (ConsensusVote) | 0 | 5 | 83% |
| 6 | 6 | 1 (ColumnSchema) | 1 (LogicalTable细化) | 4 | 83% |
| 8 | 8 | 2 (DataTable/CalculationTable) | 0 | 6 | 75% |
| 9 | 9 | 1 (BusinessConcept) | 2 (DataTable→LogicalTable子类) | 6 | 89% |

本体在 Round 6-9 趋于稳定，核心实体 ExcelFile / LogicalTable / Relationship 贯穿始终。

---

## Excel Parsing Strategy（核心技术设计）

这是整个系统的最难点。真实 Excel 无法直接转 CSV，必须先做结构识别。

### 问题分类

| 问题类型 | 示例 | 挑战 |
|----------|------|------|
| 标题/元信息行 | A1="2024年Q3汇总" | 不是数据，不能当 header |
| 表头不在第一行 | A4 才是真正的 header | 无法用 `read_excel(header=0)` |
| 小计/汇总行混入数据 | A21="小计" | 会污染数值分析 |
| 同 Sheet 多张表 | A1-A20 一张表，A25 起另一张 | 单次解析会把两张表混在一起 |
| 横向并排表格 | A列数据 + E列另一张独立表 | 需要二维岛屿检测 |
| 合并单元格表头 | A1:C1 合并写"产品线" | openpyxl 里 B1/C1 是 None |
| 空行跳跃 | 数据行中间有随机空行 | 不能把空行当表格边界 |
| 备注藏在数据后 | A23="备注：3月不含退货" | 是关键业务规则，不是数据 |

### Phase 1a：Cell Property 提取（openpyxl）

对每个单元格提取以下属性（不看值，只看格式）：

```python
CellSignature:
  value: Any
  is_empty: bool
  is_merged: bool          # 是合并区域的一部分
  merge_span: (rows, cols) # 合并跨度，(1,1) = 未合并
  is_bold: bool
  font_size: float
  bg_color: str            # hex，None = 无填充
  alignment: center|left|right
  number_format: str       # 日期/货币/百分比等
  has_formula: bool        # 以 = 开头
  row_height: float
  col_width: float
```

### Phase 1b：行类型分类（规则引擎）

对每一行，基于 CellSignature 向量打分分类：

```
TITLE_ROW 判定条件（任意满足）：
  - 该行只有 1-2 个非空单元格，且其中一个是合并跨多列
  - 字体大于相邻行 font_size > median_font_size * 1.2
  - 居中对齐 + 无数字单元格
  - 内容匹配模式：含"汇总|报告|统计|年|季度|Q[1-4]"

HEADER_ROW 判定条件：
  - 该行 >50% 单元格是字符串（非数字）
  - 相邻下方行是数字为主
  - 可能是粗体
  - 不包含"合计|小计|总计"关键词

DATA_ROW 判定条件：
  - 数字单元格比例与 HEADER_ROW 下方行一致
  - 行类型与前后行一致（行类型连续性）

SUBTOTAL_ROW 判定条件（任意满足）：
  - 包含关键词：合计|小计|总计|汇总|Total|Subtotal|Sum
  - 该行有公式（=SUM、=SUMIF 等）
  - 背景色与数据行不同

NOTES_ROW 判定条件：
  - 只有第一列非空，其余为空
  - 内容以"备注|注意|说明|*|※"开头
  - 出现在数据区域末尾之后

EMPTY_ROW：所有单元格 is_empty=True
```

### Phase 1c：二维岛屿检测（横向多表）

当 Sheet 存在横向并排表格时（一列数据 + 另一列独立表），需要做二维分割：

```
算法：
1. 构建 occupancy_matrix：(row, col) → has_data
2. 识别连通区域（BFS/洪水填充），每个连通块 = 候选逻辑表
3. 合并过于细碎的小块（<3行 or <2列的块判定为噪音）
4. 每个有效连通块单独运行 Phase 1b 行分类
```

### Phase 1d：逻辑表提取结果

每张 Sheet 输出若干 `LogicalTable`：

```python
LogicalTable:
  sheet: str
  region: (start_row, end_row, start_col, end_col)
  header_rows: [int]         # 可能多行表头（合并单元格）
  data_rows: [int]
  excluded_rows: {           # 被过滤的行及原因
    subtotal: [int],
    notes: [int],
    title: [int]
  }
  columns: [ColumnSchema]    # 表头解析后的列定义
  confidence: float          # 结构识别置信度 0-1
  ambiguous_rows: [int]      # 无法确定分类的行（触发用户确认）
```

### Phase 1e：置信度与用户确认

- `confidence >= 0.8`：自动继续，不打扰用户
- `0.5 <= confidence < 0.8`：展示检测结果，询问用户"我检测到以下结构，是否正确？"
- `confidence < 0.5`：必须用户确认后才继续

确认界面向用户展示（文字形式）：
```
文件: Q3_Sales.xlsx  Sheet: 华东区
┌─────────────────────────────────────┐
│ 行1: [TITLE]  "2024年Q3华东区销售汇总" │
│ 行2: [META]   "制表人：老王 日期：9/15"│
│ 行3: [EMPTY]                         │
│ 行4: [HEADER] 产品线 | 1月 | 2月 | 3月│
│ 行5-20: [DATA] (16行数据)             │
│ 行21: [SUBTOTAL] 小计                │
│ 行23: [NOTES] 备注：3月不含退货       │
└─────────────────────────────────────┘
识别到 1 个逻辑表（行4-20）。是否正确？
```

### 合并单元格处理

openpyxl 中合并单元格只有左上角有值，其余为 None：
```python
# 处理策略：展开合并单元格
def expand_merged_cells(ws):
    for merge_range in ws.merged_cells.ranges:
        top_left_value = ws.cell(merge_range.min_row, merge_range.min_col).value
        for row in range(merge_range.min_row, merge_range.max_row + 1):
            for col in range(merge_range.min_col, merge_range.max_col + 1):
                ws.cell(row, col).value = top_left_value
                ws.cell(row, col)._is_expanded = True  # 标记为展开
```

多行表头（合并跨行）合并为单一列名：
```
行4: "产品线" | "华东区" (合并B4:D4) | "华南区" (合并E4:G4)
行5: ""       | "1月" | "2月" | "3月" | "1月" | "2月" | "3月"
→ 列名: ["产品线", "华东区_1月", "华东区_2月", "华东区_3月", ...]
```

### 无法结构化的情况（兜底）

当 Excel 真的无法被程序化识别时（极端混乱情况）：
1. 提取前 50 行单元格内容（含格式信息），构建紧凑文本表示
2. 传给 LLM："这是一张 Excel 的单元格内容，请告诉我你能识别出哪些逻辑表"
3. LLM 输出 JSON：`{tables: [{header_row: N, data_start: N, data_end: N, columns: [...]}]}`
4. 如果 LLM 也无法确定：标记为 `UNSTRUCTURED`，向用户呈现原始内容请求手动标注

---

## Relationship Discovery Algorithm（Phase 3 详细设计）

完成 Phase 1-2（结构解析 + 语义理解）后，进入关系发现阶段。输入是所有 LogicalTable 的 ColumnSchema 集合，目标是找出所有有意义的 Sheet 对关系。

### 候选对生成（剪枝）

N×K 张 Sheet 的两两对比是 O(n²)，100 张 Sheet = 4950 对，200 张 = 19900 对。全量深度分析不现实，必须先剪枝生成候选对：

```
候选对过滤规则（任一满足则进入深度分析）：
1. 列名重叠：两张表有 ≥1 个列名完全相同或高度相似（编辑距离 < 3）
2. Schema 语义相似：列名向量的 cosine similarity > 0.6（用轻量 embedding）
3. 同文件内的 Sheet 对：全部进入深度分析（通常数量少）

不满足任何条件的 Sheet 对 → 直接判定为"无关系"，不进入深度分析
```

### 深度分析：3-Agent 表决

每个候选 Sheet 对由 3 个角色独立分析，最后投票：

#### Agent 1：结构分析师（算法驱动，不用 LLM）

```python
def structural_analysis(table_a: LogicalTable, table_b: LogicalTable) -> StructuralEvidence:
    
    # 1. 列名相似度矩阵
    col_similarity = {
        (col_a, col_b): similarity_score(col_a.name, col_b.name)
        for col_a in table_a.columns
        for col_b in table_b.columns
    }
    best_pairs = find_best_column_matches(col_similarity, threshold=0.7)
    
    # 2. 对每个候选列对，采样数据做值重叠检测
    for col_a, col_b in best_pairs:
        sample_a = sample_values(table_a, col_a, n=200)  # 取200行样本
        sample_b = sample_values(table_b, col_b, n=200)
        
        overlap_ratio = len(set(sample_a) & set(sample_b)) / len(set(sample_a) | set(sample_b))
        # overlap_ratio > 0.3 → 强 JOIN 关联证据
    
    # 3. 数据类型兼容性
    type_compatible = (col_a.inferred_type == col_b.inferred_type)
    
    return StructuralEvidence(
        best_column_pairs=best_pairs,
        overlap_ratios=overlap_ratios,
        confidence=compute_confidence(overlap_ratios, type_compatible)
    )
```

**关键设计**：值重叠检测不读全量数据，只取 200 行样本，性能可控。

#### Agent 2：语义分析师（LLM 驱动）

输入给 LLM 的是**压缩后的 Schema 表示**（不含原始数据）：

```
Table A (来自 Q3_Sales.xlsx / Sheet: 华东区):
  业务描述: "2024年Q3华东区按产品线的月度销售数据"
  列: 产品线(str), 1月销售额(float), 2月销售额(float), 3月销售额(float), 客户ID(str)
  行数: 16, 数据表

Table B (来自 Customer.xlsx / Sheet: 客户信息):
  业务描述: "客户主数据，含联系方式和地区信息"
  列: 客户编号(str), 客户名称(str), 所属地区(str), 联系人(str)
  行数: 1243, 数据表

问题：这两张表是否存在以下任意关系？
1. JOIN关联：某列的值可以对应另一张表的某列
2. 数据血缘：一张表的数据来源于另一张表的计算
3. 语义重叠：两张表描述同一批业务实体

输出JSON：{
  "join": {"exists": bool, "evidence": "...", "column_pairs": [...]},
  "lineage": {"exists": bool, "direction": "A→B|B→A|none", "evidence": "..."},
  "semantic": {"exists": bool, "overlap_type": "...", "evidence": "..."}
}
```

**关键设计**：只传 Schema（列名+类型+业务描述），不传原始数据。LLM 做语义推理，不做数据扫描。

#### Agent 3：统计分析师（算法驱动）

```python
def statistical_analysis(table_a, table_b) -> StatisticalEvidence:
    
    # 数值列的分布对比（检测数据血缘）
    for col_a in table_a.numeric_columns:
        for col_b in table_b.numeric_columns:
            # 检查 col_b 的值是否约等于 col_a 某些行的聚合
            # e.g., col_b = SUM(col_a grouped by some key)
            aggregation_patterns = test_aggregation_hypothesis(col_a, col_b)
            
    # 字符串列的基数对比（检测 JOIN 关联）
    for col_a in table_a.string_columns:
        for col_b in table_b.string_columns:
            cardinality_ratio = col_a.cardinality / col_b.cardinality
            # 基数相近 + 值重叠 → JOIN 候选
    
    return StatisticalEvidence(aggregation_patterns, cardinality_analysis)
```

### 表决规则

```
对于每个候选 Sheet 对：

3个 Agent 各自输出：
  - 关系类型列表（JOIN / 血缘 / 语义）
  - 每种关系的置信度 (0-1)
  - 支撑证据

合并规则：
  JOIN关联:
    - 结构Agent置信度 > 0.7 → 直接确认（客观事实，算法可信）
    - 结构Agent 0.4-0.7 + 语义Agent同意 → 确认
    - 仅语义Agent认为有 → 置信度 0.5，标记"需用户确认"
    - 3个Agent均不认为有 → 不输出

  数据血缘:
    - 统计Agent发现聚合模式 + 语义Agent认同 → 确认
    - 仅一方认为有 → 标记"可能存在，建议验证"

  语义重叠:
    - 语义Agent置信度 > 0.7 → 确认（主观判断，人工核实）
    - 均不认为有 → 不输出

置信度 < 0.4 的全部 → 安全失败（不输出该关系）
```

### 特殊情况：同名列但不同语义

```
检测：col_a.name == col_b.name 但 overlap_ratio < 0.1
处理：标记为"列名相同但数据无关联，可能是巧合"
     → 不判断为 JOIN 关联，输出警告
```

### 输出数据结构

```python
RelationshipResult:
  sheet_a: SheetRef         # 文件名 + Sheet名
  sheet_b: SheetRef
  relationship_type: "JOIN" | "LINEAGE" | "SEMANTIC"
  confidence: float         # 0-1
  evidence: str             # 人类可读的证据描述
  column_pairs: [           # 具体关联的列
    {col_a: str, col_b: str, similarity: float, overlap_ratio: float}
  ]
  direction: "A→B" | "B→A" | "bidirectional" | None  # 血缘关系方向
  status: "CONFIRMED" | "NEEDS_USER_REVIEW" | "POSSIBLE"
  agent_votes: {            # 透明度：各 Agent 的投票记录
    structural: {confidence: float, evidence: str},
    semantic:   {confidence: float, evidence: str},
    statistical: {confidence: float, evidence: str}
  }
```

---

## Proposed Architecture

```
用户: "帮我查找 /uploads/q3/ 下每个 Excel 之间的关系"
         │
         ▼
    lead_agent (意图识别)
         │  委托
         ▼
 ExcelRelationshipOrchestrator
         │
    ┌────┴──────────────────────────────┐
    │  Phase 1: Structure Parsing       │  (N 个文件并行)
    │  openpyxl → 每个 Sheet 逻辑表检测  │
    │  → 边界不确定 → ask_clarification  │
    │  输出: N×K 个 Sheet 的 LogicalTable│
    └────┬──────────────────────────────┘
         │ N×K 个 SheetSchema（列名+类型+样本值）
    ┌────┴──────────────────────────────┐
    │  Phase 2: Understanding           │  (每个 Sheet 并行)
    │  LLM: 业务语义描述                │
    │  LLM: DataTable vs CalculationTable│
    └────┬──────────────────────────────┘
         │ 每个 Sheet 的 BusinessConcept + 分类
    ┌────┴──────────────────────────────┐
    │  Phase 3: Relationship Discovery  │
    │                                   │
    │  3a. 同文件 Sheet 对分析          │  (每个文件内并行)
    │      结构分析（列重叠率算法）      │
    │      语义分析（LLM）              │
    │      3-Agent 表决                 │
    │                                   │
    │  3b. 跨文件 Sheet 对分析          │  (候选对并行)
    │      候选筛选（schema 相似度）     │
    │      深度分析 + 3-Agent 表决      │
    │                                   │
    │  → 低置信度 → ask_clarification   │
    │  → 极低 → 安全失败，不输出        │
    └────┬──────────────────────────────┘
         │ 有关系的 Sheet 对（含类型+置信度+证据）
    ┌────┴──────────────────────────────┐
    │  Phase 4: Output                  │
    │  Part 1: 同文件内 Sheet 关系       │
    │          Mermaid ER 图            │
    │  Part 2: 跨文件 Sheet 关系        │
    │          Mermaid ER 图            │
    │  每张 Sheet 的业务描述 + 分类      │
    │  无法确定的关系 → 待确认清单       │
    └──────────────────────────────────┘
         │ artifacts
         ▼
    呈现给用户（仅显示有关系的 Sheet 对）
```

---

## Interview Transcript

<details>
<summary>Full Q&A (9 rounds)</summary>

### Round 1-2
**Q:** "找Excel之间的关系"最接近哪种意思？
**A:** 三种都有（数据血缘、JOIN关联、语义相似性）

**Q:** Agent完成后用户看到什么？
**A:** 关系报告 + 合并Excel + Mermaid关系图 + 回答问题 + 推荐表格式

### Round 3
**Q:** 准确性期望什么级别？
**A:** 生产级关键路径

### Round 4
**Q:** 不确定时怎么办？
**A:** 多Agent表决 → 用户澄清 → 安全失败（三层）

### Round 5 (Contrarian)
**Q:** 两个业务专家答案会一样吗？
**A:** 不一定，但有基础共识——结构性关系客观，语义关系主观

### Round 6 (Simplifier)
**Q:** 一次任务上传多少个Excel？
**A:** 20+个，单文件可能100MB+

### Round 7
**Q:** 怎么判断Agent工作得好不好？
**A:** 用户自己是业务专家，符合基本事实就知道对不对

### Round 8
**Q:** 以什么形式存在于DeerFlow中？
**A:** 用户说"帮我查找这个文件夹下每个excel之间的关系，生成ER图，告诉我每个excel描述什么业务，哪些表有用，哪些是计算表"

### Round 9
**Q:** 怎么判断哪些行是表头/数据/小计？
**A:** 程序化规则先运行（空行分割、格式判断、关键字检测），然后询问用户

</details>
