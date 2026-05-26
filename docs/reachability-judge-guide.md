# 任务可达器(Reachability Judge)

> 也叫**任务触达器**。系统级机制,**在任何查询发生之前**判定"这个问题能不能从已授权的口径回答"。可答 → 放行;不可答 → 停下、说清楚为什么,以及缺什么。

本文分两部分:

- **第一部分:理念** —— 为什么会有这个东西,以及它在系统里承担什么样的角色。读完你会知道:**它和「LLM 不写 SQL」是同一件事的两面。**
- **第二部分:技术** —— 它具体是怎么实现的、跑在哪里、看哪些表 / 文件、它的 verdict 怎么被消费。

---

## 第一部分:理念

### 1. 它解决的问题:LLM 没有 Oracle,所以不能"试着答"

[manifesto](manifesto/manifesto.en.md) 里讲过:在数据分析这件事上,LLM **没有 oracle**——你问"早单率多少",它答 "12.3%",你没有自动化机制去判断这个 12.3% 是对的。模型不收敛,因为没有判错信号。

主流 Text2SQL 怎么处理?**让模型试一下、跑一下、看看结果像不像。** 这是赌博,不是工程。

text2ontology 的回应是把"oracle 缺失"翻译成"边界明确":**我们不让 LLM 在边界之外回答**。每个能被回答的问题,必须落在「已授权口径 + 它们的参数」张成的空间里。落在空间里的,答得出且每个数字都能 trace 到一条策展过的口径;落在空间外的,**不要试**——直接说"这不在我已授权的范围内,具体缺这个那个"。

任务可达器就是**那个"是否在范围内"的判定**。

### 2. 它不是过滤器,是契约

很多系统也有"reachability"——但通常是**过滤器**:模型先生成答案,然后某个 validator 检查、不通过就改写一下、再不行就降级输出。这种用法的弱点:**生成本身已经发生了**,validator 只是事后清理。

任务可达器**先于查询**:

```
用户问题
   ↓
LLM 一次分解 → (这个问题需要哪些维度/筛选/度量?每个的形状是什么?)
   ↓
Judge (纯 Go,确定性) → (这些需求,授权口径能不能逐一覆盖?)
   ↓
  feasible? ─┬─ yes → 继续到 ReAct 循环、smartquery、回答
              └─ no  → 立刻停止;把"不可答的理由 + 缺什么"作为最终回答
```

这是**契约**而不是过滤器:模型生成 SQL 这一步**根本没有发生**,因为系统在更早的关口判了。

理念落点:**真正的边界判定必须在生成之前,而不是生成之后。**

### 3. 它把"失败"变成"地址"

LLM 系统里最让人崩溃的失败,是含糊的失败——"我没法回答""技术限制""这个问题比较复杂"。用户不知道是模型不行、是数据不对、还是问错了。

任务可达器拒绝模糊。它的 verdict 是**结构化**的:

- `feasible: false` —— **二元的**,整条问题不可达(任何一个维度/筛选没覆盖,整体就不可达。不做"半个答案"——曾经的妥协做法,后来被废弃了,因为半个答案比没答案更危险)
- `requirements[]` —— 把问题拆出来的每一项,标注 `covered: true/false`
- 每个 uncovered 项带 `missing_note`:**"没有任何已授权口径提供「品牌占比」这个维度"**
- 每个 covered 项带 `covered_by[]`:**"由 `Order.Quantity.Distribution`、`Order.Quantity.BrandShareInGen` 这两条口径覆盖"**

所以一次"不可达"=一个**地址**:

- 缺哪个维度
- 哪几条口径离它最近
- 该怎么补(在哪条口径上加什么参数 / 加哪条新口径)

**Curator(本体策展人)看到这个 verdict 就知道下一步该改什么。** 这就是 manifesto 里说的"每个错答案都有一个地址"在运行时的兑现机制。

### 4. 它和「口径」是同一件事的两面

- **口径** = 我们**能**说什么的策展白名单
- **任务可达器** = 我们说话之前**先确认我们能说**的关口

这两个一起才是闭环:

```
口径(策展)              ← Curator 改这里
   ↓ 定义了"能回答什么"
任务可达器(运行时)        ← 用户问题打这里
   ↓ 拒绝越界
SmartQuery 引擎            ← 真正发 SQL
   ↓
回答
```

去掉任一半,系统都退化:

- **只有口径,没有可达器**:LLM 选了一条沾边的口径就硬答,生成的 SQL 经过编译器但**语义已经走偏**——口径里没有「品牌」维度,LLM 用 `ORDER_TYPE` 凑数,用户拿到一个数但**不知道这个数不是他要的**。又回到 oracle 问题。
- **只有可达器,没有口径**:可达器没东西判,所有问题都 `feasible: false`。

理念落点:**口径是"白名单",可达器是"门卫"。两个一起才是封闭逻辑。**

### 5. 为什么它必须是确定性的

可达器内部用了 LLM(做问题分解),但**最终的 verdict 是 Go 写的、纯函数、可单测**。

这是有意为之:

- 分解(把人话变成"需要哪些维度")是 LLM 擅长的——开放生成。
- 判定(把需求和授权集做匹配)**不允许 LLM 干**——它会忘、会瞎说、会"差不多 covered"。

具体的护栏:

- LLM 即使说"我觉得 covered_by 是口径 `X`",代码会去授权口径集里**实际查 X 在不在**。不在 → 拒绝这条 covered。
- LLM 即使整句声称 feasible,代码会**重新跑** name 匹配。两者都通不过 → 强制 uncovered。

理念落点:**让模型做生成,不让模型做裁判。** 裁判必须是可单测的代码。

### 6. 它怎么影响 Curator 的工作流

任务可达器把 Curator 的工作变成有反馈的工程:

1. **用户问"X 品牌的早单率"** → 可达器报 `不可达 / 缺「品牌」维度`。
2. **Curator 看到 verdict + 候选口径列表**(可达器告诉你最相近的几条口径) → 知道是给 `早单率` 口径加一个 `brand` 参数,还是新建一条专门的 `早单率.按品牌` 口径。
3. **改了之后**,同一个问题再问,**可达器自动放行**——不需要改提示词、不需要重训模型。

理念落点:**"模型答错了"变成"本体上少了什么",这个等价转换是整套范式的核心。**

---

## 第二部分:技术

### 1. 文件与位置

| 角色 | 文件 |
|---|---|
| 类型 / 状态机 / Mission 顶层 | `pkg/mission/mission.go` |
| Reachability 原语 + Judge (纯函数) | `pkg/mission/reachability.go` |
| Coverage 算法 (口径 ↔ 需求 匹配) | `pkg/mission/coverage.go` |
| Agent 侧 Judge handler (LLM 分解 + 调 Judge) | `services/agent-server/handler/reachability_judge.go` |
| `declare_capability_gap` 工具 | `services/agent-server/handler/tool_declare_gap.go` |
| Mission 持久化 + 状态写入 | `pkg/mission/store.go` (写到 `ont_mission` 表) |
| 形状词汇(数据驱动) | `lakehouse_shape_capability` 表 |
| 审计日志 | `capability_gap_log` 表(INSERT-only) |

特性开关: `USE_MISSION_ACT`(off 时整套机制不挂上,系统退化到纯 ReAct,**不推荐生产**)。

### 2. 关键类型

```go
type Mission struct {
    MissionID, ThreadID, ProjectID, Question string
    Recall          Recall
    Decomposition   []DecompItem
    Tasks           []Task
    Reachability    *ReachabilityVerdict  // ← 可达器结论
    Status          MissionStatus         // active|complete|partial|unanswerable
    ...
}

type ReachabilityVerdict struct {
    Feasible     bool                  // 二元、整问题
    Requirements []RequirementCoverage // 每个分解项的覆盖明细
    Reason       string                // 人话理由
    Kind         string                // "gap" | "clarify"
}

type RequirementCoverage struct {
    Dimension   string   // e.g. "品牌"
    Kind        string   // metric | dimension | filter
    Shape       string   // scalar | group-by | range | enum | 单月前缀 | 年范围 | ...
    Why         string   // 为什么问题需要这个
    Covered     bool
    CoveredBy   []string // 覆盖它的口径名(可能多个)
    MissingNote string   // 没覆盖时:为什么没覆盖
}

type GapKind string   // no_param | shape_unsupported | no_data
```

### 3. 运行流程(USE_MISSION_ACT=on)

```
POST /agent/lakehouse (lakehouse agent SSE)
  │
  ├─ 1. 召回 (recall-server) → 候选口径集 (metricIntents) + recall context
  │
  ├─ 2. Reachability gate (services/agent-server/handler/reachability_judge.go)
  │     │
  │     ├─ 一次 LLM 调用:question → [DecompItem],每项带 (Name, Kind, Shape, WhyRequired)
  │     │     形状词汇从 lakehouse_shape_capability 加载(数据驱动,不写死)
  │     │
  │     ├─ pkg/mission.Judge(decomposition, candidateIntents) → ReachabilityVerdict
  │     │     │
  │     │     └─ 对每个 dim/filter 需求,跑 CoveringIntents:
  │     │          ◦ 名字匹配 (property name 归一)
  │     │          ◦ Shape 匹配 (Intent param 声明的 shape ⊇ 需求 shape)
  │     │
  │     ├─ buildVerdictFromLLMHints:确定性护栏
  │     │     ◦ LLM 给的 covered_by 名字必须在真实授权口径集合里 → 否则拒绝
  │     │     ◦ LLM 说 covered 但 covered_by 空 → 回退到声明性 name 匹配
  │     │     ◦ 都不过 → 强制 uncovered
  │     │
  │     └─ 写 Mission.Reachability + ont_mission(best-effort,不阻塞)
  │
  ├─ 3. Verdict 分支:
  │     │
  │     ├─ Feasible → 返回 "" → 进入 ReAct 循环 (smartquery 工具被调)
  │     │
  │     └─ Infeasible → 渲染机器生成的 finalAnswer:
  │           "你的问题需要按【品牌】筛选,但没有任何已授权的口径提供这个维度。
  │            已检查的相关口径:[列表 + 各自的不足]
  │            修复方向:在「早单率」口径上添加 brand 参数,
  │                     或新建「早单率.按品牌」口径"
  │           → SSE 把这个流出去,本轮结束,**绝对不进 ReAct**
  │
  └─ 4. (即便 feasible) ReAct 循环里,LLM 仍可主动声明 gap:
        tool: declare_capability_gap(args)
        ├─ VerifyNoParamGap (二次确认 LLM 没瞎说,有则 gate_rejected)
        ├─ 写 capability_gap_log (audit sink, INSERT-only)
        └─ 终止本轮 (terminal=true, finalAnswer 由模板生成)
```

### 4. 数据库 schema(关键三表)

```sql
-- 1. ont_mission — 每个 turn 一行,可达器 verdict 落这里
CREATE TABLE ont_mission (
    id           text PRIMARY KEY,
    thread_id    text NOT NULL,
    project_id   uuid NOT NULL,
    question     text NOT NULL,
    reachability jsonb,           -- ReachabilityVerdict 完整体
    status       text,            -- active | complete | partial | unanswerable
    ...
);

-- 2. lakehouse_shape_capability — 形状词汇,数据驱动
CREATE TABLE lakehouse_shape_capability (
    name        text PRIMARY KEY, -- "年范围", "单月前缀", "等值", "枚举集合", ...
    description text,             -- LLM 看的解释
    examples    text[]            -- LLM 看的样例
);
-- Judge 只对 Name 做相等比较,Go 代码不关心具体值;Curator 直接改这张表
-- 就能调整可达器的形状判定颗粒度,不需要发版。

-- 3. capability_gap_log — INSERT-only 审计沉淀
CREATE TABLE capability_gap_log (
    id              text PRIMARY KEY,
    project_id      uuid,
    thread_id       text,
    question        text,
    missing_dim     text,
    gap_kind        text,           -- no_param | shape_unsupported | no_data
    fix_direction   text,
    candidate_intents jsonb,        -- 跟它最像的几条口径
    created_at      timestamptz
);
-- Curator 周期性扫这张表:看用户反复在问什么、但还没有口径覆盖。
-- 这是本体策展的反馈循环。
```

### 5. 形状判定的颗粒度

Pure name-match 在早期版本就用过,**会假阳性**。典型场景:

- 用户问"2024 年的销售额"(需要 `年范围` 形状)
- 某条口径有 `month_prefix` 参数(单月前缀,如 "2024-03")
- 名字勉强能套 → 但实际答这个问题要打 12 次 query

所以现在 Judge 看 `Shape`:`年范围` ⊃ `单月前缀`,前者覆盖不了后者(也覆盖不了反向),verdict = uncovered。

LLM 在分解阶段拿到完整的 Intent 参数 schema(`property + op + type + description`),所以它能写出准确的 shape;Go judge 做最终判定。

### 6. 二元 vs 半答案

早期版本有"半答案"模式:某些维度可达,就回答可达的部分,unreachable 的部分注明。**已废弃**。原因:

- 用户读到一个数,**很难知道这个数其实只回答了问题的一半**。
- 半答案给了一个错觉:系统"勉强能回答",于是 Curator 不会去补本体。**反馈循环被破坏。**
- 现在的规则:**任何一个 dim/filter 没覆盖 → 整问题 unreachable**。要么完整答,要么不答。这种"严苛"是有意保留的,因为它让本体的不完备**有可见的痛**,Curator 才会补。

### 7. 跟前端的对接

- agent-server 的 SSE 在 verdict=infeasible 时直接流 finalAnswer + 一个结构化 payload:
  ```json
  {
    "kind": "reachability_failure",
    "verdict": { ... ReachabilityVerdict 完整体 ... },
    "candidate_intents": [{name, why_insufficient}, ...]
  }
  ```
- 前端 `MissionLedger.tsx` 渲染「任务可达器」面板:
  - 顶部:绿/红 verdict + 一句话理由
  - 每个 Requirement 一行(覆盖/未覆盖 + 来源口径 / 缺失说明)
  - 不可达时:候选口径 + fix_direction
- GET `/api/ontology/lakehouse-missions?threadId=...` 把历史 Mission 拉出来,供回看。

### 8. 与「口径」编辑器的反馈闭环

Curator 在 `/ontology/lakehouse-metrics` 改口径(加参数 / 改 shape / 新建口径) → 下一次同一问题 → 可达器自动放行。**不需要重启服务、不需要清缓存、不需要改提示词。** 这就是把"模型层的失败"转换成"本体层的可修复 bug"。

### 9. 调试入口

| 想看什么 | 去哪里 |
|---|---|
| 单次问题的 verdict | 前端 MissionLedger 面板;或 `SELECT reachability FROM ont_mission WHERE id=...` |
| 形状词汇 | `SELECT * FROM lakehouse_shape_capability` |
| 历史 gap 沉淀 | `SELECT * FROM capability_gap_log ORDER BY created_at DESC` |
| Judge 单测 | `go test ./pkg/mission -run TestReachability...` (纯函数,无需 DB) |
| LLM 分解 prompt | `services/agent-server/handler/reachability_judge.go` 中的 prompt 字符串 |

### 10. 已知限制

- **依赖授权口径的元信息质量**:口径参数描述写得越清楚,LLM 分解越准,Judge 越精。"看起来对、其实模糊"的口径会拖低 verdict 质量。
- **不处理排序 / limit 这类"答案形态偏好"**:Judge 只判 dim/filter/metric。"top 10 vs all"不在它的工作范围。
- **跨 OD 维度仍依赖 `ont_causality(join_key)`**:可达器知道"需要 PRODUCT.brand",会去授权口径里找谁带这个,但跨 OD 的可达性最终仍由 SmartQuery 的 JOIN 路径解析。如果 `ont_causality` 缺边,Judge 可能把它当 uncovered。

---

## 总结

| 一句话 |
|---|
| **理念**:LLM 没有 oracle,所以它不能"试着答"。我们在 LLM 发挥之前,先用确定性代码判定"这个问题在不在我已授权的口径空间里"。**口径** 是白名单,**任务可达器** 是门卫。两者一起才闭环。 |
| **技术**:`pkg/mission.Judge` 是纯函数 Go 判定器;`reachability_judge.go` 一次 LLM 调用做问题分解,把分解 + 授权口径喂给 Judge 出 verdict;Feasible 放行进 ReAct,Infeasible 直接渲染机器答(带"缺什么、相近口径有哪些"的地址)。Curator 改本体 → 下次问题自动放行,反馈循环就此闭合。 |
