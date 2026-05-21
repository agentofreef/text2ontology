package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/llmclient"
	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

// ─── Annotation CRUD ─────────────────────────────────────────────

func handleAgentAnnotations(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)

		if r.Method == http.MethodPost {
			// Create (internal use only — normally created by pre-processing)
			body := ReadBody(r)
			question, _ := body["question"].(string)
			if question == "" {
				w.WriteHeader(400)
				JsonResp(w, M{"error": "question required"})
				return
			}
			var id string
			err := db.QueryRow(`INSERT INTO ont_agent_annotation (project_id, question) VALUES ($1, $2) RETURNING id`,
				pid, question).Scan(&id)
			if err != nil {
				w.WriteHeader(500)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id})
			return
		}

		// GET: list with pagination
		page := 1
		pageSize := 50
		if v := r.URL.Query().Get("page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				page = n
			}
		}
		if v := r.URL.Query().Get("pageSize"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				pageSize = n
			}
		}
		search := r.URL.Query().Get("search")
		statusFilter := r.URL.Query().Get("status")

		q := `SELECT id, project_id, COALESCE(thread_id::text,''), question,
			COALESCE(tokens,''), token_mappings, status, created_at, updated_at
			FROM ont_agent_annotation WHERE project_id = $1`
		args := []interface{}{pid}
		idx := 2

		if search != "" {
			q += fmt.Sprintf(` AND (question ILIKE $%d OR tokens ILIKE $%d)`, idx, idx)
			args = append(args, "%"+search+"%")
			idx++
		}
		if statusFilter == "true" {
			q += fmt.Sprintf(` AND status = $%d`, idx)
			args = append(args, true)
			idx++
		} else if statusFilter == "false" {
			q += fmt.Sprintf(` AND status = $%d`, idx)
			args = append(args, false)
			idx++
		}
		q = `SELECT sub.*, COUNT(*) OVER() AS total_count FROM (` + q + `) sub`
		q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, idx, idx+1)
		args = append(args, pageSize, (page-1)*pageSize)

		rows, err := db.Query(q, args...)
		if err != nil {
			log.Printf("agent-annotations list error: %v", err)
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var list []M
		var total int
		for rows.Next() {
			var id, projectID, threadID, question, tokens string
			var status bool
			var tmRaw sql.NullString
			var ca, ua time.Time
			rows.Scan(&id, &projectID, &threadID, &question, &tokens, &tmRaw, &status, &ca, &ua, &total)

			item := M{
				"id": id, "projectId": projectID, "threadId": threadID,
				"question": question, "tokens": tokens, "status": status,
				"createdAt": ca.Format(time.RFC3339), "updatedAt": ua.Format(time.RFC3339),
			}
			if tmRaw.Valid && tmRaw.String != "" && tmRaw.String != "null" {
				var tm interface{}
				json.Unmarshal([]byte(tmRaw.String), &tm)
				item["tokenMappings"] = tm
			}
			list = append(list, item)
		}
		if list == nil {
			list = []M{}
		}

		ListResp(w, list, total)
	}
}

func handleAgentAnnotationByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		id := ExtractID(r.URL.Path, "/api/ontology/agent-annotations")

		// Cross-project IDOR guard: confirm the caller can access this
		// annotation's project before any read/mutate by id.
		if !authmw.EnforceEntityProject(w, r, db, "ont_agent_annotation", "id", id) {
			return
		}

		if r.Method == http.MethodDelete {
			db.Exec(`DELETE FROM ont_agent_annotation WHERE id = $1`, id)
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			body := ReadBody(r)
			sets := []string{}
			vals := []interface{}{}
			i := 1

			if v, ok := body["tokens"].(string); ok {
				sets = append(sets, fmt.Sprintf("tokens = $%d", i))
				vals = append(vals, v)
				i++
			}
			if v, ok := body["status"].(bool); ok {
				sets = append(sets, fmt.Sprintf("status = $%d", i))
				vals = append(vals, v)
				i++
			}
			if len(sets) == 0 {
				JsonResp(w, M{"error": "nothing to update"})
				return
			}
			sets = append(sets, "updated_at = now()")
			q := "UPDATE ont_agent_annotation SET " + strings.Join(sets, ", ") + fmt.Sprintf(" WHERE id = $%d", i)
			vals = append(vals, id)
			db.Exec(q, vals...)

			// If status just set to true, recompute the vector if missing
			if status, ok := body["status"].(bool); ok && status {
				var hasVec bool
				db.QueryRow(`SELECT question_vector IS NOT NULL FROM ont_agent_annotation WHERE id = $1`, id).Scan(&hasVec)
				if !hasVec {
					var question string
					db.QueryRow(`SELECT question FROM ont_agent_annotation WHERE id = $1`, id).Scan(&question)
					if question != "" {
						embedAndSaveAnnotationVector(db, id, question)
					}
				}
			}

			JsonResp(w, M{"success": true})
			return
		}

		// GET
		var projectID, threadID, question, tokens string
		var status bool
		var tmRaw sql.NullString
		var ca, ua time.Time
		err := db.QueryRow(`SELECT project_id, COALESCE(thread_id::text,''), question,
			COALESCE(tokens,''), token_mappings, status, created_at, updated_at
			FROM ont_agent_annotation WHERE id = $1`, id).
			Scan(&projectID, &threadID, &question, &tokens, &tmRaw, &status, &ca, &ua)
		if err != nil {
			w.WriteHeader(404)
			JsonResp(w, M{"error": "not found"})
			return
		}
		item := M{
			"id": id, "projectId": projectID, "threadId": threadID,
			"question": question, "tokens": tokens, "status": status,
			"createdAt": ca.Format(time.RFC3339), "updatedAt": ua.Format(time.RFC3339),
		}
		if tmRaw.Valid && tmRaw.String != "" && tmRaw.String != "null" {
			var tm interface{}
			json.Unmarshal([]byte(tmRaw.String), &tm)
			item["tokenMappings"] = tm
		}
		JsonResp(w, item)
	}
}

// embedAndSaveAnnotationVector computes and saves the question vector.
func embedAndSaveAnnotationVector(db *sql.DB, annotationID, question string) {
	embeddings, err := llmclient.EmbedTexts(db, []string{question})
	if err != nil {
		log.Printf("[annotation embed] FAILED id=%s err=%v question=%q", annotationID, err, question)
		return
	}
	if len(embeddings) == 0 {
		log.Printf("[annotation embed] FAILED id=%s reason=empty_result question=%q", annotationID, question)
		return
	}
	vec := embeddings[0]
	vecStr := "["
	for i, v := range vec {
		if i > 0 {
			vecStr += ","
		}
		vecStr += fmt.Sprintf("%f", v)
	}
	vecStr += "]"
	if _, err := db.Exec(`UPDATE ont_agent_annotation SET question_vector = $1::vector WHERE id = $2`, vecStr, annotationID); err != nil {
		log.Printf("[annotation embed] UPDATE failed id=%s err=%v", annotationID, err)
		return
	}
	log.Printf("[annotation embed] OK id=%s dim=%d", annotationID, len(vec))
}

// handleAnnotationsRecompute recomputes question_vector for all annotations
// (by default only status=true ones missing vectors). POST endpoint.
// Query params: ?projectId=xxx&all=true (include pending annotations too)
func handleAnnotationsRecompute(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "POST only"})
			return
		}

		pid := GetProjectID(r)
		includeAll := r.URL.Query().Get("all") == "true"

		query := `SELECT id::text, question FROM ont_agent_annotation
			WHERE project_id = $1 AND question_vector IS NULL`
		if !includeAll {
			query += " AND status = true"
		}
		rows, err := db.Query(query, pid)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		type job struct{ id, q string }
		var jobs []job
		for rows.Next() {
			var id, q string
			rows.Scan(&id, &q)
			jobs = append(jobs, job{id, q})
		}
		rows.Close()

		queued := len(jobs)
		log.Printf("[annotation recompute] queued %d annotations for project %s (includeAll=%v)", queued, pid, includeAll)

		// Run synchronously so caller can see result count
		ok, fail := 0, 0
		for _, j := range jobs {
			before := 0
			db.QueryRow(`SELECT COUNT(*) FROM ont_agent_annotation WHERE id = $1 AND question_vector IS NOT NULL`, j.id).Scan(&before)
			embedAndSaveAnnotationVector(db, j.id, j.q)
			after := 0
			db.QueryRow(`SELECT COUNT(*) FROM ont_agent_annotation WHERE id = $1 AND question_vector IS NOT NULL`, j.id).Scan(&after)
			if after > before {
				ok++
			} else {
				fail++
			}
		}

		JsonResp(w, M{"queued": queued, "succeeded": ok, "failed": fail})
	}
}

// ─── Pre-processing: Auto-tokenize + Keyword Lookup ──────────────

// AnnotationContext holds the result of the pre-processing step.
type AnnotationContext struct {
	Tokens        []string
	TokenMappings []M
	ContextMD     string // Injected into LLM context
}

// ProcessQueryAnnotation runs the hidden pre-processing step:
// 1. Load few-shot examples (vector top5 → keyword overlap top2)
// 2. LLM tokenize user question
// 3. Each token → keyword_explanation lookup (3-tier)
// 4. Build context injection markdown
// 5. Save annotation to DB (async, non-blocking)
func ProcessQueryAnnotation(ctx context.Context, db *sql.DB, projectID, threadID, question string) AnnotationContext {
	var result AnnotationContext

	// Step 1: Embed question + load few-shot examples from ont_agent_annotation (status=true, vector top-5 → overlap top-2)
	fewShot := loadAnnotationFewShots(db, projectID, question)

	// Step 2: Tokenize using annotation few-shots (preferred) or keyword_split fallback.
	// annotation few-shots are domain-specific and confirmed by user (status=true).
	tokens := tokenizeWithAnnotationFewShots(db, projectID, question, fewShot)
	result.Tokens = tokens

	// Step 3: Token recall via keyword_explanation → Od mapping.
	// ctx is threaded from the caller (request ctx on the HTTP path) so recall +
	// downstream LLM work is cancellable rather than pinned to a background ctx.
	recallResult := recall.BuildLakehouseContext(ctx, db, projectID, tokens, question)
	var mappings []M
	var contextLines []string

	for _, tok := range tokens {
		if hits, ok := recallResult.TokenDetails[tok]; ok && len(hits) > 0 {
			var parts []string
			for _, h := range hits {
				parts = append(parts, fmt.Sprintf("%s(%s)", h.Keyword, h.Tier))
			}
			mappings = append(mappings, M{"token": tok, "result": strings.Join(parts, ", ")})
			contextLines = append(contextLines, fmt.Sprintf("- `%s` → %s", tok, strings.Join(parts, ", ")))
		} else {
			mappings = append(mappings, M{"token": tok, "result": "未命中"})
		}
	}
	result.TokenMappings = mappings

	// Step 4: Build context injection (only when we have Od matches)
	if len(contextLines) > 0 {
		var sb strings.Builder
		sb.WriteString("## 问题分词与实体识别（自动）\n\n")
		sb.WriteString(fmt.Sprintf("**分词**: `%s`\n\n", strings.Join(tokens, " | ")))
		sb.WriteString("**识别到的数据实体**:\n")
		for _, line := range contextLines {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
		result.ContextMD = sb.String()
	}

	// Step 5: Always save annotation to DB regardless of tokenization result
	// (tokens may be empty if LLM failed — user can fill them in on annotation page)
	go saveAnnotation(db, projectID, threadID, question, tokens, mappings)

	return result
}

// loadAnnotationFewShots returns top-5 confirmed annotations (status=true) by vector similarity.
// Pure vector search — no overlap re-ranking.
func loadAnnotationFewShots(db *sql.DB, projectID, question string) []M {
	embeddings, err := llmclient.EmbedTexts(db, []string{question})
	if err != nil || len(embeddings) == 0 {
		return nil
	}
	vecStr := "["
	for i, v := range embeddings[0] {
		if i > 0 {
			vecStr += ","
		}
		vecStr += fmt.Sprintf("%f", v)
	}
	vecStr += "]"

	rows, err := db.Query(`SELECT question, COALESCE(tokens,'')
		FROM ont_agent_annotation
		WHERE project_id = $1 AND status = true AND question_vector IS NOT NULL
		ORDER BY question_vector <=> $2::vector
		LIMIT 5`, projectID, vecStr)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()

	var result []M
	for rows.Next() {
		var q, toks string
		rows.Scan(&q, &toks)
		if q != "" && toks != "" {
			result = append(result, M{"question": q, "tokens": toks})
		}
	}
	return result
}

// TokenizeDebugInfo captures everything about a tokenize call for debugging.
type TokenizeDebugInfo struct {
	Path         string `json:"path"`         // "llm_fewshot" | "agent_tokenize" | "simple_split" | "llm_fallback"
	Reason       string `json:"reason"`       // why this path was chosen
	FewShotCount int    `json:"fewShotCount"` // number of few-shots retrieved
	SystemPrompt string `json:"systemPrompt"` // full system prompt sent to LLM
	UserPrompt   string `json:"userPrompt"`   // full user prompt sent to LLM
	Model        string `json:"model"`        // model name
	RawResponse  string `json:"rawResponse"`  // raw LLM response before JSON parse
}

// tokenizeWithAnnotationFewShots tokenizes a question using confirmed annotation records as few-shot examples.
// Few-shots come from ont_agent_annotation (status=true), retrieved by vector+overlap in loadAnnotationFewShots.
// Falls back to tokenizeFallback (keyword_split based) when no annotation few-shots are available,
// and further falls back to simpleSplitTokens if the LLM call fails.
func tokenizeWithAnnotationFewShots(db *sql.DB, projectID, question string, fewShots []M) []string {
	toks, _ := tokenizeWithAnnotationFewShotsDebug(db, projectID, question, fewShots)
	return toks
}

// tokenizeWithAnnotationFewShotsDebug is the instrumented version that captures the full prompt.
func tokenizeWithAnnotationFewShotsDebug(db *sql.DB, projectID, question string, fewShots []M) ([]string, *TokenizeDebugInfo) {
	dbg := &TokenizeDebugInfo{FewShotCount: len(fewShots)}

	if len(fewShots) == 0 {
		dbg.Path = "agent_tokenize"
		dbg.Reason = "no annotation few-shots available — fell back to tokenizeFallback (keyword_split based)"
		return tokenizeFallback(db, projectID, question), dbg
	}

	baseURL, apiKey, modelName, isThinking, _, vendor := llmclient.GetConfigForRole(db, "tokenize")
	if baseURL == "" || modelName == "" {
		dbg.Path = "simple_split"
		dbg.Reason = "no LLM config for 'tokenize' role"
		return simpleSplitTokens(question), dbg
	}
	dbg.Model = modelName

	// Build system prompt with annotation few-shots
	var fewShotLines []string
	for _, fs := range fewShots {
		q, _ := fs["question"].(string)
		toks, _ := fs["tokens"].(string)
		if q == "" || toks == "" {
			continue
		}
		// tokens stored as "tok1|tok2|tok3" — convert to JSON array for the prompt
		parts := strings.Split(toks, "|")
		b, _ := json.Marshal(parts)
		fewShotLines = append(fewShotLines, fmt.Sprintf("输入: %s\n输出: %s", q, string(b)))
	}

	systemPrompt := "你是中文语义分词助手。将用户的查询问题拆分为语义完整的业务术语和关键词。保留完整的业务术语（如\"销售额\"、\"产品类别\"），不要拆散它们。返回JSON数组格式。"
	if len(fewShotLines) > 0 {
		systemPrompt += "\n\n以下是已确认的分词示例（业务领域标注）:\n" + strings.Join(fewShotLines, "\n\n")
	}
	userPrompt := fmt.Sprintf("请对以下问题进行语义分词，返回JSON数组:\n%s", question)
	dbg.SystemPrompt = systemPrompt
	dbg.UserPrompt = userPrompt

	messages := []M{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}
	chatBody := M{"model": modelName, "messages": messages, "max_tokens": 512, "temperature": 0, "_vendor": vendor}
	if isThinking {
		chatBody["max_tokens"] = 8192
	}

	content, err := llmclient.DoChat(baseURL, apiKey, chatBody)
	if err != nil {
		dbg.Path = "llm_fallback"
		dbg.Reason = "LLM call failed: " + err.Error()
		return simpleSplitTokens(question), dbg
	}
	content = llmclient.StripThinkTags(content)
	content = llmclient.ExtractJSON(content)
	dbg.RawResponse = content

	var tokens []string
	if err := json.Unmarshal([]byte(content), &tokens); err != nil || len(tokens) == 0 {
		dbg.Path = "llm_fallback"
		dbg.Reason = "LLM response parse failed or empty"
		return simpleSplitTokens(question), dbg
	}
	dbg.Path = "llm_fewshot"
	dbg.Reason = fmt.Sprintf("used %d few-shots via LLM", len(fewShotLines))
	return tokens, dbg
}

// handleLakehouseTokenRecallWithTokenize is the lakehouse version of token-recall-tokenize.
// Uses the SAME LLM tokenization + fewshot annotation flow as agent-v2,
// but calls BuildLakehouseContext (lakehouse_keyword) instead of BuildContext (keyword_explanation).
//
//	POST /api/ontology/lakehouse-token-recall-tokenize?projectId=xxx
//	Body: { "question": "2025年Q1 Legion品牌产品下了多少订单?" }
func handleLakehouseTokenRecallWithTokenize(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			JsonResp(w, M{"error": "POST only"})
			return
		}

		pid := GetProjectID(r)
		body := ReadBody(r)
		question := StrVal(body, "question")
		if question == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "question required"})
			return
		}

		// Step 1: LLM tokenize with annotation fewshots + capture debug info
		fewShots := loadAnnotationFewShots(db, pid, question)
		tokens, tokenizeDebug := tokenizeWithAnnotationFewShotsDebug(db, pid, question, fewShots)
		// Persist annotation async
		go saveAnnotation(db, pid, "", question, tokens, nil)

		// Step 3: Lakehouse recall (lakehouse_keyword)
		result := recall.BuildLakehouseContext(r.Context(), db, pid, tokens, question)

		// Step 4 (debug-only, NEVER injected into LLM context):
		// For each token that produced no recall hit, run an unfiltered vector
		// top-5 against lakehouse_keyword.keyword_vector ∪ alias_vectors. Lets
		// the user see what the system would have considered if Tier 4 had run.
		// Note: the live LLM agent does NOT call this — only this debug endpoint.
		vectorCandidates := map[string][]recall.VectorCandidate{}
		for _, tok := range tokens {
			if hits, ok := result.TokenDetails[tok]; ok && len(hits) > 0 {
				continue
			}
			if cands := recall.LakehouseVectorTopN(r.Context(), db, pid, tok, 5); cands != nil {
				vectorCandidates[tok] = cands
			}
		}

		fewShotList := make([]M, 0, len(fewShots))
		for _, fs := range fewShots {
			fewShotList = append(fewShotList, M{
				"question": fs["question"],
				"tokens":   fs["tokens"],
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(M{
			"question":         question,
			"tokens":           tokens,
			"recall":           result,
			"tokenizeDebug":    tokenizeDebug,
			"fewShots":         fewShotList,
			"vectorCandidates": vectorCandidates,
		})
	}
}

// (lakehouseVectorTopN moved to recall.LakehouseVectorTopN so both this handler
// and the manual-token-debug handler in recall/handler.go can share it.)

// saveAnnotation persists the annotation to DB (called in goroutine).
// Dedup rule: if an annotation with the same (project_id, question) already exists:
//   - status=true  → keep it untouched (it's a confirmed golden example)
//   - status=false → update tokens/token_mappings with latest results
func saveAnnotation(db *sql.DB, projectID, threadID, question string, tokens []string, mappings []M) {
	tokensStr := strings.Join(tokens, "|")
	tmJSON, _ := json.Marshal(mappings)

	// Check if a confirmed (status=true) annotation exists — never overwrite golden examples
	var confirmedID string
	db.QueryRow(`SELECT id FROM ont_agent_annotation
		WHERE project_id = $1 AND md5(question) = md5($2) AND status = true LIMIT 1`,
		projectID, question).Scan(&confirmedID)
	if confirmedID != "" {
		return
	}

	// UPSERT: insert or update pending annotation (unique on project_id + md5(question))
	var annotationID string
	var err error
	if IsValidUUID(threadID) {
		err = db.QueryRow(`INSERT INTO ont_agent_annotation
			(project_id, thread_id, question, tokens, token_mappings)
			VALUES ($1, $2, $3, $4, $5::jsonb)
			ON CONFLICT (project_id, md5(question)) DO UPDATE SET
				tokens = EXCLUDED.tokens,
				token_mappings = EXCLUDED.token_mappings,
				thread_id = COALESCE(EXCLUDED.thread_id, ont_agent_annotation.thread_id),
				updated_at = now()
			WHERE ont_agent_annotation.status = false
			RETURNING id`,
			projectID, threadID, question, tokensStr, string(tmJSON)).Scan(&annotationID)
	} else {
		err = db.QueryRow(`INSERT INTO ont_agent_annotation
			(project_id, question, tokens, token_mappings)
			VALUES ($1, $2, $3, $4::jsonb)
			ON CONFLICT (project_id, md5(question)) DO UPDATE SET
				tokens = EXCLUDED.tokens,
				token_mappings = EXCLUDED.token_mappings,
				updated_at = now()
			WHERE ont_agent_annotation.status = false
			RETURNING id`,
			projectID, question, tokensStr, string(tmJSON)).Scan(&annotationID)
	}
	if err != nil || annotationID == "" {
		return
	}
	embedAndSaveAnnotationVector(db, annotationID, question)
}
