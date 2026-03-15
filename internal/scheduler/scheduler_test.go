package scheduler

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeffdhooton/orch/internal/db"
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
	msg := messenger.New(database, tc)
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
