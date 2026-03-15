# orch

A CLI orchestrator for coordinating multiple Claude Code instances via tmux.

Spin up named agents with roles, let them communicate with each other, schedule follow-up tasks, and monitor everything from a live dashboard. One binary, no dependencies beyond tmux and `claude`.

## Install

```bash
go install github.com/jeffdhooton/orch/cmd/orch@latest
```

Or build from source:

```bash
git clone https://github.com/jeffdhooton/orch.git
cd orch
go build -o orch ./cmd/orch
```

Requires: Go 1.24+, tmux, [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code)

## Quick start

```bash
# Initialize the database
orch init

# Start an agent
orch up backend --role engineer --dir ~/projects/myapp

# Start a second agent that knows about the first
orch up reviewer --role reviewer --dir ~/projects/myapp

# Send it a task
orch send backend "Implement the user authentication endpoint"

# Open the live dashboard
orch dash
```

## Commands

| Command | Description |
|---------|-------------|
| `orch init` | Initialize `~/.orch/` and the database |
| `orch up <name>` | Spin up a named agent |
| `orch down <name>` | Tear down an agent |
| `orch ps` | List all agents with live status |
| `orch send <name> <msg>` | Send a message to an agent |
| `orch logs <name>` | View message history |
| `orch schedule <name> <min> <note>` | Schedule a future message |
| `orch dash` | Live terminal dashboard |
| `orch scheduler` | Run the scheduler as a foreground process |
| `orch watch` | Auto-restart dead agents |
| `orch reset` | Nuke everything and start fresh |

### `orch up`

```
orch up <name> --role <role> --dir <path> [--spec <path>] [--skip-permissions=false]
```

- Creates a tmux window in the `orch` session
- Registers the agent in SQLite
- Pre-trusts the directory in `~/.claude.json` (skips the folder trust prompt)
- Injects agent identity and team awareness via `--append-system-prompt`
- Starts `claude --dangerously-skip-permissions` (override with `--skip-permissions=false`)
- If `--spec` is given, sends the file contents as the first message

### `orch dash`

Interactive TUI dashboard showing all agents, their status, and a live preview of the selected agent's terminal output.

| Key | Action |
|-----|--------|
| j/k or arrows | Navigate agents |
| Enter | Attach to agent's tmux window (Ctrl-B d to return) |
| x | Kill selected agent |
| r | Force refresh |
| q | Quit |

The dashboard runs the scheduler in the background, so scheduled messages and inter-agent file communication are processed automatically while it's open.

### `orch scheduler`

Runs the scheduler as a standalone foreground process. Use this when you want scheduled messages and inter-agent communication to work without the dashboard open.

```bash
orch scheduler  # Ctrl-C to stop
```

### `orch watch`

Monitors agents and automatically restarts any that have died (tmux window gone but DB still says running). Essential for 24/7 autonomous operation.

```bash
orch watch --interval 30  # check every 30 seconds
```

## Inter-agent communication

Agents communicate through files. The scheduler watches each agent's working directory for:

**`.orch-send-<agent-name>`** -- Send a message to another agent. Create a file named `.orch-send-reviewer` with the message content. The orchestrator delivers it via tmux and deletes the file.

**`.orch-schedule`** -- Schedule a follow-up. Write `<minutes> <note>` to this file. The orchestrator will send you the note after the specified delay.

Each agent's system prompt includes instructions for these conventions, plus a list of currently running teammates.

## Running 24/7

For fully autonomous, unattended operation:

### 1. Start your agents

```bash
orch up planner --role pm --dir ~/project --spec specs/plan.md
orch up builder --role engineer --dir ~/project --spec specs/task.md
orch up reviewer --role reviewer --dir ~/project
```

### 2. Run the scheduler and watcher

In separate terminals (or use tmux):

```bash
# Process scheduled messages and inter-agent files
orch scheduler &

# Auto-restart dead agents
orch watch &
```

Or run them together:

```bash
orch scheduler & orch watch & wait
```

### 3. Monitor with the dashboard

```bash
orch dash
```

### 4. Keep it alive across reboots

The agents run inside a tmux session called `orch`. As long as tmux survives (i.e., the machine stays on), the agents persist. For true 24/7:

- Run on a server or always-on machine
- Use `tmux` to keep the session alive across SSH disconnects
- Run `orch scheduler` and `orch watch` under a process manager (systemd, launchd, etc.)

### Tips for autonomous operation

- **Write detailed specs.** The better your `--spec` file, the more effectively agents work unsupervised.
- **Use the PM/engineer pattern.** A PM agent that checks in on engineers and re-prioritizes work creates a self-correcting loop.
- **Schedule check-ins.** Agents can schedule their own follow-ups: "in 30 minutes, check if the tests pass."
- **One task per agent.** Focused agents with narrow roles produce better results than one agent doing everything.
- **Commit often.** Include commit discipline in your spec files (e.g., "commit every 30 minutes with descriptive messages").

## Architecture

```
orch CLI
  |
  +-- agent.Manager     -- lifecycle: up, down, list
  +-- messenger          -- record + deliver messages via tmux
  +-- scheduler          -- poll for due schedules + agent files
  +-- dashboard          -- bubbletea TUI
  |
  +-- tmux.Client        -- thin wrapper over tmux commands
  +-- db (SQLite)        -- agents, messages, schedule tables
```

All agents run as windows inside a single tmux session named `orch`. Communication flows through SQLite (persistence) and tmux send-keys (delivery). No sockets, no daemons, no network.

## Data

Everything lives in `~/.orch/`:

- `orch.db` -- SQLite database (agents, messages, schedules)

The database is created automatically on first use. `orch reset` wipes it clean.

## License

MIT
