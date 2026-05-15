package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/lakehouse2ontology/llmclient"

	. "github.com/lakehouse2ontology/httputil"
)

// simpleSplitTokens splits Chinese text into tokens by common delimiters.
// Migrated from backend/agent/tokenize.go (SimpleSplit) after legacy RAG agent
// package was removed in the lakehouse-only branch. Used as a final fallback
// when LLM tokenization fails and no annotation few-shots are available.
func simpleSplitTokens(question string) []string {
	var tokens []string
	for _, seg := range strings.FieldsFunc(question, func(r rune) bool {
		return r == '的' || r == '，' || r == '。' || r == '？' || r == ' ' || r == '、'
	}) {
		seg = strings.TrimSpace(seg)
		if seg != "" {
			tokens = append(tokens, seg)
		}
	}
	if len(tokens) == 0 {
		tokens = []string{question}
	}
	return tokens
}

// loadTokenizeDictHints fetches keyword hints from two sources via *reverse*
// substring match — i.e. "is the dictionary entry a substring of the
// question?". This avoids the chicken-and-egg of needing tokens before
// tokenizing.
//
//   - Top 3 from `lakehouse_keyword` (machine codes excluded, longest first)
//     to seed canonical business terms.
//   - Top 5 from `ont_token_annotation` (mark=true) — the data flywheel: once
//     a user annotates a misrecognised token, future tokenisations see it
//     here and the LLM keeps it whole.
//
// "Longest first" handles the `key` vs `keyword1` overlap rule: with both in
// the dictionary and `keyword1 xxxx` as input, `keyword1` ranks above `key`
// and gets picked. Same logic for Chinese (`订单` vs `订单数量`).
//
// Best-effort: SQL errors and missing tables are silently ignored. Returns
// deduped, lower-case-deduped slice.
func loadTokenizeDictHints(db *sql.DB, pid, question string) []string {
	if db == nil || pid == "" || strings.TrimSpace(question) == "" {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		key := strings.ToLower(s)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}

	// Source 1: lakehouse_keyword (top 3)
	if rows, err := db.Query(`
		SELECT keyword
		FROM lakehouse_keyword
		WHERE project_id = $1
		  AND $2 ILIKE '%' || keyword || '%'
		  AND COALESCE(is_machine_code, false) = false
		ORDER BY LENGTH(keyword) DESC
		LIMIT 3`, pid, question); err == nil {
		for rows.Next() {
			var kw string
			if rows.Scan(&kw) == nil {
				add(kw)
			}
		}
		rows.Close()
	}

	// Source 2: ont_token_annotation (top 5, only audited rows)
	if rows, err := db.Query(`
		SELECT token
		FROM ont_token_annotation
		WHERE project_id = $1
		  AND $2 ILIKE '%' || token || '%'
		  AND mark = true
		ORDER BY LENGTH(token) DESC, created_at DESC
		LIMIT 5`, pid, question); err == nil {
		for rows.Next() {
			var tok string
			if rows.Scan(&tok) == nil {
				add(tok)
			}
		}
		rows.Close()
	}

	return out
}

// tokenizeFallback uses LLM (role=tokenize) to tokenize a Chinese question
// into semantic tokens, with keyword_split mark=true rows as fallback few-shot
// examples. Falls back to simpleSplitTokens on any failure.
//
// This is the *second* fallback layer in the annotations handler pipeline:
//
//	tokenizeWithAnnotationFewShots → tokenizeFallback → simpleSplitTokens
//
// Migrated from backend/agent/tokenize.go (Tokenize) when the legacy RAG
// agent package was removed.
func tokenizeFallback(db *sql.DB, pid, question string) []string {
	baseURL, apiKey, modelName, isThinking, _, vendor := llmclient.GetConfigForRole(db, "tokenize")
	if baseURL == "" || modelName == "" {
		return simpleSplitTokens(question)
	}

	// Fallback few-shot from keyword_split (used outside agent-v2 context)
	var fewShot []string
	rows, err := db.Query(`SELECT original_text, tokens FROM keyword_split WHERE project_id=$1 AND mark=true ORDER BY created_at DESC LIMIT 5`, pid)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var orig string
			var tokens []byte
			rows.Scan(&orig, &tokens)
			tokStr := strings.Trim(string(tokens), "{}")
			parts := strings.Split(tokStr, ",")
			for i := range parts {
				parts[i] = strings.Trim(parts[i], "\" ")
			}
			b, _ := json.Marshal(parts)
			fewShot = append(fewShot, fmt.Sprintf("输入: %s\n输出: %s", orig, string(b)))
		}
	}

	systemPrompt := "你是中文语义分词助手。将用户的查询问题拆分为语义完整的业务术语和关键词。保留完整的业务术语（如\"销售额\"、\"产品类别\"），不要拆散它们。返回JSON数组格式。"
	if len(fewShot) > 0 {
		systemPrompt += "\n\n示例:\n" + strings.Join(fewShot, "\n\n")
	}

	// Dict + flywheel hints: words known to the system that appear verbatim in
	// the question. Tells the LLM to keep them intact instead of splitting
	// them. We also keep this slice around so the LLM-failure fallback can
	// promote the hints to tokens directly — they ARE the keywords we'd
	// want to recall against, by construction (substring of the question +
	// existing in lakehouse_keyword).
	hints := loadTokenizeDictHints(db, pid, question)
	if len(hints) > 0 {
		quoted := make([]string, len(hints))
		for i, h := range hints {
			quoted[i] = `"` + h + `"`
		}
		systemPrompt += "\n\n以下关键词已存在于知识库，请优先作为整体识别（不要拆分）：\n[" + strings.Join(quoted, ", ") + "]"
		log.Printf("tokenizeFallback: injected %d dict hints: %v", len(hints), hints)
	}

	userPrompt := fmt.Sprintf("请对以下问题进行语义分词，返回JSON数组:\n%s", question)

	messages := []M{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}
	chatBody := M{"model": modelName, "messages": messages, "max_tokens": 512, "temperature": 0, "_vendor": vendor}
	if isThinking {
		chatBody["max_tokens"] = 8192
	}

	// degradeFromHints returns dict hints (if any) merged with simpleSplit
	// output as the last-resort token list. Used when the LLM call or its
	// JSON parsing fails — without this the recall layer would receive the
	// whole un-split question as a single token (since simpleSplitTokens
	// only splits on Chinese punctuation that is rarely present) and fail
	// to match any keyword.
	degradeFromHints := func() []string {
		simple := simpleSplitTokens(question)
		if len(hints) == 0 {
			return simple
		}
		seen := map[string]bool{}
		out := make([]string, 0, len(hints)+len(simple))
		for _, t := range hints {
			t = strings.TrimSpace(t)
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
		for _, t := range simple {
			t = strings.TrimSpace(t)
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
		return out
	}

	content, err := llmclient.DoChat(baseURL, apiKey, chatBody)
	if err != nil {
		log.Printf("tokenizeFallback: LLM call failed: %v (degrading to %d dict-hint tokens + simple split)", err, len(hints))
		return degradeFromHints()
	}

	content = llmclient.StripThinkTags(content)
	content = llmclient.ExtractJSON(content)

	var tokens []string
	if err := json.Unmarshal([]byte(content), &tokens); err != nil {
		log.Printf("tokenizeFallback: failed to parse JSON: %v, raw: %s (degrading to %d dict-hint tokens + simple split)", err, content, len(hints))
		return degradeFromHints()
	}
	if len(tokens) == 0 {
		return degradeFromHints()
	}
	return tokens
}
