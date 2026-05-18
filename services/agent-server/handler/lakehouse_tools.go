// lakehouse_tools.go contains helper functions migrated from
// handler_agent_v2.go during the lakehouse-only branch split (commit
// 47f519f3). They remain because handler_agent_lakehouse.go and
// handler_lh_testing.go still reference:
//   - v2ToolLinkToOd, v2ToolCreateCausality: Od linking used by
//     lakehouse agent's knowledge-related tools
//   - classifyExecError: maps SQL/DAX exec errors to user-friendly categories
//   - toolResultToMarkdown: formats tool outputs for agent chat streams
//   - toonResultVal: typed value coercion for TOON responses
package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
)

// v2ToolLinkToOd attaches a knowledge entry to an ontology anchor (object/metric/link/property/version).
// Used by the lakehouse agent's "link_to_od" tool call.
func v2ToolLinkToOd(db *sql.DB, args map[string]interface{}) M {
	knowledgeId, _ := args["knowledgeId"].(string)
	anchorType, _ := args["anchorType"].(string)
	anchorId, _ := args["anchorId"].(string)

	if knowledgeId == "" {
		return M{"error": "knowledgeId is required"}
	}
	if anchorType == "" {
		return M{"error": "anchorType is required (object|metric|link|property)"}
	}

	// Validate knowledge exists
	var title string
	if err := db.QueryRow(`SELECT title FROM ont_knowledge WHERE id = $1`, knowledgeId).Scan(&title); err != nil {
		return M{"error": fmt.Sprintf("知识 %s 不存在", knowledgeId)}
	}

	// Validate anchor entity exists
	var anchorName string
	switch anchorType {
	case "object":
		db.QueryRow(`SELECT name FROM ont_object_type WHERE id = $1`, anchorId).Scan(&anchorName)
	case "metric":
		db.QueryRow(`SELECT name FROM ont_metric WHERE id = $1`, anchorId).Scan(&anchorName)
	case "link":
		db.QueryRow(`SELECT link_name FROM ont_link_type WHERE id = $1`, anchorId).Scan(&anchorName)
	case "property":
		db.QueryRow(`SELECT name FROM ont_property WHERE id = $1`, anchorId).Scan(&anchorName)
	case "version":
		anchorName = "通用"
		anchorId = ""
	default:
		return M{"error": fmt.Sprintf("无效的 anchorType: %s", anchorType)}
	}
	if anchorName == "" && anchorType != "version" {
		return M{"error": fmt.Sprintf("%s %s 不存在", anchorType, anchorId)}
	}

	var err error
	if anchorType == "object" && anchorId != "" {
		// For Od links: set primary anchor_id AND append to anchor_ids array (dedup)
		_, err = db.Exec(`UPDATE ont_knowledge
			SET anchor_type = $2, anchor_id = $3, updated_at = now(),
			    anchor_ids = (SELECT array_agg(DISTINCT e) FROM unnest(COALESCE(anchor_ids,'{}') || $3::uuid) e)
			WHERE id = $1`, knowledgeId, anchorType, anchorId)
	} else {
		_, err = db.Exec(`UPDATE ont_knowledge SET anchor_type = $2, anchor_id = $3, updated_at = now() WHERE id = $1`,
			knowledgeId, anchorType, NilIfEmpty(anchorId))
	}
	if err != nil {
		return M{"error": "更新失败: " + err.Error()}
	}

	return M{"success": true, "summary": fmt.Sprintf("已将知识「%s」挂靠到 %s: %s", title, anchorType, anchorName)}
}

// v2ToolCreateCausality creates a causality relationship between two knowledge entries.
// Used by the lakehouse agent's "create_causality" tool call.
func v2ToolCreateCausality(db *sql.DB, projectID string, args map[string]interface{}) M {
	fromID, _ := args["fromKnowledgeId"].(string)
	toID, _ := args["toKnowledgeId"].(string)
	relType, _ := args["relationType"].(string)
	direction, _ := args["direction"].(string)
	desc, _ := args["description"].(string)

	if fromID == "" || toID == "" {
		return M{"error": "fromKnowledgeId and toKnowledgeId are required"}
	}
	if relType == "" {
		relType = "correlates"
	}
	if direction == "" {
		direction = "neutral"
	}

	var causalityID string
	err := db.QueryRow(`INSERT INTO ont_causality (project_id, from_knowledge_id, to_knowledge_id, relation_type, direction, description)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		projectID, fromID, toID, relType, direction, desc).Scan(&causalityID)
	if err != nil {
		return M{"error": "创建失败: " + err.Error()}
	}

	var fromTitle, toTitle string
	db.QueryRow(`SELECT COALESCE(title,'') FROM ont_knowledge WHERE id = $1`, fromID).Scan(&fromTitle)
	db.QueryRow(`SELECT COALESCE(title,'') FROM ont_knowledge WHERE id = $1`, toID).Scan(&toTitle)

	return M{"success": true, "causalityId": causalityID,
		"summary": fmt.Sprintf("已创建因果关系: 「%s」 —[%s/%s]→ 「%s」", fromTitle, relType, direction, toTitle)}
}

// classifyExecError converts a raw PBI/DAX execution error into a human-readable
// classification without exposing DAX syntax or physical table/column names.
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
		// Strip any DAX-like fragments before returning
		daxHints := []string{"EVALUATE", "SUMMARIZECOLUMNS", "FILTER(", "TOPN(", "ADDCOLUMNS(", "CALCULATE("}
		for _, kw := range daxHints {
			if strings.Contains(strings.ToUpper(raw), kw) {
				return "查询执行失败：参数有误，请重新检查 objects/filters/groupBy/metric 的值。"
			}
		}
		// Safe to show a trimmed version
		msg := raw
		if len([]rune(msg)) > 150 {
			msg = string([]rune(msg)[:150]) + "..."
		}
		return "查询失败：" + msg
	}
}

// toolResultToMarkdown formats a tool call as YAML with explicit INPUT and OUTPUT sections.
// The LLM sees "what was called with what args" and "what came back" — no raw JSON, no DAX.
func toolResultToMarkdown(toolName string, _ map[string]interface{}, result M) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## function_call: %s\n\n", toolName))

	// ── OUTPUT only — arguments are already in the assistant's tool_use message ──
	sb.WriteString("### 输出 (result):\n")

	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		sb.WriteString("```yaml\nerror: " + errMsg + "\n```\n")
		return sb.String()
	}

	// For smartquery: show result table (not DAX)
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
						"# 本结果 id=%s。报告其中的数值时**不要手写数字**，写引用："+
							"整列聚合「sum(%s.列名)」/「avg/count/min/max(…)」；"+
							"逐行/单行「sum(%s.列名 WHERE 筛选列='值')」；"+
							"派生值（占比/差值等）整个算式包进「」如「sum(…)/sum(…)*100」；"+
							"整表「%s」。前端会渲染成真值。列名见下方表头。\n",
						sid, sid, sid))
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
