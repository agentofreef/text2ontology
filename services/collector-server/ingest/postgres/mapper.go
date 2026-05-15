package postgres

import (
	"github.com/lakehouse2ontology/contracts"
	pbit "github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
)

// MapToPbitSchema converts a discovered catalog + wizard decisions into
// a PbitSchema that PopulateOntology can consume. Only tables not marked
// "skip" in wizard.TableRoles are included. Column roles drive IsHidden.
//
// This is a minimal implementation sufficient for Phase 3 wiring.
// Full semantic mapping (data-type fidelity, FK→PbitRelationship, M-expr
// generation) is deferred to Phase 6 e2e.
func MapToPbitSchema(
	tables []contracts.TableInfo,
	wizard contracts.WizardStateUpdate,
) *pbit.PbitSchema {
	schema := &pbit.PbitSchema{}

	for _, t := range tables {
		role := wizard.TableRoles[t.Name]
		if role == "skip" {
			continue
		}

		pt := pbit.PbitTable{Name: t.Name}

		colRoles := wizard.ColumnRoles[t.Name]
		for _, c := range t.Columns {
			colRole := ""
			if colRoles != nil {
				colRole = colRoles[c.Name]
			}
			isHidden := colRole == "skip"
			pt.Columns = append(pt.Columns, pbit.PbitColumn{
				Name:     c.Name,
				DataType: pgTypeToPbit(c.DataType),
				IsHidden: isHidden,
			})
		}

		schema.Tables = append(schema.Tables, pt)
	}

	// FK → PbitRelationship (minimal: use first column of each FK)
	for _, t := range tables {
		role := wizard.TableRoles[t.Name]
		if role == "skip" {
			continue
		}
		for _, fk := range t.ForeignKeys {
			schema.Relationships = append(schema.Relationships, pbit.PbitRelationship{
				Name:       fk.FromTable + "." + fk.FromColumn + "->" + fk.ToTable + "." + fk.ToColumn,
				FromTable:  fk.FromTable,
				FromColumn: fk.FromColumn,
				ToTable:    fk.ToTable,
				ToColumn:   fk.ToColumn,
				IsActive:   true,
			})
		}
	}

	return schema
}

// pgTypeToPbit maps Postgres information_schema data_type strings to the
// PBIT dataType strings accepted by pbit.pbitTypeToSQL.
func pgTypeToPbit(pgType string) string {
	switch pgType {
	case "integer", "bigint", "smallint", "int", "int2", "int4", "int8":
		return "int64"
	case "double precision", "real", "float", "float4", "float8":
		return "double"
	case "numeric", "decimal", "money":
		return "decimal"
	case "boolean":
		return "boolean"
	case "timestamp without time zone", "timestamp with time zone",
		"timestamp", "date", "time":
		return "datetime"
	default:
		return "string"
	}
}
