package handler

// intent_helpers.go contains shared helper types and functions that were
// previously defined in handler_intent.go / intent_validate_client.go.
// Those CRUD handler files were removed (the /api/ontology/metric-intents*
// routes are gone), but handler_lakehouse_metric.go and handler_export_import.go
// still rely on these primitives.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

// rowScanner is satisfied by *sql.Row and *sql.Rows, letting scan functions
// accept either without duplicating code.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// intValDefault reads an int from a body field, defaulting if absent or
// unparseable. JSON numbers decode to float64; int and int64 are also handled.
func intValDefault(body M, key string, def int) int {
	v, ok := body[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}

// stringSliceFromBody coerces body[key] (a JSON array decoded as
// []interface{}) into []string, dropping nil / non-string entries.
// Returns nil on miss so the caller can detect "field absent" vs "field empty".
func stringSliceFromBody(body M, key string) []string {
	v, ok := body[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// jsonArrayBytes normalises a JSON-ish value from ReadBody into a JSON array
// byte slice. Accepts []interface{} directly, or nil → "[]".
func jsonArrayBytes(v interface{}) []byte {
	if v == nil {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return []byte("[]")
	}
	return b
}

// optBoolPtr returns *bool for a field if present, else nil (so SQL uses
// COALESCE default).
func optBoolPtr(body M, key string) *bool {
	v, ok := body[key]
	if !ok || v == nil {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

// ── intent dry-run validator (used by handler_lakehouse_metric.go) ─────────

// intentValidateInput mirrors lakehouse-sql-server/smartquery.IntentValidationInput
// over the wire. Field names use the same JSON tags so the body marshals into
// the validator-side struct without a translation layer. We don't import the
// remote type because services-level Go imports are forbidden by the layer-deps
// gate (scripts/check-service-deps.sh).
type intentValidateInput struct {
	Name                string                    `json:"name"`
	ObjectName          string                    `json:"objectName"`
	CanonicalMetric     string                    `json:"canonicalMetric,omitempty"`
	CanonicalFilters    []intentValidateFilter    `json:"canonicalFilters,omitempty"`
	AutoGroupBy         []string                  `json:"autoGroupBy,omitempty"`
	ReplaceGroupBy      bool                      `json:"replaceGroupBy,omitempty"`
	DefaultOrderByLabel string                    `json:"defaultOrderByLabel,omitempty"`
	DefaultOrderByDir   string                    `json:"defaultOrderByDir,omitempty"`
	DefaultLimit        int                       `json:"defaultLimit,omitempty"`
	Parameters          []intentValidateParameter `json:"parameters,omitempty"`
}

type intentValidateFilter struct {
	Prop  string `json:"prop"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type intentValidateParameter struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Property    string      `json:"property,omitempty"`
	Op          string      `json:"op,omitempty"`
	Default     interface{} `json:"default,omitempty"`
	Optional    bool        `json:"optional,omitempty"`
	Description string      `json:"description,omitempty"`
	FuzzyMatch  bool        `json:"fuzzyMatch,omitempty"`
}

type intentValidateResult struct {
	Ok        bool                   `json:"ok"`
	Errors    []string               `json:"errors,omitempty"`
	Code      string                 `json:"code,omitempty"`
	BoundSpec map[string]interface{} `json:"boundSpec,omitempty"`
}

// validateIntentRemote calls lakehouse-sql-server's
// POST /internal/smartquery/validate-intent. When LAKEHOUSE_SQL_URL is unset,
// returns ok=true with a degraded-mode warning logged.
func validateIntentRemote(ctx context.Context, in intentValidateInput) intentValidateResult {
	url := strings.TrimSpace(os.Getenv("LAKEHOUSE_SQL_URL"))
	if url == "" {
		log.Printf("intent dry-run: LAKEHOUSE_SQL_URL unset — skipping validation (dev mode)")
		return intentValidateResult{Ok: true, Code: "VALIDATION_SKIPPED"}
	}
	token := strings.TrimSpace(os.Getenv("INTERNAL_TOKEN"))
	body, err := json.Marshal(in)
	if err != nil {
		return intentValidateResult{Ok: false, Errors: []string{fmt.Sprintf("encode validation body: %v", err)}, Code: "ENCODE_ERROR"}
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url+"/internal/smartquery/validate-intent", bytes.NewReader(body))
	if err != nil {
		return intentValidateResult{Ok: false, Errors: []string{fmt.Sprintf("build request: %v", err)}, Code: "REQUEST_ERROR"}
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
		req.Header.Set("Authorization", "Internal "+token)
	}
	req.Header.Set("X-Caller-Service", "backend-api")
	req.Header.Set("X-On-Behalf-Of", "intent-save")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("intent dry-run: transport failure (skipping): %v", err)
		return intentValidateResult{Ok: true, Code: "VALIDATION_TRANSPORT_FAILED"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("intent dry-run: lakehouse-sql-server returned %d (skipping)", resp.StatusCode)
		return intentValidateResult{Ok: true, Code: "VALIDATION_HTTP_ERROR"}
	}
	var out intentValidateResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Printf("intent dry-run: decode response failed: %v (skipping)", err)
		return intentValidateResult{Ok: true, Code: "VALIDATION_DECODE_ERROR"}
	}
	return out
}

// buildIntentValidateInput extracts dry-run fields from the POST/PUT body
// produced by ReadBody (already-deserialized map[string]interface{}).
func buildIntentValidateInput(db *sql.DB, body map[string]interface{}) intentValidateInput {
	in := intentValidateInput{
		Name:                StrVal(body, "name"),
		CanonicalMetric:     StrVal(body, "canonicalMetric"),
		AutoGroupBy:         stringsFromBody(body, "autoGroupBy"),
		ReplaceGroupBy:      boolFromBody(body, "replaceGroupBy"),
		DefaultOrderByLabel: StrVal(body, "defaultOrderByLabel"),
		DefaultOrderByDir:   StrVal(body, "defaultOrderByDir"),
		DefaultLimit:        intValDefault(body, "defaultLimit", 0),
	}
	if objID := StrVal(body, "objectId"); objID != "" && IsValidUUID(objID) {
		var name string
		if err := db.QueryRow(`SELECT COALESCE(name,'') FROM ont_object_type WHERE id = $1`, objID).Scan(&name); err == nil && name != "" {
			in.ObjectName = name
		}
	}
	if in.ObjectName == "" {
		in.ObjectName = "_validation_stub_"
	}
	if raw, ok := body["canonicalFilters"].([]interface{}); ok {
		for _, item := range raw {
			if fm, ok := item.(map[string]interface{}); ok {
				prop, _ := fm["prop"].(string)
				op, _ := fm["op"].(string)
				val, _ := fm["value"].(string)
				if prop != "" {
					in.CanonicalFilters = append(in.CanonicalFilters, intentValidateFilter{
						Prop: prop, Op: op, Value: val,
					})
				}
			}
		}
	}
	if raw, ok := body["parameters"].([]interface{}); ok {
		for _, item := range raw {
			if pm, ok := item.(map[string]interface{}); ok {
				p := intentValidateParameter{
					Name:        StrVal(pm, "name"),
					Type:        StrVal(pm, "type"),
					Property:    StrVal(pm, "property"),
					Op:          StrVal(pm, "op"),
					Default:     pm["default"],
					Optional:    boolFromBody(pm, "optional"),
					Description: StrVal(pm, "description"),
					FuzzyMatch:  boolFromBody(pm, "fuzzyMatch"),
				}
				if p.Name != "" {
					in.Parameters = append(in.Parameters, p)
				}
			}
		}
	}
	return in
}

// stringsFromBody extracts a []string from body[key].
func stringsFromBody(body map[string]interface{}, key string) []string {
	v, ok := body[key]
	if !ok || v == nil {
		return nil
	}
	switch raw := v.(type) {
	case []string:
		return raw
	case []interface{}:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// boolFromBody extracts a bool from body[key], returning false on miss.
func boolFromBody(body map[string]interface{}, key string) bool {
	v, ok := body[key]
	if !ok || v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
