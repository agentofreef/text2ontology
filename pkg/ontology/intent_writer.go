// Package ontology contains shared helpers that enforce cross-table
// invariants the database itself can't express. The motivating example —
// and the only one in this file — is the metric-intent ↔ trigger-keyword
// pair: a metric intent is functionally invisible to recall unless at
// least one row in lakehouse_keyword carries metric_intent_id pointing
// at it. Splitting that pair across two tables made it trivial for
// individual code paths (REST POST /metric-intents, bulk endpoints, ad-hoc
// migrations) to write only the intent and silently produce orphans.
//
// Every code path that creates or updates a lakehouse_metric_intent row
// must use the helpers here so the (intent, triggers) pair lands
// atomically. New paths that need to bypass this contract should not be
// written; if you think you need one, the answer is almost certainly to
// extend WriteIntentWithTriggers / UpdateIntentTriggers instead.
package ontology

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// ErrNoTriggers is returned by WriteIntentWithTriggers / UpdateIntentTriggers
// when the caller passes an empty (or all-blank) triggers slice. Callers
// should map this to a 400-class error and surface a message that nudges
// the user toward supplying at least one Chinese / English trigger word.
var ErrNoTriggers = errors.New("metric intent must have at least one trigger keyword (otherwise recall cannot match)")

// dbExec is the smallest interface that lets the helpers run inside either
// a *sql.DB (auto-committed, single statement convenience) or a *sql.Tx
// (caller composes a larger transaction).
type dbExec interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

// IntentSpec mirrors the persisted columns of lakehouse_metric_intent.
// All fields are written by WriteIntentWithTriggers; zero values land as
// the column default per the schema (see docs/schema/schema.sql).
//
// Use NormalizeAutoGroupBy / NormalizeCanonicalFilters helpers if you have
// loose-typed JSON to feed in; we don't normalise inside the writer so
// the caller stays in control of validation order.
type IntentSpec struct {
	ProjectID         string
	ObjectID          string // FK to ont_object_type
	Name              string
	DisplayName       string
	CanonicalMetric   string
	CanonicalFilters  string // raw JSON — must be a valid JSON array; default "[]"
	AutoGroupBy       []string
	PivotOn           string
	PivotValues       []string
	PivotColumnLabels []string
	PivotTotalLabel   string
	PivotPercentAxis  string // "row" | "column" — falls back to "row"
	PivotPercentScope string // "filtered" | "global" — falls back to "filtered"
	PivotPercentSuffix string
	PivotWithPercent  *bool
	PivotAppendGrandTotal *bool
	ResponseTemplate  string
	Description       string
	Priority          int
	Mark              *bool // nil → default true (matches schema column default)
	ReplaceGroupBy    bool
	DefaultOrderByLabel string
	DefaultOrderByDir   string
	DefaultLimit        *int
	Parameters          string // raw JSON — must be a valid JSON array; default "[]"
}

// WriteIntentWithTriggers performs the canonical "create a new metric intent"
// operation: an INSERT into lakehouse_metric_intent followed by N INSERTs
// into lakehouse_keyword (metric_intent_id pointing at the new row), all
// inside the supplied dbExec.
//
// Rules:
//   - triggers must contain ≥1 non-blank entry; otherwise ErrNoTriggers.
//   - Trigger entries are TrimSpace'd; duplicates after trim are deduped.
//   - The intent is created with mark=true (or whatever spec.Mark dictates).
//   - Keywords are inserted with is_machine_code=false; conflict on the
//     (property_id, keyword) unique constraint is treated as a successful
//     no-op (a previous binding wins).
//
// On any error after the intent INSERT succeeds the caller's transaction
// will reflect the partial state — that is the caller's responsibility to
// roll back. Helpers do not own the tx.
func WriteIntentWithTriggers(ctx context.Context, db dbExec, spec IntentSpec, triggers []string) (intentID string, err error) {
	cleaned, err := normaliseTriggers(triggers)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(spec.Name) == "" {
		return "", errors.New("intent name is required")
	}
	if strings.TrimSpace(spec.CanonicalMetric) == "" {
		return "", errors.New("canonical_metric is required")
	}
	if strings.TrimSpace(spec.ObjectID) == "" {
		return "", errors.New("objectId is required")
	}

	// Default pivot enum values — matches handler_intent.go's prior behaviour
	// so the helper is a drop-in replacement.
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

	err = db.QueryRowContext(ctx, `
		INSERT INTO lakehouse_metric_intent
			(project_id, object_id, name, display_name,
			 canonical_metric, canonical_filters, auto_group_by,
			 pivot_on, pivot_values, pivot_column_labels,
			 pivot_total_label, pivot_percent_axis, pivot_percent_scope,
			 pivot_percent_suffix,
			 pivot_with_percent, pivot_append_grand_total,
			 response_template, description, priority, mark,
			 replace_group_by, default_order_by_label, default_order_by_dir,
			 default_limit, parameters)
		VALUES ($1, $2, $3, $4,
		        $5, $6::jsonb, $7,
		        $8, $9, $10,
		        COALESCE(NULLIF($11,''),'Total'), $12, $13,
		        $14,
		        COALESCE($15, false), COALESCE($16, false),
		        $17, $18, $19, COALESCE($20, true),
		        $21, NULLIF($22,''), NULLIF($23,''),
		        $24, $25::jsonb)
		RETURNING id`,
		spec.ProjectID, spec.ObjectID, spec.Name, spec.DisplayName,
		spec.CanonicalMetric, canonicalFilters, pq.Array(autoGB),
		nilIfEmpty(spec.PivotOn), pq.Array(spec.PivotValues), pq.Array(spec.PivotColumnLabels),
		spec.PivotTotalLabel, pivotPercentAxis, pivotPercentScope,
		pivotPercentSuffix,
		spec.PivotWithPercent, spec.PivotAppendGrandTotal,
		spec.ResponseTemplate, spec.Description, spec.Priority, spec.Mark,
		spec.ReplaceGroupBy, spec.DefaultOrderByLabel, spec.DefaultOrderByDir,
		spec.DefaultLimit, parameters,
	).Scan(&intentID)
	if err != nil {
		return "", fmt.Errorf("insert lakehouse_metric_intent: %w", err)
	}

	if err := insertTriggerKeywords(ctx, db, spec.ProjectID, spec.ObjectID, intentID, cleaned); err != nil {
		return intentID, err
	}
	return intentID, nil
}

// UpdateIntentTriggers atomically replaces the triggerKeywords bound to the
// supplied intent. Used by analyst / builder edit flows where the intent
// row already exists and the user wants to edit the trigger list. Empty
// list rejected with ErrNoTriggers.
//
// objectTypeID is required for the lakehouse_keyword.object_type_id NOT NULL
// column; we don't infer it from the intent because callers usually have
// it on hand and inference (extra SELECT) would invalidate the "atomic
// inside caller's tx" property when the caller is composing a Tx.
func UpdateIntentTriggers(ctx context.Context, db dbExec, projectID, intentID, objectTypeID string, triggers []string) error {
	cleaned, err := normaliseTriggers(triggers)
	if err != nil {
		return err
	}
	if strings.TrimSpace(intentID) == "" {
		return errors.New("intentID is required")
	}
	if strings.TrimSpace(projectID) == "" {
		return errors.New("projectID is required")
	}
	if strings.TrimSpace(objectTypeID) == "" {
		return errors.New("objectTypeID is required")
	}

	// Wipe the prior bindings so the new set is exactly `cleaned`. We delete
	// only rows whose anchor is THIS intent — keyword rows anchored to a
	// property or object are untouched even if the keyword text matches.
	if _, err := db.ExecContext(ctx, `
		DELETE FROM lakehouse_keyword
		WHERE project_id = $1 AND metric_intent_id = $2`,
		projectID, intentID,
	); err != nil {
		return fmt.Errorf("delete prior intent triggers: %w", err)
	}

	return insertTriggerKeywords(ctx, db, projectID, objectTypeID, intentID, cleaned)
}

// CountIntentTriggers returns how many trigger keywords are bound to the
// given intent. Used by the orphan-check path in validate_intent_runs and
// by the list(intents) tool to surface "this intent is invisible to
// recall" warnings to the analyst agent.
func CountIntentTriggers(ctx context.Context, db dbExec, intentID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM lakehouse_keyword
		WHERE metric_intent_id = $1`,
		intentID,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// FindOrphanIntents returns the IDs (and names) of intents in the project
// that have zero trigger keywords. Used by the analyst-agent audit tool.
func FindOrphanIntents(ctx context.Context, db dbExec, projectID string) ([]OrphanIntent, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT mi.id, mi.name, mi.canonical_metric, COALESCE(mi.auto_group_by, '{}')
		FROM lakehouse_metric_intent mi
		LEFT JOIN lakehouse_keyword lk
		  ON lk.metric_intent_id = mi.id
		WHERE mi.project_id = $1
		GROUP BY mi.id, mi.name, mi.canonical_metric, mi.auto_group_by
		HAVING count(lk.id) = 0
		ORDER BY mi.name`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanIntent
	for rows.Next() {
		var o OrphanIntent
		var gb pq.StringArray
		if err := rows.Scan(&o.IntentID, &o.Name, &o.CanonicalMetric, &gb); err != nil {
			return nil, err
		}
		o.AutoGroupBy = []string(gb)
		out = append(out, o)
	}
	return out, rows.Err()
}

// OrphanIntent is the shape returned by FindOrphanIntents — a thin record
// the analyst agent can present to the user before proposing triggers.
type OrphanIntent struct {
	IntentID        string
	Name            string
	CanonicalMetric string
	AutoGroupBy     []string
}

// ── helpers ──────────────────────────────────────────────────────────────

func normaliseTriggers(triggers []string) ([]string, error) {
	seen := make(map[string]bool, len(triggers))
	out := make([]string, 0, len(triggers))
	for _, t := range triggers {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, ErrNoTriggers
	}
	return out, nil
}

func insertTriggerKeywords(ctx context.Context, db dbExec, projectID, objectTypeID, intentID string, cleaned []string) error {
	for _, kw := range cleaned {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO lakehouse_keyword
				(project_id, object_type_id, metric_intent_id, keyword, is_machine_code)
			VALUES ($1, $2, $3, $4, false)
			ON CONFLICT DO NOTHING`,
			projectID, objectTypeID, intentID, kw,
		); err != nil {
			return fmt.Errorf("insert trigger keyword %q: %w", kw, err)
		}
	}
	return nil
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
