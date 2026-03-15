package agent

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/tmux"
)

func testSetup(t *testing.T) (*sql.DB, *tmux.Mock, *Manager) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	tc := tmux.NewMock()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := New(database, tc, log)
	return database, tc, mgr
}

func TestUpCreatesSessionAndWindow(t *testing.T) {
	_, tc, mgr := testSetup(t)
	dir := t.TempDir()

	err := mgr.Up(UpOpts{Name: "builder", Role: "engineer", Dir: dir})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	if !tc.HasSession(SessionName) {
		t.Error("expected orch tmux session to exist")
	}
	windows := tc.Windows[SessionName]
	if len(windows) != 1 || windows[0] != "builder" {
		t.Errorf("expected window 'builder', got %v", windows)
	}
}

func TestUpSendsClaudeCommand(t *testing.T) {
	_, tc, mgr := testSetup(t)
	dir := t.TempDir()

	err := mgr.Up(UpOpts{Name: "a1", Role: "pm", Dir: dir, SkipPermissions: true})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Should have sent at least one SendKeys call (the claude command).
	found := false
	for _, sk := range tc.SentKeys {
		if sk.Window == "a1" && len(sk.Text) > 0 {
			found = true
			if sk.Text[:6] != "claude" {
				t.Errorf("expected claude command, got %q", sk.Text)
			}
		}
	}
	if !found {
		t.Error("expected SendKeys call to start claude")
	}
}

func TestUpWithSkipPermissions(t *testing.T) {
	_, tc, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "a1", Role: "pm", Dir: dir, SkipPermissions: true})

	found := false
	for _, sk := range tc.SentKeys {
		if sk.Window == "a1" {
			if contains(sk.Text, "--dangerously-skip-permissions") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected --dangerously-skip-permissions in claude command")
	}
}

func TestUpWithoutSkipPermissions(t *testing.T) {
	_, tc, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "a1", Role: "pm", Dir: dir, SkipPermissions: false})

	for _, sk := range tc.SentKeys {
		if sk.Window == "a1" && contains(sk.Text, "--dangerously-skip-permissions") {
			t.Error("did not expect --dangerously-skip-permissions")
		}
	}
}

func TestUpWithSpec(t *testing.T) {
	_, tc, mgr := testSetup(t)
	dir := t.TempDir()

	specFile := filepath.Join(dir, "spec.md")
	os.WriteFile(specFile, []byte("Build the thing"), 0o644)

	err := mgr.Up(UpOpts{Name: "a1", Role: "engineer", Dir: dir, SpecPath: specFile})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Should have sent the spec content.
	found := false
	for _, sk := range tc.SentKeys {
		if sk.Text == "Build the thing" {
			found = true
		}
	}
	if !found {
		t.Error("expected spec content to be sent via SendKeys")
	}
}

func TestUpDuplicateName(t *testing.T) {
	_, _, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "dup", Role: "engineer", Dir: dir})
	err := mgr.Up(UpOpts{Name: "dup", Role: "pm", Dir: dir})
	if err == nil {
		t.Fatal("expected error on duplicate agent name")
	}
}

func TestUpBadDir(t *testing.T) {
	_, _, mgr := testSetup(t)

	err := mgr.Up(UpOpts{Name: "a1", Role: "engineer", Dir: "/nonexistent/path/xyz"})
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestDown(t *testing.T) {
	database, tc, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "a1", Role: "engineer", Dir: dir})
	err := mgr.Down("a1")
	if err != nil {
		t.Fatalf("Down: %v", err)
	}

	// Should have killed the window.
	if len(tc.Killed) != 1 {
		t.Errorf("expected 1 kill, got %d", len(tc.Killed))
	}

	// Should be removed from DB.
	_, err = db.GetAgent(database, "a1")
	if err == nil {
		t.Error("expected agent to be deleted from DB")
	}
}

func TestDownNonexistent(t *testing.T) {
	_, _, mgr := testSetup(t)

	err := mgr.Down("ghost")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestListEmpty(t *testing.T) {
	_, _, mgr := testSetup(t)

	agents, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestListWithAgents(t *testing.T) {
	_, _, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "a1", Role: "engineer", Dir: dir})
	mgr.Up(UpOpts{Name: "a2", Role: "pm", Dir: dir})

	agents, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}
}

func TestListDetectsDeadAgent(t *testing.T) {
	database, tc, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "a1", Role: "engineer", Dir: dir})

	// Simulate the window dying by removing it from the mock.
	tc.Windows[SessionName] = nil

	agents, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].EffectiveStatus != "dead" {
		t.Errorf("expected status 'dead', got %q", agents[0].EffectiveStatus)
	}

	_ = database // used by mgr
}

func TestTeamAwarenessInSystemPrompt(t *testing.T) {
	_, _, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "builder", Role: "engineer", Dir: dir})

	prompt, err := mgr.buildSystemPrompt("reviewer", "reviewer")
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}

	if !contains(prompt, "builder") {
		t.Error("expected system prompt to mention teammate 'builder'")
	}
	if !contains(prompt, "engineer") {
		t.Error("expected system prompt to mention teammate role 'engineer'")
	}
}

func TestSystemPromptNoSelfReference(t *testing.T) {
	_, _, mgr := testSetup(t)
	dir := t.TempDir()

	mgr.Up(UpOpts{Name: "builder", Role: "engineer", Dir: dir})

	prompt, err := mgr.buildSystemPrompt("builder", "engineer")
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}

	// Should not list itself as a teammate.
	if contains(prompt, "## Team") {
		t.Error("agent should not list itself as a teammate")
	}
}

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
		{"a 'b' c", "'a '\\''b'\\'' c'"},
	}
	for _, tt := range tests {
		got := shellEscape(tt.input)
		if got != tt.want {
			t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
