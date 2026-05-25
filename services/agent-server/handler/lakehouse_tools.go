// lakehouse_tools.go contains helper functions migrated from
// handler_agent_v2.go during the lakehouse-only branch split (commit
// 47f519f3). They remain because handler_agent_lakehouse.go still references:
//   - classifyExecError: maps SQL exec errors to user-friendly categories
//   - toolResultToMarkdown: formats tool outputs for agent chat streams
//   - toonResultVal: typed value coercion for TOON responses
package handler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
)

// classifyExecError converts a raw SQL execution error into a human-readable
// classification without exposing query syntax or physical table/column names.
func classifyExecError(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "syntax") || strings.Contains(lower, "the syntax for"):
		return "查询语法错误：筛选条件或聚合参数格式有误（如操作符不正确、括号未闭合等），请重新检查 filters 中的 op 值和整体参数结构。"
	case strings.Contains(lower, "does not exist") || strings.Contains(lower, "cannot find") ||
		strings.Contains(lower, "no column") || strings.Contains(lower, "column not found"):
		return "列不存在：filters 或 groupBy 中引用的属性名在数据模型中未找到。请确认属性名称与 lookup 返回的 property 列表完全一致（区分大小写）。"
	case strings.Contains(lower, "no table found") || strings.Contains(lower, "table not found") ||
		strings.Contains(lower, "unknown table"):
		return "数据对象不存在：objects 中引用的对象名在数据集中不存在，请检查 od 名称是否正确。"
	case strings.Contains(lower, "cannot be converted") || strings.Contains(lower, "type mismatch") ||
		(strings.Contains(lower, "type") && strings.Contains(lower, "cannot")):
		return "数据类型不匹配：筛选值的类型与列类型不符（如对数值列使用了文字值，或对文本列使用了数字比较），请调整 filters.value。"
	case strings.Contains(lower, "circular"):
		return "循环依赖：查询中存在循环引用，请简化聚合或筛选条件。"
	case strings.Contains(lower, "a single value"):
		return "列值不唯一：当前上下文中某属性有多个值，无法确定单一值。请增加 filters 缩小范围，或改用聚合指标。"
	case strings.Contains(lower, "ambiguous") || strings.Contains(lower, "multiple tables"):
		return "列引用不明确：属性名在多个数据对象中重复，请在 filters/groupBy 中加上对象前缀（如 Object.Property）。"
	case strings.Contains(lower, "400") || strings.Contains(lower, "bad request"):
		return "请求格式错误：API 拒绝了查询请求，可能是参数组合不合法（如空 groupBy + 无指标），请检查 objects/metric/filters 是否完整。"
	case strings.Contains(lower, "0 rows") || strings.Contains(lower, "empty"):
		return "查询返回 0 行：筛选条件过于严格，请确认 filters.value 的值是否存在于数据中（可通过 lookup keyword 查看已知值）。"
	default:
		// Safe to show a trimmed version
		msg := raw
		if len([]rune(msg)) > 150 {
			msg = string([]rune(msg)[:150]) + "..."
		}
		return "查询失败：" + msg
	}
}

// toolResultToMarkdown formats a tool call as YAML with explicit INPUT and OUTPUT sections.
// The LLM sees "what was called with what args" and "what came back" — no raw JSON.
func toolResultToMarkdown(toolName string, _ map[string]interface{}, result M) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## function_call: %s\n\n", toolName))

	// ── OUTPUT only — arguments are already in the assistant's tool_use message ──
	sb.WriteString("### 输出 (result):\n")

	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		sb.WriteString("```yaml\nerror: " + errMsg + "\n```\n")
		return sb.String()
	}

	// For smartquery: show result table
	if execStatus, ok := result["execution_status"].(string); ok {
		if execStatus == "error" {
			execErr, _ := result["execution_error"].(string)
			classified := classifyExecError(execErr)
			sb.WriteString("```yaml\nstatus: error\n")
			sb.WriteString("error: " + classified + "\n```\n")
			return sb.String()
		}
		// Parse and render result rows as TOON pipe-delimited tabular.
		// Repeated field names per row (YAML list) waste tokens; TOON declares
		// columns once and streams rows compactly.
		if execResult, ok := result["execution_result"].(string); ok && execResult != "" && execResult != "null" {
			var rows []map[string]interface{}
			if json.Unmarshal([]byte(execResult), &rows) == nil && len(rows) > 0 {
				// Collect column names from first row; sort for deterministic order.
				cols := make([]string, 0, len(rows[0]))
				for k := range rows[0] {
					cols = append(cols, k)
				}
				sort.Strings(cols)

				limit := len(rows)
				if limit > 20 {
					limit = 20
				}

				// Data-template step id: when set, tell the LLM this result is
				// addressable — it should report key numbers as references
				// (「sum(t1.col)」 / 「t1」) rather than transcribing digits.
				if sid, _ := result["step_id"].(string); sid != "" {
					sb.WriteString(fmt.Sprintf(
						"# 本结果 id=%s —— 报告其中数值时写引用「%s」/「agg(%s.列)」等"+
							"（语法见系统提示「数据模板」节），不要手写数字。\n",
						sid, sid, sid))
				}

				// Large result → ALSO instruct the LLM to emit a chart schema at
				// the END of its answer so the frontend renders a visualization
				// instead of a giant table. The schema names columns + a chart
				// type only — never data values; the frontend resolves the data
				// from THIS result, same invariant as 「tN」. This trigger lives
				// here (runtime, on the tool result) — NOT in the system prompt —
				// so charting guidance appears only when a result is actually
				// large. Gate is len(rows)>20: results >200 are pre-truncated to
				// a 10-row preview upstream, so len(rows) here is the true count
				// only within (20,200], which is exactly the chartable band.
				if sid, _ := result["step_id"].(string); sid != "" && len(rows) > 20 {
					sb.WriteString(fmt.Sprintf(
						"# 本结果有 %d 行，表格过长。请在回答【末尾】追加一个图表 schema："+
							"「chart type=bar from=%s x=<某一列> y=<一或多列,英文逗号分隔> series=<可选分组列> filter=<可选,列=值;列=值>」。"+
							"type 可选 bar|line|pie|area；列名只能取自：%s。"+
							"filter 是**画图前**对源行的过滤，支持 1~N 个 列=值 等式用分号 ; 串联（AND），值写**字面量**"+
							"（如 filter=ORDER_TYPE=Real Order;GEO=EMEA），用于把图聚焦到某个切片，避免一张图把"+
							"几条无关曲线挤在一起。需要对比多个切片时则不用 filter，改让 series 承担分组。"+
							"只写列名/图型/筛选字面量，绝不写任何聚合后的数值——前端会用本结果的数据渲染图表。\n",
						len(rows), sid, strings.Join(cols, "、")))
				}

				// TOON header
				colHeader := strings.Join(cols, "|")
				sb.WriteString(fmt.Sprintf("status: success\nrow_count: %d\nrows[%d|]{%s}:\n", len(rows), len(rows), colHeader))

				for i := 0; i < limit; i++ {
					vals := make([]string, len(cols))
					for j, col := range cols {
						vals[j] = toonResultVal(fmt.Sprintf("%v", rows[i][col]))
					}
					sb.WriteString("  " + strings.Join(vals, "|") + "\n")
				}
				if len(rows) > 20 {
					sb.WriteString(fmt.Sprintf("  # ... 共 %d 行，仅展示前 20 行\n", len(rows)))
				}
				return sb.String()
			}
		}
	}

	// For lookup: show content directly (already markdown)
	if content, ok := result["content"].(string); ok && content != "" {
		if len([]rune(content)) > 2000 {
			content = string([]rune(content)[:2000]) + "..."
		}
		sb.WriteString(content + "\n")
		return sb.String()
	}

	// Generic fallback: YAML
	sb.WriteString("```yaml\n")
	for k, v := range result {
		sb.WriteString(fmt.Sprintf("%s: %v\n", k, v))
	}
	sb.WriteString("```\n")
	return sb.String()
}

// toonResultVal escapes a value for use in a TOON pipe-delimited tabular row.
// Wraps in double quotes if the value contains pipe, quote, colon, or newline.
func toonResultVal(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "|\"\n\r:") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
