package recall

import (
	"fmt"
	"strings"
)

// FormatContext renders a RecallResult into a markdown context block
// suitable for injection into the LLM system/user prompt.
func FormatContext(result RecallResult, tokens []string, question string) string {
	if !result.HasMatches {
		tokStr := strings.Join(tokens, "、")
		return fmt.Sprintf("## 上下文识别结果\n\n用户问题: %q\nAI 理解的关键词: [%s]\n未能从知识库匹配到以上关键词。\n\n请向用户澄清其问题意图，并提示可用的查询方向。不要猜测或编造数据对象和指标。",
			question, tokStr)
	}

	var sb strings.Builder
	sb.WriteString("## 已识别的数据上下文\n\n")

	isLookup := question == "lookup"

	// ── Metric Intents (canonical query templates) ──
	// Rendered FIRST (before Od blocks) because intents carry the decision:
	// LLM should copy canonical_metric/filters/auto_group_by verbatim into
	// smartquery args and only append user's additional dimensions. This
	// short-circuits the "should I filter or group Order_Type?" ambiguity.
	// Collect all recalled Od names (OdBlocks + DirectOds) — Intent's objects
	// field must be union of Intent.object + recalled Ods, otherwise dense SQL
	// misclassifies cross-Od filter props (e.g. PRODUCT.BRAND with objects=[ORDER]
	// → resolver falsely assumes BRAND is on ORDER → "column ORDER.BRAND does
	// not exist").
	recalledOds := collectRecalledOds(result)

	if len(result.MetricIntents) > 0 {
		sb.WriteString("### 🎯 查询指标（Metric）\n\n")
		sb.WriteString("以下指标已匹配到用户问题。**默认走 Mode B（自由组合）**：取指标的口径在 smartquery **顶层**自己拼一次查询；只有确实需要指标模板自带的 pivot 透视时，才用 intent 走 Mode A。\n\n")
		for _, mi := range result.MetricIntents {
			formatMetricIntent(&sb, mi, recalledOds)
		}
		sb.WriteString("**组合规则（Mode B，默认）**：\n")
		sb.WriteString("- 把这些字段写在 smartquery **顶层**（odName/metric/filters/groupBy），**不要**放进 `params`\n")
		sb.WriteString("- metric = 指标的 canonical_metric（函数名一字不差照抄，**严禁**替换为其它聚合）\n")
		sb.WriteString("- filters = 指标的 canonical_filters **+** 用户提到的其它筛选条件\n")
		sb.WriteString("- groupBy = 指标的 auto_group_by **+** 用户提到的其它分组维度（auto_group_by 不可省略）\n")
		sb.WriteString("- objects 见上方指标块里给出的成品列表，不要自行删减\n")
		sb.WriteString("- 回复时套用 response_template\n")
		sb.WriteString("**仅当需要模板内置 pivot 时走 Mode A**：填 `intent` = 指标 name，按其 parameters 列表填 `params`；此时不要再在顶层写 metric/filters/groupBy（指标已包含）。\n\n")
	}

	// ── Od blocks (keyword → property → Od) ──
	if len(result.OdBlocks) > 0 {
		sb.WriteString("### 数据对象（Od）\n\n")
		for _, blk := range result.OdBlocks {
			formatOdBlock(&sb, blk, true, isLookup)
		}
	}

	// ── Direct Od blocks (name match fallback) ──
	if len(result.DirectOds) > 0 {
		for _, blk := range result.DirectOds {
			formatOdBlock(&sb, blk, false, isLookup)
		}
	}

	// ── Ok entries (non-property knowledge, fallback) ──
	if len(result.OkEntries) > 0 {
		sb.WriteString("### 知识参考（Ok）\n\n")
		for _, e := range result.OkEntries {
			sb.WriteString(fmt.Sprintf("`Ok:%s` **%s**", e.Title, e.Title))
			if e.Summary != "" {
				sb.WriteString(": " + e.Summary)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// ── Ol entries (confirmed learned facts, matched via tags/vector) ──
	if len(result.OlEntries) > 0 {
		sb.WriteString("### 相关经验知识（Ol）\n\n")
		sb.WriteString("以下为对话中习得的可复用业务规则/模式，请优先参考以避免重复提问或犯相同错误：\n\n")
		for _, e := range result.OlEntries {
			title := e.Title
			if title == "" {
				title = "(无标题)"
			}
			sb.WriteString(fmt.Sprintf("`Ol:%s` **%s**: %s\n", title, title, e.Summary))
			if len(e.Tags) > 0 {
				sb.WriteString(fmt.Sprintf("  标签: %s\n", strings.Join(e.Tags, "、")))
			}
			if len(e.Tokens) > 0 {
				tierInfo := e.Tier
				if e.Tier == "VEC" {
					tierInfo = fmt.Sprintf("VEC, %.2f", e.Score)
				}
				sb.WriteString(fmt.Sprintf("  ← 命中 token: %s (%s)\n", strings.Join(e.Tokens, ", "), tierInfo))
			}
		}
		sb.WriteString("\n")
	}

	// ── Filter value hints + GROUP BY hints ──
	// Recall reports facts, it does NOT decide query shape. Column-ref and
	// value-alias keywords on the same property are both emitted — the LLM
	// (or matched Intent) decides filter-vs-groupBy. Critically, value hints
	// always carry the canonical DB literal (kw.Keyword), since SQL WHERE
	// needs "N.A." even when the user typed "NA地区".
	type filterHint struct{ odName, propName, dbValue, aliasToken string }
	type groupByHint struct{ odName, propName, matchedToken string }
	type ilikeHint struct {
		odName, propName, token string
		valueCount              int
	}
	var hints []filterHint
	var gbHints []groupByHint
	var ilikeHints []ilikeHint
	gbSeen := map[string]bool{}
	for _, blk := range result.OdBlocks {
		for _, p := range blk.MatchedProps {
			dn := p.DisplayName
			if dn == "" {
				dn = p.Name
			}
			for _, kw := range p.Keywords {
				// FUZZY_LIKE = recall collapsed > 4 fuzzy value hits on this
				// column. Surface as an ILIKE recommendation, not a value row.
				if kw.Tier == "FUZZY_LIKE" {
					ilikeHints = append(ilikeHints, ilikeHint{
						odName: blk.Name, propName: dn,
						token: kw.MatchedToken, valueCount: kw.FuzzyValueCount,
					})
					continue
				}
				if isColumnRef(kw, p.Name, dn) {
					key := blk.Name + "." + dn
					if !gbSeen[key] {
						gbSeen[key] = true
						gbHints = append(gbHints, groupByHint{blk.Name, dn, kw.MatchedToken})
					}
				} else {
					hints = append(hints, filterHint{blk.Name, dn, kw.Keyword, kw.MatchedToken})
				}
			}
		}
	}

	// Render ILIKE recommendations — emitted whenever recall collapsed > 4
	// fuzzy value hits on a column. The LLM must NOT enumerate the values; it
	// should emit a `contains` filter on the column instead so SQL generates
	// `WHERE col ILIKE '%token%'` and PG matches all candidates server-side.
	if len(ilikeHints) > 0 {
		sb.WriteString("### 🔍 模糊匹配建议（FUZZY_LIKE，禁止枚举值）\n\n")
		sb.WriteString("以下 token 在对应列上有 >4 个模糊值候选，**不要**逐个枚举写入 filters。请在 smartquery.filters 中改用 `contains`（substring ILIKE）：\n\n")
		sb.WriteString(fmt.Sprintf("ilike[%d|]{token|od|prop|valueCount|recommendedFilter}:\n", len(ilikeHints)))
		for _, h := range ilikeHints {
			rec := fmt.Sprintf(`{"prop":"%s","op":"contains","value":"%s"}`, h.propName, h.token)
			sb.WriteString(fmt.Sprintf("  %s|%s|%s|%d|%s\n",
				toonVal(h.token), toonVal(h.odName), toonVal(h.propName),
				h.valueCount, toonVal(rec)))
		}
		sb.WriteString("\n")
	}

	// Render filter hints — alias → DB literal mapping. Always emitted (even
	// when the same property also has a column-ref hint) because: (a) SQL WHERE
	// needs the DB literal; (b) response phrasing ("北美（N.A.）") benefits from
	// the alias↔canonical pairing.
	if len(hints) > 0 {
		sb.WriteString("### 识别到的筛选值（别名 → DB 值映射）\n\n")
		sb.WriteString("SQL 字面量**必须**用 `dbValue`，不要用 `aliasToken`（后者只是用户的说法，数据库里不存在）。\n\n")
		sb.WriteString(fmt.Sprintf("filters[%d|]{od|prop|dbValue|aliasToken}:\n", len(hints)))
		for _, h := range hints {
			sb.WriteString(fmt.Sprintf("  %s|%s|%s|%s\n",
				toonVal(h.odName), toonVal(h.propName), toonVal(h.dbValue), toonVal(h.aliasToken)))
		}
		sb.WriteString("\n")
	}

	// Render groupBy hints.
	if len(gbHints) > 0 {
		sb.WriteString("### 识别到的列引用（可作为 groupBy 维度）\n\n")
		sb.WriteString(fmt.Sprintf("groupBy[%d|]{token|od|prop}:\n", len(gbHints)))
		for _, h := range gbHints {
			sb.WriteString(fmt.Sprintf("  %s|%s|%s\n", toonVal(h.matchedToken), toonVal(h.odName), toonVal(h.propName)))
		}
		var gbCols []string
		for _, h := range gbHints {
			gbCols = append(gbCols, fmt.Sprintf("%q", h.propName))
		}
		sb.WriteString(fmt.Sprintf("→ 建议 `groupBy: [%s]`\n\n", strings.Join(gbCols, ", ")))
	}

	// Combined guidance — emitted whenever recall produced filter or groupBy
	// hints, reminding the LLM the two are NOT mutually exclusive.
	if len(hints) > 0 || len(gbHints) > 0 {
		sb.WriteString("### smartquery 构造规则（强制）\n\n")
		sb.WriteString("**filters 与 groupBy 不互斥，请同时返回**：\n")
		sb.WriteString("- `filters` — 承载上文「筛选值」所有行（`prop`、`op`、`dbValue`），用于 WHERE 定位具体记录\n")
		sb.WriteString("- `groupBy` — 承载上文「列引用」所有列 + `filters` 中命中的同一 prop（即使该 prop 已被 filter，也要列入 groupBy，保证结果里出现该维度列，便于用户读出语义和做对比）\n")
		sb.WriteString("- 例：用户问「NA地区在所有GEO中的占比」→ `filters:[{prop:GEO,op:=,value:\"N.A.\"}]` **且** `groupBy:[\"GEO\", ...]`，全局分母由指标的 `pivot_percent_scope=global` 处理\n")
		sb.WriteString("- 仅当用户问题**完全不涉及**某类维度时（如纯粹「共有多少订单」无任何切片意图），才省略 groupBy\n\n")
	}

	// ── Lookup guidance (only for pre-processing recall, not for lookup tool itself) ──
	if len(result.OdBlocks) > 0 && !isLookup {
		sb.WriteString("### 操作提示\n\n")
		sb.WriteString("以上属性描述已截断。调用 **lookup** 工具可获取完整的属性定义、Ok 知识条目和业务规则。\n\n")
	}

	// ── Ambiguity warning (genuine cross-Od conflicts needing user clarification) ──
	if len(result.Ambiguities) > 0 {
		sb.WriteString("### ⚠ 需要澄清\n\n")
		sb.WriteString("以下筛选值命中多个 Od，但缺少维度锚点（root \"one\" 端不在召回结果中），无法自动判断用户意图。\n\n")
		sb.WriteString("**处理步骤（严格顺序）**：\n")
		sb.WriteString("1. **禁止**直接调用 smartquery\n")
		sb.WriteString("2. **先向用户澄清**：列出候选 Od，请用户明确想查询的是哪个业务场景\n")
		sb.WriteString("3. 用户回答后，调用 `clarify_and_branch` 工具，将用户的选择和原始问题传入，创建干净的执行子线程\n\n")
		for _, a := range result.Ambiguities {
			sb.WriteString(fmt.Sprintf("**歧义关键词**: `%s`\n\n候选 Od：\n", a.Keyword))
			for _, c := range a.Candidates {
				line := fmt.Sprintf("- **%s**", c.OdName)
				// Od description is never truncated — disambiguation choices hinge on
				// understanding what each Od represents, and a clipped "用于记录订单基..."
				// leaves the LLM (and reviewing user) guessing.
				if c.OdDescription != "" {
					line += " — " + strings.Join(strings.Fields(c.OdDescription), " ")
				}
				line += fmt.Sprintf("（属性: `%s`", c.PropertyName)
				if c.PropertyDesc != "" {
					pd := c.PropertyDesc
					if len([]rune(pd)) > 40 {
						pd = string([]rune(pd)[:40]) + "..."
					}
					line += ": " + pd
				}
				line += "）"
				sb.WriteString(line + "\n")
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// truncRunes normalises whitespace to a single line, then truncates to
// maxRunes and appends "..." if exceeded.
func truncRunes(s string, maxRunes int) string {
	// Collapse newlines / excess whitespace into single spaces
	flat := strings.Join(strings.Fields(s), " ")
	r := []rune(flat)
	if len(r) <= maxRunes {
		return flat
	}
	return string(r[:maxRunes]) + "..."
}

// isColumnRef returns true if the keyword refers to the column/property itself
// (e.g. "GEO" → Geo column), as opposed to a data value within that column
// (e.g. "IPS5 15IWC11" → a value in Product_Name).
// Checks the DB-persisted flag first, falls back to string comparison.
func isColumnRef(kw KeywordHit, propName, propDisplayName string) bool {
	if kw.IsColumnRef {
		return true
	}
	return strings.EqualFold(kw.Keyword, propName) ||
		strings.EqualFold(kw.Keyword, propDisplayName) ||
		strings.EqualFold(kw.Keyword, kw.MappedField)
}

// renderKwMatch renders one keyword hit for the TOON `match` column.
//   - FUZZY_LIKE  → "userToken→ILIKE×N"         (>4 fuzzy values collapsed; use
//     contains filter, see 🔍 section)
//   - column ref  → "userToken→col"            (user named the column itself)
//   - data value  → "userToken→=dbValue"       (user's alias mapped to the real
//     DB-stored value = kw.Keyword)
//
// The DB value (kw.Keyword) is what goes into SQL filters, NOT the user's
// surface token, since SQL WHERE needs the canonical value literal.
func renderKwMatch(kw KeywordHit, propName, propDisplayName string) string {
	if kw.Tier == "FUZZY_LIKE" {
		return fmt.Sprintf("%s→ILIKE×%d", kw.MatchedToken, kw.FuzzyValueCount)
	}
	if isColumnRef(kw, propName, propDisplayName) {
		return fmt.Sprintf("%s→col", kw.MatchedToken)
	}
	return fmt.Sprintf("%s→=%s", kw.MatchedToken, kw.Keyword)
}

// collectRecalledOds returns all Od names surfaced by recall (OdBlocks +
// DirectOds), deduped, in first-seen order. Used to render the full objects
// list for Metric Intents so the LLM doesn't miss cross-Od references.
func collectRecalledOds(result RecallResult) []string {
	seen := map[string]bool{}
	var ods []string
	for _, b := range result.OdBlocks {
		k := strings.ToLower(b.Name)
		if b.Name != "" && !seen[k] {
			seen[k] = true
			ods = append(ods, b.Name)
		}
	}
	for _, b := range result.DirectOds {
		k := strings.ToLower(b.Name)
		if b.Name != "" && !seen[k] {
			seen[k] = true
			ods = append(ods, b.Name)
		}
	}
	return ods
}

// formatMetricIntent renders one MetricIntent for **strict-mode** dispatch
// (P7): instead of dumping spec fragments for the LLM to copy, it surfaces
// the call shape `smartquery({intent, params})` plus the parameter schema
// the binder will accept. The Intent's canonical_metric / auto_group_by /
// canonical_filters / pivot / default_order / default_limit are server-side
// concerns — LLM doesn't need to fill them and shouldn't.
//
// `recalledOds` is unused in strict mode (objects come from Intent.ObjectName
// server-side) but kept in the signature for callsite stability.
func formatMetricIntent(sb *strings.Builder, mi MetricIntent, _ []string) {
	title := mi.DisplayName
	if title == "" {
		title = mi.Name
	}
	sb.WriteString(fmt.Sprintf("#### `指标:%s` %s\n", mi.Name, title))
	if mi.Description != "" {
		sb.WriteString(fmt.Sprintf("> %s\n\n", strings.Join(strings.Fields(mi.Description), " ")))
	}
	if len(mi.MatchedTokens) > 0 {
		sb.WriteString(fmt.Sprintf("- **触发词**（命中 %s）：%s\n", mi.Tier, strings.Join(mi.MatchedTokens, "、")))
	}
	if mi.ObjectName != "" {
		sb.WriteString(fmt.Sprintf("- **target Od**：`%s`（服务端自动写入 spec.Objects）\n", mi.ObjectName))
	}

	// Strict-mode call shape — show a copyable example with the actual intent
	// name and any required params. Optional params show their default.
	sb.WriteString(fmt.Sprintf("- **调用形式**：`{\"intent\":\"%s\",\"params\":{...}}`\n", mi.Name))

	// Parameters schema — LLM consults this to decide what to fill into params.
	// Renders as a small table so each row carries name/type/property/默认/必填/说明.
	if len(mi.Parameters) > 0 {
		sb.WriteString("- **parameters**（按需填）：\n")
		for _, p := range mi.Parameters {
			required := "可选"
			if !p.Optional {
				required = "**必填**"
			}
			defaultStr := ""
			if p.Default != nil {
				defaultStr = fmt.Sprintf("，默认 `%v`", p.Default)
			}
			targetStr := ""
			switch p.Type {
			case "int":
				targetStr = " → spec.Limit"
			case "property_filter":
				op := p.Op
				if op == "" {
					op = "="
				}
				targetStr = fmt.Sprintf(" → 追加 filter `%s %s {value}`", p.Property, op)
			case "string":
				if p.Property != "" {
					op := p.Op
					if op == "" {
						op = "="
					}
					targetStr = fmt.Sprintf(" → filter `%s %s {value}`", p.Property, op)
				}
			case "enum_ref":
				op := p.Op
				if op == "" {
					op = "="
				}
				targetStr = fmt.Sprintf(" → filter `%s %s {value}`", p.Property, op)
			}
			desc := p.Description
			if desc == "" {
				desc = "（无说明）"
			}
			// enum_ref with candidates — render the finite candidate set
			// inline. Spec .omc/specs/bounded-value-ref-contract.md §3.3:
			// without the list the LLM guesses → PARAM_VALUE_UNKNOWN retry
			// loop. Empty AllowedValues (recall couldn't resolve or list >
			// cap) falls through to the plain line so the markdown stays
			// clean.
			if p.Type == "enum_ref" && len(p.AllowedValues) > 0 {
				sb.WriteString(fmt.Sprintf("    - `%s` (%s, %s%s, **必须从以下选一个**: %s)%s — %s\n",
					p.Name, p.Type, required, defaultStr,
					strings.Join(p.AllowedValues, ", "), targetStr, desc))
				continue
			}
			sb.WriteString(fmt.Sprintf("    - `%s` (%s, %s%s)%s — %s\n",
				p.Name, p.Type, required, defaultStr, targetStr, desc))
		}
	} else {
		sb.WriteString("- **parameters**：（无）— 直接 `{\"intent\":\"" + mi.Name + "\",\"params\":{}}`\n")
	}

	// Default shape (informational — server applies these automatically).
	if mi.DefaultLimit > 0 {
		sb.WriteString(fmt.Sprintf("- **默认 limit**：%d（用户没说 Top N 时走这个）\n", mi.DefaultLimit))
	}
	if mi.DefaultOrderByLabel != "" {
		dir := mi.DefaultOrderByDir
		if dir == "" {
			dir = "DESC"
		}
		sb.WriteString(fmt.Sprintf("- **默认排序**：`%s %s`\n", mi.DefaultOrderByLabel, dir))
	}

	// Pivot hint stays — LLM's reply still needs to know the result is wide
	// format with specific column labels.
	if mi.PivotOn != "" {
		label := mi.PivotTotalLabel
		if label == "" {
			label = "Total"
		}
		if len(mi.PivotValues) > 0 {
			quoted := make([]string, 0, len(mi.PivotValues))
			for _, v := range mi.PivotValues {
				quoted = append(quoted, fmt.Sprintf(`"%s"`, v))
			}
			sb.WriteString(fmt.Sprintf("- **结果形态（pivot）**：按 `%s` 展开为列 %s + 求和列 `\"%s\"`（每行 = %s）\n",
				mi.PivotOn, strings.Join(quoted, "、"), label,
				pivotHintRowDescription(mi)))
		} else {
			sb.WriteString(fmt.Sprintf("- **结果形态（pivot）**：按 `%s` 动态展开 + 求和列 `\"%s\"`\n",
				mi.PivotOn, label))
		}
		suffix := mi.PivotPercentSuffix
		if suffix == "" {
			suffix = "占比"
		}
		if mi.PivotPercentScope == "global" {
			sb.WriteString(fmt.Sprintf("- **%s模式**：global（分母 = 全局合计）\n", suffix))
		} else {
			sb.WriteString(fmt.Sprintf("- **%s模式**：行内（分母 = 本行合计）\n", suffix))
		}
	}
	if mi.ResponseTemplate != "" {
		sb.WriteString(fmt.Sprintf("- **response_template**：%s\n", mi.ResponseTemplate))
	}
	sb.WriteString("\n")
}

// pivotHintRowDescription describes one pivoted row using the Intent's other
// groupBy dimensions (excluding pivotOn itself) so the LLM knows what each row represents.
func pivotHintRowDescription(mi MetricIntent) string {
	var others []string
	for _, g := range mi.AutoGroupBy {
		if g != mi.PivotOn {
			others = append(others, g)
		}
	}
	if len(others) == 0 {
		return "行（若 groupBy 无其它维度则仅一行汇总）"
	}
	return fmt.Sprintf("(%s)+各 pivot 列数值", strings.Join(others, ", "))
}

// toonVal escapes a value for use in a TOON pipe-delimited tabular row.
// Wraps in double quotes if the value contains pipe, quote, colon, or newline.
func toonVal(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "|\"\n\r:") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// formatOdBlock writes a single Od block with its matched/unmatched properties.
//
// When fullDetail is true (lookup tool), existing verbose markdown is used so the
// LLM gets full property definitions, Ok entries, and multi-line Ok defs.
//
// When fullDetail is false (smartquery pre-processing recall), TOON pipe-delimited
// tabular format is used for matched and unmatched properties to minimize token cost.
func formatOdBlock(sb *strings.Builder, blk OdBlock, showMatchDetail bool, fullDetail bool) {
	// Od header — shared across both paths.
	// Od description is never truncated: users complained that the 60-rune cap
	// in the TOON path was dropping business-critical sentences right at the
	// boundary (e.g. "... 订单量 = 未转化 + 已转化，early Order 指..."). Token
	// cost of full Od description is modest — typically one or two lines per
	// Od — and keeping it complete materially improves LLM grounding. Property
	// descriptions in TOON rows stay truncated separately (unmatched=15 runes).
	line := fmt.Sprintf("`Od:%s` **%s** [%s]", blk.Name, blk.Name, blk.Kind)
	if blk.Description != "" {
		line += ": " + strings.Join(strings.Fields(blk.Description), " ")
	}
	sb.WriteString(line + "\n")

	if fullDetail {
		// ── Full markdown path (lookup tool) ─────────────────────────────────────
		matchedNames := map[string]bool{}
		if showMatchDetail {
			for _, p := range blk.MatchedProps {
				displayName := p.DisplayName
				if displayName == "" {
					displayName = p.Name
				}
				matchedNames[displayName] = true

				sb.WriteString(fmt.Sprintf("  **%s**", displayName))
				if p.DataType != "" {
					sb.WriteString(fmt.Sprintf(" (%s)", p.DataType))
				}
				if p.Description != "" {
					sb.WriteString(": " + strings.Join(strings.Fields(p.Description), " "))
				}
				sb.WriteString("\n")

				// Keyword trace — each keyword decides col-vs-value for itself.
				for _, kw := range p.Keywords {
					tierInfo := kw.Tier
					if kw.Tier == "VEC" {
						tierInfo = fmt.Sprintf("VEC, %.2f", kw.Score)
					}
					if isColumnRef(kw, p.Name, displayName) {
						sb.WriteString(fmt.Sprintf("    ← %q → 列引用: %s (%s)\n", kw.MatchedToken, kw.Keyword, tierInfo))
					} else {
						sb.WriteString(fmt.Sprintf("    ← %q → 筛选值: %s = %q (%s)\n", kw.MatchedToken, displayName, kw.Keyword, tierInfo))
					}
				}

				// Ok entry — full detail only.
				if p.OkTitle != "" {
					sb.WriteString(fmt.Sprintf("    `Ok:%s` **%s**", p.OkTitle, p.OkTitle))
					if p.OkSummary != "" {
						sb.WriteString(" — " + p.OkSummary)
					}
					sb.WriteString("\n")
				}
				if len(p.OkDefs) > 0 {
					for i, def := range p.OkDefs {
						sb.WriteString(fmt.Sprintf("    Ok定义%d: %s\n", i+1, strings.Join(strings.Fields(def), " ")))
					}
				}
			}
		}

		// Unmatched properties — verbose list.
		var unmatched []string
		for _, name := range blk.AllPropNames {
			if !matchedNames[name] {
				if desc, ok := blk.AllPropDescs[name]; ok && desc != "" {
					unmatched = append(unmatched, fmt.Sprintf("%s: %s", name, strings.Join(strings.Fields(desc), " ")))
				} else {
					unmatched = append(unmatched, name)
				}
			}
		}
		if len(unmatched) > 0 {
			sb.WriteString("  其他属性:\n")
			for _, u := range unmatched {
				sb.WriteString(fmt.Sprintf("    - %s\n", u))
			}
		}

		// Od↔Od links.
		for _, link := range blk.Links {
			sb.WriteString(fmt.Sprintf("  ↔ %s (%s)\n", link.TargetOdName, link.Cardinality))
		}

	} else {
		// ── TOON compressed path (smartquery pre-processing recall) ──────────────
		// Matched properties: one TOON row per property, pipe-delimited.
		// Columns: prop | type | desc(40) | match(token→kw,col/val) | tier
		matchedNames := map[string]bool{}
		if showMatchDetail && len(blk.MatchedProps) > 0 {
			sb.WriteString(fmt.Sprintf("  matched[%d|]{prop|type|desc|match|tier}:\n", len(blk.MatchedProps)))
			for _, p := range blk.MatchedProps {
				dn := p.DisplayName
				if dn == "" {
					dn = p.Name
				}
				matchedNames[dn] = true

				// Each keyword decides col-vs-value for itself. A property can legitimately
				// carry both (e.g. GEO column has keyword "Geo" with is_column_name=true
				// AND keyword "N.A." with is_column_name=false whose alias "NA地区" hit) —
				// rendering must preserve the distinction so the LLM knows NA地区 filters
				// to DB value "N.A." while 所有GEO means "group by this column".
				matchStr := ""
				tier := ""
				if len(p.Keywords) > 0 {
					tier = p.Keywords[0].Tier
					if p.Keywords[0].Tier == "VEC" {
						tier = fmt.Sprintf("VEC%.2f", p.Keywords[0].Score)
					}
					parts := make([]string, 0, len(p.Keywords))
					for _, kw := range p.Keywords {
						parts = append(parts, renderKwMatch(kw, p.Name, dn))
					}
					matchStr = strings.Join(parts, ";")
				}

				// Matched props keep full description (flattened whitespace only) —
				// LLM needs the枚举值/业务逻辑 blocks to decide filter semantics.
				// Only unmatched props below get hard-truncated to save tokens.
				fullDesc := strings.Join(strings.Fields(p.Description), " ")
				sb.WriteString(fmt.Sprintf("    %s|%s|%s|%s|%s\n",
					toonVal(dn),
					toonVal(p.DataType),
					toonVal(fullDesc),
					toonVal(matchStr),
					toonVal(tier),
				))
			}
		}

		// Unmatched properties: TOON tabular, prop | short_desc.
		var unmatchedRows [][2]string
		for _, name := range blk.AllPropNames {
			if !matchedNames[name] {
				desc := ""
				if d, ok := blk.AllPropDescs[name]; ok && d != "" {
					desc = truncRunes(d, 15)
				}
				unmatchedRows = append(unmatchedRows, [2]string{name, desc})
			}
		}
		if len(unmatchedRows) > 0 {
			sb.WriteString(fmt.Sprintf("  others[%d|]{prop|desc}:\n", len(unmatchedRows)))
			for _, r := range unmatchedRows {
				sb.WriteString(fmt.Sprintf("    %s|%s\n", toonVal(r[0]), toonVal(r[1])))
			}
		}

		// Od↔Od links: TOON tabular.
		if len(blk.Links) > 0 {
			sb.WriteString(fmt.Sprintf("  links[%d|]{target|card}:\n", len(blk.Links)))
			for _, link := range blk.Links {
				sb.WriteString(fmt.Sprintf("    %s|%s\n", toonVal(link.TargetOdName), toonVal(link.Cardinality)))
			}
		}
	}

	sb.WriteString("\n")
}
