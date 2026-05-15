package main

import "encoding/json"

// sensitiveConfigKeys are stripped from config_json before any response
// crosses the wire. The Postgres connector stores `password` here; any
// new connector adding credentials should grow this list rather than
// inventing a new "private" column.
//
// Match is case-insensitive — the wizard sometimes round-trips
// camelCase from the frontend.
var sensitiveConfigKeys = []string{
	"password",
	"secret",
	"api_key",
	"apiKey",
	"token",
}

// redactConfigJSON parses raw JSON, deletes any sensitive top-level key,
// and re-marshals. On any parse failure, returns an empty object so we
// never leak the raw string.
func redactConfigJSON(raw string) string {
	if raw == "" {
		return "{}"
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "{}"
	}
	for _, k := range sensitiveConfigKeys {
		delete(m, k)
	}
	// Also scrub case-insensitive matches that survived the literal pass.
	for k := range m {
		for _, sens := range sensitiveConfigKeys {
			if equalFoldShort(k, sens) {
				delete(m, k)
				break
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// equalFoldShort is a stdlib-free ASCII case-fold compare. Avoids
// importing strings just to make the redactor tiny and inlineable.
func equalFoldShort(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
