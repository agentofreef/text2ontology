package wizard

import (
	"context"
	"database/sql"
	"log"
)

// SweepStaleOnBoot scans data_source rows with status='wizard_in_progress'
// that have not been updated in the last 5 minutes and marks them as
// 'failed_resumable'. Called once at collector startup so the frontend can
// offer a "Resume / Abandon" prompt for interrupted wizard sessions.
//
// This implements Pre-Mortem scenario D: collector restarts mid-wizard.
func SweepStaleOnBoot(db *sql.DB) {
	ctx := context.Background()
	res, err := db.ExecContext(ctx, `
		UPDATE data_source
		SET status     = 'failed_resumable',
		    updated_at = now()
		WHERE status     = 'wizard_in_progress'
		  AND updated_at < now() - interval '5 minutes'
	`)
	if err != nil {
		log.Printf("wizard SweepStaleOnBoot: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("wizard SweepStaleOnBoot: marked %d source(s) as failed_resumable", n)
	}
}
