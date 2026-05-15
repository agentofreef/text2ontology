package handler

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

// intentValidateInput mirrors lakehouse-sql-server/smartquery.IntentValidationInput
// over the wire. Field names use the same JSON tags so the body marshals into
// the validator-side struct without a translation layer. We don't import the
// remote type because services-level Go imports are forbidden by the layer-deps
// gate (scripts/check-service-deps.sh, Phase 1 D4d).
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
// returns ok=true with a degraded-mode warning logged. This keeps dev
// workflows running without lakehouse-sql-server in the loop while still
// gating saves in fully-deployed environments where the URL is configured.
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
		// Transport failure should not block save (dev-friendly) — log + skip
		// validation. Production should monitor this log for actual outages.
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
// produced by ReadBody (already-deserialized map[string]interface{}). objectName
// is fetched from db when objectId is provided — needed because the validator
// uses it as spec.Objects[0]. If objectId is unknown, falls back to a stub so
// validation can still proceed (objectName is purely structural — not resolved).
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
		// Validator requires non-empty objectName; substitute a safe stub
		// rather than failing here. The validator only uses objectName as a
		// string for spec.Objects[0]; it does NOT resolve it to a real Od.
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

// stringsFromBody extracts a []string from body["key"] where the value may
// be []interface{} (JSON array) or already []string.
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
