package messenger

import (
	"database/sql"
	"fmt"

	"github.com/jhoot/orch/internal/db"
	"github.com/jhoot/orch/internal/tmux"
)

// Messenger handles sending messages to agents.
type Messenger struct {
	DB   *sql.DB
	Tmux tmux.Runner
}

// New creates a new Messenger.
func New(database *sql.DB, tc tmux.Runner) *Messenger {
	return &Messenger{DB: database, Tmux: tc}
}

// Send records a message in the DB and delivers it to the agent's tmux window.
func (m *Messenger) Send(from, agentName, content string) error {
	// Look up the agent to get tmux coordinates.
	agent, err := db.GetAgent(m.DB, agentName)
	if err != nil {
		return fmt.Errorf("looking up agent: %w", err)
	}

	if agent.Status != "running" {
		return fmt.Errorf("agent %q is not running (status: %s)", agentName, agent.Status)
	}

	// Record the message.
	msgID, err := db.InsertMessage(m.DB, from, agentName, content)
	if err != nil {
		return fmt.Errorf("recording message: %w", err)
	}

	// Deliver via tmux.
	if err := m.Tmux.SendKeys(agent.TmuxSession, agent.TmuxWindow, content); err != nil {
		return fmt.Errorf("delivering message via tmux: %w", err)
	}

	// Mark delivered.
	if err := db.MarkMessageDelivered(m.DB, msgID); err != nil {
		return fmt.Errorf("marking message delivered: %w", err)
	}

	// Update agent activity.
	if err := db.TouchAgent(m.DB, agentName); err != nil {
		return fmt.Errorf("updating agent activity: %w", err)
	}

	return nil
}
