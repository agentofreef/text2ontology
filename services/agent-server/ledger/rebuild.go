package ledger

import (
	"database/sql"
	"fmt"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// TokenizerFn is the caller-supplied question → tokens splitter.
// The ontology package owns the actual tokenizer (few-shot driven
// LLM call); ledger stays pure by accepting it as a function pointer.
type TokenizerFn func(question string) []string

// RecallFn is the caller-supplied recall entry point. Kept as a
// function pointer so rebuild doesn't have to reach into the recall
// package's specific invocation shape (versionID, cached context, …).
type RecallFn func(tokens []string, question string) recall.RecallResult

// RebuildFromSteps reconstructs a ledger by replaying the last N
// user questions from ont_agent_step through tokenize + recall +
// merge. Bounded (replayLimit) so a long-running thread's first
// post-ledger turn doesn't stall on a minute-long warm-up.
//
// Replay order is OLDEST-first among the selected window, so
// FirstSeenInTurn / LoadedInTurn remain monotonic. turnBase is the
// turn number assigned to the oldest replayed step (typically 1).
//
// This reads only role='user' content — assistant tool-call results
// are NOT replayed (recall re-derives them from current ontology
// state, which is the whole point of the ledger).
//
// Returns the number of steps actually replayed.
func RebuildFromSteps(db *sql.DB, threadID string, replayLimit int,
	tokenize TokenizerFn, doRecall RecallFn,
	l *Ledger,
) (int, error) {
	if l == nil {
		return 0, fmt.Errorf("ledger.RebuildFromSteps: nil ledger")
	}
	if replayLimit <= 0 {
		replayLimit = 20
	}
	l.EnsureMaps()

	// Pull the most recent N user-role steps; reverse to chronological
	// order so merge sees them oldest-first. step_index is monotonic
	// per thread (incremented on each INSERT).
	rows, err := db.Query(`
		SELECT step_index, COALESCE(content,'')
		  FROM ont_agent_step
		 WHERE thread_id = $1 AND role = 'user' AND COALESCE(content,'') <> ''
		 ORDER BY step_index DESC
		 LIMIT $2`, threadID, replayLimit)
	if err != nil {
		return 0, fmt.Errorf("ledger.RebuildFromSteps query: %w", err)
	}
	defer rows.Close()

	type step struct {
		idx     int
		content string
	}
	var collected []step
	for rows.Next() {
		var s step
		if err := rows.Scan(&s.idx, &s.content); err != nil {
			return 0, fmt.Errorf("ledger.RebuildFromSteps scan: %w", err)
		}
		collected = append(collected, s)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("ledger.RebuildFromSteps rows: %w", err)
	}

	// Reverse to chronological order.
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}

	if len(collected) == 0 {
		return 0, nil
	}

	// Turn numbering: each replayed user step counts as one turn,
	// starting from 1. This is independent of the step_index (which
	// also counts assistant steps). Downstream renderers only care
	// about relative ordering.
	for i, s := range collected {
		if tokenize == nil || doRecall == nil {
			break
		}
		tokens := tokenize(s.content)
		if len(tokens) == 0 {
			continue
		}
		r := doRecall(tokens, s.content)
		turn := i + 1
		l.MergeRecallResult(r, turn)
	}

	// Record where we stopped so subsequent turns don't trigger rebuild.
	// Plus set TurnCount to len(collected) so new turns number naturally.
	last := collected[len(collected)-1]
	l.RebuiltFromStep = last.idx
	if l.TurnCount < len(collected) {
		l.TurnCount = len(collected)
	}
	return len(collected), nil
}
