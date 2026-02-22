# vNext Roadmap

## Milestone 0 — RFC + Design Freeze (2-3 days)

Deliverables:
- finalize architecture docs
- finalize lock/lease semantics
- define internal API contracts

Exit criteria:
- open questions resolved
- implementation backlog approved

---

## Milestone 1 — Coordinator Foundation (4-6 days)

Build:
- coordinator process scaffold
- SQLite schema + migrations
- agent registration + heartbeat
- event logging

Exit criteria:
- coordinator persists/reloads state
- agent lifecycle visible in CLI

---

## Milestone 2 — Main Env Lock + Queue (4-6 days)

Build:
- request queue
- lease lock acquisition/release
- TTL + heartbeat + stale reclaim
- force-release operator action

Exit criteria:
- only one holder at a time
- stale lease reliably reclaimed

---

## Milestone 3 — Main Env Gateway (4-5 days)

Build allowlisted actions:
- `run_feature_specs`
- `dev_server start/stop/restart/status/logs`

Exit criteria:
- requests execute through coordinator only
- outputs captured and associated with request id

---

## Milestone 4 — Agent Runtime Integration (5-7 days)

Build:
- spawn/manage multiple Claude Code agent sessions
- workspace/worktree mapping per agent
- request-main-env flow from agents

Exit criteria:
- 3+ agents can run concurrently
- waiting agents queue correctly for main env

---

## Milestone 5 — TUI Orchestrator + Demo Controls (5-7 days)

Build:
- agent panel, queue panel, main-env banner
- keybindings: `M`, `R`, `E`, `F`, `C`
- manual demo switch flow (queue/preempt)

Exit criteria:
- operator can promote agent to main env quickly
- queue + holder state always visible

---

## Milestone 6 — Hardening + Beta (4-6 days)

Focus:
- crash recovery tests
- timeout/preemption race tests
- usability polish and docs updates

Exit criteria:
- restart-safe orchestration
- predictable behavior under failure
- beta-ready release notes

---

## Suggested issue breakdown

1. Add coordinator daemon package + command.
2. Add SQLite store and migration runner.
3. Add agent registry + heartbeat APIs.
4. Add main-env queue + lease manager.
5. Add main-env gateway command runners.
6. Add multi-agent process management.
7. Add TUI panels for orchestrator state.
8. Add manual demo switch UX.
9. Add audit/event log viewer.
10. Add failure-mode integration tests.

---

## Release readiness checklist

- [ ] Main env mutual exclusion guaranteed
- [ ] No direct sub-agent main-env shell access
- [ ] Lease expiry/reclaim validated
- [ ] Manual demo switch + preemption validated
- [ ] Operator docs updated
- [ ] Upgrade notes for existing users published
