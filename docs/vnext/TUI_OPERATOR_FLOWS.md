# TUI Operator Flows (vNext)

## Objectives

The TUI must make orchestration obvious and fast:
- See who owns main env right now.
- See who is waiting.
- Manually switch a selected agent into main env for demos.

## Layout (proposed)

## Top status banner
`MAIN ENV: FREE` or `MAIN ENV: <agent-name> (mode: normal|demo, ttl: 09:31)`

## Left panel: Agents
Each row:
- name
- status
- current task
- badge(s): `DEMO`, `WAITING`, `ERROR`

## Right panel: Main env queue
Each row:
- request id
- agent
- type (`specs` / `dev_server`)
- age
- reason (`normal` / `manual-demo-switch`)

## Bottom panel: Event log
Recent coordinator events and failures.

---

## Keybindings (additions)

- `M` — **Promote selected agent to Main Env (Demo Mode)**
- `R` — **Return selected agent to workspace** (release lease)
- `E` — Extend current demo lease
- `F` — Force release main env (admin safety action)
- `C` — Cancel selected queue request

Keep existing navigation/search bindings.

---

## Manual Demo Switch Flow

## Happy path
1. Operator selects agent.
2. Press `M`.
3. Confirm dialog: `Move <agent> to main environment?`
4. If main env free, grant immediately with `mode=demo`.
5. Banner updates, agent row gets `DEMO` badge.

## If main env is currently occupied
Dialog options:
- `Queue as next` (default safe option)
- `Preempt current holder` (requires second confirmation)

### Preempt flow
1. Coordinator sends graceful stop/release signal to current holder.
2. Wait up to configurable handoff timeout (e.g., 10s).
3. Force release if no response.
4. Grant demo lease to selected agent.
5. Emit audit event: `mainenv.preempted`.

---

## Guardrails

- Demo leases are timeboxed (default 15m).
- Auto-expire + reclaim if no heartbeat.
- All manual switch/preempt operations logged in event stream.
- Destructive actions (`F`) require explicit confirmation.

---

## Visual states

### Agent states
- `idle`
- `working`
- `waiting_mainenv`
- `holding_mainenv`
- `error`
- `offline`

### Queue states
- `queued`
- `granted`
- `running`
- `completed`
- `failed`
- `cancelled`

---

## Demo UX acceptance criteria

1. Operator can move an agent to main env in <= 2 interactions.
2. Operator can identify current holder in <= 1 second from glance.
3. If occupied, operator gets clear options (queue/preempt) with no ambiguity.
4. Every manual switch leaves an auditable event.
