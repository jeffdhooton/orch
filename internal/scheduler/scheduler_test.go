package scheduler

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/inbox"
	"github.com/jeffdhooton/orch/internal/messenger"
	"github.com/jeffdhooton/orch/internal/tmux"
)

func testSetup(t *testing.T) (*tmux.Mock, *Scheduler, string) {
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

	agentDir := t.TempDir()
	db.InsertAgent(database, "builder", "engineer", agentDir, "", "orch", "builder")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	msg := messenger.New(database, tc, nil)
	sched := New(database, msg, log)

	return tc, sched, agentDir
}

func TestProcessDueSchedules(t *testing.T) {
	tc, sched, _ := testSetup(t)

	// Insert a schedule in the past.
	past := time.Now().Add(-1 * time.Minute)
	db.InsertSchedule(sched.DB, "builder", past, "check status")

	sched.RunOnce()

	// Should have delivered the message.
	if len(tc.SentKeys) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(tc.SentKeys))
	}
	if tc.SentKeys[0].Text != "check status" {
		t.Errorf("expected 'check status', got %q", tc.SentKeys[0].Text)
	}

	// Should be marked executed.
	due, _ := db.DueSchedules(sched.DB)
	if len(due) != 0 {
		t.Errorf("expected 0 due schedules after execution, got %d", len(due))
	}
}

func TestFutureScheduleNotExecuted(t *testing.T) {
	tc, sched, _ := testSetup(t)

	future := time.Now().Add(1 * time.Hour)
	db.InsertSchedule(sched.DB, "builder", future, "later")

	sched.RunOnce()

	if len(tc.SentKeys) != 0 {
		t.Errorf("expected 0 SendKeys calls for future schedule, got %d", len(tc.SentKeys))
	}
}

func TestProcessScheduleFile(t *testing.T) {
	_, sched, agentDir := testSetup(t)

	// Write a .orch-schedule file.
	schedFile := filepath.Join(agentDir, ".orch-schedule")
	os.WriteFile(schedFile, []byte("5 review the PR"), 0o644)

	sched.RunOnce()

	// File should be deleted.
	if _, err := os.Stat(schedFile); !os.IsNotExist(err) {
		t.Error("expected .orch-schedule file to be deleted")
	}

	// Should have inserted a schedule (not yet due since it's 5 min out).
	due, _ := db.DueSchedules(sched.DB)
	if len(due) != 0 {
		t.Error("schedule should not be due yet (5 min in future)")
	}
}

func TestProcessSendFile(t *testing.T) {
	tc, sched, agentDir := testSetup(t)

	// Create a second agent to send to.
	db.InsertAgent(sched.DB, "reviewer", "reviewer", t.TempDir(), "", "orch", "reviewer")
	tc.Windows["orch"] = append(tc.Windows["orch"], "reviewer")

	// Write a .orch-send-reviewer file in builder's dir.
	sendFile := filepath.Join(agentDir, ".orch-send-reviewer")
	os.WriteFile(sendFile, []byte("please review my changes"), 0o644)

	sched.RunOnce()

	// File should be deleted.
	if _, err := os.Stat(sendFile); !os.IsNotExist(err) {
		t.Error("expected .orch-send-reviewer file to be deleted")
	}

	// Should have delivered to reviewer.
	found := false
	for _, sk := range tc.SentKeys {
		if sk.Window == "reviewer" && sk.Text == "please review my changes" {
			found = true
		}
	}
	if !found {
		t.Error("expected message to be delivered to reviewer")
	}
}

func TestProcessSendFileEmptyContent(t *testing.T) {
	tc, sched, agentDir := testSetup(t)

	db.InsertAgent(sched.DB, "reviewer", "reviewer", t.TempDir(), "", "orch", "reviewer")

	sendFile := filepath.Join(agentDir, ".orch-send-reviewer")
	os.WriteFile(sendFile, []byte(""), 0o644)

	sched.RunOnce()

	// File should be deleted.
	if _, err := os.Stat(sendFile); !os.IsNotExist(err) {
		t.Error("expected empty send file to be deleted")
	}

	// Should NOT have sent anything.
	if len(tc.SentKeys) != 0 {
		t.Errorf("expected 0 SendKeys calls for empty message, got %d", len(tc.SentKeys))
	}
}

func TestMalformedScheduleFile(t *testing.T) {
	_, sched, agentDir := testSetup(t)

	schedFile := filepath.Join(agentDir, ".orch-schedule")
	os.WriteFile(schedFile, []byte("notanumber do something"), 0o644)

	// Should not panic.
	sched.RunOnce()

	// File should still be deleted (bad input is cleaned up).
	if _, err := os.Stat(schedFile); !os.IsNotExist(err) {
		t.Error("expected malformed .orch-schedule to be deleted")
	}
}

func TestNoFilesNoError(t *testing.T) {
	_, sched, _ := testSetup(t)

	// Should not error when no files exist.
	err := sched.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce with no files: %v", err)
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

func TestDoneFileWritesInboxMessage(t *testing.T) {
	_, sched, agentDir := testSetup(t)
	inboxDir := setupInbox(t)

	// Write a .orch-done file.
	os.WriteFile(filepath.Join(agentDir, ".orch-done"), []byte("All tasks completed"), 0o644)

	sched.RunOnce()

	// Check that an inbox message was written.
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		t.Fatalf("reading inbox dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least 1 inbox file for done event")
	}

	// Find the message that contains the completion info.
	found := false
	for _, e := range entries {
		content, _ := os.ReadFile(filepath.Join(inboxDir, e.Name()))
		s := string(content)
		if strings.Contains(s, "completed") && strings.Contains(s, "All tasks completed") {
			found = true
			if !strings.Contains(s, "type: info") {
				t.Error("done inbox message should be type: info")
			}
			if !strings.Contains(s, "target: all") {
				t.Error("done inbox message should target: all")
			}
		}
	}
	if !found {
		t.Error("expected inbox message with completion summary")
	}
}

func TestPollInboxDeliversToAgent(t *testing.T) {
	tc, sched, agentDir := testSetup(t)
	inboxDir := setupInbox(t)

	// Set lastInboxCheck to the past so our message is picked up.
	sched.lastInboxCheck = time.Now().Unix() - 10

	// Write an inbox message from a non-orch session targeting the agent's project.
	project := filepath.Base(agentDir)
	ts := time.Now().Unix()
	content := fmt.Sprintf("---\ntype: question\nfrom: %s/main (session-999)\ntarget: %s\ndate: 2026-04-04 10:00\n---\n\nWhat DB schema should I use?\n", project, project)
	msgFile := filepath.Join(inboxDir, fmt.Sprintf("%d-session-999.md", ts))
	os.WriteFile(msgFile, []byte(content), 0644)

	sched.pollInbox()

	// Should have delivered the message to the builder agent.
	found := false
	for _, sk := range tc.SentKeys {
		if sk.Window == "builder" && strings.Contains(sk.Text, "What DB schema") {
			found = true
		}
	}
	if !found {
		t.Error("expected inbox message to be delivered to builder agent")
	}
}

func TestPollInboxSkipsOrchMessages(t *testing.T) {
	tc, sched, agentDir := testSetup(t)
	inboxDir := setupInbox(t)

	sched.lastInboxCheck = time.Now().Unix() - 10

	// Write an inbox message FROM an orch agent — should be skipped.
	project := filepath.Base(agentDir)
	ts := time.Now().Unix()
	content := fmt.Sprintf("---\ntype: info\nfrom: %s/main (orch:reviewer)\ntarget: %s\ndate: 2026-04-04 10:00\n---\n\nTests passing\n", project, project)
	msgFile := filepath.Join(inboxDir, fmt.Sprintf("%d-orch-reviewer.md", ts))
	os.WriteFile(msgFile, []byte(content), 0644)

	sched.pollInbox()

	// Should NOT have delivered anything (it's from orch, already handled).
	if len(tc.SentKeys) != 0 {
		t.Errorf("expected 0 deliveries for orch-sourced inbox message, got %d", len(tc.SentKeys))
	}
}

func TestPollInboxBroadcastReachesAllAgents(t *testing.T) {
	tc, sched, _ := testSetup(t)
	inboxDir := setupInbox(t)

	// Add a second agent.
	reviewerDir := t.TempDir()
	db.InsertAgent(sched.DB, "reviewer", "reviewer", reviewerDir, "", "orch", "reviewer")
	tc.Windows["orch"] = append(tc.Windows["orch"], "reviewer")

	sched.lastInboxCheck = time.Now().Unix() - 10

	// Write a broadcast message.
	ts := time.Now().Unix()
	content := "---\ntype: info\nfrom: other/main (session-42)\ntarget: all\ndate: 2026-04-04 10:00\n---\n\nDeploy complete\n"
	os.WriteFile(filepath.Join(inboxDir, fmt.Sprintf("%d-session-42.md", ts)), []byte(content), 0644)

	sched.pollInbox()

	// Both agents should receive the message.
	builderGot, reviewerGot := false, false
	for _, sk := range tc.SentKeys {
		if sk.Window == "builder" && strings.Contains(sk.Text, "Deploy complete") {
			builderGot = true
		}
		if sk.Window == "reviewer" && strings.Contains(sk.Text, "Deploy complete") {
			reviewerGot = true
		}
	}
	if !builderGot {
		t.Error("expected builder to receive broadcast")
	}
	if !reviewerGot {
		t.Error("expected reviewer to receive broadcast")
	}
}

func TestPollInboxUpdatesLastCheck(t *testing.T) {
	_, sched, agentDir := testSetup(t)
	inboxDir := setupInbox(t)

	before := sched.lastInboxCheck

	project := filepath.Base(agentDir)
	ts := time.Now().Unix() + 100
	content := fmt.Sprintf("---\ntype: info\nfrom: x/main (session-1)\ntarget: %s\ndate: 2026-04-04 10:00\n---\n\ntest\n", project)
	os.WriteFile(filepath.Join(inboxDir, fmt.Sprintf("%d-session-1.md", ts)), []byte(content), 0644)

	sched.pollInbox()

	if sched.lastInboxCheck <= before {
		t.Error("expected lastInboxCheck to advance after processing a message")
	}
	if sched.lastInboxCheck != ts {
		t.Errorf("expected lastInboxCheck = %d, got %d", ts, sched.lastInboxCheck)
	}
}

func TestPollInboxNoMessagesNoError(t *testing.T) {
	_, sched, _ := testSetup(t)
	setupInbox(t)

	// Should not panic or error with an empty inbox.
	sched.pollInbox()
}
