package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jeffdhooton/orch/internal/agent"
	"github.com/jeffdhooton/orch/internal/dashboard"
	"github.com/jeffdhooton/orch/internal/db"
	"github.com/jeffdhooton/orch/internal/messenger"
	"github.com/jeffdhooton/orch/internal/scheduler"
	"github.com/jeffdhooton/orch/internal/specgen/analyze"
	"github.com/jeffdhooton/orch/internal/specgen/generate"
	"github.com/jeffdhooton/orch/internal/tmux"
	"github.com/spf13/cobra"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	rootCmd := &cobra.Command{
		Use:   "orch",
		Short: "Orchestrate multiple Claude Code instances via tmux",
	}

	rootCmd.AddCommand(
		initCmd(log),
		upCmd(log),
		upDirCmd(log),
		downCmd(log),
		psCmd(log),
		sendCmd(log),
		logsCmd(log),
		scheduleCmd(log),
		dashCmd(log),
		resetCmd(log),
		schedulerCmd(log),
		watchCmd(log),
		attachCmd(log),
		statusCmd(log),
		specgenCmd(log),
		schedulerRestartCmd(log),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// openDB is a helper that opens the default database.
func openDB() (*sql.DB, error) {
	dbPath, err := db.DefaultDBPath()
	if err != nil {
		return nil, err
	}
	return db.Open(dbPath)
}

func initCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize ~/.orch/ and the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			dir, _ := db.DefaultDir()
			fmt.Printf("Initialized orch at %s\n", dir)
			return nil
		},
	}
}

func upCmd(log *slog.Logger) *cobra.Command {
	var role, dir, specPath, skills string
	var skipPermissions, noScheduler bool

	cmd := &cobra.Command{
		Use:   "up <name>",
		Short: "Spin up a named agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if dir == "" {
				var err error
				dir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("getting working directory: %w", err)
				}
			}

			skillPaths, err := resolveSkillPaths(skills)
			if err != nil {
				return err
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			mgr := agent.New(database, tc, log)

			if err := mgr.Up(agent.UpOpts{
				Name:            name,
				Role:            role,
				Dir:             dir,
				SpecPath:        specPath,
				SkipPermissions: skipPermissions,
				SkillPaths:      skillPaths,
			}); err != nil {
				return err
			}

			fmt.Printf("Agent %q started (role: %s, dir: %s)\n", name, role, dir)

			// Auto-start the scheduler if not already running.
			if !noScheduler {
				ensureScheduler(log)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "engineer", "Role for the agent")
	cmd.Flags().StringVar(&dir, "dir", "", "Working directory (defaults to current directory)")
	cmd.Flags().StringVar(&specPath, "spec", "", "Path to a spec file to send as the first message")
	cmd.Flags().BoolVar(&skipPermissions, "skip-permissions", true, "Pass --dangerously-skip-permissions to claude (default: true for autonomous agents)")
	cmd.Flags().StringVar(&skills, "skills", "", "Comma-separated skill/plugin directories to load (e.g. gstack, ~/.claude/skills/custom)")
	cmd.Flags().BoolVar(&noScheduler, "no-scheduler", false, "Don't auto-start the background scheduler")

	return cmd
}

func upDirCmd(log *slog.Logger) *cobra.Command {
	var dir, prefix, skills string
	var skipPermissions, noScheduler bool

	cmd := &cobra.Command{
		Use:   "up-dir <spec-directory>",
		Short: "Spin up agents for every spec file in a directory",
		Long:  "Scans a directory for *.md files and creates one agent per file. The filename (minus .md) is used as the role, and the agent name is derived from the role (optionally prefixed with --prefix).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			specDir := args[0]

			// Resolve spec directory.
			absSpecDir, err := filepath.Abs(specDir)
			if err != nil {
				return fmt.Errorf("resolving spec directory: %w", err)
			}
			info, err := os.Stat(absSpecDir)
			if err != nil {
				return fmt.Errorf("spec directory %q: %w", absSpecDir, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%q is not a directory", absSpecDir)
			}

			// Find all .md files in the directory.
			entries, err := os.ReadDir(absSpecDir)
			if err != nil {
				return fmt.Errorf("reading spec directory: %w", err)
			}
			var specFiles []os.DirEntry
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					specFiles = append(specFiles, e)
				}
			}
			if len(specFiles) == 0 {
				return fmt.Errorf("no .md spec files found in %s", absSpecDir)
			}

			if dir == "" {
				dir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("getting working directory: %w", err)
				}
			}

			skillPaths, err := resolveSkillPaths(skills)
			if err != nil {
				return err
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			mgr := agent.New(database, tc, log)

			for _, e := range specFiles {
				role := strings.TrimSuffix(e.Name(), ".md")
				name := role
				if prefix != "" {
					name = prefix + "-" + role
				}
				specPath := filepath.Join(absSpecDir, e.Name())

				fmt.Printf("Starting agent %q (role: %s, spec: %s)\n", name, role, e.Name())
				if err := mgr.Up(agent.UpOpts{
					Name:            name,
					Role:            role,
					Dir:             dir,
					SpecPath:        specPath,
					SkipPermissions: skipPermissions,
					SkillPaths:      skillPaths,
				}); err != nil {
					return fmt.Errorf("starting agent %q: %w", name, err)
				}
			}

			fmt.Printf("\n%d agent(s) started from %s\n", len(specFiles), absSpecDir)

			if !noScheduler {
				ensureScheduler(log)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Working directory for all agents (defaults to current directory)")
	cmd.Flags().StringVar(&prefix, "prefix", "", "Prefix for agent names (e.g. 'api' → 'api-engineer', 'api-pm')")
	cmd.Flags().StringVar(&skills, "skills", "", "Comma-separated skill/plugin directories to load (e.g. gstack, ~/.claude/skills/custom)")
	cmd.Flags().BoolVar(&skipPermissions, "skip-permissions", true, "Pass --dangerously-skip-permissions to claude")
	cmd.Flags().BoolVar(&noScheduler, "no-scheduler", false, "Don't auto-start the background scheduler")

	return cmd
}

func downCmd(log *slog.Logger) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "down [name]",
		Short: "Tear down an agent (or all agents with --all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all && len(args) == 0 {
				return fmt.Errorf("specify an agent name or use --all")
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			mgr := agent.New(database, tc, log)

			if all {
				agents, err := mgr.List()
				if err != nil {
					return err
				}
				if len(agents) == 0 {
					fmt.Println("No agents running.")
					return nil
				}
				for _, a := range agents {
					if err := mgr.Down(a.Agent.Name); err != nil {
						log.Warn("failed to stop agent", "name", a.Agent.Name, "error", err)
					} else {
						fmt.Printf("Agent %q stopped\n", a.Agent.Name)
					}
				}
				return nil
			}

			if err := mgr.Down(args[0]); err != nil {
				return err
			}
			fmt.Printf("Agent %q stopped\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Tear down all agents")
	return cmd
}

func psCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List all agents and their status",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			mgr := agent.New(database, tc, log)

			agents, err := mgr.List()
			if err != nil {
				return err
			}

			if len(agents) == 0 {
				fmt.Println("No agents registered.")
				return nil
			}

			// Print header.
			fmt.Printf("%-15s %-12s %-10s %-40s %s\n", "NAME", "ROLE", "STATUS", "DIR", "LAST ACTIVITY")
			fmt.Println(strings.Repeat("-", 100))

			for _, a := range agents {
				lastActivity := a.Agent.LastActivity.Format(time.DateTime)
				status := a.EffectiveStatus
				if status == "running" && a.Idle {
					status = "idle"
				}
				fmt.Printf("%-15s %-12s %-10s %-40s %s\n",
					a.Agent.Name,
					a.Agent.Role,
					status,
					truncate(a.Agent.Dir, 40),
					lastActivity,
				)
			}

			return nil
		},
	}
}

func sendCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "send <name> <message>",
		Short: "Send a message to an agent",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			message := strings.Join(args[1:], " ")

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			msg := messenger.New(database, tc, log)

			if err := msg.Send("user", name, message); err != nil {
				return err
			}

			fmt.Printf("Message sent to %q\n", name)
			return nil
		},
	}
}

func logsCmd(log *slog.Logger) *cobra.Command {
	var tail int

	cmd := &cobra.Command{
		Use:   "logs [name]",
		Short: "View message history (all agents, or filter by name)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			var msgs []db.Message
			if len(args) == 1 {
				msgs, err = db.ListMessages(database, args[0], tail)
			} else {
				limit := tail
				if limit == 0 {
					limit = 50
				}
				msgs, err = db.ListAllMessages(database, limit)
			}
			if err != nil {
				return err
			}

			if len(msgs) == 0 {
				if len(args) == 1 {
					fmt.Printf("No messages for agent %q\n", args[0])
				} else {
					fmt.Println("No messages.")
				}
				return nil
			}

			// Print in chronological order (query returns newest first).
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				delivered := " "
				if m.Delivered {
					delivered = "✓"
				}
				fmt.Printf("[%s] %s %s → %s: %s\n",
					m.CreatedAt.Format(time.DateTime),
					delivered,
					m.FromSource,
					m.ToAgent,
					m.Content,
				)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&tail, "tail", 0, "Limit to last N messages (default 50 for all, 0=unlimited for named agent)")

	return cmd
}

func scheduleCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "schedule <name> <minutes> <note>",
		Short: "Schedule a future message to an agent",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			minutes, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid minutes %q: %w", args[1], err)
			}
			note := strings.Join(args[2:], " ")

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			runAt := time.Now().Add(time.Duration(minutes) * time.Minute)
			if err := db.InsertSchedule(database, name, runAt, note); err != nil {
				return err
			}

			fmt.Printf("Scheduled message to %q in %d minutes: %s\n", name, minutes, note)
			return nil
		},
	}
}

func dashCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "dash",
		Short: "Live terminal dashboard of all agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Kill any stale background scheduler so the dashboard's
			// internal scheduler is the only one running.
			stopScheduler()

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			return dashboard.Run(database, log)
		},
	}
}

func resetCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Kill all agents, destroy the orch tmux session, and wipe the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			tc := tmux.New()

			// Kill the background scheduler if running.
			stopScheduler()

			// Kill the orch tmux session if it exists.
			if tc.HasSession(agent.SessionName) {
				fmt.Println("Killing orch tmux session...")
				if err := tc.KillSession(agent.SessionName); err != nil {
					log.Warn("failed to kill tmux session", "error", err)
				}
			}

			// Remove the database file to wipe all state.
			dbPath, err := db.DefaultDBPath()
			if err != nil {
				return err
			}

			fmt.Printf("Removing database %s...\n", dbPath)
			if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing database: %w", err)
			}
			// Also remove WAL/SHM files.
			os.Remove(dbPath + "-wal")
			os.Remove(dbPath + "-shm")

			// Re-initialize a fresh database.
			database, err := db.Open(dbPath)
			if err != nil {
				return fmt.Errorf("re-initializing database: %w", err)
			}
			database.Close()

			fmt.Println("Reset complete. Clean slate.")
			return nil
		},
	}
}

func schedulerCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "scheduler",
		Short: "Run the scheduler as a foreground process (delivers scheduled messages and processes agent files)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// When running as a background daemon, redirect logs to file.
			orchDir, _ := db.DefaultDir()
			if orchDir != "" {
				logFile := filepath.Join(orchDir, "scheduler.log")
				f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
				if err == nil {
					log = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
					// Also redirect stdout/stderr so nothing leaks to terminal.
					os.Stdout = f
					os.Stderr = f
				}
			}

			// Write PID file so reset can find us.
			if orchDir != "" {
				pidFile := filepath.Join(orchDir, "scheduler.pid")
				os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
				defer os.Remove(pidFile)
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			msg := messenger.New(database, tc, log)
			sched := scheduler.New(database, msg, log)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			log.Info("scheduler started", "schedule_poll", "30s", "file_poll", "10s")
			sched.Run(ctx, 30*time.Second, 10*time.Second)
			log.Info("scheduler stopped")
			return nil
		},
	}
}

func watchCmd(log *slog.Logger) *cobra.Command {
	var interval int

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch for dead agents and automatically restart them",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			mgr := agent.New(database, tc, log)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			ticker := time.NewTicker(time.Duration(interval) * time.Second)
			defer ticker.Stop()

			fmt.Printf("Watching for dead agents every %ds. Ctrl-C to stop.\n", interval)

			for {
				select {
				case <-ctx.Done():
					fmt.Println("Watch stopped.")
					return nil
				case <-ticker.C:
					agents, err := mgr.List()
					if err != nil {
						log.Error("listing agents", "error", err)
						continue
					}
					for _, a := range agents {
						if a.EffectiveStatus == "dead" {
							log.Info("restarting dead agent", "name", a.Agent.Name, "role", a.Agent.Role)
							// Remove old record and re-up.
							_ = mgr.Down(a.Agent.Name)
							if err := mgr.Up(agent.UpOpts{
								Name:            a.Agent.Name,
								Role:            a.Agent.Role,
								Dir:             a.Agent.Dir,
								SpecPath:        a.Agent.SpecPath.String,
								SkipPermissions: true,
							}); err != nil {
								log.Error("restarting agent", "name", a.Agent.Name, "error", err)
							} else {
								fmt.Printf("Restarted agent %q\n", a.Agent.Name)
							}
						}
					}
				}
			}
		},
	}

	cmd.Flags().IntVar(&interval, "interval", 30, "Check interval in seconds")
	return cmd
}

func attachCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to an agent's tmux window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			a, err := db.GetAgent(database, name)
			if err != nil {
				return err
			}

			// Select the agent's window, then attach.
			_ = tc.SelectWindow(a.TmuxSession, a.TmuxWindow)
			return tc.AttachSession(a.TmuxSession)
		},
	}
}

func statusCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Quick pulse check on orch",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			mgr := agent.New(database, tc, log)

			agents, err := mgr.List()
			if err != nil {
				return err
			}

			// Count by status.
			running, dead := 0, 0
			var lastActivity time.Time
			for _, a := range agents {
				switch a.EffectiveStatus {
				case "running":
					running++
				case "dead":
					dead++
				}
				if a.Agent.LastActivity.After(lastActivity) {
					lastActivity = a.Agent.LastActivity
				}
			}

			// Scheduler status.
			schedulerRunning := false
			orchDir, _ := db.DefaultDir()
			if orchDir != "" {
				pidFile := filepath.Join(orchDir, "scheduler.pid")
				if data, err := os.ReadFile(pidFile); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
						if proc, err := os.FindProcess(pid); err == nil {
							if proc.Signal(syscall.Signal(0)) == nil {
								schedulerRunning = true
							}
						}
					}
				}
			}

			if len(agents) == 0 {
				fmt.Println("No agents. Use `orch up` to start one.")
				return nil
			}

			// Print summary.
			fmt.Printf("%d agent(s) running", running)
			if dead > 0 {
				fmt.Printf(", %d dead", dead)
			}
			fmt.Println()

			if schedulerRunning {
				fmt.Println("Scheduler: running")
			} else {
				fmt.Println("Scheduler: stopped")
			}

			if !lastActivity.IsZero() {
				ago := time.Since(lastActivity)
				if ago < time.Minute {
					fmt.Println("Last activity: just now")
				} else {
					fmt.Printf("Last activity: %dm ago\n", int(ago.Minutes()))
				}
			}

			return nil
		},
	}
}

func specgenCmd(log *slog.Logger) *cobra.Command {
	var dir, task, name, output, roles, model, skills, fromPlan string
	var analyzeOnly, verbose bool

	cmd := &cobra.Command{
		Use:   "specgen",
		Short: "Generate role-specific specs for multi-agent workflows",
		Long:  "Analyzes a codebase and generates engineer and reviewer specs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				var err error
				dir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("getting working directory: %w", err)
				}
			}

			// Verify directory exists
			info, err := os.Stat(dir)
			if err != nil {
				return fmt.Errorf("directory %q: %w", dir, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%q is not a directory", dir)
			}

			// Run analysis
			fmt.Fprintf(os.Stderr, "Analyzing codebase at %s...\n", dir)
			result, err := analyze.Analyze(dir)
			if err != nil {
				return fmt.Errorf("analyzing codebase: %w", err)
			}

			// If --analyze, print and exit
			if analyzeOnly {
				printAnalysis(result)
				return nil
			}

			// Need --task or --from-plan for generation
			if task == "" && fromPlan == "" {
				return fmt.Errorf("either --task or --from-plan is required (or use --analyze)")
			}

			// Parse roles
			roleList := parseRoles(roles)

			// Set output directory with task-based subdirectory
			if output == "" {
				slug := name
				if slug == "" {
					slug = task
				}
				if slug == "" && fromPlan != "" {
					// Use plan filename as slug
					slug = strings.TrimSuffix(filepath.Base(fromPlan), filepath.Ext(fromPlan))
				}
				slug = slugify(slug)
				output = filepath.Join(dir, "specs", slug)
			}

			// Resolve skill paths
			skillPaths, err := resolveSkillPaths(skills)
			if err != nil {
				return err
			}

			// Generate specs
			gen := generate.New()
			gen.Verbose = verbose
			gen.Model = model
			return gen.Generate(cmd.Context(), generate.GenerateOpts{
				Analysis:   result,
				Task:       task,
				Roles:      roleList,
				OutputDir:  output,
				SkillPaths: skillPaths,
				PlanPath:   fromPlan,
			})
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Target codebase directory (defaults to current directory)")
	cmd.Flags().StringVar(&task, "task", "", "Task description for spec generation")
	cmd.Flags().StringVar(&name, "name", "", "Short name for this spec set (auto-generated from task if omitted)")
	cmd.Flags().StringVar(&output, "output", "", "Output directory (default: <dir>/specs/<name>/)")
	cmd.Flags().StringVar(&roles, "roles", "engineer,reviewer", "Comma-separated roles to generate (available: engineer, reviewer, pm)")
	cmd.Flags().BoolVar(&analyzeOnly, "analyze", false, "Just print codebase analysis, skip generation")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed progress and timing for each step")
	cmd.Flags().StringVar(&model, "model", "", "Claude model to use (e.g. sonnet, haiku, opus)")
	cmd.Flags().StringVar(&skills, "skills", "", "Comma-separated skill/plugin directories whose commands should be referenced in generated specs")
	cmd.Flags().StringVar(&fromPlan, "from-plan", "", "Path to an existing plan document to slice into role specs (skips task-based generation)")

	return cmd
}

// resolveSkillPaths takes a comma-separated string of skill names or paths
// and returns resolved absolute paths. Short names like "gstack" are expanded
// to ~/.claude/skills/<name>.
func resolveSkillPaths(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home dir: %w", err)
	}

	var paths []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, "/") || strings.HasPrefix(s, "~") {
			s = strings.Replace(s, "~", home, 1)
			abs, err := filepath.Abs(s)
			if err != nil {
				return nil, fmt.Errorf("resolving skill path %q: %w", s, err)
			}
			paths = append(paths, abs)
		} else {
			candidate := filepath.Join(home, ".claude", "skills", s)
			if _, err := os.Stat(candidate); err != nil {
				candidate2 := filepath.Join(home, ".claude", "plugins", s)
				if _, err2 := os.Stat(candidate2); err2 != nil {
					return nil, fmt.Errorf("skill %q not found at %s or %s", s, candidate, candidate2)
				}
				candidate = candidate2
			}
			paths = append(paths, candidate)
		}
	}
	return paths, nil
}

// slugify converts a task description into a filesystem-safe directory name.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "default"
	}
	return s
}

func parseRoles(s string) []string {
	var roles []string
	for _, r := range strings.Split(s, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			roles = append(roles, r)
		}
	}
	return roles
}

func printAnalysis(a *analyze.Analysis) {
	fmt.Printf("# Codebase Analysis: %s\n\n", a.Dir)

	fmt.Printf("## Tech Stack\n")
	fmt.Printf("- Language: %s\n", a.Stack.Language)
	if a.Stack.Framework != "" {
		fmt.Printf("- Framework: %s\n", a.Stack.Framework)
	}
	fmt.Printf("- Build: %s\n", a.Stack.BuildCmd)
	fmt.Printf("- Test: %s\n", a.Stack.TestCmd)
	if a.Stack.LintCmd != "" {
		fmt.Printf("- Lint: %s\n", a.Stack.LintCmd)
	}
	if len(a.Stack.Dependencies) > 0 {
		fmt.Printf("- Dependencies: %s\n", strings.Join(a.Stack.Dependencies, ", "))
	}
	fmt.Println()

	fmt.Printf("## Project Structure\n")
	fmt.Printf("- Top-level dirs: %s\n", strings.Join(a.Structure.TopLevelDirs, ", "))
	fmt.Printf("- Total files: %d\n", a.Structure.TotalFiles)
	if len(a.Structure.KeyFiles) > 0 {
		fmt.Printf("- Key files: %s\n", strings.Join(a.Structure.KeyFiles, ", "))
	}
	if len(a.Structure.TestFiles) > 0 {
		fmt.Printf("- Test files: %s\n", strings.Join(a.Structure.TestFiles, ", "))
	}
	if len(a.Structure.FilesByExt) > 0 {
		fmt.Printf("- Files by type: ")
		parts := make([]string, 0, len(a.Structure.FilesByExt))
		for ext, count := range a.Structure.FilesByExt {
			parts = append(parts, fmt.Sprintf("%s=%d", ext, count))
		}
		fmt.Println(strings.Join(parts, ", "))
	}
	fmt.Println()

	if a.Git.Branch != "" {
		fmt.Printf("## Git State\n")
		fmt.Printf("- Branch: %s\n", a.Git.Branch)
		fmt.Printf("- Uncommitted changes: %v\n", a.Git.HasUncommitted)
		if a.Git.RemoteURL != "" {
			fmt.Printf("- Remote: %s\n", a.Git.RemoteURL)
		}
		if len(a.Git.RecentCommits) > 0 {
			fmt.Printf("- Recent commits:\n")
			for _, c := range a.Git.RecentCommits {
				fmt.Printf("  %s\n", c)
			}
		}
		fmt.Println()
	}

	if len(a.Documentation) > 0 {
		fmt.Printf("## Documentation\n")
		for _, doc := range a.Documentation {
			fmt.Printf("- %s (%d lines)\n", doc.Path, strings.Count(doc.Content, "\n"))
		}
		fmt.Println()
	}
}

// ensureScheduler starts the scheduler as a background process if one isn't
// already running. Uses a PID file at ~/.orch/scheduler.pid to track it.
func ensureScheduler(log *slog.Logger) {
	orchDir, err := db.DefaultDir()
	if err != nil {
		return
	}
	pidFile := filepath.Join(orchDir, "scheduler.pid")

	// Check if a scheduler is already running.
	if data, err := os.ReadFile(pidFile); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			// Check if process is still alive.
			proc, err := os.FindProcess(pid)
			if err == nil {
				// Signal 0 checks if process exists without killing it.
				if proc.Signal(syscall.Signal(0)) == nil {
					return // Scheduler already running.
				}
			}
		}
	}

	// Find our own binary path to spawn the scheduler.
	self, err := os.Executable()
	if err != nil {
		log.Warn("could not find orch executable for scheduler", "error", err)
		return
	}

	// Start scheduler as a detached background process.
	logFile := filepath.Join(orchDir, "scheduler.log")
	outFile, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Warn("could not open scheduler log", "error", err)
		return
	}

	cmd := exec.Command(self, "scheduler")
	cmd.Stdout = outFile
	cmd.Stderr = outFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // Detach from parent.
	if err := cmd.Start(); err != nil {
		outFile.Close()
		log.Warn("could not start scheduler", "error", err)
		return
	}
	outFile.Close()

	// Write PID file.
	os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	fmt.Printf("Scheduler started (pid %d, log: %s)\n", cmd.Process.Pid, logFile)
}

func schedulerRestartCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "scheduler-restart",
		Short: "Kill and restart the background scheduler",
		RunE: func(cmd *cobra.Command, args []string) error {
			stopScheduler()
			fmt.Println("Stopped old scheduler.")
			ensureScheduler(log)
			return nil
		},
	}
}

// stopScheduler kills all background scheduler processes.
func stopScheduler() {
	orchDir, _ := db.DefaultDir()

	// Try PID file first.
	if orchDir != "" {
		pidFile := filepath.Join(orchDir, "scheduler.pid")
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				if proc, err := os.FindProcess(pid); err == nil {
					proc.Signal(syscall.SIGTERM)
				}
			}
		}
		os.Remove(pidFile)
	}

	// Also pkill any stragglers — the PID file alone isn't reliable.
	exec.Command("pkill", "-f", "orch scheduler").Run()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return "..." + s[len(s)-maxLen+3:]
}
