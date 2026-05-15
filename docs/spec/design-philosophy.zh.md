# text2ontology 设计哲学

> 三层本体 + 两层查询 + 一次召回

---

text2ontology 是一套让企业可以**用自然语言问数据问题、并得到一致答案**的系统。

它跟 Text2SQL 的根本不同在于:**LLM 不生成 SQL,LLM 只填参数**。SQL 是从企业内部维护的本体里 deterministic 地生成的。

本文用**一个具体问答**作为主线,讲清整套设计。读完之后你应该能回答三件事:
- 用户问一句话,系统做了什么
- 7 个核心概念(OD、OK、OL、Link、Causality、Intent、Keyword)各自负责什么
- 这个系统为什么是这个结构,不是别的

---

## 一、从一次问答开始:用户问"早单率怎么样"

这是整篇文章的主线。先看完整流程,再看每一步背后的设计。

```
用户问:"早单率怎么样"
   ↓
[1] 强制分词
    系统对每个用户问题都强制 tokenize。
    结果:["早单率", "怎么样"]
   ↓
[2] 召回(deterministic 后端代码,无 LLM)
    每个 token 走三层级联:精确匹配 → 模糊匹配 → 向量语义
    
    "早单率" 命中:
      - Keyword 表里的 "早单率" (EXACT)
      - 指向 Intent: Order.EarlyOrderRate
      - 该 Intent 锚定的 OD: Order
      - Order 关联的 OK: "早单的业务定义"
      - Order 关联的 Link: Order → Customer
    
    "怎么样" 是非关键 token,被忽略。
   ↓
[3] 上下文组装
    把召回到的所有结构化信息组装成 LLM 可读形式。
    上下文里明确告诉 LLM:
      "你拿到了这些 OD、这些 Intent、这些 OK"
      "你的工具只有 Lookup 和 Query"
   ↓
[4] LLM 工具调用
    LLM 看到上下文,决定:
      - 直接调 Query(参数填好就行) — 大多数情况
      - 或先调 Lookup(查更多细节) — 信息不够时
      - 或直接回答(已经够了)
    
    LLM 不能自由拼 SQL。它只能填:
      {intent_id: "Order.EarlyOrderRate", params: {period: "Q1"}}
   ↓
[5] Query 工具执行
    Intent 携带完整查询形状:metric、filters、groupBy、pivot 配置
    系统将 Intent + params 翻译成 SQL,在 Postgres 执行,返回结果
   ↓
[6] LLM 总结返回
    LLM 看到结果,按预设模板(或自由)生成自然语言答案
```

**这套流程的核心洞察**:

> **"分词 + 召回"是 deterministic 的(后端 SQL 代码,无 LLM)。**
> **LLM 是受约束的执行者,不是真理来源 —— 它只能从召回上下文里挑、填参数、调工具。**

这就是 text2ontology 跟"AI Agent 自由发挥"路线的本质区别。

---

## 二、整体架构

把上面的运行时抽象成结构,就得到 text2ontology 的两个核心 framework:

### 三层本体生命周期

```
┌────────────────────────────────────────────────┐
│  存在层 (Existence)                            │
│    OD (对象)        OK (知识)                  │
│    回答:业务里有什么                          │
├────────────────────────────────────────────────┤
│  关联层 (Connection)                           │
│    Link (OD↔OD)    Causality (OK↔OK)          │
│    回答:这些东西怎么连起来                    │
├────────────────────────────────────────────────┤
│  学习层 (Learning)                             │
│    OL (运营事实,可沉淀为 OK)                  │
│    回答:运营中学到了什么                      │
├────────────────────────────────────────────────┤
│  入口层 (Entry)                                │
│    Intent + Keyword                            │
│    回答:自然语言怎么进系统                    │
└────────────────────────────────────────────────┘
```

层内灵活组合,**层间单向依赖** —— 入口层引用存在层,关联层在存在层之上,学习层是产物。

### 两层查询架构

```
Level 1: Ontology Level
  抽象 function call: {intent_id, params}
  操作对象:OD / Intent / Property
  特点:deterministic,跟物理表无关
                  ↓
        (OD 的 semantic_sql 翻译)
                  ↓
Level 2: Physical SQL
  实际的 SELECT ... FROM ... 语句
  操作对象:Postgres staging schema
  特点:可被 EXPLAIN,可被审计
```

**两层架构的本质**:把"变化的成本"按层分配。schema 变只改 OD 的 semantic_sql;Intent 变只改 Intent 行;NL 变只加 Keyword alias。**每一层吸收下层的变化,对上层暴露稳定接口**。

---

## 三、7 个概念各自负责什么

| 概念 | 物理表 | 它处理的张力 |
|---|---|---|
| **OD** (Ontology Data) | `ont_object_type` + `ont_property` | 业务实体 vs 物理表的解耦 |
| **OK** (Ontology Knowledge) | `ont_knowledge` | 业务结构 vs 业务知识的分离 |
| **OL** (Ontology Learned-fact) | `ont_learned_fact` | 静态知识 vs 动态学习的并存 |
| **Link** | `ont_link_type` | 物理 JOIN 路径的显式化 |
| **Causality** | `ont_causality` | 业务因果 vs 物理关系的区分 |
| **Intent** | `lakehouse_metric_intent` | NL 模糊性 vs SQL deterministic 的桥接 |
| **Keyword** | `lakehouse_keyword` | 字面匹配 vs 语义匹配的双通道入口 |

### 三个关键关系

**OD 与 OK**:OK 必须依附在 OD 上才存在(可以挂到 OD 本身,可以挂到 Property,可以形成 OK 树)。OK 是 OD 的语义补丁。

**OL 与 OK**:OL 是 AI 在对话中提议的事实(`confidence=pending`),BOE 审核后变成 `confirmed`。**多条相似的 OL 在反复确认后,可以沉淀成一条 OK**(从经验抽象成知识)。

**Intent 与 Keyword**:Intent 是查询模板(锚定到一个 OD),Keyword 是触发词(指向 property 或 Intent)。它们一起构成 NL 到 deterministic 查询的桥梁。

### OL → OK 沉淀(设计意图,尚未实现)

举例:三条 OL 分别记录"2024-04、2025-04、2026-04 销量都超过 1M"。

```
AI 周期性聚类相似 OL
    ↓
对紧密的 OL 组生成"沉淀候选 OK"
    例:"4 月历史上是销售旺季,典型在 1M 以上"
    ↓
BOE 审核 → 接受则入 OK 表,带 evidence_for_ok 指回原 OL
原 OL 不删除(audit trail)
```

**关键原则**:AI 检测模式,**人类决定真理**。归纳问题不能交给算法独立解决,否则会从噪音里幻觉规律。

---

## 四、两个核心工具:Lookup 和 Query

整个系统的工具集只有两个。简洁是设计哲学。

**Lookup** — 查本体内容,只读,无副作用
- 输入:keyword / OD 名 / Intent 名
- 输出:该单元的完整定义 + 解释层文本 + 它的关联
- 何时用:LLM 需要查更详细的定义、解释、关联

**Query** — 执行 Ontology 查询
- 输入:`{intent_id, params}`
- 走 Level 1 → Level 2 翻译,在 Postgres 执行
- 何时用:LLM 准备好回答数据问题

**关键约束**:Query 不能写 SQL。LLM 只能提供 intent 和 params,不能自由拼 SQL 字符串。这是"本体先于查询"的工程兑现 —— **LLM 永远只是 deterministic 路径上的填空者**。

---

## 五、四个设计原则

把整套设计浓缩成四条:

### 1. OD 是根

无 active OD 的项目无法回答任何业务问题。OK 让答案带"为什么",OL 让系统能演化,**OD 是唯一的必要条件**。

### 2. Ontology 与 Schema 是两个 level

Schema 是物理事实(表、列、类型),Ontology 是业务事实(业务对象、关系、口径)。一个 OD 对应**有且只有一段** semantic_sql,这段 SQL 可以引用任意多张物理表。物理表是实现细节,Ontology 是业务封装。

### 3. 每个本体单元都有强制双层

任何 OD、OK、OL、Link、Causality、Intent、Keyword 都同时包含**结构层**(给机器)+ **解释层**(给 AI 和人类)。解释层向量化存储,召回时双通道(字面 + 语义)。

> **在自然语言入口处,名字不重要,意思重要。**
> **列名告诉机器;解释告诉 AI。**

### 4. "对"是组织共识,不是客观真理

"早单率是多少"不是个有标准答案的问题 —— 它是 under-determined(欠定)的。早单 = 已确认 / 已支付 / 已发货?分母 = 触达 / 新增 / 目标客户?每组组合都对应一个真实数字,**每个都不是"答案",都是答案集合里的一个解**。

ontology 做的不是 Discovery(发现真理),是 Resolution(指定共识):
- 在多个合法解里选择一个,作为本组织标准
- 锁定这个选择,所有未来查询都用同一选择
- 出错时不是"客观错",是"跟当前共识不一致" → 改 ontology,所有未来查询同步修正

**这是系统真正卖的东西**:不是"AI 更准",是"组织共识可治理"。

---

## 六、三个硬不变量 + 两个未来工作

### 三个硬不变量(架构强制,不可被绕过)

1. **OD 必要性** —— 无 active OD 的项目,Query 工具拒绝执行
2. **OD 1:1 semantic_sql** —— 每个 active OD 有且只有一段 SQL 定义(可引用多张物理表)
3. **OD 不可孤岛** —— 多于一个 active OD 时,任意 active OD 必须通过至少一条 active Link 与另一个 active OD 关联

### 两个未来工作

1. **OL → OK 自动沉淀**:聚类 + AI 提议 + BOE 审核的工作流,目前未实现
2. **解释层版本化**:每个本体单元的描述改动应留下历史,目前未实现

### 与外部生态的关系

- 跟 **OWL / RDF / Semantic Web**:思想同源,工程路径不同(SQL 表 vs 三元组 + SPARQL)
- 跟 **dbt Semantic Layer / Cube**:它们解决 BI 一致性(消费者是 dashboard);text2ontology 解决 AI 答案一致性(消费者是 LLM agent)
- 跟 **LangChain / LlamaIndex**:它们是 LLM 工具链;text2ontology 是 LLM 之外的本体治理基础设施

召回机制(三层级联 + Intent priority + 解释层向量召回)的细节本文不展开 —— 实现见[代码仓库](https://github.com/agentofreef/text2ontology) `recall-server/` 目录。

---

## 七、为什么 7 个概念,而不是 3 个

每个概念对应一个**真实存在、无法回避的设计张力**(见第三节表格)。

概念多不是缺点。**概念少且模糊才是缺点** —— 当一个领域的复杂度无法被合并时,试图合并它只会把复杂度转移到使用者身上。

如果你只记一句话:

> **OD 是根。两层查询是骨架。运行时核心是召回,不是推理。**
> **AI 不是真理来源,是受约束的执行者。**

剩下的概念都是这句话在不同侧面的展开。

---

> 本文是 [text2ontology](https://github.com/agentofreef/text2ontology) 系列内容的一部分:
> - 技术 thesis:[《本体先于查询》](./manifesto.zh.md)
> - 商业 thesis:[《责任即利润率》](./responsibility-as-moat.zh.md)
> - 反方解构:[《AI Agentic Data Analyst 是 2026 年最贵的错觉》](./ai-agentic-illusion.zh.md)
> - 角色定义:[《业务本体工程师》](./business-ontology-engineer.zh.md)
> - **设计哲学**:本文 ★
>
> 本文以 [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/) 授权。

AgentOfReef · 2026-05
