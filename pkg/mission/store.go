package mission

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// DB is the minimal database surface the mission store needs.
// *sql.DB and *sql.Tx both satisfy it.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store persists missions to the ont_mission table. The full mission
// JSON is the source of truth (the `state` column); the scalar columns
// are denormalised copies kept only for indexing.
type Store struct{ db DB }

// NewStore wraps a database handle.
func NewStore(db DB) *Store { return &Store{db: db} }

// Save upserts the mission by its id.
func (s *Store) Save(ctx context.Context, m *Mission) error {
	state, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal mission %s: %w", m.MissionID, err)
	}
	var parent any
	if m.ParentMissionID != "" {
		parent = m.ParentMissionID
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO ont_mission
			(id, thread_id, parent_mission_id, project_id, question, state, status, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (id) DO UPDATE SET
			state = EXCLUDED.state,
			status = EXCLUDED.status,
			question = EXCLUDED.question,
			updated_at = now()`,
		m.MissionID, m.ThreadID, parent, m.ProjectID, m.Question, state, string(m.Status))
	if err != nil {
		return fmt.Errorf("save mission %s: %w", m.MissionID, err)
	}
	return nil
}

// Load reads a mission by id and reconstructs it from the `state` JSON.
func (s *Store) Load(ctx context.Context, missionID string) (*Mission, error) {
	var state []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT state FROM ont_mission WHERE id = $1`, missionID).Scan(&state)
	if err != nil {
		return nil, fmt.Errorf("load mission %s: %w", missionID, err)
	}
	m, err := DecodeMission(state)
	if err != nil {
		return nil, fmt.Errorf("decode mission %s: %w", missionID, err)
	}
	return m, nil
}

// EncodeMission serialises a mission to the JSON stored in the `state`
// column. Exposed (and tested) separately so the round trip can be
// verified without a database.
func EncodeMission(m *Mission) ([]byte, error) {
	return json.Marshal(m)
}

// DecodeMission is the inverse of EncodeMission.
func DecodeMission(state []byte) (*Mission, error) {
	var m Mission
	if err := json.Unmarshal(state, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
