package main

import (
	"fmt"
	"os"

	"github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
)

// Usage: go run ./ingest/pbitlakehouse/cmd/dump <file.pbit>
// Prints a summary of tables, relationships, measures, and partition kind breakdown.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dump <file.pbit>")
		os.Exit(1)
	}
	path := os.Args[1]

	schema, err := pbit.ParsePbit(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	kindCounts := map[string]int{
		"unsupported":  0,
		"combine":      0,
		"constant_csv": 0,
		"unpivot":      0,
	}

	for _, t := range schema.Tables {
		for _, p := range t.Partitions {
			kind, _, _ := pbit.ClassifyPartition(string(p.Source.Expression))
			switch kind {
			case pbit.KindCombine:
				kindCounts["combine"]++
			case pbit.KindConstantCsv:
				kindCounts["constant_csv"]++
			case pbit.KindUnpivot:
				kindCounts["unpivot"]++
			default:
				kindCounts["unsupported"]++
			}
		}
	}

	measures := schema.AllMeasures()

	fmt.Printf("tables=%d relationships=%d measures=%d\n",
		len(schema.Tables), len(schema.Relationships), len(measures))
	fmt.Printf("partition kinds: unsupported=%d combine=%d constant_csv=%d unpivot=%d\n",
		kindCounts["unsupported"], kindCounts["combine"],
		kindCounts["constant_csv"], kindCounts["unpivot"])

	fmt.Println("\n--- Tables ---")
	for _, t := range schema.Tables {
		fmt.Printf("  %s  columns=%d partitions=%d measures=%d\n",
			t.Name, len(t.Columns), len(t.Partitions), len(t.Measures))
	}

	fmt.Println("\n--- Relationships ---")
	for _, r := range schema.Relationships {
		active := ""
		if r.IsActive {
			active = " [active]"
		}
		fmt.Printf("  %s.%s → %s.%s  cardinality=%s:%s%s\n",
			r.FromTable, r.FromColumn, r.ToTable, r.ToColumn,
			r.FromCardinality, r.ToCardinality, active)
	}
}
