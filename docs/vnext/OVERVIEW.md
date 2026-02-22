# claude-manager vNext — Multi-Agent Orchestration

## Summary

vNext evolves `claude-manager` from a session browser into a local multi-agent orchestrator:

- Multiple Claude Code agents run in parallel in isolated workspaces/worktrees.
- A coordinator controls access to one shared **main environment**.
- Sub-agents can request main-environment access when needed.
- The main environment is reserved for:
  1. running feature specs
  2. running the local dev server

A key operator flow is manual demo control from the TUI: quickly move a specific agent into the main environment.

## Why this change

Today, parallel work is possible but coordination is manual and collision-prone. vNext adds explicit scheduling and lock ownership so:

- Sub-agents stay productive in parallel.
- Main environment actions are serialized.
- Demos are easy and safe.

## Product goals (MVP)

1. Run 3+ agents concurrently in separate workspaces.
2. Enforce single-holder access to main environment.
3. Support main-environment request queue + lease-based lock.
4. Expose clear coordinator + queue + lock state in TUI.
5. Provide one-key operator flow to switch an agent to main environment for demos.

## Non-goals (MVP)

- Distributed orchestration across multiple machines.
- Auto-decomposition of tasks into sub-agent plans.
- Generic resource scheduler beyond main env.

## Success criteria

- No simultaneous feature-spec or dev-server operation in main env.
- If lock holder dies, coordinator reclaims lock automatically.
- Coordinator restart preserves queue + ownership state.
- Operator can promote an agent to main env within 2 key actions in TUI.

## Deliverables in this docs set

- `ARCHITECTURE.md` — system design, contracts, state model.
- `TUI_OPERATOR_FLOWS.md` — UX and keybindings, including demo switch.
- `ROADMAP.md` — milestones, phased implementation, acceptance criteria.
