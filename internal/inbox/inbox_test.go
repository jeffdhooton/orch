package inbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupInboxDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "messages")
	os.MkdirAll(dir, 0755)
	inboxDirOverride = dir
	t.Cleanup(func() { inboxDirOverride = "" })
	return dir
}

func TestSendMessageCreatesFile(t *testing.T) {
	dir := setupInboxDir(t)

	err := SendMessage("info", "myproject/main (orch:builder)", "all", "Tests passing")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading inbox dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	content, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	s := string(content)

	if !strings.Contains(s, "type: info") {
		t.Error("expected type: info in frontmatter")
	}
	if !strings.Contains(s, "from: myproject/main (orch:builder)") {
		t.Error("expected from field in frontmatter")
	}
	if !strings.Contains(s, "target: all") {
		t.Error("expected target: all in frontmatter")
	}
	if !strings.Contains(s, "Tests passing") {
		t.Error("expected body content")
	}
}

func TestSendMessageTypes(t *testing.T) {
	setupInboxDir(t)

	for _, msgType := range []string{"info", "unblock", "handoff", "question"} {
		err := SendMessage(msgType, "test/main (orch:a)", "all", "body")
		if err != nil {
			t.Errorf("SendMessage(%s): %v", msgType, err)
		}
	}
}

func TestReadMessagesEmpty(t *testing.T) {
	setupInboxDir(t)

	msgs, err := ReadMessages("all", 0)
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestReadMessagesNonexistentDir(t *testing.T) {
	inboxDirOverride = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { inboxDirOverride = "" })

	msgs, err := ReadMessages("", 0)
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestReadMessagesRoundTrip(t *testing.T) {
	setupInboxDir(t)

	SendMessage("handoff", "proj/main (orch:builder)", "proj", "API done")

	msgs, err := ReadMessages("proj", 0)
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	msg := msgs[0]
	if msg.Type != "handoff" {
		t.Errorf("expected type handoff, got %q", msg.Type)
	}
	if msg.From != "proj/main (orch:builder)" {
		t.Errorf("expected from 'proj/main (orch:builder)', got %q", msg.From)
	}
	if msg.Target != "proj" {
		t.Errorf("expected target 'proj', got %q", msg.Target)
	}
	if msg.Body != "API done" {
		t.Errorf("expected body 'API done', got %q", msg.Body)
	}
	if msg.FileTime == 0 {
		t.Error("expected non-zero FileTime")
	}
}

func TestReadMessagesTargetFiltering(t *testing.T) {
	dir := setupInboxDir(t)

	// Write two messages with different targets manually to control timestamps.
	now := time.Now().Unix()
	writeMsg(t, dir, now, "sender-a", "info", "a/main (session-1)", "proj-a", "msg for a")
	writeMsg(t, dir, now+1, "sender-b", "info", "b/main (session-2)", "proj-b", "msg for b")
	writeMsg(t, dir, now+2, "sender-c", "info", "c/main (session-3)", "all", "broadcast")

	// Filter for proj-a: should get proj-a message + broadcast.
	msgs, _ := ReadMessages("proj-a", 0)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages for proj-a, got %d", len(msgs))
	}
	if msgs[0].Body != "msg for a" {
		t.Errorf("expected 'msg for a', got %q", msgs[0].Body)
	}
	if msgs[1].Body != "broadcast" {
		t.Errorf("expected 'broadcast', got %q", msgs[1].Body)
	}

	// Filter for proj-b: should get proj-b message + broadcast.
	msgs, _ = ReadMessages("proj-b", 0)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages for proj-b, got %d", len(msgs))
	}

	// Empty target: get everything.
	msgs, _ = ReadMessages("", 0)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages for empty target, got %d", len(msgs))
	}
}

func TestReadMessagesSinceFiltering(t *testing.T) {
	dir := setupInboxDir(t)

	now := time.Now().Unix()
	writeMsg(t, dir, now-10, "old", "info", "p/main (s-1)", "all", "old message")
	writeMsg(t, dir, now+10, "new", "info", "p/main (s-2)", "all", "new message")

	// Since now: should only get the newer message.
	msgs, _ := ReadMessages("", now)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message since %d, got %d", now, len(msgs))
	}
	if msgs[0].Body != "new message" {
		t.Errorf("expected 'new message', got %q", msgs[0].Body)
	}
}

func TestReadMessagesSortedByTimestamp(t *testing.T) {
	dir := setupInboxDir(t)

	// Write in reverse order.
	writeMsg(t, dir, 300, "c", "info", "from-c", "all", "third")
	writeMsg(t, dir, 100, "a", "info", "from-a", "all", "first")
	writeMsg(t, dir, 200, "b", "info", "from-b", "all", "second")

	msgs, _ := ReadMessages("", 0)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Body != "first" || msgs[1].Body != "second" || msgs[2].Body != "third" {
		t.Errorf("messages not sorted: %q, %q, %q", msgs[0].Body, msgs[1].Body, msgs[2].Body)
	}
}

func TestIsOrchMessage(t *testing.T) {
	if !IsOrchMessage("proj/main (orch:builder)") {
		t.Error("expected orch message to be detected")
	}
	if !IsOrchMessage("proj/main (orch:pm)") {
		t.Error("expected orch message to be detected")
	}
	if IsOrchMessage("proj/main (session-12345)") {
		t.Error("expected non-orch message")
	}
	if IsOrchMessage("user@laptop") {
		t.Error("expected non-orch message")
	}
}

func TestAgentFrom(t *testing.T) {
	// Uses gitBranch which will fall back to "main" for non-git dirs.
	from := AgentFrom("/tmp/myproject", "builder", "engineer")
	if !strings.HasPrefix(from, "myproject/") {
		t.Errorf("expected 'myproject/' prefix, got %q", from)
	}
	if !strings.Contains(from, "(orch:builder)") {
		t.Errorf("expected '(orch:builder)' in from, got %q", from)
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"simple", "simple"},
		{"with spaces", "with-spaces"},
		{"with/slashes", "with-slashes"},
		{"proj/main (orch:builder)", "proj-main--orch-builder-"},
	}
	for _, tt := range tests {
		got := sanitize(tt.input)
		if got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeTruncates(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitize(long)
	if len(got) != 60 {
		t.Errorf("expected length 60, got %d", len(got))
	}
}

func TestExtractTimestamp(t *testing.T) {
	tests := []struct {
		filename string
		want     int64
	}{
		{"1712345678-sender.md", 1712345678},
		{"0-x.md", 0},
		{"notanumber-x.md", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := extractTimestamp(tt.filename)
		if got != tt.want {
			t.Errorf("extractTimestamp(%q) = %d, want %d", tt.filename, got, tt.want)
		}
	}
}

func TestParseMessage(t *testing.T) {
	content := "---\ntype: handoff\nfrom: proj/main (orch:builder)\ntarget: proj\ndate: 2026-04-04 10:30\n---\n\nAPI layer complete.\n"
	msg := parseMessage(content)

	if msg.Type != "handoff" {
		t.Errorf("type = %q, want handoff", msg.Type)
	}
	if msg.From != "proj/main (orch:builder)" {
		t.Errorf("from = %q", msg.From)
	}
	if msg.Target != "proj" {
		t.Errorf("target = %q, want proj", msg.Target)
	}
	if msg.Date != "2026-04-04 10:30" {
		t.Errorf("date = %q", msg.Date)
	}
	if msg.Body != "API layer complete." {
		t.Errorf("body = %q", msg.Body)
	}
}

func TestParseMessageNoFrontmatter(t *testing.T) {
	msg := parseMessage("just plain text")
	if msg.Body != "just plain text" {
		t.Errorf("body = %q, want 'just plain text'", msg.Body)
	}
	if msg.Type != "" {
		t.Errorf("type = %q, want empty", msg.Type)
	}
}

// writeMsg is a test helper that writes an inbox message file directly.
func writeMsg(t *testing.T, dir string, ts int64, sender, msgType, from, target, body string) {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("%d-%s.md", ts, sanitize(sender)))
	content := fmt.Sprintf("---\ntype: %s\nfrom: %s\ntarget: %s\ndate: 2026-04-04 10:00\n---\n\n%s\n",
		msgType, from, target, body)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing test message: %v", err)
	}
}
