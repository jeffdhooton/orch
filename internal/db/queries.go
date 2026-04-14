package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Agent represents a row in the agents table.
type Agent struct {
	ID           int64
	Name         string
	Role         string
	Dir          string
	SpecPath     sql.NullString
	TmuxSession  string
	TmuxWindow   string
	Status       string
	CreatedAt    time.Time
	LastActivity time.Time
}

// Message represents a row in the messages table.
type Message struct {
	ID          int64
	FromSource  string
	ToAgent     string
	Content     string
	Delivered   bool
	CreatedAt   time.Time
	DeliveredAt sql.NullTime
}

// Schedule represents a row in the schedule table.
type Schedule struct {
	ID        int64
	AgentName string
	Dir       string
	RunAt     time.Time
	Note      string
	Executed  bool
	CreatedAt time.Time
}

// InsertAgent registers a new agent in the database.
func InsertAgent(db *sql.DB, name, role, dir, specPath, tmuxSession, tmuxWindow string) error {
	var sp sql.NullString
	if specPath != "" {
		sp = sql.NullString{String: specPath, Valid: true}
	}
	_, err := db.Exec(
		`INSERT INTO agents (name, role, dir, spec_path, tmux_session, tmux_window, status)
		 VALUES (?, ?, ?, ?, ?, ?, 'running')`,
		name, role, dir, sp, tmuxSession, tmuxWindow,
	)
	if err != nil {
		return fmt.Errorf("inserting agent %q: %w", name, err)
	}
	return nil
}

// UpdateAgentStatus sets the status of an agent and updates last_activity.
func UpdateAgentStatus(db *sql.DB, name, status string) error {
	res, err := db.Exec(
		`UPDATE agents SET status = ?, last_activity = CURRENT_TIMESTAMP WHERE name = ?`,
		status, name,
	)
	if err != nil {
		return fmt.Errorf("updating agent %q status: %w", name, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent %q not found", name)
	}
	return nil
}

// TouchAgent updates last_activity for an agent.
func TouchAgent(db *sql.DB, name string) error {
	_, err := db.Exec(
		`UPDATE agents SET last_activity = CURRENT_TIMESTAMP WHERE name = ?`, name,
	)
	if err != nil {
		return fmt.Errorf("touching agent %q: %w", name, err)
	}
	return nil
}

// GetAgent returns a single agent by name.
func GetAgent(db *sql.DB, name string) (*Agent, error) {
	row := db.QueryRow(
		`SELECT id, name, role, dir, spec_path, tmux_session, tmux_window, status, created_at, last_activity
		 FROM agents WHERE name = ?`, name,
	)
	a := &Agent{}
	err := row.Scan(&a.ID, &a.Name, &a.Role, &a.Dir, &a.SpecPath,
		&a.TmuxSession, &a.TmuxWindow, &a.Status, &a.CreatedAt, &a.LastActivity)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("querying agent %q: %w", name, err)
	}
	return a, nil
}

// ListAgents returns all agents, optionally filtered by status.
func ListAgents(db *sql.DB, statusFilter string) ([]Agent, error) {
	var rows *sql.Rows
	var err error
	if statusFilter != "" {
		rows, err = db.Query(
			`SELECT id, name, role, dir, spec_path, tmux_session, tmux_window, status, created_at, last_activity
			 FROM agents WHERE status = ? ORDER BY created_at`, statusFilter,
		)
	} else {
		rows, err = db.Query(
			`SELECT id, name, role, dir, spec_path, tmux_session, tmux_window, status, created_at, last_activity
			 FROM agents ORDER BY created_at`,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.Name, &a.Role, &a.Dir, &a.SpecPath,
			&a.TmuxSession, &a.TmuxWindow, &a.Status, &a.CreatedAt, &a.LastActivity); err != nil {
			return nil, fmt.Errorf("scanning agent row: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// DeleteAgent removes an agent from the database.
func DeleteAgent(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM agents WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("deleting agent %q: %w", name, err)
	}
	return nil
}

// InsertMessage records a message in the database.
func InsertMessage(db *sql.DB, from, to, content string) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO messages (from_source, to_agent, content) VALUES (?, ?, ?)`,
		from, to, content,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting message: %w", err)
	}
	return res.LastInsertId()
}

// MarkMessageDelivered marks a message as delivered.
func MarkMessageDelivered(db *sql.DB, id int64) error {
	_, err := db.Exec(
		`UPDATE messages SET delivered = 1, delivered_at = CURRENT_TIMESTAMP WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("marking message %d delivered: %w", id, err)
	}
	return nil
}

// ListMessages returns messages to or from a given agent, most recent first, with an optional limit.
func ListMessages(db *sql.DB, agentName string, limit int) ([]Message, error) {
	query := `SELECT id, from_source, to_agent, content, delivered, created_at, delivered_at
	          FROM messages WHERE to_agent = ? OR from_source = ? ORDER BY created_at DESC`
	var args []any
	args = append(args, agentName, agentName)
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	return scanMessages(db, query, args)
}

// ListAllMessages returns all messages across all agents, most recent first, with an optional limit.
func ListAllMessages(db *sql.DB, limit int) ([]Message, error) {
	query := `SELECT id, from_source, to_agent, content, delivered, created_at, delivered_at
	          FROM messages ORDER BY created_at DESC`
	var args []any
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	return scanMessages(db, query, args)
}

func scanMessages(db *sql.DB, query string, args []any) ([]Message, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.FromSource, &m.ToAgent, &m.Content,
			&m.Delivered, &m.CreatedAt, &m.DeliveredAt); err != nil {
			return nil, fmt.Errorf("scanning message row: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// InsertSchedule adds a scheduled message. dir scopes the schedule to a
// specific working directory so it cannot leak to an unrelated project that
// happens to reuse the agent name.
func InsertSchedule(db *sql.DB, agentName, dir string, runAt time.Time, note string) error {
	_, err := db.Exec(
		`INSERT INTO schedule (agent_name, dir, run_at, note) VALUES (?, ?, ?, ?)`,
		agentName, dir, runAt.UTC().Format("2006-01-02 15:04:05"), note,
	)
	if err != nil {
		return fmt.Errorf("inserting schedule: %w", err)
	}
	return nil
}

// DueSchedules returns unexecuted schedules past their run_at time whose
// (agent_name, dir) tuple matches a currently-registered agent. Schedules
// with no matching agent (including legacy rows with empty dir) are filtered
// out here, and callers should purge them via PurgeOrphanSchedules so they
// don't accumulate.
func DueSchedules(db *sql.DB) ([]Schedule, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	rows, err := db.Query(
		`SELECT s.id, s.agent_name, s.dir, s.run_at, s.note, s.executed, s.created_at
		 FROM schedule s
		 INNER JOIN agents a ON a.name = s.agent_name AND a.dir = s.dir
		 WHERE s.executed = 0 AND s.run_at <= ? AND s.dir != ''`, now,
	)
	if err != nil {
		return nil, fmt.Errorf("querying due schedules: %w", err)
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		if err := rows.Scan(&s.ID, &s.AgentName, &s.Dir, &s.RunAt, &s.Note, &s.Executed, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning schedule row: %w", err)
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

// PurgeOrphanSchedules marks as executed any pending schedules that cannot be
// delivered: legacy rows with empty dir, or rows whose (agent_name, dir) does
// not match any registered agent. Prevents cross-project bleed when an agent
// name gets reused in a different working directory.
func PurgeOrphanSchedules(db *sql.DB) (int64, error) {
	res, err := db.Exec(
		`UPDATE schedule
		 SET executed = 1
		 WHERE executed = 0
		   AND (dir = ''
		        OR NOT EXISTS (
		            SELECT 1 FROM agents a
		            WHERE a.name = schedule.agent_name AND a.dir = schedule.dir
		        ))`,
	)
	if err != nil {
		return 0, fmt.Errorf("purging orphan schedules: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeletePendingSchedulesForAgent removes any unexecuted schedules for the
// given agent name whose dir does not match the supplied dir. Used when an
// agent is (re-)registered so stale schedules from a prior project don't
// survive the new binding.
func DeletePendingSchedulesForAgent(db *sql.DB, agentName, dir string) (int64, error) {
	res, err := db.Exec(
		`DELETE FROM schedule
		 WHERE executed = 0 AND agent_name = ? AND dir != ?`,
		agentName, dir,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting stale schedules: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkScheduleExecuted marks a schedule as executed.
func MarkScheduleExecuted(db *sql.DB, id int64) error {
	_, err := db.Exec(
		`UPDATE schedule SET executed = 1 WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("marking schedule %d executed: %w", id, err)
	}
	return nil
}
