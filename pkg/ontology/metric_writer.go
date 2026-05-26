package ontology

// metric_writer.go — the unified-metric (lakehouse_metric) twin of
// intent_writer.go. Same invariant: a metric is invisible to recall unless at
// least one lakehouse_keyword row carries metric_id pointing at it, so the
// (metric, triggers) pair must land atomically. Targets the NEW lakehouse_metric
// table and writes the FULL field set the old intent writer dropped on the floor
// (level, parameters, replace_group_by, default_order_by_*, default_limit, plan).
//
// Coexists with WriteIntentWithTriggers during the compatibility window — this
// does NOT touch lakehouse_metric_intent.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// MetricSpec mirrors the persisted columns of lakehouse_metric. Zero values land
// as the column default per docs/schema/schema.sql. CanonicalFilters / Parameters
// / Plan are raw JSON strings (Plan empty/"null" → SQL NULL = single-query metric).
type MetricSpec struct {
	ProjectID             string
	ObjectID              string // FK to ont_object_type — the bundled Od
	Name                  string
	DisplayName           string
	Description           string
	Level                 string // "simple" | "plan" — empty → "simple"
	CanonicalMetric       string
	CanonicalFilters      string // raw JSON array; default "[]"
	AutoGroupBy           []string
	ReplaceGroupBy        bool
	DefaultOrderByLabel   string
	DefaultOrderByDir     string // "" | "ASC" | "DESC"
	DefaultLimit          *int
	PivotOn               string
	PivotValues           []string
	PivotColumnLabels     []string
	PivotTotalLabel       string
	PivotWithPercent      *bool
	PivotAppendGrandTotal *bool
	PivotPercentAxis      string // "row" | "column" — falls back to "row"
	PivotPercentScope     string // "filtered" | "global" — falls back to "filtered"
	PivotPercentSuffix    string
	Parameters            string // raw JSON array; default "[]"
	Plan                  string // raw JSON object/array; empty/"null" → NULL
	QuerySQL              string // level='sql': human SQL (Od names + {{params}}); empty → NULL
	ResponseTemplate      string
	Priority              int
	Mark                  *bool  // nil → default true
	CreatedBy             string // optional UUID; empty → NULL
}

// WriteMetricWithTriggers inserts one lakehouse_metric row followed by N
// lakehouse_keyword rows (metric_id pointing at the new metric), inside the
// supplied dbExec (use a *sql.Tx for atomicity). Returns ErrNoTriggers if
// triggers is empty/blank. Mirrors WriteIntentWithTriggers.
func WriteMetricWithTriggers(ctx context.Context, db dbExec, spec MetricSpec, triggers []string) (metricID string, err error) {
	cleaned, err := normaliseTriggers(triggers)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(spec.Name) == "" {
		return "", errors.New("metric name is required")
	}
	if strings.TrimSpace(spec.ObjectID) == "" {
		return "", errors.New("objectId is required")
	}

	level := strings.TrimSpace(spec.Level)
	if level != "plan" && level != "sql" {
		level = "simple"
	}

	// SQL mode: query_sql IS the metric; canonical_metric carries the '(sql)'
	// sentinel (mirrors '(plan)') so the NOT NULL column stays and runtime routing
	// keys on level. Structured modes still require a real canonical_metric.
	canonicalMetric := spec.CanonicalMetric
	querySQL := strings.TrimSpace(spec.QuerySQL)
	if level == "sql" {
		if querySQL == "" {
			return "", errors.New("query_sql is required for level='sql'")
		}
		if strings.TrimSpace(canonicalMetric) == "" {
			canonicalMetric = "(sql)"
		}
	} else if strings.TrimSpace(canonicalMetric) == "" {
		return "", errors.New("canonical_metric is required")
	}
	pivotPercentAxis := spec.PivotPercentAxis
	if pivotPercentAxis != "row" && pivotPercentAxis != "column" {
		pivotPercentAxis = "row"
	}
	pivotPercentScope := spec.PivotPercentScope
	if pivotPercentScope != "global" {
		pivotPercentScope = "filtered"
	}
	pivotPercentSuffix := spec.PivotPercentSuffix
	if pivotPercentSuffix == "" {
		pivotPercentSuffix = "占比"
	}
	canonicalFilters := spec.CanonicalFilters
	if strings.TrimSpace(canonicalFilters) == "" || canonicalFilters == "null" {
		canonicalFilters = "[]"
	}
	parameters := spec.Parameters
	if strings.TrimSpace(parameters) == "" || parameters == "null" {
		parameters = "[]"
	}
	autoGB := spec.AutoGroupBy
	if autoGB == nil {
		autoGB = []string{}
	}
	// plan: empty/"null" → SQL NULL (single-query metric).
	var planArg interface{}
	if p := strings.TrimSpace(spec.Plan); p != "" && p != "null" {
		planArg = spec.Plan
	}

	err = db.QueryRowContext(ctx, `
		INSERT INTO lakehouse_metric
			(project_id, object_id, name, display_name, description, level,
			 canonical_metric, canonical_filters, auto_group_by, replace_group_by,
			 default_order_by_label, default_order_by_dir, default_limit,
			 pivot_on, pivot_values, pivot_column_labels, pivot_total_label,
			 pivot_with_percent, pivot_append_grand_total,
			 pivot_percent_axis, pivot_percent_scope, pivot_percent_suffix,
			 parameters, plan, response_template, priority, mark, created_by, query_sql)
		VALUES ($1, $2, $3, $4, $5, $6,
		        $7, $8::jsonb, $9, $10,
		        NULLIF($11,''), NULLIF($12,''), $13,
		        $14, $15, $16, COALESCE(NULLIF($17,''),'Total'),
		        COALESCE($18, false), COALESCE($19, false),
		        $20, $21, $22,
		        $23::jsonb, $24::jsonb, $25, $26, COALESCE($27, true), $28, $29)
		RETURNING id`,
		spec.ProjectID, spec.ObjectID, spec.Name, spec.DisplayName, spec.Description, level,
		canonicalMetric, canonicalFilters, pq.Array(autoGB), spec.ReplaceGroupBy,
		spec.DefaultOrderByLabel, spec.DefaultOrderByDir, spec.DefaultLimit,
		nilIfEmpty(spec.PivotOn), pq.Array(spec.PivotValues), pq.Array(spec.PivotColumnLabels), spec.PivotTotalLabel,
		spec.PivotWithPercent, spec.PivotAppendGrandTotal,
		pivotPercentAxis, pivotPercentScope, pivotPercentSuffix,
		parameters, planArg, spec.ResponseTemplate, spec.Priority, spec.Mark, nilIfEmpty(spec.CreatedBy), nilIfEmpty(querySQL),
	).Scan(&metricID)
	if err != nil {
		return "", fmt.Errorf("insert lakehouse_metric: %w", err)
	}

	if err := insertMetricTriggerKeywords(ctx, db, spec.ProjectID, spec.ObjectID, metricID, cleaned); err != nil {
		return metricID, err
	}
	return metricID, nil
}

// UpdateMetricTriggers atomically replaces the trigger keywords bound to the
// supplied metric (delete-then-insert). Empty list rejected with ErrNoTriggers.
// objectTypeID feeds lakehouse_keyword.object_type_id (NOT NULL).
func UpdateMetricTriggers(ctx context.Context, db dbExec, projectID, metricID, objectTypeID string, triggers []string) error {
	cleaned, err := normaliseTriggers(triggers)
	if err != nil {
		return err
	}
	if strings.TrimSpace(metricID) == "" {
		return errors.New("metricID is required")
	}
	if strings.TrimSpace(projectID) == "" {
		return errors.New("projectID is required")
	}
	if strings.TrimSpace(objectTypeID) == "" {
		return errors.New("objectTypeID is required")
	}

	if _, err := db.ExecContext(ctx, `
		DELETE FROM lakehouse_keyword
		WHERE project_id = $1 AND metric_id = $2`,
		projectID, metricID,
	); err != nil {
		return fmt.Errorf("delete prior metric triggers: %w", err)
	}

	return insertMetricTriggerKeywords(ctx, db, projectID, objectTypeID, metricID, cleaned)
}

// CountMetricTriggers returns how many trigger keywords are bound to the metric.
func CountMetricTriggers(ctx context.Context, db dbExec, metricID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM lakehouse_keyword
		WHERE metric_id = $1`,
		metricID,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func insertMetricTriggerKeywords(ctx context.Context, db dbExec, projectID, objectTypeID, metricID string, cleaned []string) error {
	for _, kw := range cleaned {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO lakehouse_keyword
				(project_id, object_type_id, metric_id, keyword, is_machine_code)
			VALUES ($1, $2, $3, $4, false)
			ON CONFLICT DO NOTHING`,
			projectID, objectTypeID, metricID, kw,
		); err != nil {
			return fmt.Errorf("insert metric trigger keyword %q: %w", kw, err)
		}
	}
	return nil
}
