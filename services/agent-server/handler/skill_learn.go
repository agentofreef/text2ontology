package handler

import (
	"database/sql"
	"fmt"
	"strings"

	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/llmclient"
)

// embedAndSaveFactVector computes and stores the embedding for a learned fact.
// Text = summary + space-joined tags (the semantic surface of the fact).
func embedAndSaveFactVector(db *sql.DB, factID, summary, tagsJoined string) {
	text := strings.TrimSpace(summary)
	if tagsJoined != "" {
		kws := strings.ReplaceAll(tagsJoined, "|", " ")
		if text != "" {
			text += " " + kws
		} else {
			text = kws
		}
	}
	if text == "" {
		return
	}
	embeddings, err := llmclient.EmbedTexts(db, []string{text})
	if err != nil || len(embeddings) == 0 {
		return
	}
	vecStr := "["
	for i, v := range embeddings[0] {
		if i > 0 {
			vecStr += ","
		}
		vecStr += fmt.Sprintf("%f", v)
	}
	vecStr += "]"
	db.Exec(`UPDATE ont_learned_fact SET content_vector = $1::vector WHERE id = $2`, vecStr, factID)
}

// ─── Learn Skill ──────────────────────────────────────────────────────────────
// Triggered when user says "学习/记住/记录/请你学习" etc.
// The LLM calls propose_learned_fact with (title, summary, content, tags, fact_type, links).
//
// Design principles:
//   - Links use strict ID format: "Od:{uuid}", "Ok:{uuid}", "Ol:{uuid}"
//   - At least one link is required (no orphan Ol nodes)
//   - fact_type classifies the knowledge purpose
//   - Variables (time, amounts, names) must be stripped by the LLM
//   - Vector embedding uses summary + tags for richer semantics

// factLinkArg represents a parsed link from tool args (strict ID format).
type factLinkArg struct {
	TargetType string // "object" | "knowledge" | "fact" | "metric" | "property" | "link"
	TargetID   string // UUID
	Role       string // "about" | "extends" | "corrects" | "related" | "conflicts"
}

// parseStrictLinkArgs extracts links from tool args using strict "Od:{uuid}" format.
// Returns parsed links and any error messages for unresolved references.
func parseStrictLinkArgs(args map[string]interface{}) ([]factLinkArg, []string) {
	raw, ok := args["links"].([]interface{})
	if !ok {
		return nil, nil
	}
	var out []factLinkArg
	var errors []string
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		target, _ := m["target"].(string)
		role, _ := m["role"].(string)
		target = strings.TrimSpace(target)
		role = strings.TrimSpace(strings.ToLower(role))
		if target == "" {
			continue
		}
		if role == "" {
			role = "about"
		}

		// Parse "Od:{uuid}", "Ok:{uuid}", "Ol:{uuid}" format
		parts := strings.SplitN(target, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			errors = append(errors, fmt.Sprintf("无效的引用格式: %q（需要 Od:{id} / Ok:{id} / Ol:{id}）", target))
			continue
		}
		prefix := parts[0]
		id := strings.TrimSpace(parts[1])
		if !IsValidUUID(id) {
			errors = append(errors, fmt.Sprintf("无效的 UUID: %q", target))
			continue
		}

		var targetType string
		switch prefix {
		case "Od":
			targetType = "object"
		case "Ok":
			targetType = "knowledge"
		case "Ol":
			targetType = "fact"
		default:
			errors = append(errors, fmt.Sprintf("未知前缀 %q（支持 Od/Ok/Ol）", prefix))
			continue
		}

		out = append(out, factLinkArg{TargetType: targetType, TargetID: id, Role: role})
	}
	return out, errors
}

// validateLinkTarget checks that the referenced entity actually exists in the database.
func validateLinkTarget(db *sql.DB, targetType, targetID string) bool {
	var exists bool
	switch targetType {
	case "object":
		db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ont_object_type WHERE id = $1)`, targetID).Scan(&exists)
	case "knowledge":
		db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ont_knowledge WHERE id = $1)`, targetID).Scan(&exists)
	case "fact":
		db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ont_learned_fact WHERE id = $1)`, targetID).Scan(&exists)
	case "metric":
		db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ont_metric WHERE id = $1)`, targetID).Scan(&exists)
	case "property":
		db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ont_property WHERE id = $1)`, targetID).Scan(&exists)
	case "link":
		db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ont_link_type WHERE id = $1)`, targetID).Scan(&exists)
	}
	return exists
}

// resolveFactLinkTarget looks up a target entity by name, returning its UUID or "" if not found.
// Kept for backward compat with legacy targetName-based links.
func resolveFactLinkTarget(db *sql.DB, projectID, targetType, targetName string) string {
	var id string
	switch targetType {
	case "object":
		db.QueryRow(`SELECT id::text FROM ont_object_type
			WHERE project_id = $1 AND (LOWER(name) = LOWER($2) OR name ILIKE '%'||$2||'%')
			ORDER BY CASE WHEN LOWER(name)=LOWER($2) THEN 0 ELSE 1 END LIMIT 1`,
			projectID, targetName).Scan(&id)
	case "knowledge":
		db.QueryRow(`SELECT id::text FROM ont_knowledge
			WHERE project_id = $1
			  AND (LOWER(title) = LOWER($2) OR title ILIKE '%'||$2||'%')
			ORDER BY CASE WHEN LOWER(title)=LOWER($2) THEN 0 ELSE 1 END LIMIT 1`,
			projectID, targetName).Scan(&id)
	case "fact":
		db.QueryRow(`SELECT id::text FROM ont_learned_fact
			WHERE project_id = $1
			  AND (LOWER(title) = LOWER($2) OR LOWER(summary) = LOWER($2) OR title ILIKE '%'||$2||'%')
			ORDER BY CASE WHEN LOWER(title)=LOWER($2) THEN 0 ELSE 1 END LIMIT 1`,
			projectID, targetName).Scan(&id)
	}
	return id
}

// v2ToolProposeLearnedFact saves a pending Ol fact and returns the proposal for confirmation.
func v2ToolProposeLearnedFact(db *sql.DB, projectID, threadID string, args map[string]interface{}) M {
	summary, _ := args["summary"].(string)
	content, _ := args["content"].(string)
	title, _ := args["title"].(string)
	factType, _ := args["fact_type"].(string)
	if summary == "" {
		return M{"error": "summary is required"}
	}
	if content == "" {
		content = summary
	}

	// Validate fact_type
	validFactTypes := map[string]bool{
		"business_rule": true, "calibration": true, "misconception": true,
		"filter_hint": true, "calculation_note": true,
	}
	if factType == "" || !validFactTypes[factType] {
		factType = "business_rule"
	}

	// Parse tags (N items, no limit). Accept both "tags" (new) and "keywords" (legacy).
	var tags []string
	if ts, ok := args["tags"].([]interface{}); ok {
		for _, t := range ts {
			if s := strings.TrimSpace(fmt.Sprintf("%v", t)); s != "" {
				tags = append(tags, s)
			}
		}
	} else if ks, ok := args["keywords"].([]interface{}); ok {
		for _, k := range ks {
			if s := strings.TrimSpace(fmt.Sprintf("%v", k)); s != "" {
				tags = append(tags, s)
			}
		}
	}
	// Legacy column — store first 2 tags pipe-joined for backward compat
	legacyKeywords := ""
	if len(tags) > 0 {
		n := len(tags)
		if n > 2 {
			n = 2
		}
		legacyKeywords = strings.Join(tags[:n], "|")
	}
	tagsLit := StringsSliceToPgArray(tags)

	// Parse links — name-based: {targetType: "object", targetName: "Product", role: "about"}
	// Backend resolves name → ID. Unresolved names are reported as errors.
	type nameLink struct {
		TargetType string
		TargetName string
		Role       string
	}
	var nameLinks []nameLink
	if raw, ok := args["links"].([]interface{}); ok {
		for _, item := range raw {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			tt, _ := m["targetType"].(string)
			tn, _ := m["targetName"].(string)
			role, _ := m["role"].(string)
			tt = strings.TrimSpace(strings.ToLower(tt))
			tn = strings.TrimSpace(tn)
			role = strings.TrimSpace(strings.ToLower(role))
			if tn == "" {
				continue
			}
			if tt != "object" && tt != "knowledge" && tt != "fact" {
				tt = "object" // default
			}
			if role == "" {
				role = "about"
			}
			nameLinks = append(nameLinks, nameLink{TargetType: tt, TargetName: tn, Role: role})
		}
	}

	// Also accept legacy involvedOds shortcut
	if ods, ok := args["involvedOds"].([]interface{}); ok {
		for _, o := range ods {
			if s := strings.TrimSpace(fmt.Sprintf("%v", o)); s != "" {
				nameLinks = append(nameLinks, nameLink{TargetType: "object", TargetName: s, Role: "about"})
			}
		}
	}

	// Enforce at least one link specified
	if len(nameLinks) == 0 {
		return M{"error": "至少需要一个关联（links）。请指定: [{\"targetType\":\"object\", \"targetName\":\"Od英文名\", \"role\":\"about\"}]"}
	}

	// Resolve names → IDs, track failures
	var links []factLinkArg
	var unresolvedNames []string
	seen := map[string]bool{}
	for _, nl := range nameLinks {
		targetID := resolveFactLinkTarget(db, projectID, nl.TargetType, nl.TargetName)
		if targetID == "" {
			unresolvedNames = append(unresolvedNames, fmt.Sprintf("%s(%s)", nl.TargetName, nl.TargetType))
			continue
		}
		dedupKey := nl.TargetType + ":" + targetID + ":" + nl.Role
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		links = append(links, factLinkArg{TargetType: nl.TargetType, TargetID: targetID, Role: nl.Role})
	}

	// If all names failed to resolve, return error with specific names
	if len(links) == 0 {
		return M{"error": fmt.Sprintf("所有关联名称均未找到对应实体: %s。请检查名称拼写（Od用英文名如Product，Ok用标题，Ol用标题），然后重试。", strings.Join(unresolvedNames, "、"))}
	}
	// Partial failures: warn but continue with resolved ones
	validLinks := links

	// Build note
	var noteParts []string
	if len(tags) > 0 {
		noteParts = append(noteParts, "tags:"+strings.Join(tags, ","))
	}
	noteParts = append(noteParts, "type:"+factType)
	if len(unresolvedNames) > 0 {
		noteParts = append(noteParts, "unresolved:"+strings.Join(unresolvedNames, ","))
	}
	note := strings.Join(noteParts, " | ")

	// Insert fact with confidence='pending'
	var factID string
	var err error
	if IsValidUUID(threadID) {
		err = db.QueryRow(`INSERT INTO ont_learned_fact
			(project_id, title, summary, content, confidence, source_thread_id, source_type, keywords, tags, fact_type, note)
			VALUES ($1, $2, $3, $4, 'pending', $5, 'agent', $6, $7, $8, $9) RETURNING id`,
			projectID, title, summary, content, threadID, legacyKeywords, tagsLit, factType, note).Scan(&factID)
	} else {
		err = db.QueryRow(`INSERT INTO ont_learned_fact
			(project_id, title, summary, content, confidence, source_type, keywords, tags, fact_type, note)
			VALUES ($1, $2, $3, $4, 'pending', 'agent', $5, $6, $7, $8) RETURNING id`,
			projectID, title, summary, content, legacyKeywords, tagsLit, factType, note).Scan(&factID)
	}
	if err != nil || factID == "" {
		errMsg := "unknown"
		if err != nil {
			errMsg = err.Error()
		}
		return M{"error": "保存失败: " + errMsg}
	}

	// Insert validated ont_fact_link rows
	type resolvedLink struct {
		TargetType string
		TargetID   string
		TargetName string
		Role       string
	}
	var resolved []resolvedLink
	for _, l := range validLinks {
		db.Exec(`INSERT INTO ont_fact_link (project_id, fact_id, target_type, target_id, role)
			VALUES ($1, $2, $3, $4, $5)`,
			projectID, factID, l.TargetType, l.TargetID, l.Role)
		targetName := resolveFactLinkTargetName(db, l.TargetType, l.TargetID)
		resolved = append(resolved, resolvedLink{TargetType: l.TargetType, TargetID: l.TargetID, TargetName: targetName, Role: l.Role})
	}

	// Compute and save embedding (async) — text = summary + all tags
	go embedAndSaveFactVector(db, factID, summary, strings.Join(tags, " "))

	// Build display strings
	var linkSummary []string
	for _, r := range resolved {
		prefix := "?"
		switch r.TargetType {
		case "object":
			prefix = "Od"
		case "knowledge":
			prefix = "Ok"
		case "fact":
			prefix = "Ol"
		}
		name := r.TargetName
		if name == "" {
			name = r.TargetID[:8]
		}
		linkSummary = append(linkSummary, fmt.Sprintf("%s:%s(%s)", prefix, name, r.Role))
	}
	linkedDisplay := strings.Join(linkSummary, "、")
	tagsDisplay := "无"
	if len(tags) > 0 {
		tagsDisplay = strings.Join(tags, "、")
	}

	// Build response links array for frontend
	respLinks := make([]M, 0, len(resolved))
	for _, r := range resolved {
		respLinks = append(respLinks, M{
			"targetType": r.TargetType,
			"targetId":   r.TargetID,
			"targetName": r.TargetName,
			"role":       r.Role,
		})
	}

	// fact_type label for display
	factTypeLabel := map[string]string{
		"business_rule":    "业务规则",
		"calibration":      "口径修正",
		"misconception":    "误解纠正",
		"filter_hint":      "默认过滤",
		"calculation_note": "计算注意",
	}

	// Build unresolved warning
	unresolvedWarning := ""
	if len(unresolvedNames) > 0 {
		unresolvedWarning = fmt.Sprintf("\n⚠ 未解析的关联: %s", strings.Join(unresolvedNames, "、"))
	}

	return M{
		"factId":               factID,
		"title":                title,
		"summary":              summary,
		"content":              content,
		"factType":             factType,
		"tags":                 tags,
		"links":                respLinks,
		"unresolvedLinks":      unresolvedNames,
		"pending_confirmation": true,
		"summary_text": fmt.Sprintf(
			"已生成待确认的习得知识：\n**%s** [%s]\n%s\n\n标签：%s\n关联：%s%s\n\n默认未启用，请点击「启用」使其生效。",
			title,
			factTypeLabel[factType],
			summary,
			tagsDisplay,
			linkedDisplay,
			unresolvedWarning,
		),
	}
}
