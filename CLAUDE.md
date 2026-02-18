# claude-manager

Interactive TUI for browsing and resuming Claude Code sessions, built with Go and Bubble Tea.

## Architecture

```
main.go                        — CLI entrypoint, session resume via syscall.Exec
internal/
  sessions/
    session.go                 — Session struct and TimeAgo helper
    parser.go                  — JSONL parser, LoadAll discovers ~/.claude/projects/
  tui/
    app.go                     — Bubble Tea Model, Update, View, key handlers
    list.go                    — Session list rendering, filtering, truncation
    detail.go                  — Detail panel rendering
    styles.go                  — All lipgloss styles and color definitions
  worktree/
    worktree.go                — Worktree discovery (Discover) and removal (Remove)
```

## Conventions

- **Bubble Tea pattern**: All I/O (git commands, filesystem) happens in `tea.Cmd` functions, never in `Update` or `View`
- **Screen modes**: Boolean flags (`showHelp`, `showWorktrees`, `searching`) checked in order in `View()` and `Update()` for dispatching
- **Key handling**: Separate `handle*Key` methods per screen mode
- **Styles**: All lipgloss styles live in `styles.go`, reused across renderers
- **Module path**: `claude-manager` (no github prefix)

## Build & Install

```sh
go build .          # build
go install .        # install to $GOPATH/bin
```

## Releases

Cross-compile and publish via `gh`:

```sh
mkdir -p dist
GOOS=darwin GOARCH=arm64 go build -o dist/claude-manager-darwin-arm64 .
GOOS=darwin GOARCH=amd64 go build -o dist/claude-manager-darwin-amd64 .
GOOS=linux  GOARCH=amd64 go build -o dist/claude-manager-linux-amd64 .
GOOS=linux  GOARCH=arm64 go build -o dist/claude-manager-linux-arm64 .
gh release create v<VERSION> dist/* --title "v<VERSION>" --notes "..."
```

## Guidelines

- Keep the TUI responsive — never block in Update; use Cmds for shell/filesystem work
- Prefer editing existing files over creating new ones
- No tests currently; verify with `go build` and manual testing
