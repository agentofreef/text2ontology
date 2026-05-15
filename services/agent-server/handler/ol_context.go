package handler

import (
	"database/sql"
	"fmt"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
)

// BuildOlIndex returns a compact Ol keyword index for agent system prompts.
// Shows title + tags per fact so LLM knows what experiences exist.
// LLM should call lookup(keyword=["tag"]) to get full details when needed.
func BuildOlIndex(db *sql.DB, projectID, _ string) string {
	rows, err := db.Query(`SELECT f.id, COALESCE(f.title,''), f.summary,
		COALESCE(f.tags,'{}')::text, COALESCE(f.fact_type,'business_rule')
		FROM ont_learned_fact f
		WHERE f.project_id = $1 AND f.confidence = 'confirmed'
		ORDER BY f.created_at DESC`,
		projectID)
	if err != nil || rows == nil {
		return "暂无学习事实。\n"
	}
	defer rows.Close()

	var sb strings.Builder
	var allTags []string
	tagSeen := map[string]bool{}
	count := 0

	sb.WriteString("## 已习得的业务经验 (Ol)\n\n")

	for rows.Next() {
		var id, title, summary, tagsRaw, factType string
		rows.Scan(&id, &title, &summary, &tagsRaw, &factType)
		count++
		tags := ParsePgTextArray(tagsRaw)
		for _, t := range tags {
			if !tagSeen[t] {
				tagSeen[t] = true
				allTags = append(allTags, t)
			}
		}
		displayTitle := title
		if displayTitle == "" {
			r := []rune(summary)
			if len(r) > 20 {
				displayTitle = string(r[:20]) + "…"
			} else {
				displayTitle = summary
			}
		}
		_ = id
		sb.WriteString(fmt.Sprintf("- `Ol:%s` [%s]\n", displayTitle, factType))
	}
	if count == 0 {
		return "暂无学习事实。\n"
	}

	sb.WriteString(fmt.Sprintf("\n共 %d 条经验。", count))
	if len(allTags) > 0 {
		sb.WriteString(fmt.Sprintf("\n经验关键词: %s", strings.Join(allTags, "、")))
	}
	sb.WriteString("\n\n**使用方式**: 遇到需要参考经验的情况时（歧义、口径、多对象查询模式等），调用 `lookup(keyword=[\"经验关键词\"])` 获取完整经验内容及关联的 Od/Ok 信息，然后据此执行查询。\n")

	return sb.String()
}

// resolveFactLinkTargetName returns the display name for an ol_fact_link
// target row. Inlined from services/agent-server/handler/handler_ol.go during A1 to
// avoid pulling the entire Ol CRUD surface into agent-server — the
// monolith keeps its own copy for the CRUD endpoint.
func resolveFactLinkTargetName(db *sql.DB, targetType, targetID string) string {
	var name string
	switch targetType {
	case "object":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_object_type WHERE id = $1`, targetID).Scan(&name)
	case "metric":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_metric WHERE id = $1`, targetID).Scan(&name)
	case "property":
		db.QueryRow(`SELECT COALESCE(name,'') FROM ont_property WHERE id = $1`, targetID).Scan(&name)
	case "link":
		db.QueryRow(`SELECT COALESCE(link_name,'') FROM ont_link_type WHERE id = $1`, targetID).Scan(&name)
	case "knowledge":
		db.QueryRow(`SELECT COALESCE(title,'') FROM ont_knowledge WHERE id = $1`, targetID).Scan(&name)
	case "fact":
		db.QueryRow(`SELECT COALESCE(summary,'') FROM ont_learned_fact WHERE id = $1`, targetID).Scan(&name)
	}
	return name
}
