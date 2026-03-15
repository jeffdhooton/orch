package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestInsertAndGetAgent(t *testing.T) {
	d := testDB(t)

	err := InsertAgent(d, "builder", "engineer", "/tmp/work", "", "orch", "builder")
	if err != nil {
		t.Fatalf("inserting agent: %v", err)
	}

	agent, err := GetAgent(d, "builder")
	if err != nil {
		t.Fatalf("getting agent: %v", err)
	}

	if agent.Name != "builder" {
		t.Errorf("expected name 'builder', got %q", agent.Name)
	}
	if agent.Role != "engineer" {
		t.Errorf("expected role 'engineer', got %q", agent.Role)
	}
	if agent.Status != "running" {
		t.Errorf("expected status 'running', got %q", agent.Status)
	}
}

func TestInsertDuplicateAgent(t *testing.T) {
	d := testDB(t)

	err := InsertAgent(d, "dup", "engineer", "/tmp", "", "orch", "dup")
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err = InsertAgent(d, "dup", "pm", "/tmp", "", "orch", "dup")
	if err == nil {
		t.Fatal("expected error on duplicate insert")
	}
}

func TestUpdateAgentStatus(t *testing.T) {
	d := testDB(t)

	InsertAgent(d, "a1", "engineer", "/tmp", "", "orch", "a1")
	if err := UpdateAgentStatus(d, "a1", "stopped"); err != nil {
		t.Fatalf("updating status: %v", err)
	}
	agent, _ := GetAgent(d, "a1")
	if agent.Status != "stopped" {
		t.Errorf("expected 'stopped', got %q", agent.Status)
	}
}

func TestUpdateAgentStatusNotFound(t *testing.T) {
	d := testDB(t)
	err := UpdateAgentStatus(d, "ghost", "stopped")
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
}

func TestListAgents(t *testing.T) {
	d := testDB(t)

	InsertAgent(d, "a", "engineer", "/tmp/a", "", "orch", "a")
	InsertAgent(d, "b", "pm", "/tmp/b", "", "orch", "b")
	UpdateAgentStatus(d, "b", "stopped")

	tests := []struct {
		filter string
		want   int
	}{
		{"", 2},
		{"running", 1},
		{"stopped", 1},
	}

	for _, tt := range tests {
		agents, err := ListAgents(d, tt.filter)
		if err != nil {
			t.Fatalf("listing agents (filter=%q): %v", tt.filter, err)
		}
		if len(agents) != tt.want {
			t.Errorf("filter=%q: got %d agents, want %d", tt.filter, len(agents), tt.want)
		}
	}
}

func TestGetAgentNotFound(t *testing.T) {
	d := testDB(t)
	_, err := GetAgent(d, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
}

func TestMessagesRoundTrip(t *testing.T) {
	d := testDB(t)

	id, err := InsertMessage(d, "user", "builder", "hello there")
	if err != nil {
		t.Fatalf("inserting message: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero message ID")
	}

	if err := MarkMessageDelivered(d, id); err != nil {
		t.Fatalf("marking delivered: %v", err)
	}

	msgs, err := ListMessages(d, "builder", 0)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !msgs[0].Delivered {
		t.Error("expected message to be delivered")
	}
	if msgs[0].Content != "hello there" {
		t.Errorf("expected content 'hello there', got %q", msgs[0].Content)
	}
}

func TestListMessagesLimit(t *testing.T) {
	d := testDB(t)

	for i := 0; i < 5; i++ {
		InsertMessage(d, "user", "agent", "msg")
	}

	msgs, err := ListMessages(d, "agent", 3)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
}

func TestScheduleRoundTrip(t *testing.T) {
	d := testDB(t)

	// Insert a schedule in the past so it's immediately due.
	past := time.Now().Add(-1 * time.Minute)
	if err := InsertSchedule(d, "builder", past, "check status"); err != nil {
		t.Fatalf("inserting schedule: %v", err)
	}

	due, err := DueSchedules(d)
	if err != nil {
		t.Fatalf("querying due schedules: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due schedule, got %d", len(due))
	}

	if err := MarkScheduleExecuted(d, due[0].ID); err != nil {
		t.Fatalf("marking executed: %v", err)
	}

	due, _ = DueSchedules(d)
	if len(due) != 0 {
		t.Errorf("expected 0 due schedules after execution, got %d", len(due))
	}
}

func TestScheduleFutureNotDue(t *testing.T) {
	d := testDB(t)

	future := time.Now().Add(1 * time.Hour)
	InsertSchedule(d, "builder", future, "later")

	due, err := DueSchedules(d)
	if err != nil {
		t.Fatalf("querying due schedules: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected 0 due schedules, got %d", len(due))
	}
}

func TestDefaultDir(t *testing.T) {
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("getting default dir: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".orch")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}
