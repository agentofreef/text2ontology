package smartquery

import (
	"fmt"
	"strconv"
)

// BindIntentParams applies LLM-provided params to spec per the intent's
// parameter schema. Pure function — no DB, no I/O.
//
// Mirrors agent-server/smartquery.BindIntentParams (intentional duplication;
// service-deps gate forbids cross-service Go imports). Both implementations
// must stay in lock-step on rules:
//
//   - "int":             coerce raw → int; write to spec.Limit. Reject ≤ 0.
//   - "string":          coerce raw → string; if Property set, append filter
//     using Property + Op (default "=") + Value.
//   - "property_filter": coerce raw → string; append spec.Filters with
//     {Prop=Property, Op=Op (default "="), Value=raw, FuzzyMatch}.
//
// Validation outcomes (ResolveError codes match agent-server):
//
//   - PARAM_REQUIRED        Required + missing + no Default
//   - PARAM_TYPE_ERROR      Raw cannot coerce to declared type
//   - PARAM_UNKNOWN         LLM passes a name not in schema
//   - PARAM_SCHEMA_INVALID  property_filter without Property, or unknown Type
//
// This function is also the workhorse of P7.4's dry-run validation: passing
// dummy values per parameter type lets backend-api confirm the Intent's
// canonical_* + parameters schema produces a valid spec before save.
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

// DummyParamValue returns a type-appropriate sample value for the parameter,
// used by dry-run validation: every non-optional schema entry is exercised
// without callers having to provide real-world inputs. Returned value is
// guaranteed to coerce successfully under BindIntentParams when Default is
// absent and the parameter is required.
func DummyParamValue(p IntentParameter) interface{} {
	if p.Default != nil {
		return p.Default
	}
	switch p.Type {
	case "int":
		return float64(1) // float64 — JSON numeric default
	case "property_filter", "string":
		return "x"
	default:
		return ""
	}
}

// BuildBaseSpecFromHint constructs the spec skeleton from an Intent's
// canonical fields. It is the agent-server-side strict-mode pre-bind state
// — same skeleton lakehouseToolSmartQuery builds before BindIntentParams,
// reused here for dry-run validation so backend-api can verify the Intent
// metadata produces a valid spec without any LLM in the loop.
func BuildBaseSpecFromHint(hint *IntentHint, objectName string) QuerySpec {
	spec := QuerySpec{
		Objects:     []string{objectName},
		IntentHint:  hint,
		DisplayMode: "table",
	}
	return spec
}
