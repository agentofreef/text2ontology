package handler

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/lakehouse2ontology/llmclient"

	. "github.com/lakehouse2ontology/httputil"
)

// GET /api/ontology/query-logs-template — download CSV template with examples
func handleQueryLogTemplate(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=ontology_flywheel_template.csv")
		// BOM for Excel UTF-8
		w.Write([]byte{0xEF, 0xBB, 0xBF})

		writer := csv.NewWriter(w)
		// Header
		writer.Write([]string{"question", "tokens", "objects", "metric", "groupBy", "mark", "is_example"})
		// Example rows with instructions
		writer.Write([]string{
			"PRC地区的Real Order总数量按Geo分组",
			"PRC|Real Order|总数量|Geo|分组",
			"EARLY_ORDER",
			"Order.sum",
			"Order.Geo",
			"true",
			"true",
		})
		writer.Write([]string{
			"X11代legion产品有多少订单",
			"X11代|legion|产品|订单",
			"EARLY_ORDER|Product",
			"Order.sum",
			"",
			"true",
			"false",
		})
		writer.Write([]string{
			"LOQ Brand还不能下单的产品分布情况",
			"LOQ|Brand|还不能下单|产品分布情况",
			"Product",
			"",
			"Product.Series",
			"false",
			"false",
		})
		// Instruction rows
		writer.Write([]string{"# --- 以上为示例，以下为字段说明 ---"})
		writer.Write([]string{"# question", "用户的自然语言问题（必填，唯一标识）"})
		writer.Write([]string{"# tokens", "分词结果，用 | 分隔（如: X11代|legion|产品）"})
		writer.Write([]string{"# objects", "对象名，用 | 分隔（如: Order|Product）"})
		writer.Write([]string{"# metric", "口径，有且只能有一个（如: Order.sum），无则留空"})
		writer.Write([]string{"# groupBy", "分组属性，格式 对象名.属性名，用 | 分隔，无则留空"})
		writer.Write([]string{"# mark", "是否启用为飞轮数据（true/false）"})
		writer.Write([]string{"# is_example", "是否作为示例问题显示在Agent页面（true/false）"})
		writer.Flush()
	}
}

// POST /api/ontology/query-logs-upload — upload CSV to import flywheel data
func handleQueryLogUpload(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}

		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "projectId required"})
			return
		}

		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "file required"})
			return
		}
		defer file.Close()

		// Read all bytes, detect encoding, convert to UTF-8
		raw, _ := io.ReadAll(file)
		raw = bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF")) // strip BOM
		if !utf8.Valid(raw) {
			log.Printf("[flywheel-upload] detected non-UTF-8 encoding, converting from GBK")
			if decoded, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw); err == nil {
				raw = decoded
			}
		}

		reader := csv.NewReader(bytes.NewReader(raw))
		// Skip header
		header, err := reader.Read()
		if err != nil {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "invalid CSV"})
			return
		}
		_ = header

		var imported, skipped int
		for {
			row, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil || len(row) < 4 {
				continue
			}
			// Skip instruction rows
			if strings.HasPrefix(row[0], "#") {
				continue
			}

			question := strings.TrimSpace(row[0])
			if question == "" {
				continue
			}

			tokensStr := ""
			objectsStr := ""
			metricStr := ""
			groupByStr := ""
			markStr := "false"
			isExampleStr := "false"

			if len(row) > 1 {
				tokensStr = strings.TrimSpace(row[1])
			}
			if len(row) > 2 {
				objectsStr = strings.TrimSpace(row[2])
			}
			if len(row) > 3 {
				metricStr = strings.TrimSpace(row[3])
			}
			if len(row) > 4 {
				groupByStr = strings.TrimSpace(row[4])
			}
			if len(row) > 5 {
				markStr = strings.TrimSpace(row[5])
			}
			if len(row) > 6 {
				isExampleStr = strings.TrimSpace(row[6])
			}

			// Parse tokens: "X11代|legion|产品" → ["X11代","legion","产品"]
			var tokens []string
			if tokensStr != "" {
				for _, t := range strings.Split(tokensStr, "|") {
					t = strings.TrimSpace(t)
					if t != "" {
						tokens = append(tokens, t)
					}
				}
			}
			tokensJSON, _ := json.Marshal(tokens)

			// Parse objects: "Order|Product" → "Order,Product"
			objects := strings.ReplaceAll(objectsStr, "|", ",")

			// Parse groupBy: "Order.Geo|Product.Series" → "Order.Geo,Product.Series"
			groupBy := strings.ReplaceAll(groupByStr, "|", ",")

			mark := strings.ToLower(markStr) == "true"
			isExample := strings.ToLower(isExampleStr) == "true"

			// Compute question vector
			var vecStr *string
			if vecs, err := llmclient.EmbedTexts(db, []string{question}); err == nil && len(vecs) > 0 {
				vecStr = ptrStr(PgVec(vecs[0]))
			}

			_, err2 := db.Exec(`INSERT INTO ont_query_log (project_id, user_question, tokens, objects, metric, group_by, mark, is_example, question_vector, used_llm)
				VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, false)
				ON CONFLICT (project_id, user_question) DO UPDATE SET
					tokens = EXCLUDED.tokens, objects = EXCLUDED.objects, metric = EXCLUDED.metric,
					group_by = EXCLUDED.group_by, mark = EXCLUDED.mark, is_example = EXCLUDED.is_example`,
				pid, question, string(tokensJSON), objects, metricStr, groupBy, mark, isExample, vecStr)
			if err2 != nil {
				skipped++
			} else {
				imported++
			}
		}

		JsonResp(w, M{"imported": imported, "skipped": skipped})
	}
}

// GET /api/ontology/query-logs-export — export flywheel data as CSV
func handleQueryLogExport(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			w.WriteHeader(400)
			return
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=ontology_flywheel_export.csv")
		w.Write([]byte{0xEF, 0xBB, 0xBF})

		writer := csv.NewWriter(w)
		writer.Write([]string{"question", "tokens", "objects", "metric", "groupBy", "mark", "is_example"})

		rows, _ := db.Query(`SELECT user_question, tokens, COALESCE(objects,''), COALESCE(metric,''),
			COALESCE(group_by,''), mark, COALESCE(is_example, false)
			FROM ont_query_log WHERE project_id = $1 ORDER BY created_at DESC`, pid)
		if rows != nil {
			for rows.Next() {
				var q, objects, metric, groupBy string
				var tokBytes []byte
				var mark, isExample bool
				rows.Scan(&q, &tokBytes, &objects, &metric, &groupBy, &mark, &isExample)

				// Parse tokens JSON → pipe-separated
				var tokArr []string
				json.Unmarshal(tokBytes, &tokArr)
				tokStr := strings.Join(tokArr, "|")

				// Convert objects comma → pipe
				objStr := strings.ReplaceAll(objects, ",", "|")
				gbStr := strings.ReplaceAll(groupBy, ",", "|")

				writer.Write([]string{q, tokStr, objStr, metric, gbStr, fmt.Sprintf("%v", mark), fmt.Sprintf("%v", isExample)})
			}
			rows.Close()
		}
		writer.Flush()
	}
}

func ptrStr(s string) *string { return &s }
