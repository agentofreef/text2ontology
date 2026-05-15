// Package wizard manages wizard state persistence in data_source.wizard_state.
// The wizard is the multi-step UI flow for connecting a new data source:
// users assign table/column roles and decide which FK-backed links to create.
// State is stored as JSONB so the wizard survives page refresh and collector
// restarts (SweepStaleOnBoot marks stale wizard_in_progress rows as
// failed_resumable so the UI can offer "Resume / Abandon").
package wizard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/contracts"
	pbitpkg "github.com/lakehouse2ontology/services/collector-server/ingest/pbit"
	pgpkg "github.com/lakehouse2ontology/services/collector-server/ingest/postgres"
	sqlitepkg "github.com/lakehouse2ontology/services/collector-server/ingest/sqlite"
)

// ErrAlreadyCompleted is returned by Confirm when the data source has already
// been imported (status='completed'). The route handler maps this to HTTP 409
// so the user sees a friendly message instead of the staging-schema-not-found
// error from MergeStagingIntoFinal.
var ErrAlreadyCompleted = errors.New("数据源已导入，无需重复操作")

// Get reads wizard_state JSONB from data_source and returns the parsed struct.
// Returns an empty WizardStateUpdate (not an error) when wizard_state is NULL.
func Get(ctx context.Context, db *sql.DB, dsID string) (*contracts.WizardStateUpdate, error) {
	var raw []byte
	err := db.QueryRowContext(ctx,
		`SELECT wizard_state FROM data_source WHERE id = $1`, dsID,
	).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var st contracts.WizardStateUpdate
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, err
		}
	}
	return &st, nil
}

// Update writes wizard_state JSONB and marks status='wizard_in_progress'.
func Update(ctx context.Context, db *sql.DB, dsID string, st contracts.WizardStateUpdate) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE data_source
		SET wizard_state = $1,
		    updated_at   = now(),
		    status       = 'wizard_in_progress'
		WHERE id = $2
	`, raw, dsID)
	return err
}

// Confirm is the terminal wizard step: it runs the full promotion tx.
//
//  1. Read data_source row (type, project_id, staging_schema, config_json, wizard_state)
//  2. Build catalog (re-discover for postgres; use stored columns for file/pbi)
//  3. MapToPbitSchema(catalog, wizardState) → PbitSchema
//  4. Open terminal tx on collector DB
//  5. pbit.RenameStagingToFinal(tx, stagingSchema, finalSchema)
//  6. pbit.PopulateOntology(tx, projectID, finalSchema, pbitSchema, nil)
//  7. UPDATE data_source SET status='completed', last_sync_at=now()
//  8. tx.Commit()
func Confirm(ctx context.Context, db *sql.DB, dsID string) error {
	// 1. Read data_source row.
	var (
		srcType       string
		projectID     string
		status        string
		stagingSchema sql.NullString
		configRaw     []byte
		wizardRaw     []byte
	)
	err := db.QueryRowContext(ctx, `
		SELECT type, project_id, status, staging_schema,
		       config_json, COALESCE(wizard_state, '{}'::jsonb)
		FROM data_source WHERE id = $1
	`, dsID).Scan(&srcType, &projectID, &status, &stagingSchema, &configRaw, &wizardRaw)
	if err != nil {
		return fmt.Errorf("wizard.Confirm: load data_source: %w", err)
	}

	// Idempotency guard: a successful Confirm sets status='completed' and
	// drops the staging schema. Calling Confirm again on the same data source
	// would fail deep inside MergeStagingIntoFinal with a "schema not found"
	// stack trace; surface a friendly sentinel instead so the wizard route
	// handler can return 409 with a clear message.
	if status == "completed" {
		return ErrAlreadyCompleted
	}

	var ws contracts.WizardStateUpdate
	if err := json.Unmarshal(wizardRaw, &ws); err != nil {
		return fmt.Errorf("wizard.Confirm: parse wizard_state: %w", err)
	}

	// 2. Build catalog depending on source type.
	var catalog []contracts.TableInfo
	switch srcType {
	case "postgres":
		// Re-open source DB from stored config_json credentials.
		var cfgMap map[string]any
		if err := json.Unmarshal(configRaw, &cfgMap); err != nil {
			return fmt.Errorf("wizard.Confirm: parse pg config: %w", err)
		}
		port := 5432
		if p, ok := cfgMap["port"].(float64); ok {
			port = int(p)
		}
		strVal := func(key string) string {
			if v, ok := cfgMap[key].(string); ok {
				return v
			}
			return ""
		}
		cfg := pgpkg.Config{
			Host:     strVal("host"),
			Port:     port,
			Database: strVal("database"),
			User:     strVal("user"),
			Password: strVal("password"),
			SSLMode:  strVal("ssl_mode"),
		}
		srcDB, err := pgpkg.Open(ctx, cfg)
		if err != nil {
			return fmt.Errorf("wizard.Confirm: open source pg: %w", err)
		}
		defer srcDB.Close()
		catalog, err = pgpkg.Discover(ctx, srcDB)
		if err != nil {
			return fmt.Errorf("wizard.Confirm: discover catalog: %w", err)
		}

	case "file", "pbi":
		// For file/pbi sources the catalog was returned at upload/parse time.
		// Re-derive a minimal catalog from wizard_state table_roles + column_roles
		// (the wizard stores enough info to reconstruct it without re-reading disk).
		catalog = catalogFromWizardState(ws)

	case "sqlite":
		// Re-open the uploaded .sqlite file from disk_path stored in
		// config_json at upload time, then re-Discover so we recover
		// foreign-key info (which catalogFromWizardState wouldn't have).
		var cfgMap map[string]any
		if err := json.Unmarshal(configRaw, &cfgMap); err != nil {
			return fmt.Errorf("wizard.Confirm: parse sqlite config: %w", err)
		}
		dbPath, _ := cfgMap["disk_path"].(string)
		if dbPath == "" {
			return fmt.Errorf("wizard.Confirm: sqlite disk_path missing in config_json")
		}
		sdb, err := sqlitepkg.Open(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("wizard.Confirm: open sqlite file: %w", err)
		}
		defer sdb.Close()
		catalog, err = sqlitepkg.Discover(ctx, sdb)
		if err != nil {
			return fmt.Errorf("wizard.Confirm: discover sqlite catalog: %w", err)
		}

	default:
		return fmt.Errorf("wizard.Confirm: unknown source type: %s", srcType)
	}

	// Staging schema must be set (sync must have run before confirm).
	if !stagingSchema.Valid || stagingSchema.String == "" {
		return fmt.Errorf("wizard.Confirm: staging_schema not set — run sync first")
	}

	// 3. MapToPbitSchema.
	pbitSchema := pgpkg.MapToPbitSchema(catalog, ws)

	// Derive final schema name from project_id — must use the SAME canonical
	// rule as PBIT/PBIX path (pbit.SanitizeSchemaName) otherwise wizard-driven
	// imports (CSV/XLSX/Postgres/URL) and PBI imports diverge into two
	// schemas (`proj_<dash_to_underscore>` vs `proj_<hex_no_dash>`),
	// breaking "single lakehouse per project" guarantee and cross-source
	// agent queries.
	finalSchema := pbitpkg.SanitizeSchemaName(projectID)

	// 4. Terminal tx on collector's own DB.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("wizard.Confirm: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 5. Merge staging tables into final schema (incremental — works even if
	//    final schema already exists from a prior import). This is the ONLY
	//    transformation Confirm does to the lakehouse: it lands raw data in
	//    proj_<hex>.<table>. Ontology design (ont_object_type / ont_property
	//    / lakehouse_keyword / metric_intent) is intentionally left to the
	//    user (or an AI agent) to author later via the dedicated pages —
	//    "import to lakehouse" and "design ontology" are separate concerns.
	if err := pbitpkg.MergeStagingIntoFinal(tx, stagingSchema.String, finalSchema); err != nil {
		return fmt.Errorf("wizard.Confirm: merge staging: %w", err)
	}
	// Reference pbitSchema so the unused-var compiler check stays quiet — kept
	// around for callers that may want it for future ontology-suggestion flows.
	_ = pbitSchema

	// 6. Write the final schema name back to project.lakehouse_schema so
	//    downstream consumers (Lakehouse Agent, Builder Agent, smartquery, etc.)
	//    can locate the lakehouse for this project. finalSchema is deterministic
	//    from project_id (SanitizeSchemaName), so re-imports are no-ops on this
	//    column — but the first import MUST set it or the project's lakehouse
	//    is invisible to every reader downstream.
	if _, err := tx.ExecContext(ctx, `
		UPDATE project
		SET lakehouse_schema = $1,
		    updated_at = now()
		WHERE id = $2
	`, finalSchema, projectID); err != nil {
		return fmt.Errorf("wizard.Confirm: update project.lakehouse_schema: %w", err)
	}

	// 7. Mark data_source completed.
	if _, err := tx.ExecContext(ctx, `
		UPDATE data_source
		SET status       = 'completed',
		    last_sync_at = now(),
		    updated_at   = now()
		WHERE id = $1
	`, dsID); err != nil {
		return fmt.Errorf("wizard.Confirm: update status: %w", err)
	}

	// 8. Commit.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("wizard.Confirm: commit: %w", err)
	}

	// 9. Trigger vector embedding in the background (best-effort).
	go func(pid string) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := pbitpkg.RecomputeVectorsForProject(bgCtx, db, pid); err != nil {
			log.Printf("[wizard] auto vector compute: %v", err)
		}
	}(projectID)

	return nil
}

// cleanOntologyByTableNames deletes ont_object_type rows (and their CASCADE
// dependents: ont_property, lakehouse_keyword, ont_link_type) for the specific
// table names being re-imported. This makes Confirm idempotent on projects that
// already have ontology rows from a prior import of the same tables.
// Idempotent — safe to call when no matching rows exist.
func cleanOntologyByTableNames(ctx context.Context, tx *sql.Tx, projectID string, tableNames []string) error {
	if len(tableNames) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM ont_object_type
		WHERE project_id = $1 AND name = ANY($2)
	`, projectID, pq.Array(tableNames))
	if err != nil {
		return fmt.Errorf("cleanOntologyByTableNames: %w", err)
	}
	return nil
}

// catalogFromWizardState reconstructs a minimal []TableInfo from wizard_state
// for file/pbi sources where the original catalog is not re-readable from disk.
// It produces one TableInfo per non-skipped table with columns from column_roles.
func catalogFromWizardState(ws contracts.WizardStateUpdate) []contracts.TableInfo {
	var tables []contracts.TableInfo
	for tbl, role := range ws.TableRoles {
		if role == "skip" || role == "" {
			continue
		}
		var cols []contracts.ColumnInfo
		if colMap, ok := ws.ColumnRoles[tbl]; ok {
			for col, cr := range colMap {
				if cr == "skip" {
					continue
				}
				cols = append(cols, contracts.ColumnInfo{Name: col, DataType: "text"})
			}
		}
		tables = append(tables, contracts.TableInfo{Name: tbl, Columns: cols})
	}
	return tables
}
