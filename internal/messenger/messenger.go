package messenger

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/tmux"
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

	// Reactivate done agents — a new message means there's more work to do.
	if agent.Status == "done" {
		if err := db.UpdateAgentStatus(m.DB, agentName, "running"); err != nil {
			return fmt.Errorf("reactivating agent %q: %w", agentName, err)
		}
		// Clean up any stale done marker so the agent can create it fresh.
		os.Remove(filepath.Join(agent.Dir, ".orch-done"))
		// Remind the agent to signal done again when it finishes the new work.
		content = content + "\n\n(You were previously done. Your .orch-done file has been cleared. When you finish this new work, create .orch-done again with a summary.)"
	} else if agent.Status != "running" {
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
