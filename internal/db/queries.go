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

// ListMessages returns messages for a given agent, most recent first, with an optional limit.
func ListMessages(db *sql.DB, agentName string, limit int) ([]Message, error) {
	query := `SELECT id, from_source, to_agent, content, delivered, created_at, delivered_at
	          FROM messages WHERE to_agent = ? ORDER BY created_at DESC`
	var args []any
	args = append(args, agentName)
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing messages for %q: %w", agentName, err)
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

// InsertSchedule adds a scheduled message.
func InsertSchedule(db *sql.DB, agentName string, runAt time.Time, note string) error {
	_, err := db.Exec(
		`INSERT INTO schedule (agent_name, run_at, note) VALUES (?, ?, ?)`,
		agentName, runAt.UTC().Format("2006-01-02 15:04:05"), note,
	)
	if err != nil {
		return fmt.Errorf("inserting schedule: %w", err)
	}
	return nil
}

// DueSchedules returns all unexecuted schedules that are past their run_at time.
func DueSchedules(db *sql.DB) ([]Schedule, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	rows, err := db.Query(
		`SELECT id, agent_name, run_at, note, executed, created_at
		 FROM schedule WHERE executed = 0 AND run_at <= ?`, now,
	)
	if err != nil {
		return nil, fmt.Errorf("querying due schedules: %w", err)
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		if err := rows.Scan(&s.ID, &s.AgentName, &s.RunAt, &s.Note, &s.Executed, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning schedule row: %w", err)
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
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
