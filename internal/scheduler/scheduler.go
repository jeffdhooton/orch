package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/messenger"
)

// Scheduler polls for due schedules and agent file-based requests.
type Scheduler struct {
	DB          *sql.DB
	Messenger   *messenger.Messenger
	Log         *slog.Logger
	lastCommits map[string]string // dir -> last known commit hash
}

// New creates a new Scheduler.
func New(database *sql.DB, msg *messenger.Messenger, log *slog.Logger) *Scheduler {
	return &Scheduler{DB: database, Messenger: msg, Log: log, lastCommits: make(map[string]string)}
}

// RunOnce checks for and executes any due schedules and processes agent files.
func (s *Scheduler) RunOnce() error {
	if err := s.processDueSchedules(); err != nil {
		s.Log.Error("processing due schedules", "error", err)
	}
	if err := s.processAgentFiles(); err != nil {
		s.Log.Error("processing agent files", "error", err)
	}
	s.processGitCommits()
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

	idleTicks := 0
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
			s.processGitCommits()

			// Auto-exit when no running agents remain.
			agents, err := db.ListAgents(s.DB, "running")
			if err == nil && len(agents) == 0 {
				idleTicks++
				if idleTicks >= 3 { // ~30s of no agents before exiting
					s.Log.Info("no running agents, scheduler exiting")
					return
				}
			} else {
				idleTicks = 0
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

// processGitCommits checks each unique agent directory for new git commits.
// When a new commit is detected, it notifies any PM-role agents in the same
// directory so they can check progress without waiting for their next scheduled check-in.
func (s *Scheduler) processGitCommits() {
	agents, err := db.ListAgents(s.DB, "running")
	if err != nil {
		return
	}

	// Group agents by directory and find PMs.
	type dirInfo struct {
		pms      []db.Agent
		builders []db.Agent
	}
	dirs := make(map[string]*dirInfo)
	for _, a := range agents {
		di, ok := dirs[a.Dir]
		if !ok {
			di = &dirInfo{}
			dirs[a.Dir] = di
		}
		if a.Role == "pm" {
			di.pms = append(di.pms, a)
		} else {
			di.builders = append(di.builders, a)
		}
	}

	for dir, di := range dirs {
		// Skip directories with no PMs or no builders.
		if len(di.pms) == 0 || len(di.builders) == 0 {
			continue
		}

		// Get latest commit hash.
		cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
		out, err := cmd.Output()
		if err != nil {
			continue // Not a git repo or no commits.
		}
		hash := strings.TrimSpace(string(out))

		prev, seen := s.lastCommits[dir]
		s.lastCommits[dir] = hash

		if !seen {
			continue // First time seeing this dir, just record it.
		}
		if hash == prev {
			continue // No new commits.
		}

		// Get the commit message for context.
		cmd = exec.Command("git", "-C", dir, "log", "--oneline", "-1")
		msgOut, _ := cmd.Output()
		commitMsg := strings.TrimSpace(string(msgOut))

		// Notify all PMs in this directory.
		for _, pm := range di.pms {
			notification := fmt.Sprintf("New commit detected: %s — check progress and verify build.", commitMsg)
			if err := s.Messenger.Send("git-watcher", pm.Name, notification); err != nil {
				s.Log.Error("notifying PM of new commit", "pm", pm.Name, "error", err)
			} else {
				s.Log.Info("notified PM of new commit", "pm", pm.Name, "commit", commitMsg)
			}
		}
	}
}
