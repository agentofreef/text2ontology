// Builder mode handler additions. The actual SSE entry point is shared with
// handler_agent_lakehouse.go; this file only owns the builder-specific system
// prompt and tool definitions. See plan Step 1.1.
package handler

import (
	"github.com/lakehouse2ontology/llmclient"

	. "github.com/lakehouse2ontology/httputil"
)

// builderSystemPrompt returns the full builder-mode system prompt for a given
// project. Builder mode runs Interview → propose_* → user-confirm cycles
// instead of the lakehouse query/recall flow. The 3-turn server-side guard in
// dispatchTool backs up the interview rule below; both must remain in sync.
func builderSystemPrompt(projectName string) string {
	return `你是 ` + projectName + ` 的本体构造助手 (OD Builder Agent)。

## 第一节：你的工作

你的任务不是回答数据查询，而是帮助用户**构造**项目的本体资产 (OD / 口径 / Link)。
每次会话围绕一个核心 OD 展开。OD 启用后，你会主动追问是否要附加口径或 OD 间 Link，形成级联流。

### 关于 OD 的根本认知（极重要，**绝不能错**）

OD 是这套系统**所有数据查询的唯一入口**。这不是"复杂查询才需要"或"聚合分析才需要"的可选层 ——
**任何项目里的表，要被查询模式 Agent / smartquery / 前端任何下游消费者看到，必须先有 OD**。

**包括但不限于以下场景，全部需要 OD**：
- 单列字典查询（"这个串码存不存在"）
- 简单 SELECT-WHERE（"找到 status=Confirmed 的记录"）
- 聚合 / GROUP BY
- 多表 JOIN
- 任何业务报表

**绝对禁止**告诉用户："你这个场景简单，直接写 SQL 就行了，不需要 OD" ——
说这句话就是误导用户，因为查询模式 Agent **物理上不接受** SQL，只接受
sma rtquery({objects:[odName], ...})；而 smartquery 解析 odName 必须命中
ont_object_type.name。**没 OD 就没有 odName，下游就 100% 查不到这张表**。

唯一不需要 OD 的场景：
- 用户只是想用本系统做"一次性分析"导出，不打算长期用 → 这种情况整个系统就用错了，
  应该让他用通用 SQL 工具（dbeaver / pgadmin），而不是这套本体平台。

所以你看到任何表，**默认都要按"造成 OD"目标推进**，哪怕只有 1 列。
小表也是 OD —— 1 列的"串码字典"也是合法的 OD（kind=dimension）。

---

## 第二节：探索方法论（新增，极重要）

**侦查在前，提问在后**：
- 第一轮并行调用 list(type=tables) + list(type=ods) 获取全局概况。不要先问用户"你的湖仓有哪些表"——自己查完再问有意义的问题。

**批量分析候选表**：
- 用户提到表名后，对该表 + 可能相关的 1-3 张候选表并行调用 inspect(mode=schema, table=…)。
- 利用返回的启发式口径（is_likely_id / is_likely_machine_code / is_likely_timestamp / value_distribution）形成基于真实数据的具体问题，而非抽象问"业务口径"。

**关系发现优先于 propose_link**：
- propose_link 之前**必须**先调用 inspect(mode=fk_candidates, tables=[…])，用 Jaccard 相似度找到 FK 候选列对。
- 不要让用户手动输入 fromPropertyId/toPropertyId UUID——让 list(type=ods) 返回的 property.id 自动填充。

**跨列关键词搜索**：
- 用户提到具体业务值（如 "X11"）时，调用 inspect(mode=value_search, keyword="X11", inTable="order_header") 看哪一列含这个值。

**样本驱动反问**：
- 看到 inspect(mode=schema) 返回 status 列分布 [Confirmed 62%, Partial 15%, Cancelled 12%, Draft 11%]，用真实值反问"哪些算真实订单（Confirmed 还是也包括 Partial）"，而不是抽象问"业务过滤口径"。

---

## 第三节：Readiness 清单（propose_od 之前必须覆盖）

后端有硬性闸门：user 消息少于 3 条时调用 propose_od 会被拒绝。

1. **主表/视图来源**：用 list(type=tables) 列出候选，问用户指认主表
2. **数据粒度**：用 inspect(mode=schema) 看样本行，问一行代表什么（header/line/event/snapshot）
3. **业务过滤口径**：用 inspect(mode=schema) 的 value_distribution 问哪些状态/标志该排除
4. **列选择**：用 inspect(mode=schema) 的列列表问哪些列保留为 properties、哪些不要
5. **命名约定**：OD 英文名（用户写 smartquery 会引用）、property 名是否需要重命名
6. **机器码列**：用 inspect(mode=schema) 的 is_likely_machine_code 标记帮用户确认枚举码列

---

## 第四节：11 个工具快速索引

**统一探索（2 个）：**
- list(type)        — type ∈ {tables, ods, intents, links}：列出实体或湖仓表
- inspect(mode, …)  — mode ∈ {schema, fk_candidates, sql, value_search}：单表/多表/SQL/跨列搜索

**OD 管理（3 个）：**
- propose_od    — 提议新 OD 草稿（pending，用户点"启用"才落库）
- update_od     — 修改已有 OD 字段 / property（pending 或 active 均可）
- delete_od     — 删除 OD（默认级联删 properties / intents / links）

**口径管理（3 个）：**
- propose_intent — 提议新口径草稿（pending，用户点"启用"才落库）
- update_intent  — 修改口径字段 / 关键词
- delete_intent  — 删除口径

**Link 管理（3 个）：**
- propose_link — 提议新 OD 间 join_key Link（pending，用户点"启用"才落库）
- update_link  — 修改 Link 字段 / property anchor
- delete_link  — 删除 Link

---

## 第五节：级联流程

1. 会话开始 → 用户描述业务 → 并行调用 list(type=tables) + list(type=ods) 侦查全局
2. 分析候选表 → inspect(mode=schema) → 问 readiness 清单（≥3 轮）→ propose_od → 用户启用
3. OD 启用成功后 → 问："要不要为 <OdName> 设个口径？(例：sum(订单量) 按月分组)"
   - 用户回 "好" → 进入 propose_intent 流
   - 用户回 "skip" / "算了" → 跳过
4. 口径启用（或跳过）→ 调用 inspect(mode=fk_candidates) 找 FK 候选 → 问："<OdName> 与 <CandidateOd> 的 <col> 看起来能 JOIN，要建 link 吗?"
   - 用户回 "好" → 调用 list(type=ods) 找具体 property.id → propose_link
   - 用户回 "skip" → 结束级联

---

## 第六节：/skip 逃生口

用户随时可以输入 "/skip" 或 "跳过" 或 "算了" 来终止当前问询并结束 session。看到这种字眼立即给一个礼貌的总结收尾，不要继续问。

---

## 第七节：严格约束

- **不要回答数据查询问题**。如果用户问 "X 产品销量是多少" 这类问题，提醒他们切换到查询模式（先确认 OD 已建好）。
- **不要建议用户"直接写 SQL"或"不需要 OD"**。系统下游（查询模式 Agent / smartquery / 前端）100% 走 OD，
  不走原生 SQL；你绕过 OD 给"SQL 解决方案"，等于让用户去用别的工具，违背平台定位。再简单的查询场景，也要按 OD 流程构造。
- **不要直接写库**。所有写动作通过 propose_od / propose_intent / propose_link 提案，由用户在卡片上点"启用"才落库。update_od / update_intent / update_link 同理，修改后需用户确认。
- **propose_od 之前用户消息必须 ≥3 条**。后端硬闸会拒绝，不要尝试绕过。
- **propose_link 之前必须用 inspect(mode=fk_candidates) 拿到 FK 候选，用 list(type=ods) 拿到 property.id**。不能让用户手输 UUID。
- **不要绕过 list(type=ods) 重复造同名 OD**。propose_od 之前先调 list(type=ods) 看是否已有同名/同主表的 OD；如有，告知用户而非新建。
- **必须用 tool call，不能用 markdown 描述提案**。绝对禁止在 assistant text 里输出 'json 代码块' 或 '{"action":"propose_intent",…}' 这类「文本形式的工具调用」。当用户说"好"/"可以"/"请继续"确认后，**必须直接 emit propose_od / propose_intent / propose_link 的 tool_use**，不要先用文字描述「我将为你创建…」再让用户在卡片上看到——前端只对真正的 function_call 渲染确认卡片，文本形式的 JSON 不会触发任何 UI。
- **每轮 user message 后只能要么调工具要么收尾**。如果上一轮 propose_* 用户已确认，本轮要么调下一个 propose_*（按 §5 级联流程），要么调 /skip 收尾——绝不允许「先文字铺垫一段再等用户回 OK」。
`
}

// builderV2Tools returns the 11 builder-mode tools (P5 consolidated 16 → 11):
//   list(type) — replaces list_lakehouse_tables / list_ods / list_intents / list_links
//   inspect(mode, ...) — replaces analyze_table / analyze_relationships / query_data
//   propose/update/delete × {od, intent, link} — kept (different schemas don't merge cleanly)
func builderV2Tools() []llmclient.ToolDef {
	return []llmclient.ToolDef{
		// ── Unified exploration (2) ─────────────────────────────────────────
		{Name: "list", Description: "列出 ontology 实体或湖仓表。type 选择维度：\n• tables — 当前项目湖仓 schema 下的物理表（含行数估算）。会话开头与 list(type=ods) 并行调用，确认可用主表。\n• ods — 项目已有的 OD（含 properties + sourceTable）；propose_link 前必须先 list ods 拿 property.id (UUID)。可选过滤：markFilter / kindFilter / searchName。\n• intents — 口径（含 canonicalMetric / pivot 配置 / 触发词）；propose_intent 前调用避免重复。可选过滤：markFilter / objectId / searchName。\n• links — OD 间 Link（含 fromObject / toObject / cardinality / fkColumn）；propose_link 前调用。", Parameters: M{
			"type":     "object",
			"required": []string{"type"},
			"properties": M{
				"type":       M{"type": "string", "enum": []string{"tables", "ods", "intents", "links"}, "description": "要列出的实体类型"},
				"markFilter": M{"type": "string", "enum": []string{"active", "pending", "all"}, "description": "[type=ods/intents] 过滤激活状态：active(默认)/pending/all"},
				"kindFilter": M{"type": "string", "enum": []string{"entity", "event", "attribute"}, "description": "[type=ods] 按 OD 类型过滤"},
				"objectId":   M{"type": "string", "description": "[type=intents] 按绑定 OD 的 UUID 过滤"},
				"searchName": M{"type": "string", "description": "[type=ods/intents] 按名称子串模糊搜索（ILIKE）"},
			},
		}},
		{Name: "inspect", Description: "深度检查湖仓状态。mode 选择行为：\n• schema — 单表深度分析；返回 **columns_toon**（TOON 表格形式 columns[N]{name,type,nullable,card,uniqRatio,kind,samples}，kind ∈ pk|id|fk|code|enum|ts，samples 为 [v1|v2|v3] 限 80 字符），加上 sampleRows（2 行 × 各列 80 字符截断）+ hypotheses 文本。用 columns_toon 像读 CSV：name 列即列名、kind 标签直接告诉你是主键/外键/枚举/时间，不必再去解析 isLikely* 布尔。用户指认主表后立刻调用，基于具体值发起 readiness 反问。\n• fk_candidates — 多表 FK 发现：对给定表组计算列级 Jaccard 相似度 + 值域重叠，返回最可能的 FK 候选列对（参数 tables ≥ 2 张）。propose_link 之前必须先调用，不要让用户手输 UUID。\n• sql — 对湖仓表执行安全 SELECT 验证数据（参数 sql + 可选 limit）。\n• value_search — 跨列搜索包含特定业务值的列（如 'X11'），找出值在哪一列（参数 keyword + inTable）。", Parameters: M{
			"type":     "object",
			"required": []string{"mode"},
			"properties": M{
				"mode":    M{"type": "string", "enum": []string{"schema", "fk_candidates", "sql", "value_search"}, "description": "检查模式"},
				"table":   M{"type": "string", "description": "[mode=schema] 要分析的表名（不带 schema 前缀）"},
				"tables":  M{"type": "array", "description": "[mode=fk_candidates] 要分析关系的表名列表，至少 2 张最多 10 张", "minItems": 2, "maxItems": 10, "items": M{"type": "string"}},
				"sql":     M{"type": "string", "description": "[mode=sql] 要执行的 SELECT SQL"},
				"keyword": M{"type": "string", "description": "[mode=value_search] 跨列搜索关键词"},
				"inTable": M{"type": "string", "description": "[mode=value_search] 搜索的目标表名"},
				"limit":   M{"type": "integer", "description": "[mode=sql/value_search] 返回行数上限，默认 50"},
			},
		}},

		// ── OD CRUD (3) — propose/update/delete; list_ods folded into list ──
		{Name: "propose_od", Description: "在 ≥3 轮访谈后，把业务信息整合成 OD 草稿（pending 状态，mark=false）。后端会记录 origin='builder'，前端渲染可编辑卡片，用户点\"启用\"才落库。返回 pending_confirmation: true。", Parameters: M{
			"type":     "object",
			"required": []string{"name", "semanticSql", "properties"},
			"properties": M{
				"name":        M{"type": "string", "description": "OD 英文名（smartquery 中会引用，如 \"Order\"）"},
				"kind":        M{"type": "string", "enum": []string{"entity", "event", "attribute"}, "description": "OD 类别，默认 entity"},
				"semanticSql": M{"type": "string", "description": "湖仓 SQL。引用 staging 物理表用占位符 staging.，**表名必须双引号**（CamelCase 不引号会被 Postgres 折成小写）。例：FROM staging.\"Order Details\" od JOIN staging.\"Orders\" o ON od.\"OrderID\"=o.\"OrderID\"。服务端会把 staging. 替换成项目真实 lakehouse_schema 并保留引号。**不要**直接写 proj_xxx 实际 schema 名。"},
				"description": M{"type": "string", "description": "OD 业务描述"},
				"properties": M{"type": "array", "minItems": 1, "items": M{
					"type":     "object",
					"required": []string{"name", "dataType", "sourceColumn"},
					"properties": M{
						"name":          M{"type": "string", "description": "property 英文名（用户写 smartquery 会引用，不带对象前缀）"},
						"dataType":      M{"type": "string", "description": "数据类型，如 text/int/numeric/date/timestamp"},
						"sourceColumn":  M{"type": "string", "description": "对应 semanticSql 投影中的物理列名"},
						"isFilterable":  M{"type": "boolean", "description": "是否允许在 smartquery filter 中使用，默认 false"},
						"isGroupable":   M{"type": "boolean", "description": "是否允许在 smartquery groupBy 中使用，默认 false"},
						"isMachineCode": M{"type": "boolean", "description": "是否为机器码列（如 GEO_CODE='1'），默认 false"},
					},
				}},
			},
		}},
		{Name: "update_od", Description: "修改已有 OD（pending 或 active 均可）的字段、properties，或新增/删除 properties。edits 仅需包含要改的字段（partial update）。", Parameters: M{
			"type":     "object",
			"required": []string{"objectId"},
			"properties": M{
				"objectId": M{"type": "string", "description": "要修改的 OD UUID"},
				"edits": M{
					"type":        "object",
					"description": "OD 顶级字段的修改：可含 name / kind / semanticSql / description / sourceTable（部分更新，未包含的字段不变）",
				},
				"propertyEdits": M{
					"type":        "array",
					"description": "修改已有 property：每项含 id（必填）+ 要改的字段（partial update）",
					"items":       M{"type": "object"},
				},
				"propertyAdds": M{
					"type":        "array",
					"description": "新增 property：每项含 name / dataType / sourceColumn（必填）及可选字段",
					"items":       M{"type": "object"},
				},
				"propertyDeletes": M{
					"type":        "array",
					"description": "要删除的 property UUID 列表",
					"items":       M{"type": "string"},
				},
			},
		}},
		{Name: "delete_od", Description: "删除一个 OD（pending 或 active）。默认级联删除该 OD 的所有 properties、intents 和 links。", Parameters: M{
			"type":     "object",
			"required": []string{"objectId"},
			"properties": M{
				"objectId": M{"type": "string", "description": "要删除的 OD UUID"},
				"cascade":  M{"type": "boolean", "description": "是否级联删除关联实体（properties / intents / links），默认 true"},
			},
		}},

		// ── Intent CRUD (3) — propose/update/delete; list_intents folded into list ──
		{Name: "propose_intent", Description: "在已激活 OD 上提议一个口径（查询模板，pending 状态，mark=false）。triggerKeywords 仅存在工具结果 JSON 中，启用时才会写 lakehouse_keyword。返回 pending_confirmation: true。", Parameters: M{
			"type":     "object",
			"required": []string{"objectId", "name", "canonicalMetric", "autoGroupBy", "triggerKeywords"},
			"properties": M{
				"objectId":        M{"type": "string", "description": "口径绑定的已激活 OD 的 UUID"},
				"name":            M{"type": "string", "description": "口径名称，如 Order.Quantity"},
				"canonicalMetric": M{"type": "string", "description": "smartquery.metric 的固定值，如 sum(Order_Quantity)"},
				"canonicalFilters": M{"type": "array", "items": M{
					"type": "object",
					"properties": M{
						"prop":  M{"type": "string"},
						"op":    M{"type": "string"},
						"value": M{"type": "string"},
					},
				}, "description": "口径永远追加的过滤条件"},
				"autoGroupBy":           M{"type": "array", "items": M{"type": "string"}, "description": "口径必须保留的 groupBy 维度（后端强制注入）"},
				"pivotOn":               M{"type": "string", "description": "结果 pivot 的列名（可选）"},
				"pivotValues":           M{"type": "array", "items": M{"type": "string"}, "description": "匹配 pivotOn 的实际数据值，固定列顺序"},
				"pivotColumnLabels":     M{"type": "array", "items": M{"type": "string"}, "description": "与 pivotValues 平行的输出列名（默认= pivotValues）"},
				"pivotTotalLabel":       M{"type": "string", "description": "求和列名，默认 Total"},
				"pivotWithPercent":      M{"type": "boolean", "description": "是否为每个 value 列追加占比列"},
				"pivotAppendGrandTotal": M{"type": "boolean", "description": "是否追加首列值为「合计」的汇总行"},
				"triggerKeywords":       M{"type": "array", "items": M{"type": "string"}, "minItems": 1, "description": "命中此口径的触发词列表（启用时才会写 lakehouse_keyword）"},
			},
		}},
		{Name: "update_intent", Description: "修改已有口径的字段、新增或删除触发关键词（pending 或 active 均可，partial update）。", Parameters: M{
			"type":     "object",
			"required": []string{"intentId"},
			"properties": M{
				"intentId": M{"type": "string", "description": "要修改的口径 UUID"},
				"edits": M{
					"type":        "object",
					"description": "口径顶级字段的修改（partial update）：可含 name / canonicalMetric / canonicalFilters / autoGroupBy / pivotOn 等",
				},
				"keywordAdds": M{
					"type":        "array",
					"description": "新增触发关键词列表（字符串数组）",
					"items":       M{"type": "string"},
				},
				"keywordDeletes": M{
					"type":        "array",
					"description": "要删除的触发关键词列表（字符串数组）",
					"items":       M{"type": "string"},
				},
			},
		}},
		{Name: "delete_intent", Description: "删除一个口径（同时删除其触发关键词 lakehouse_keyword 记录）。", Parameters: M{
			"type":     "object",
			"required": []string{"intentId"},
			"properties": M{
				"intentId": M{"type": "string", "description": "要删除的口径 UUID"},
			},
		}},

		// ── Link CRUD (3) — propose/update/delete; list_links folded into list ──
		{Name: "propose_link", Description: "提议两个 OD 之间的 join_key Link（pending 状态，mark=false）。fromPropertyId 和 toPropertyId 必须是 list_ods 返回的 property.id (UUID)。必须先调用 analyze_relationships 找到 FK 候选。返回 pending_confirmation: true。", Parameters: M{
			"type":     "object",
			"required": []string{"fromObjectId", "toObjectId", "fromPropertyId", "toPropertyId", "fkColumn", "linkName"},
			"properties": M{
				"fromObjectId":   M{"type": "string", "description": "源 OD 的 UUID"},
				"toObjectId":     M{"type": "string", "description": "目标 OD 的 UUID"},
				"fromPropertyId": M{"type": "string", "description": "源端 property 的 UUID（来自 list_ods）"},
				"toPropertyId":   M{"type": "string", "description": "目标端 property 的 UUID（来自 list_ods）"},
				"fkColumn":       M{"type": "string", "description": "外键物理列名"},
				"cardinality":    M{"type": "string", "enum": []string{"many_to_one", "one_to_many", "many_to_many"}, "description": "关系基数，默认 many_to_one"},
				"linkName":       M{"type": "string", "description": "Link 名称"},
				"description":    M{"type": "string", "description": "Link 业务描述"},
			},
		}},
		{Name: "update_link", Description: "修改已有 Link 的字段或 property anchor（fromPropertyId / toPropertyId）（pending 或 active 均可，partial update）。", Parameters: M{
			"type":     "object",
			"required": []string{"linkId"},
			"properties": M{
				"linkId": M{"type": "string", "description": "要修改的 Link UUID"},
				"edits": M{
					"type":        "object",
					"description": "Link 顶级字段的修改（partial update）：可含 linkName / description / cardinality / fkColumn",
				},
				"propertyAnchorEdits": M{
					"type":        "object",
					"description": "修改 property anchor：可含 fromPropertyId（string）和/或 toPropertyId（string）",
					"properties": M{
						"fromPropertyId": M{"type": "string", "description": "新的源端 property UUID"},
						"toPropertyId":   M{"type": "string", "description": "新的目标端 property UUID"},
					},
				},
			},
		}},
		{Name: "delete_link", Description: "删除一个 OD 间 Link（pending 或 active）。", Parameters: M{
			"type":     "object",
			"required": []string{"linkId"},
			"properties": M{
				"linkId": M{"type": "string", "description": "要删除的 Link UUID"},
			},
		}},
	}
}
