package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/messenger"
)

// Scheduler polls for due schedules and agent file-based requests.
type Scheduler struct {
	DB        *sql.DB
	Messenger *messenger.Messenger
	Log       *slog.Logger
}

// New creates a new Scheduler.
func New(database *sql.DB, msg *messenger.Messenger, log *slog.Logger) *Scheduler {
	return &Scheduler{DB: database, Messenger: msg, Log: log}
}

// RunOnce checks for and executes any due schedules and processes agent files.
func (s *Scheduler) RunOnce() error {
	if err := s.processDueSchedules(); err != nil {
		s.Log.Error("processing due schedules", "error", err)
	}
	if err := s.processAgentFiles(); err != nil {
		s.Log.Error("processing agent files", "error", err)
	}
	return nil
}

// Run starts the scheduler loop, polling at the given interval until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context, scheduleInterval, fileInterval time.Duration) {
	scheduleTicker := time.NewTicker(scheduleInterval)
	fileTicker := time.NewTicker(fileInterval)
	defer scheduleTicker.Stop()
	defer fileTicker.Stop()

	// Run once immediately.
	s.RunOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-scheduleTicker.C:
			if err := s.processDueSchedules(); err != nil {
				s.Log.Error("processing due schedules", "error", err)
			}
		case <-fileTicker.C:
			if err := s.processAgentFiles(); err != nil {
				s.Log.Error("processing agent files", "error", err)
			}
		}
	}
}

func (s *Scheduler) processDueSchedules() error {
	schedules, err := db.DueSchedules(s.DB)
	if err != nil {
		return fmt.Errorf("querying due schedules: %w", err)
	}

	for _, sched := range schedules {
		s.Log.Info("executing scheduled message", "agent", sched.AgentName, "note", sched.Note)
		if err := s.Messenger.Send("scheduler", sched.AgentName, sched.Note); err != nil {
			s.Log.Error("delivering scheduled message", "agent", sched.AgentName, "error", err)
			continue
		}
		if err := db.MarkScheduleExecuted(s.DB, sched.ID); err != nil {
			s.Log.Error("marking schedule executed", "id", sched.ID, "error", err)
		}
	}
	return nil
}

func (s *Scheduler) processAgentFiles() error {
	agents, err := db.ListAgents(s.DB, "running")
	if err != nil {
		return fmt.Errorf("listing running agents: %w", err)
	}

	for _, agent := range agents {
		s.processScheduleFile(agent)
		s.processSendFiles(agent)
	}
	return nil
}

func (s *Scheduler) processScheduleFile(agent db.Agent) {
	path := filepath.Join(agent.Dir, ".orch-schedule")
	content, err := os.ReadFile(path)
	if err != nil {
		return // File doesn't exist, that's normal.
	}

	line := strings.TrimSpace(string(content))
	parts := strings.SplitN(line, " ", 2)
	if len(parts) != 2 {
		s.Log.Warn("malformed .orch-schedule file", "agent", agent.Name, "content", line)
		os.Remove(path)
		return
	}

	minutes, err := strconv.Atoi(parts[0])
	if err != nil {
		s.Log.Warn("invalid minutes in .orch-schedule", "agent", agent.Name, "value", parts[0])
		os.Remove(path)
		return
	}

	note := parts[1]
	runAt := time.Now().Add(time.Duration(minutes) * time.Minute)

	if err := db.InsertSchedule(s.DB, agent.Name, runAt, note); err != nil {
		s.Log.Error("inserting schedule from file", "agent", agent.Name, "error", err)
	} else {
		s.Log.Info("scheduled from agent file", "agent", agent.Name, "minutes", minutes, "note", note)
	}

	os.Remove(path)
}

func (s *Scheduler) processSendFiles(agent db.Agent) {
	entries, err := os.ReadDir(agent.Dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".orch-send-") {
			continue
		}
		targetName := strings.TrimPrefix(entry.Name(), ".orch-send-")
		if targetName == "" {
			continue
		}

		path := filepath.Join(agent.Dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			s.Log.Warn("reading send file", "path", path, "error", err)
			continue
		}

		msg := strings.TrimSpace(string(content))
		if msg == "" {
			os.Remove(path)
			continue
		}

		if err := s.Messenger.Send(agent.Name, targetName, msg); err != nil {
			s.Log.Error("delivering inter-agent message", "from", agent.Name, "to", targetName, "error", err)
		} else {
			s.Log.Info("delivered inter-agent message", "from", agent.Name, "to", targetName)
		}

		os.Remove(path)
	}
}
