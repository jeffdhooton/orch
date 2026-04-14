package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Runner is the interface for tmux operations. Use this in other packages
// to allow testing without a real tmux.
type Runner interface {
	HasSession(name string) bool
	NewSession(name string) error
	NewWindow(session, name, dir string) error
	KillWindow(session, name string) error
	SendKeys(session, window, text string) error
	SendMessage(session, window, text string) error
	CapturePane(session, window string, lines int) (string, error)
	SelectWindow(session, name string) error
	KillSession(session string) error
	ListWindows(session string) ([]string, error)
}

// Client wraps tmux command execution. Implements Runner.
type Client struct{}

// New returns a new tmux Client.
func New() *Client {
	return &Client{}
}

// HasSession checks if a tmux session exists.
func (c *Client) HasSession(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}

// NewSession creates a new tmux session (detached).
func (c *Client) NewSession(name string) error {
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating tmux session %q: %s: %w", name, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// NewWindow creates a new window in an existing session, starting in the given directory.
func (c *Client) NewWindow(session, name, dir string) error {
	cmd := exec.Command("tmux", "new-window", "-t", session, "-n", name, "-c", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating tmux window %q in session %q: %s: %w", name, session, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// KillWindow kills a named window in a session.
func (c *Client) KillWindow(session, name string) error {
	target := session + ":" + name
	cmd := exec.Command("tmux", "kill-window", "-t", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("killing tmux window %q: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SendKeys sends text to a tmux window followed by Enter, as raw keystrokes.
// Use for shell commands (e.g. launching claude). For delivering messages to
// a running Claude Code TUI, use SendMessage instead — CC v2.1.105+ treats
// rapid keystroke bursts as pastes and absorbs the trailing Enter.
func (c *Client) SendKeys(session, window, text string) error {
	target := session + ":" + window
	cmd := exec.Command("tmux", "send-keys", "-t", target, text, "Enter")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sending keys to %q: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SendMessage delivers a message to a running Claude Code TUI via paste-buffer,
// then submits with a separate Enter after a delay long enough for CC v2.1.105's
// smart-paste UI to settle. Using a single `tmux send-keys text Enter` call
// fails on CC v2.1.105 because the characters land fast enough to trigger
// paste detection, which then swallows the trailing Enter.
func (c *Client) SendMessage(session, window, text string) error {
	target := session + ":" + window

	// Strip trailing newlines — we send Enter ourselves.
	text = strings.TrimRight(text, "\n")

	tmpFile, err := os.CreateTemp("", "orch-msg-*.txt")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(text); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	tmpFile.Close()

	cmd := exec.Command("tmux", "load-buffer", tmpFile.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("loading tmux buffer: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// -p: bracketed paste (so CC recognizes input as a paste, not typing).
	// -d: delete the buffer after pasting.
	cmd = exec.Command("tmux", "paste-buffer", "-p", "-d", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pasting tmux buffer: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Wait long enough for CC v2.1.105's smart-paste UI to finalize the paste
	// and become ready to accept submit. 500ms isn't enough; 1500ms is robust
	// across observed variance.
	time.Sleep(1500 * time.Millisecond)

	cmd = exec.Command("tmux", "send-keys", "-t", target, "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sending Enter to %q: %s: %w", target, strings.TrimSpace(string(out)), err)
	}

	return nil
}

// CapturePane captures the last N lines from a tmux pane.
func (c *Client) CapturePane(session, window string, lines int) (string, error) {
	target := session + ":" + window
	startLine := fmt.Sprintf("-%d", lines)
	cmd := exec.Command("tmux", "capture-pane", "-p", "-t", target, "-S", startLine)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("capturing pane %q: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// SelectWindow selects (switches to) a window within a session.
func (c *Client) SelectWindow(session, name string) error {
	target := session + ":" + name
	cmd := exec.Command("tmux", "select-window", "-t", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("selecting tmux window %q: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// AttachSession attaches to an existing session. This replaces the current process.
func (c *Client) AttachSession(session string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("finding tmux: %w", err)
	}
	return syscall.Exec(tmuxPath, []string{"tmux", "attach-session", "-t", session}, os.Environ())
}

// KillSession kills an entire tmux session.
func (c *Client) KillSession(session string) error {
	cmd := exec.Command("tmux", "kill-session", "-t", session)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("killing tmux session %q: %s: %w", session, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ListWindows returns the names of all windows in a session.
func (c *Client) ListWindows(session string) ([]string, error) {
	cmd := exec.Command("tmux", "list-windows", "-t", session, "-F", "#{window_name}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("listing tmux windows in %q: %s: %w", session, strings.TrimSpace(string(out)), err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}
