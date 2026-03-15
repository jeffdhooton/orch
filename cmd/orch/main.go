package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jhoot/orch/internal/agent"
	"github.com/jhoot/orch/internal/dashboard"
	"github.com/jhoot/orch/internal/db"
	"github.com/jhoot/orch/internal/messenger"
	"github.com/jhoot/orch/internal/scheduler"
	"github.com/jhoot/orch/internal/tmux"
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
		downCmd(log),
		psCmd(log),
		sendCmd(log),
		logsCmd(log),
		scheduleCmd(log),
		dashCmd(log),
		resetCmd(log),
		schedulerCmd(log),
		watchCmd(log),
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
	var role, dir, specPath string
	var skipPermissions bool

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
			}); err != nil {
				return err
			}

			fmt.Printf("Agent %q started (role: %s, dir: %s)\n", name, role, dir)
			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "engineer", "Role for the agent")
	cmd.Flags().StringVar(&dir, "dir", "", "Working directory (defaults to current directory)")
	cmd.Flags().StringVar(&specPath, "spec", "", "Path to a spec file to send as the first message")
	cmd.Flags().BoolVar(&skipPermissions, "skip-permissions", true, "Pass --dangerously-skip-permissions to claude (default: true for autonomous agents)")

	return cmd
}

func downCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "down <name>",
		Short: "Tear down an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			mgr := agent.New(database, tc, log)

			if err := mgr.Down(name); err != nil {
				return err
			}

			fmt.Printf("Agent %q stopped\n", name)
			return nil
		},
	}
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
				fmt.Printf("%-15s %-12s %-10s %-40s %s\n",
					a.Agent.Name,
					a.Agent.Role,
					a.EffectiveStatus,
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
			msg := messenger.New(database, tc)

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
		Use:   "logs <name>",
		Short: "View message history for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			msgs, err := db.ListMessages(database, name, tail)
			if err != nil {
				return err
			}

			if len(msgs) == 0 {
				fmt.Printf("No messages for agent %q\n", name)
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

	cmd.Flags().IntVar(&tail, "tail", 0, "Limit to last N messages (0 = all)")

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
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			tc := tmux.New()
			msg := messenger.New(database, tc)
			sched := scheduler.New(database, msg, log)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			fmt.Println("Scheduler running. Ctrl-C to stop.")
			fmt.Println("  Schedule poll: every 30s")
			fmt.Println("  File poll:     every 10s")
			sched.Run(ctx, 30*time.Second, 10*time.Second)
			fmt.Println("Scheduler stopped.")
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return "..." + s[len(s)-maxLen+3:]
}
