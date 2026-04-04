package messenger

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/inbox"
	"github.com/jeffdhooton/orch/internal/tmux"
)

// Messenger handles sending messages to agents.
type Messenger struct {
	DB   *sql.DB
	Tmux tmux.Runner
	Log  *slog.Logger
}

// New creates a new Messenger.
func New(database *sql.DB, tc tmux.Runner, log *slog.Logger) *Messenger {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Messenger{DB: database, Tmux: tc, Log: log}
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

	// Write to gstack global inbox so non-orch CC sessions can see the message.
	m.writeToInbox(from, agent, content)

	return nil
}

// writeToInbox writes a copy of the message to ~/.gstack/inbox/messages/.
// Failures are logged but don't fail the send — inbox is best-effort.
func (m *Messenger) writeToInbox(from string, agent *db.Agent, content string) {
	// Determine inbox message type based on the sender.
	msgType := "info"
	switch from {
	case "scheduler", "inactivity-nudge", "inbox":
		return // Internal housekeeping or already from inbox, don't broadcast.
	case "git-watcher":
		msgType = "info"
	case "user":
		msgType = "info"
	default:
		// Agent-to-agent message — treat as a handoff/unblock.
		msgType = "handoff"
	}

	senderFrom := inbox.AgentFrom(agent.Dir, from, "")
	target := filepath.Base(agent.Dir) // project-scoped by default

	if err := inbox.SendMessage(msgType, senderFrom, target, content); err != nil {
		m.Log.Warn("failed to write inbox message", "error", err)
	}
}
