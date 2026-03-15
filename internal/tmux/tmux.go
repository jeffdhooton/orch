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

// SendKeys sends text to a tmux window, followed by Enter.
// For multiline text, it uses tmux load-buffer/paste-buffer to avoid
// garbled input, then sends Enter separately after a short delay.
func (c *Client) SendKeys(session, window, text string) error {
	target := session + ":" + window

	if strings.Contains(text, "\n") {
		return c.sendMultiline(target, text)
	}

	cmd := exec.Command("tmux", "send-keys", "-t", target, text, "Enter")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sending keys to %q: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// sendMultiline handles multiline text by writing to a temp file,
// loading it into a tmux buffer, pasting it, then pressing Enter.
func (c *Client) sendMultiline(target, text string) error {
	// Write text to a temp file for tmux load-buffer.
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

	// Load into tmux buffer.
	cmd := exec.Command("tmux", "load-buffer", tmpFile.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("loading tmux buffer: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Paste the buffer into the target pane.
	cmd = exec.Command("tmux", "paste-buffer", "-t", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pasting tmux buffer: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Brief pause to let Claude Code process the paste.
	time.Sleep(500 * time.Millisecond)

	// Send Enter to submit.
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
