package messenger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/inbox"
	"github.com/jeffdhooton/orch/internal/tmux"
)

func testSetup(t *testing.T) (*db.Agent, *tmux.Mock, *Messenger) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	tc := tmux.NewMock()
	tc.Sessions["orch"] = true
	tc.Windows["orch"] = []string{"builder"}

	// Insert a running agent.
	db.InsertAgent(database, "builder", "engineer", "/tmp", "", "orch", "builder")

	msg := New(database, tc, nil)
	agent, _ := db.GetAgent(database, "builder")
	return agent, tc, msg
}

func TestSendDeliversMessage(t *testing.T) {
	_, tc, msg := testSetup(t)

	err := msg.Send("user", "builder", "hello agent")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Check tmux received the message.
	if len(tc.SentKeys) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(tc.SentKeys))
	}
	if tc.SentKeys[0].Text != "hello agent" {
		t.Errorf("expected text 'hello agent', got %q", tc.SentKeys[0].Text)
	}
	if tc.SentKeys[0].Session != "orch" || tc.SentKeys[0].Window != "builder" {
		t.Errorf("wrong target: %+v", tc.SentKeys[0])
	}
}

func TestSendRecordsInDB(t *testing.T) {
	_, _, msg := testSetup(t)

	msg.Send("user", "builder", "test message")

	msgs, err := db.ListMessages(msg.DB, "builder", 0)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "test message" {
		t.Errorf("expected content 'test message', got %q", msgs[0].Content)
	}
	if !msgs[0].Delivered {
		t.Error("expected message to be marked delivered")
	}
	if msgs[0].FromSource != "user" {
		t.Errorf("expected from 'user', got %q", msgs[0].FromSource)
	}
}

func TestSendToNonexistentAgent(t *testing.T) {
	_, _, msg := testSetup(t)

	err := msg.Send("user", "ghost", "hello")
	if err == nil {
		t.Fatal("expected error sending to nonexistent agent")
	}
}

func TestSendToStoppedAgent(t *testing.T) {
	_, _, msg := testSetup(t)

	// Mark agent as stopped.
	db.UpdateAgentStatus(msg.DB, "builder", "stopped")

	err := msg.Send("user", "builder", "hello")
	if err == nil {
		t.Fatal("expected error sending to stopped agent")
	}
}

func TestSendMultipleMessages(t *testing.T) {
	_, tc, msg := testSetup(t)

	msg.Send("user", "builder", "msg1")
	msg.Send("scheduler", "builder", "msg2")
	msg.Send("reviewer", "builder", "msg3")

	if len(tc.SentKeys) != 3 {
		t.Errorf("expected 3 SendKeys calls, got %d", len(tc.SentKeys))
	}

	msgs, _ := db.ListMessages(msg.DB, "builder", 0)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages in DB, got %d", len(msgs))
	}
}

func setupInbox(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "messages")
	os.MkdirAll(dir, 0755)
	restore := inbox.SetDirForTest(dir)
	t.Cleanup(restore)
	return dir
}

func TestSendWritesToInbox(t *testing.T) {
	_, _, msg := testSetup(t)
	inboxDir := setupInbox(t)

	msg.Send("user", "builder", "hello from user")

	// Check that an inbox message was written.
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		t.Fatalf("reading inbox dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 inbox file, got %d", len(entries))
	}

	content, _ := os.ReadFile(filepath.Join(inboxDir, entries[0].Name()))
	s := string(content)
	if !strings.Contains(s, "type: info") {
		t.Error("expected type: info for user message")
	}
	if !strings.Contains(s, "hello from user") {
		t.Error("expected message body in inbox file")
	}
}

func TestSendAgentToAgentWritesHandoff(t *testing.T) {
	_, _, msg := testSetup(t)
	inboxDir := setupInbox(t)

	msg.Send("reviewer", "builder", "fix the tests")

	entries, _ := os.ReadDir(inboxDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 inbox file, got %d", len(entries))
	}

	content, _ := os.ReadFile(filepath.Join(inboxDir, entries[0].Name()))
	if !strings.Contains(string(content), "type: handoff") {
		t.Error("expected type: handoff for agent-to-agent message")
	}
}

func TestSendSchedulerSkipsInbox(t *testing.T) {
	_, _, msg := testSetup(t)
	inboxDir := setupInbox(t)

	msg.Send("scheduler", "builder", "scheduled check")

	entries, _ := os.ReadDir(inboxDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 inbox files for scheduler message, got %d", len(entries))
	}
}

func TestSendInactivityNudgeSkipsInbox(t *testing.T) {
	_, _, msg := testSetup(t)
	inboxDir := setupInbox(t)

	msg.Send("inactivity-nudge", "builder", "wake up")

	entries, _ := os.ReadDir(inboxDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 inbox files for nudge message, got %d", len(entries))
	}
}

func TestSendFromInboxSkipsInbox(t *testing.T) {
	_, _, msg := testSetup(t)
	inboxDir := setupInbox(t)

	msg.Send("inbox", "builder", "external message")

	entries, _ := os.ReadDir(inboxDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 inbox files for inbox-sourced message, got %d", len(entries))
	}
}
