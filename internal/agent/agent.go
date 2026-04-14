package agent

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/tmux"
)

const (
	SessionName = "orch"
)

// Manager handles agent lifecycle operations.
type Manager struct {
	DB   *sql.DB
	Tmux tmux.Runner
	Log  *slog.Logger
}

// New creates a new agent Manager.
func New(database *sql.DB, tc tmux.Runner, log *slog.Logger) *Manager {
	return &Manager{DB: database, Tmux: tc, Log: log}
}

// UpOpts are the options for creating a new agent.
type UpOpts struct {
	Name            string
	Role            string
	Dir             string
	SpecPath        string
	SkipPermissions bool     // Pass --dangerously-skip-permissions to claude
	SkillPaths      []string // Plugin directories to load via --plugin-dir
}

// Up creates and starts a new agent.
func (m *Manager) Up(opts UpOpts) error {
	// Resolve the working directory to an absolute, symlink-resolved path.
	// This is important on macOS where /tmp → /private/tmp, etc.
	absDir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return fmt.Errorf("resolving directory: %w", err)
	}
	absDir, err = filepath.EvalSymlinks(absDir)
	if err != nil {
		return fmt.Errorf("resolving symlinks for %q: %w", absDir, err)
	}

	// Verify the directory exists.
	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("checking directory %q: %w", absDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", absDir)
	}

	// Ensure the working directory is a git repo (needed for claude trust + commits).
	gitDir := filepath.Join(absDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		m.Log.Info("initializing git repo", "dir", absDir)
		gitInit := exec.Command("git", "init", absDir)
		if out, gitErr := gitInit.CombinedOutput(); gitErr != nil {
			m.Log.Warn("failed to git init", "error", gitErr, "output", string(out))
		}
	}

	// Ensure the orch tmux session exists.
	if !m.Tmux.HasSession(SessionName) {
		m.Log.Info("creating orch tmux session")
		if err := m.Tmux.NewSession(SessionName); err != nil {
			return fmt.Errorf("creating tmux session: %w", err)
		}
	}

	// Create the tmux window.
	m.Log.Info("creating tmux window", "name", opts.Name, "dir", absDir)
	if err := m.Tmux.NewWindow(SessionName, opts.Name, absDir); err != nil {
		return fmt.Errorf("creating tmux window: %w", err)
	}

	// Register in DB.
	if err := db.InsertAgent(m.DB, opts.Name, opts.Role, absDir, opts.SpecPath, SessionName, opts.Name); err != nil {
		// Clean up the tmux window on failure.
		_ = m.Tmux.KillWindow(SessionName, opts.Name)
		return fmt.Errorf("registering agent: %w", err)
	}

	// Drop any stale pending schedules that were queued against this agent
	// name in a different working directory. Without this, a zombie schedule
	// from a prior run in project A would deliver to a fresh agent in project B.
	if n, err := db.DeletePendingSchedulesForAgent(m.DB, opts.Name, absDir); err != nil {
		m.Log.Warn("failed to purge stale schedules", "agent", opts.Name, "error", err)
	} else if n > 0 {
		m.Log.Info("purged stale schedules for rebound agent", "agent", opts.Name, "count", n)
	}

	// Pre-trust the directory so claude doesn't prompt.
	if err := trustDirectory(absDir); err != nil {
		m.Log.Warn("failed to pre-trust directory", "dir", absDir, "error", err)
	}

	// Build the system prompt with agent identity and team awareness.
	systemPrompt, err := m.buildSystemPrompt(opts.Name, opts.Role)
	if err != nil {
		m.Log.Warn("failed to build system prompt", "error", err)
		systemPrompt = fmt.Sprintf("You are agent %q with role %q.", opts.Name, opts.Role)
	}

	// Build the claude command.
	claudeCmd := m.buildClaudeCmd(opts, systemPrompt)
	m.Log.Info("starting claude", "agent", opts.Name)
	if err := m.Tmux.SendKeys(SessionName, opts.Name, claudeCmd); err != nil {
		return fmt.Errorf("starting claude: %w", err)
	}

	// Wait for claude to start up before sending the spec.
	if opts.SpecPath != "" {
		time.Sleep(3 * time.Second)
	}

	// If a spec file is given, tell claude to read it rather than pasting
	// the contents (tmux send-keys garbles large multiline text).
	if opts.SpecPath != "" {
		absSpec, err := filepath.Abs(opts.SpecPath)
		if err != nil {
			return fmt.Errorf("resolving spec path: %w", err)
		}
		absSpec, _ = filepath.EvalSymlinks(absSpec)
		m.Log.Info("sending spec to agent", "agent", opts.Name, "spec", absSpec)
		specMsg := fmt.Sprintf("Read and follow the instructions in %s", absSpec)
		if err := m.Tmux.SendMessage(SessionName, opts.Name, specMsg); err != nil {
			return fmt.Errorf("sending spec to agent: %w", err)
		}
	}

	return nil
}

// buildClaudeCmd constructs the full claude command string with all flags.
func (m *Manager) buildClaudeCmd(opts UpOpts, systemPrompt string) string {
	var parts []string
	parts = append(parts, "claude")

	if opts.SkipPermissions {
		parts = append(parts, "--dangerously-skip-permissions")
	}

	for _, sp := range opts.SkillPaths {
		parts = append(parts, "--plugin-dir", shellEscape(sp))
	}

	// Use --append-system-prompt to inject agent identity and team info
	// directly into the session. This avoids writing/clobbering CLAUDE.md files.
	escaped := shellEscape(systemPrompt)
	parts = append(parts, "--append-system-prompt", escaped)

	return strings.Join(parts, " ")
}

// buildSystemPrompt generates the system prompt text for an agent.
func (m *Manager) buildSystemPrompt(name, role string) (string, error) {
	agents, err := db.ListAgents(m.DB, "running")
	if err != nil {
		return "", fmt.Errorf("listing agents: %w", err)
	}

	type teammate struct {
		Name string
		Role string
	}
	var teammates []teammate
	for _, a := range agents {
		if a.Name != name {
			teammates = append(teammates, teammate{Name: a.Name, Role: a.Role})
		}
	}

	data := map[string]any{
		"Name":      name,
		"Role":      role,
		"Teammates": teammates,
	}

	t, err := template.New("prompt").Parse(systemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing system prompt template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing system prompt template: %w", err)
	}

	return buf.String(), nil
}

// Down tears down an agent.
func (m *Manager) Down(name string) error {
	agent, err := db.GetAgent(m.DB, name)
	if err != nil {
		return fmt.Errorf("looking up agent: %w", err)
	}

	// Kill the tmux window.
	m.Log.Info("killing tmux window", "agent", name)
	if err := m.Tmux.KillWindow(agent.TmuxSession, agent.TmuxWindow); err != nil {
		m.Log.Warn("failed to kill tmux window (may already be gone)", "error", err)
	}

	// Remove from DB so the name can be reused.
	if err := db.DeleteAgent(m.DB, name); err != nil {
		return fmt.Errorf("deleting agent from db: %w", err)
	}

	return nil
}

// List returns all agents with their live tmux status cross-referenced.
func (m *Manager) List() ([]AgentStatus, error) {
	agents, err := db.ListAgents(m.DB, "")
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	// Get live tmux windows for cross-reference.
	liveWindows := make(map[string]bool)
	if m.Tmux.HasSession(SessionName) {
		windows, err := m.Tmux.ListWindows(SessionName)
		if err == nil {
			for _, w := range windows {
				liveWindows[w] = true
			}
		}
	}

	var result []AgentStatus
	for _, a := range agents {
		as := AgentStatus{
			Agent: a,
			Live:  liveWindows[a.TmuxWindow],
		}
		if a.Status == "done" {
			as.EffectiveStatus = "done"
		} else if a.Status == "running" && !as.Live {
			as.EffectiveStatus = "dead"
		} else if a.Status == "running" && as.Live {
			as.EffectiveStatus = "running"
			// Check tmux pane for detailed status.
			if pane, err := m.Tmux.CapturePane(a.TmuxSession, a.TmuxWindow, 5); err == nil {
				as.Idle = isIdle(pane)
				as.NeedsInput = needsInput(pane)
			}
		} else {
			as.EffectiveStatus = a.Status
		}
		result = append(result, as)
	}

	return result, nil
}

// AgentStatus combines DB state with live tmux state.
type AgentStatus struct {
	Agent           db.Agent
	Live            bool
	Idle            bool
	NeedsInput      bool
	EffectiveStatus string
}

// isIdle checks if the tmux pane output suggests claude is sitting at its
// prompt waiting for input. Looks for the ❯ prompt character on recent lines.
func isIdle(paneOutput string) bool {
	lines := strings.Split(strings.TrimRight(paneOutput, "\n"), "\n")
	// Check the last few non-empty lines for the prompt character.
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		// Claude Code shows ❯ when waiting for input.
		if strings.Contains(line, "❯") {
			return true
		}
		// If we hit a line with actual content (not prompt-related), it's working.
		if len(line) > 0 && !strings.Contains(line, "bypass permissions") && !strings.Contains(line, "shift+tab") {
			return false
		}
	}
	return false
}

// needsInput checks if the tmux pane output suggests claude is waiting for
// user action, such as a permission prompt or a question.
func needsInput(paneOutput string) bool {
	lines := strings.Split(strings.TrimRight(paneOutput, "\n"), "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		// Skip the status bar — "bypass permissions on" is always present
		// when running with --dangerously-skip-permissions and is NOT a prompt.
		if strings.HasPrefix(line, "▸▸") || strings.HasPrefix(line, ">>") {
			continue
		}
		// Actual interactive permission prompts asking the user to decide.
		if strings.Contains(line, "Press up to edit queued messages") ||
			strings.Contains(line, "Allow once") ||
			strings.Contains(line, "Allow always") ||
			strings.Contains(line, "Do you want to") {
			return true
		}
	}
	return false
}

// trustDirectory adds a directory to ~/.claude.json as trusted so claude
// skips the "do you trust this folder?" prompt.
func trustDirectory(dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	claudeJSONPath := filepath.Join(home, ".claude.json")

	// Read existing file.
	data := make(map[string]any)
	raw, err := os.ReadFile(claudeJSONPath)
	if err == nil {
		if err := json.Unmarshal(raw, &data); err != nil {
			return fmt.Errorf("parsing %s: %w", claudeJSONPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", claudeJSONPath, err)
	}

	// Ensure projects map exists.
	projects, ok := data["projects"].(map[string]any)
	if !ok {
		projects = make(map[string]any)
		data["projects"] = projects
	}

	// Check if already trusted.
	if proj, ok := projects[dir].(map[string]any); ok {
		if trusted, _ := proj["hasTrustDialogAccepted"].(bool); trusted {
			return nil
		}
	}

	// Set trust for this directory.
	proj, ok := projects[dir].(map[string]any)
	if !ok {
		proj = make(map[string]any)
		projects[dir] = proj
	}
	proj["hasTrustDialogAccepted"] = true

	// Write back.
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", claudeJSONPath, err)
	}
	if err := os.WriteFile(claudeJSONPath, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", claudeJSONPath, err)
	}

	return nil
}

// shellEscape wraps a string in single quotes for safe shell usage,
// escaping any embedded single quotes.
func shellEscape(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", `'\''`)
	return "'" + escaped + "'"
}

const systemPromptTemplate = `You are an autonomous agent managed by orch. Your name is "{{.Name}}" and your role is "{{.Role}}".

## Role Boundaries

CRITICAL: You must ONLY perform work appropriate for your role. If a task falls outside your role, delegate it to the appropriate teammate via .orch-send-<agent-name>.
{{- if eq .Role "pm"}}

As a PM, you are STRICTLY forbidden from writing, editing, or modifying any code, config files, or technical artifacts. Your ONLY tools are:
- Checking build/lint/test status by READING output (not fixing it)
- Sending instructions to engineers or reviewers via .orch-send-<agent-name>
- Scheduling check-ins via .orch-schedule
- Coordinating and unblocking teammates

When you see a problem, do NOT fix it yourself. Send a message to the engineer describing what needs to be fixed and why.
{{- end}}
{{- if eq .Role "reviewer"}}

As a reviewer, you review code and send feedback. You do NOT write or modify code. Send all feedback to the engineer via .orch-send-<agent-name> with specific file paths, line numbers, and what needs to change.
{{- end}}
{{- if eq .Role "engineer"}}

As an engineer, you write and modify code. When you complete work that needs review, notify the PM or reviewer via .orch-send-<agent-name>.
{{- end}}
{{if .Teammates}}
## Team
Other agents currently running:
{{range .Teammates}}- "{{.Name}}" ({{.Role}})
{{end}}
## Inter-agent Communication

To send a message to another agent, create a file named .orch-send-<agent-name> in your working directory with the message content. The orchestrator will pick it up and deliver it. Always delegate work that falls outside your role to the appropriate teammate.
{{end}}
To schedule a follow-up task for yourself, create a file named .orch-schedule with the format:
<minutes> <note describing what to do>

The orchestrator will send you the note as a message after the specified number of minutes.

When you have fully completed your mission, create a file named .orch-done with a brief summary of what you accomplished. This signals to the orchestrator that you are finished.

Stay focused on your assigned role. Never do another role's job — always delegate.`
