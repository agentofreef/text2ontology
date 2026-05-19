package smartquery

import (
	"fmt"
	"strconv"
	"strings"
)

// BindIntentParams applies LLM-provided params to spec per the intent's
// parameter schema. Pure function — no DB, no I/O.
//
// Defense line 1 of the strict-mode contract: the LLM only supplies typed
// param values (e.g. {"n":5,"genre":"Rock"}), and this function deterministically
// translates them to QuerySpec fields the lakehouse pipeline understands.
// LLM never gets to fill spec.Filters / spec.Limit / spec.OrderBy directly.
//
// Behavior per IntentParameter.Type:
//
//   - "int":             coerce raw → int; write to spec.Limit. Negative or zero
//     rejected (LIMIT 0 silently returns empty result — that's "wrong SQL").
//   - "string":          coerce raw → string; if Property set, append filter
//     using Property + Op (default "=") + Value. Otherwise no-op (reserved).
//   - "property_filter": coerce raw → string; append spec.Filters with
//     {Prop=Property, Op=Op (default "="), Value=raw, FuzzyMatch}.
//
// Validation rules:
//
//   - Required (Optional=false) + missing + no Default → PARAM_REQUIRED
//   - Type mismatch (raw cannot coerce to declared type) → PARAM_TYPE_ERROR
//   - LLM passes a name not in schema → PARAM_UNKNOWN
//   - Schema declares property_filter without Property → PARAM_SCHEMA_INVALID
//   - Unknown Type → PARAM_SCHEMA_INVALID
//
// Idempotency: calling twice with the same args produces the same spec
// (filters dedup is left to downstream resolve; we don't try to dedup here
// because the same property may legitimately have multiple filters with
// different ops).
func BindIntentParams(spec *QuerySpec, llmParams map[string]interface{}, intentParams []IntentParameter) error {
	schema := make(map[string]IntentParameter, len(intentParams))
	for _, p := range intentParams {
		schema[p.Name] = p
	}
	for k := range llmParams {
		if _, ok := schema[k]; !ok {
			return &ResolveError{
				Code:    "PARAM_UNKNOWN",
				Message: fmt.Sprintf("未知参数 %q — 该 Intent 仅接受: %v", k, paramNames(intentParams)),
				Detail:  map[string]any{"unknown": k, "allowed": paramNames(intentParams)},
			}
		}
	}
	for _, p := range intentParams {
		raw, present := llmParams[p.Name]
		if !present {
			if p.Default != nil {
				raw = p.Default
				present = true
			} else if !p.Optional {
				return &ResolveError{
					Code:    "PARAM_REQUIRED",
					Message: fmt.Sprintf("缺少必填参数 %q (%s)", p.Name, p.Description),
					Detail:  map[string]any{"name": p.Name},
				}
			}
		}
		if !present {
			continue
		}
		if err := applyOneParam(spec, p, raw); err != nil {
			return err
		}
	}
	return nil
}

func applyOneParam(spec *QuerySpec, p IntentParameter, raw interface{}) error {
	switch p.Type {
	case "int":
		n, err := coerceInt(raw)
		if err != nil {
			return &ResolveError{
				Code:    "PARAM_TYPE_ERROR",
				Message: fmt.Sprintf("参数 %q 期望 int，得到 %v (%T): %v", p.Name, raw, raw, err),
				Detail:  map[string]any{"name": p.Name, "got": raw},
			}
		}
		if n <= 0 {
			return &ResolveError{
				Code:    "PARAM_TYPE_ERROR",
				Message: fmt.Sprintf("参数 %q 必须 > 0，得到 %d", p.Name, n),
				Detail:  map[string]any{"name": p.Name, "got": n},
			}
		}
		spec.Limit = n
	case "string":
		s, ok := coerceString(raw)
		if !ok {
			return &ResolveError{
				Code:    "PARAM_TYPE_ERROR",
				Message: fmt.Sprintf("参数 %q 期望 string，得到 %v (%T)", p.Name, raw, raw),
				Detail:  map[string]any{"name": p.Name, "got": raw},
			}
		}
		if p.Property != "" {
			op := p.Op
			if op == "" {
				op = "="
			}
			spec.Filters = append(spec.Filters, FilterItem{
				Prop: p.Property, Op: op, Value: s, FuzzyMatch: p.FuzzyMatch,
			})
		}
	case "enum_ref":
		// Bounded value-ref contract — spec
		// .omc/specs/bounded-value-ref-contract.md. The Intent declares the
		// param value MUST come from a finite set (the project's
		// lakehouse_keyword rows for p.Property). The caller has already
		// resolved that set into p.AllowedValues; the binder is pure and
		// only compares against that in-memory list.
		//
		// Three failure modes worth distinguishing:
		//   1. Schema bug: p.Property is empty → PARAM_SCHEMA_INVALID.
		//      Catches "wrote type:enum_ref but forgot which keyword
		//      column" early, before any LLM input is examined.
		//   2. Caller didn't populate AllowedValues (nil slice) → fall back
		//      to type:string pass-through. Required by dry-run save
		//      validation, which has no DB to enumerate candidates with.
		//   3. AllowedValues populated AND raw not in it → PARAM_VALUE_UNKNOWN
		//      with full candidate list, so the agent loop can choose a
		//      valid one without guessing.
		if p.Property == "" {
			return &ResolveError{
				Code:    "PARAM_SCHEMA_INVALID",
				Message: fmt.Sprintf("参数 %q 类型 enum_ref 必须声明 property", p.Name),
				Detail:  map[string]any{"name": p.Name},
			}
		}
		s, ok := coerceString(raw)
		if !ok {
			return &ResolveError{
				Code:    "PARAM_TYPE_ERROR",
				Message: fmt.Sprintf("参数 %q 期望 string 值（enum_ref），得到 %v (%T)", p.Name, raw, raw),
				Detail:  map[string]any{"name": p.Name, "got": raw},
			}
		}
		op := p.Op
		if op == "" {
			op = "="
		}
		// nil AllowedValues = caller signaled "no candidate context"; behave
		// like type:string so dry-run / legacy callers don't regress.
		if p.AllowedValues == nil {
			spec.Filters = append(spec.Filters, FilterItem{
				Prop: p.Property, Op: op, Value: s, FuzzyMatch: p.FuzzyMatch,
			})
			return nil
		}
		// Strict mode: match against the candidate set. Match rules mirror
		// the corrector's Tier 1 (case-insensitive) plus a defensive Trim
		// so trailing whitespace from the LLM doesn't tank the bind. The
		// canonical value we push is the AllowedValues entry, NOT the raw
		// LLM string — that way the FilterItem.Value matches the DB literal
		// exactly even when LLM typed `shanghai` for `Shanghai`.
		trimmed := strings.TrimSpace(s)
		for _, av := range p.AllowedValues {
			if strings.EqualFold(strings.TrimSpace(av), trimmed) {
				spec.Filters = append(spec.Filters, FilterItem{
					Prop: p.Property, Op: op, Value: av, FuzzyMatch: p.FuzzyMatch,
				})
				return nil
			}
		}
		// Snapshot the allowed slice into Detail so callers can render a
		// hint without re-reading the schema; copy avoids the caller mutating
		// the schema's slice through the error.
		allowedCopy := append([]string(nil), p.AllowedValues...)
		return &ResolveError{
			Code: "PARAM_VALUE_UNKNOWN",
			Message: fmt.Sprintf("参数 %s 的值 %q 不在已知集合中。可选：[%s]",
				p.Name, s, strings.Join(p.AllowedValues, ", ")),
			Detail: map[string]any{
				"param":   p.Name,
				"got":     s,
				"allowed": allowedCopy,
			},
		}
	case "property_filter":
		if p.Property == "" {
			return &ResolveError{
				Code:    "PARAM_SCHEMA_INVALID",
				Message: fmt.Sprintf("参数 %q 类型 property_filter 必须声明 property", p.Name),
				Detail:  map[string]any{"name": p.Name},
			}
		}
		s, ok := coerceString(raw)
		if !ok {
			return &ResolveError{
				Code:    "PARAM_TYPE_ERROR",
				Message: fmt.Sprintf("参数 %q 期望 string 值（property_filter），得到 %v (%T)", p.Name, raw, raw),
				Detail:  map[string]any{"name": p.Name, "got": raw},
			}
		}
		op := p.Op
		if op == "" {
			op = "="
		}
		spec.Filters = append(spec.Filters, FilterItem{
			Prop: p.Property, Op: op, Value: s, FuzzyMatch: p.FuzzyMatch,
		})
	default:
		return &ResolveError{
			Code:    "PARAM_SCHEMA_INVALID",
			Message: fmt.Sprintf("参数 %q 未知类型 %q (合法: int/string/property_filter)", p.Name, p.Type),
			Detail:  map[string]any{"name": p.Name, "type": p.Type},
		}
	}
	return nil
}

func coerceInt(raw interface{}) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case float64:
		// JSON numbers come in as float64. Reject non-integers to avoid
		// silent truncation surprising the user (Top "5.5" → 5 is wrong).
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("non-integer numeric value %v", v)
		}
		return int(v), nil
	case float32:
		if v != float32(int64(v)) {
			return 0, fmt.Errorf("non-integer numeric value %v", v)
		}
		return int(v), nil
	case string:
		return strconv.Atoi(v)
	default:
		return 0, fmt.Errorf("unsupported type for int coercion: %T", raw)
	}
}

func coerceString(raw interface{}) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true
	case fmt.Stringer:
		return v.String(), true
	case nil:
		return "", false
	case bool:
		return strconv.FormatBool(v), true
	case int, int32, int64:
		return fmt.Sprintf("%d", v), true
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), true
		}
		return strconv.FormatFloat(v, 'f', -1, 64), true
	default:
		return "", false
	}
}

func paramNames(ps []IntentParameter) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}
