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

## Keybindings

| Key | Action |
|---|---|
| `↑`/`k` | Move up |
| `↓`/`j` | Move down |
| `g`/`Home` | Go to top |
| `G`/`End` | Go to bottom |
| `PgUp`/`PgDn` | Page up/down |
| `Enter` | Resume selected session |
| `/` | Search (use `@repo` to filter by project) |
| `Tab` | Toggle full-text search (in search mode) |
| `!` | Toggle `--dangerously-skip-permissions` |
| `Esc` | Clear search / close help |
| `?` | Toggle help |
| `q` | Quit |

## Search

Type `/` to open search, then:

- **Quick search** (default) — matches against project name, summary, and git branch
- **Full-text search** (press `Tab` to toggle) — also searches all user message history
- **`@repo`** — prefix with `@` to filter by project name, e.g. `@prod` or `@producthunt some query`

## Platforms

- macOS (Apple Silicon & Intel)
- Linux (amd64 & arm64)
