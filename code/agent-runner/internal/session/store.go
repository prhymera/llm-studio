// Package session provides persistent storage for agent session metadata.
package session

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Session represents an agent session with its full lifecycle metadata.
type Session struct {
	ID             string     `json:"id"`
	UserID         string     `json:"user_id"`
	AgentType      string     `json:"agent_type"`
	Model          string     `json:"model"`
	Endpoint       string     `json:"endpoint"`
	Status         string     `json:"status"`    // creating, running, stopped, failed
	ContainerID    string     `json:"container_id"`
	WorkspacePath  string     `json:"workspace_path"`
	WorkspaceLabel string     `json:"workspace_label"`
	ConfigJSON     string     `json:"config_json"`
	CreatedAt      time.Time  `json:"created_at"`
	LastActiveAt   time.Time  `json:"last_active_at"`
	StoppedAt      *time.Time `json:"stopped_at,omitempty"`
}

// Store provides thread-safe persistence for sessions backed by SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens or creates the session database at the given path.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			id              TEXT PRIMARY KEY,
			user_id         TEXT NOT NULL,
			agent_type      TEXT NOT NULL,
			model           TEXT NOT NULL DEFAULT '',
			endpoint        TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'creating',
			container_id    TEXT NOT NULL DEFAULT '',
			workspace_path  TEXT NOT NULL DEFAULT '',
			workspace_label TEXT NOT NULL DEFAULT '',
			config_json     TEXT NOT NULL DEFAULT '{}',
			created_at      TEXT NOT NULL,
			last_active_at  TEXT NOT NULL,
			stopped_at      TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
		CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
	`)
	return err
}

// Save inserts a new session record.
func (s *Store) Save(sess *Session) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions (id, user_id, agent_type, model, endpoint, status,
			container_id, workspace_path, workspace_label, config_json,
			created_at, last_active_at, stopped_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.AgentType, sess.Model, sess.Endpoint,
		sess.Status, sess.ContainerID, sess.WorkspacePath, sess.WorkspaceLabel,
		sess.ConfigJSON, sess.CreatedAt.Format(time.RFC3339),
		sess.LastActiveAt.Format(time.RFC3339), formatTimePtr(sess.StoppedAt),
	)
	return err
}

// Get retrieves a session by ID.
func (s *Store) Get(id string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, user_id, agent_type, model, endpoint, status,
			container_id, workspace_path, workspace_label, config_json,
			created_at, last_active_at, stopped_at
		FROM sessions WHERE id = ?`, id)

	sess := &Session{}
	var created, lastActive, stopped sql.NullString
	err := row.Scan(&sess.ID, &sess.UserID, &sess.AgentType, &sess.Model,
		&sess.Endpoint, &sess.Status, &sess.ContainerID, &sess.WorkspacePath,
		&sess.WorkspaceLabel, &sess.ConfigJSON, &created, &lastActive, &stopped)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}

	sess.CreatedAt, _ = time.Parse(time.RFC3339, created.String)
	sess.LastActiveAt, _ = time.Parse(time.RFC3339, lastActive.String)
	if stopped.Valid {
		t, _ := time.Parse(time.RFC3339, stopped.String)
		sess.StoppedAt = &t
	}

	return sess, nil
}

// List returns all sessions, ordered by most recent first.
func (s *Store) List() ([]*Session, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, agent_type, model, endpoint, status,
			container_id, workspace_path, workspace_label, config_json,
			created_at, last_active_at, stopped_at
		FROM sessions ORDER BY last_active_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]*Session, 0)
	for rows.Next() {
		sess := &Session{}
		var created, lastActive, stopped sql.NullString
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.AgentType, &sess.Model,
			&sess.Endpoint, &sess.Status, &sess.ContainerID, &sess.WorkspacePath,
			&sess.WorkspaceLabel, &sess.ConfigJSON, &created, &lastActive, &stopped); err != nil {
			return nil, err
		}
		sess.CreatedAt, _ = time.Parse(time.RFC3339, created.String)
		sess.LastActiveAt, _ = time.Parse(time.RFC3339, lastActive.String)
		if stopped.Valid {
			t, _ := time.Parse(time.RFC3339, stopped.String)
			sess.StoppedAt = &t
		}
		sessions = append(sessions, sess)
	}

	return sessions, nil
}

// UpdateStatus updates the status of a session.
func (s *Store) UpdateStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE sessions SET status = ?, last_active_at = ? WHERE id = ?`,
		status, time.Now().Format(time.RFC3339), id)
	return err
}

// Delete removes a session record.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}
