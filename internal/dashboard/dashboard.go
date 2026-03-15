package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/orch/internal/agent"
	"github.com/jeffdhooton/orch/internal/messenger"
	"github.com/jeffdhooton/orch/internal/scheduler"
	"github.com/jeffdhooton/orch/internal/tmux"
)

// Run starts the dashboard TUI. It blocks until the user quits.
func Run(database *sql.DB, log *slog.Logger) error {
	tc := tmux.New()
	msg := messenger.New(database, tc)
	sched := scheduler.New(database, msg, log)
	mgr := agent.New(database, tc, log)

	// Start the scheduler in the background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx, 30*time.Second, 10*time.Second)

	m := newModel(database, mgr, tc)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// --- messages ---

type tickMsg time.Time

type agentData struct {
	agents  []agent.AgentStatus
	preview string
}

type agentDataMsg agentData
type attachDoneMsg struct{}
type killDoneMsg struct{ err error }

// --- styles ---

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("252"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("236"))

	runningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("82"))

	stoppedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	deadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	previewTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("110")).
				MarginTop(1)

	previewStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
)

// --- key bindings ---

type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Attach  key.Binding
	Kill    key.Binding
	Refresh key.Binding
	Quit    key.Binding
}

var keys = keyMap{
	Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Attach:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "attach")),
	Kill:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "kill agent")),
	Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}

// --- model ---

type model struct {
	db       *sql.DB
	mgr      *agent.Manager
	tmux     tmux.Runner
	agents   []agent.AgentStatus
	preview  string
	cursor   int
	width    int
	height   int
	lastTick time.Time
}

func newModel(database *sql.DB, mgr *agent.Manager, tc tmux.Runner) model {
	return model{
		db:   database,
		mgr:  mgr,
		tmux: tc,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetchData(), tickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			return m, m.fetchData()
		case key.Matches(msg, keys.Down):
			if m.cursor < len(m.agents)-1 {
				m.cursor++
			}
			return m, m.fetchData()
		case key.Matches(msg, keys.Attach):
			if m.cursor < len(m.agents) {
				selected := m.agents[m.cursor]
				if selected.Live {
					return m, m.attachToAgent(selected)
				}
			}
		case key.Matches(msg, keys.Kill):
			if m.cursor < len(m.agents) {
				selected := m.agents[m.cursor]
				return m, m.killAgent(selected)
			}
		case key.Matches(msg, keys.Refresh):
			return m, m.fetchData()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		m.lastTick = time.Time(msg)
		return m, tea.Batch(m.fetchData(), tickCmd())

	case agentDataMsg:
		m.agents = msg.agents
		m.preview = msg.preview
		if m.cursor >= len(m.agents) && len(m.agents) > 0 {
			m.cursor = len(m.agents) - 1
		}
		return m, nil

	case attachDoneMsg:
		return m, m.fetchData()

	case killDoneMsg:
		return m, m.fetchData()
	}

	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var b strings.Builder

	// Title bar.
	title := titleStyle.Render(" orch dashboard ")
	updatedAt := dimStyle.Render(fmt.Sprintf("  updated %s", m.lastTick.Format("15:04:05")))
	b.WriteString(title + updatedAt + "\n\n")

	if len(m.agents) == 0 {
		b.WriteString(dimStyle.Render("  No agents registered. Use `orch up` to start one.\n"))
	} else {
		// Table header.
		header := fmt.Sprintf("  %-15s %-12s %-10s %-35s %s", "NAME", "ROLE", "STATUS", "DIR", "LAST ACTIVITY")
		b.WriteString(headerStyle.Render(header) + "\n")
		b.WriteString(dimStyle.Render("  " + strings.Repeat("─", min(m.width-4, 96))) + "\n")

		// Agent rows.
		for i, a := range m.agents {
			name := a.Agent.Name
			role := a.Agent.Role
			status := a.EffectiveStatus
			dir := truncatePath(a.Agent.Dir, 35)
			lastAct := formatRelativeTime(a.Agent.LastActivity)

			styledStatus := statusText(status)
			line := fmt.Sprintf("  %-15s %-12s %-10s %-35s %s", name, role, styledStatus, dir, lastAct)

			if i == m.cursor {
				// Highlight the selected row — render the full line in selected style.
				line = fmt.Sprintf("▸ %-15s %-12s %-10s %-35s %s", name, role, status, dir, lastAct)
				b.WriteString(selectedStyle.Render(line))
			} else {
				b.WriteString(line)
			}
			b.WriteString("\n")
		}

		// Preview pane — show last few lines of the selected agent's tmux pane.
		if m.preview != "" && m.cursor < len(m.agents) {
			selected := m.agents[m.cursor]
			previewTitle := previewTitleStyle.Render(fmt.Sprintf("  ── %s (%s) ──", selected.Agent.Name, selected.Agent.Role))
			b.WriteString("\n" + previewTitle + "\n")

			// Calculate available lines for preview.
			usedLines := 4 + len(m.agents) + 4 // title + header + separator + agents + preview header + help
			availableLines := m.height - usedLines
			if availableLines < 3 {
				availableLines = 3
			}

			lines := strings.Split(strings.TrimRight(m.preview, "\n"), "\n")
			if len(lines) > availableLines {
				lines = lines[len(lines)-availableLines:]
			}
			for _, line := range lines {
				// Truncate long lines to terminal width.
				if len(line) > m.width-4 {
					line = line[:m.width-4]
				}
				b.WriteString(previewStyle.Render("  "+line) + "\n")
			}
		}
	}

	// Help bar at the bottom.
	help := helpStyle.Render("  ↑/k up • ↓/j down • enter attach • x kill • r refresh • q quit")
	// Pad to push help to the bottom.
	rendered := b.String()
	renderedLines := strings.Count(rendered, "\n")
	for i := renderedLines; i < m.height-2; i++ {
		b.WriteString("\n")
	}
	b.WriteString(help)

	return b.String()
}

// --- commands ---

func tickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) attachToAgent(a agent.AgentStatus) tea.Cmd {
	// Select the agent's window first, then attach to the orch session.
	// When the user detaches (Ctrl-B d), they return to the dashboard.
	_ = m.tmux.SelectWindow(a.Agent.TmuxSession, a.Agent.TmuxWindow)
	c := exec.Command("tmux", "attach-session", "-t", a.Agent.TmuxSession)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return attachDoneMsg{}
	})
}

func (m model) killAgent(a agent.AgentStatus) tea.Cmd {
	return func() tea.Msg {
		err := m.mgr.Down(a.Agent.Name)
		return killDoneMsg{err: err}
	}
}

func (m model) fetchData() tea.Cmd {
	return func() tea.Msg {
		agents, err := m.mgr.List()
		if err != nil {
			return agentDataMsg{agents: nil}
		}

		var preview string
		if len(agents) > 0 {
			cursor := m.cursor
			if cursor >= len(agents) {
				cursor = len(agents) - 1
			}
			selected := agents[cursor]
			if selected.Live {
				out, err := m.tmux.CapturePane(selected.Agent.TmuxSession, selected.Agent.TmuxWindow, 20)
				if err == nil {
					preview = out
				}
			}
		}

		return agentDataMsg{agents: agents, preview: preview}
	}
}

// --- helpers ---

func statusText(status string) string {
	switch status {
	case "running":
		return runningStyle.Render("running")
	case "stopped":
		return stoppedStyle.Render("stopped")
	case "dead":
		return deadStyle.Render("dead")
	default:
		return status
	}
}

func truncatePath(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return "…" + s[len(s)-maxLen+1:]
}

func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 02 15:04")
	}
}
