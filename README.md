# claude-manager

Interactive TUI to browse and resume [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions.

Claude Code stores session data as JSONL files in `~/.claude/projects/`. This tool parses those files and presents a searchable, filterable interface to quickly find and resume any session.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/lalexgap/claude-manager/master/install.sh | sh
```

Or with Go:

```sh
go install github.com/lalexgap/claude-manager@latest
```

## Usage

```sh
# Launch interactive TUI
claude-manager

# List all sessions as a table
claude-manager list

# Resume a specific session directly
claude-manager resume <session-id>
```

## Experimental Orchestrator Commands

```sh
# Initialize orchestrator SQLite state
claude-manager orchestrator init

# Show orchestrator status (initialized + counts + db path)
claude-manager orchestrator status

# Register an agent
claude-manager agent add <name> <workspace>

# List registered agents
claude-manager agent list

# Start an agent runtime process in its workspace
# default command: claude
claude-manager agent start <name> [command ...]

# Stop an agent runtime process (SIGTERM by default, SIGKILL with --force)
claude-manager agent stop <name> [--force]

# Show agent status and PID liveness (when session_id is pid:<n>)
claude-manager agent status <name>

# Agent heartbeat (updates agent + lease heartbeat timestamps)
claude-manager agent heartbeat <name>

# Keep heartbeats flowing on an interval (default every 15s)
claude-manager agent heartbeat-loop <name> [interval]

# Show main environment lease + queue depth
claude-manager mainenv status

# Queue a main environment request for an agent
# request-type: run_feature_specs | dev_server
claude-manager mainenv request <agent-name> <request-type> [payload-json]

# Show queued + running main environment requests
claude-manager mainenv queue

# Grant oldest queued request (defaults: mode=normal, ttl=10m)
claude-manager mainenv grant-next [mode] [ttl]

# Grant + execute oldest queued request via gateway commands
# (auto-renews lease while command is running)
claude-manager mainenv run-next [mode] [ttl]

# Extend the active lease (default ttl=10m)
claude-manager mainenv renew [ttl]

# Release current lease
claude-manager mainenv release [success|failed] [result-json]

# Force-reclaim an expired lease
claude-manager mainenv reclaim-stale
```

`mainenv run-next` reads gateway config from:

`~/.claude/claude-manager/mainenv.json`

Example:

```json
{
  "workdir": "~/code/your-main-repo",
  "default_timeout_seconds": 1200,
  "commands": {
    "run_feature_specs": ["pnpm", "test:features"],
    "dev_server": {
      "start": ["pnpm", "dev"],
      "stop": ["pkill", "-f", "vite"],
      "restart": ["sh", "-lc", "pkill -f vite || true; pnpm dev"],
      "status": ["sh", "-lc", "lsof -i :3000"],
      "logs": ["sh", "-lc", "tail -n 200 ./dev.log"]
    }
  }
}
```

For `dev_server` requests, pass payload json like:
`{"action":"start"}` (`start|stop|restart|status|logs`).

`grant-next` / `run-next` automatically reclaim expired stale leases before granting.

Agent runtime logs are written to:

`~/.claude/claude-manager/agents/<agent-name>.log`

## Keybindings

| Key | Action |
|---|---|
| `↑`/`k` | Move up |
| `↓`/`j` | Move down |
| `g`/`Home` | Go to top |
| `G`/`End` | Go to bottom |
| `PgUp`/`PgDn` | Page up/down |
| `Enter` | Resume selected session |
| `n` | Start a new session (choose project) |
| `o` | Open orchestration mode |
| `w`/`t` | Show worktree mode info |
| `/` | Search (use `@repo` to filter by project) |
| `Tab` | Toggle full-text search (in search mode) |
| `!` | Toggle `--dangerously-skip-permissions` |
| `Esc` | Clear search / close help |
| `?` | Toggle help |
| `q` | Quit |

### Orchestration Mode Keybindings (`o`)

| Key | Action |
|---|---|
| `Esc` | Return to normal session view |
| `q` | Quit app |
| `r` | Refresh orchestrator data |
| `↑`/`k`, `↓`/`j` | Move selected agent |
| `s` | Start selected agent (`claude`) |
| `S` | Start all non-running agents (`claude`) |
| `x` | Stop selected agent (SIGTERM) |
| `X` | Force stop selected agent |
| `h` | Heartbeat selected agent |
| `f` | Queue `run_feature_specs` for selected agent |
| `d` | Queue `dev_server` status for selected agent |
| `D` | Queue `dev_server` start for selected agent |
| `n` | `grant-next` (`mode=normal`, `ttl=10m`) |
| `R` | `run-next` (`mode=normal`, `ttl=10m`) via gateway config |
| `u` | Renew active lease (`10m`) |
| `l` | Release lease as success |

The orchestration screen shows:
- Main env lease summary: active, holder, mode, expires, queue depth
- Agent list: name, status, session ID, workspace (truncated as needed)
- Main env queue: id, agent, type, status, queued_at
- Last action/status message line

## Worktree mode

claude-manager calls Claude Code with `--worktree` when starting new sessions.

Resuming a session reopens that exact session ID (no new worktree is created during resume).

claude-manager no longer creates git worktrees or session symlinks directly.

Since new sessions are worktree-backed, the session selector resumes those worktree sessions directly.

Use orchestrator mode (`o`) to coordinate access to the main environment.

## Search

Type `/` to open search, then:

- **Quick search** (default) — matches against project name, summary, and git branch
- **Full-text search** (press `Tab` to toggle) — also searches all user message history
- **`@repo`** — prefix with `@` to filter by project name, e.g. `@prod` or `@producthunt some query`

## Platforms

- macOS (Apple Silicon & Intel)
- Linux (amd64 & arm64)

## vNext planning docs

- `docs/vnext/OVERVIEW.md`
- `docs/vnext/ARCHITECTURE.md`
- `docs/vnext/TUI_OPERATOR_FLOWS.md`
- `docs/vnext/ROADMAP.md`
