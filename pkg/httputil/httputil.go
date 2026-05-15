package httputil

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// M is a shorthand for map[string]interface{}.
type M = map[string]interface{}

// CorsHeaders is a no-op fallback that defers entirely to the outer
// CORSMiddleware. The previous implementation set
//
//	Access-Control-Allow-Origin: *
//
// whenever no upstream middleware had already written ACAO — meaning any
// service that forgot to wrap with CORSMiddleware silently shipped a
// fully open CORS policy. Combined with bearer-token auth (no SameSite
// cookies), this is exploitable from any malicious origin.
//
// We now leave the headers untouched here. CORS handling lives in
// exactly one place: CORSMiddleware in cors.go, which reads
// CORS_ALLOW_ORIGINS and echoes the request Origin only when it
// matches. Missing env = no ACAO header = browsers block the response.
//
// Legacy handler call sites still invoke this for forward compatibility;
// the function stays in the API surface so we don't have to touch
// every handler when the CORS contract changes again.
func CorsHeaders(w http.ResponseWriter) {
	_ = w // intentionally empty; see comment above
}

func JsonResp(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	CorsHeaders(w)
	json.NewEncoder(w).Encode(data)
}

// JsonError writes a JSON body with the given non-200 status code.
//
// Use this instead of `w.WriteHeader(status); JsonResp(w, ...)` —
// once WriteHeader is called, w.Header().Set("Content-Type",...) is
// silently dropped, so the body sails out as text/plain even when it's
// JSON. JsonError sets the Content-Type *before* WriteHeader so clients
// that switch on Content-Type (e.g. our front-end api wrapper) can parse
// the error body as JSON.
func JsonError(w http.ResponseWriter, status int, data interface{}) {
	CorsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func HandleOptions(w http.ResponseWriter) {
	CorsHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

func ListResp(w http.ResponseWriter, data interface{}, total int) {
	JsonResp(w, M{"data": data, "total": total})
}

func GetProjectID(r *http.Request) string {
	return r.URL.Query().Get("projectId")
}

// ExtractID extracts an ID from path like /api/model/tables/{id} or /api/model/tables/{id}/mark
func ExtractID(path, prefix string) string {
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.TrimPrefix(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func ReadBody(r *http.Request) M {
	var body M
	json.NewDecoder(r.Body).Decode(&body)
	return body
}

func StrVal(m M, key string) string {
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func BoolVal(m M, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// PgArrayToStrings converts a PostgreSQL text array string to []string.
func PgArrayToStrings(s sql.NullString) []string {
	if !s.Valid || s.String == "" || s.String == "{}" {
		return []string{}
	}
	inner := strings.Trim(s.String, "{}")
	if inner == "" {
		return []string{}
	}
	var result []string
	inQuote := false
	current := ""
	for _, ch := range inner {
		switch {
		case ch == '"' && !inQuote:
			inQuote = true
		case ch == '"' && inQuote:
			inQuote = false
		case ch == ',' && !inQuote:
			result = append(result, current)
			current = ""
		default:
			current += string(ch)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

// ParsePgTextArray parses a PostgreSQL text[] literal like {a,"b c","d\"e"} into []string.
// Handles quoted elements (with escaped quotes and backslashes) and unquoted elements.
// Returns empty slice for empty array {} or empty input.
func ParsePgTextArray(s string) []string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return []string{}
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}

	var result []string
	var cur strings.Builder
	inQuote := false
	hasContent := false
	i := 0
	for i < len(inner) {
		c := inner[i]
		if inQuote {
			if c == '\\' && i+1 < len(inner) {
				cur.WriteByte(inner[i+1])
				i += 2
				hasContent = true
				continue
			}
			if c == '"' {
				inQuote = false
				i++
				continue
			}
			cur.WriteByte(c)
			hasContent = true
			i++
			continue
		}
		if c == '"' {
			inQuote = true
			hasContent = true
			i++
			continue
		}
		if c == ',' {
			if hasContent {
				result = append(result, cur.String())
			}
			cur.Reset()
			hasContent = false
			i++
			continue
		}
		cur.WriteByte(c)
		hasContent = true
		i++
	}
	if hasContent {
		result = append(result, cur.String())
	}
	// Filter out NULL markers
	var out []string
	for _, e := range result {
		if e != "NULL" && e != "" {
			out = append(out, e)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// StringsSliceToPgArray converts a Go []string directly to a PostgreSQL text array literal.
func StringsSliceToPgArray(arr []string) string {
	if len(arr) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(arr))
	for _, s := range arr {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		parts = append(parts, `"`+s+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// StringsToPgArray converts a JSON body field ([]interface{}) to a PostgreSQL text array literal.
func StringsToPgArray(m M, key string) interface{} {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	parts := make([]string, 0, len(arr))
	for _, item := range arr {
		s := fmt.Sprintf("%v", item)
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		parts = append(parts, `"`+s+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func NullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func NullTimeStr(nt sql.NullTime) string {
	if nt.Valid {
		return nt.Time.Format(time.RFC3339)
	}
	return ""
}

func NullFloat(nf sql.NullFloat64) interface{} {
	if nf.Valid {
		return nf.Float64
	}
	return nil
}

func NullInt(ni sql.NullInt64) interface{} {
	if ni.Valid {
		return ni.Int64
	}
	return nil
}

func NullTime(nt sql.NullTime) string {
	if nt.Valid {
		return nt.Time.Format(time.RFC3339)
	}
	return ""
}

// PgVec formats a float64 slice as a Postgres vector literal string: "[0.1,0.2,...]"
func PgVec(v []float64) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(f, 'f', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// IsValidUUID checks if s looks like a valid UUID (not empty, not "undefined", not "null").
func IsValidUUID(s string) bool {
	return s != "" && s != "undefined" && s != "null" && len(s) >= 32
}

func NilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// SseEvent writes a single SSE event to the response writer and flushes.
func SseEvent(w http.ResponseWriter, data interface{}) {
	jsonBytes, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func SetupSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	CorsHeaders(w)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
