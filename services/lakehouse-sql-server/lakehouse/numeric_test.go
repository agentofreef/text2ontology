package lakehouse

import "testing"

// TestIsNumericType_PostgresAliases guards the regression where Postgres
// internal type aliases (int8, int4, float8, etc.) were missing from the
// numeric whitelist, causing properties with data_type='int8' to be
// misclassified as non-numeric. That misclassification dumped the property
// into GroupByCols and triggered a COUNT(*) fallback in resolveMetricLakehouse
// — observed as "sum(ORDER_QUANTITY) → COUNT(COUNTRY) AS Count" on the
// lakehouse-agent.
func TestIsNumericType_PostgresAliases(t *testing.T) {
	numeric := []string{
		// English/generic
		"integer", "int", "int64", "double", "decimal", "float", "number",
		"currency", "bigint", "numeric", "real",
		// Postgres aliases — the ones that were missing
		"int2", "int4", "int8", "smallint", "float4", "float8",
		"double precision", "money", "serial", "bigserial",
		// Case + whitespace tolerance
		"INT8", " int8 ", "BigInt",
		// Parameterised
		"numeric(10,2)", "decimal(38,6)", "float(53)",
	}
	for _, d := range numeric {
		if !isNumericType(d) {
			t.Errorf("isNumericType(%q) = false, want true", d)
		}
	}

	nonNumeric := []string{
		"text", "varchar", "char", "string",
		"date", "timestamp", "time", "datetime",
		"boolean", "bool",
		"json", "jsonb",
		"", "unknown", "uuid",
	}
	for _, d := range nonNumeric {
		if isNumericType(d) {
			t.Errorf("isNumericType(%q) = true, want false", d)
		}
	}
}
