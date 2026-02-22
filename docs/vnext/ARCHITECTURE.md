# vNext Architecture

## 1) System Components

## 1.1 Coordinator (new)
Single authority for orchestration state and main-environment access.

Responsibilities:
- Register/manage agents.
- Accept and queue main-env requests.
- Grant/release/reclaim main-env leases.
- Execute approved main-env actions via gateway.
- Persist state and event history.

## 1.2 Agent Runtimes
Each agent maps to one Claude Code process in one isolated workspace/worktree.

Responsibilities:
- Execute normal assigned tasks in its own workspace.
- Request main-env lease through coordinator when needed.
- Heartbeat while holding lease.

## 1.3 Main Environment Gateway
Narrow command surface for the shared environment.

Allowed action families (MVP):
- `run_feature_specs`
- `dev_server.{start|stop|restart|status|logs}`

No direct sub-agent shell access to main env in MVP.

## 1.4 TUI / CLI Surface
`claude-manager` remains the operator front-end:
- agent lifecycle controls
- queue/lease visibility
- manual demo switch controls

---

## 2) Core Concurrency Model

Main environment is protected by a single **lease lock**.

- Exactly one holder at a time.
- All requests pass through coordinator queue.
- Lease has TTL and heartbeat requirement.
- Stale lease reclaimed automatically.

### 2.1 Lease modes
- `normal`: acquired via queued request.
- `demo`: operator-forced switch for live demo.

### 2.2 Suggested defaults
- lease TTL (normal): 10m (renewable)
- lease TTL (demo): 15m (renewable)
- heartbeat interval: 5s
- stale threshold: 2 missed heartbeats

---

## 3) Persistence Model (SQLite)

## 3.1 Tables

### `agents`
- `id` (pk)
- `name`
- `workspace_path`
- `status` (`idle|working|waiting_mainenv|holding_mainenv|error|offline`)
- `session_id`
- `last_heartbeat_at`
- `created_at`
- `updated_at`

### `main_env_requests`
- `id` (pk)
- `agent_id`
- `request_type` (`run_feature_specs|dev_server`)
- `payload_json`
- `priority` (`normal|operator`)
- `status` (`queued|granted|running|completed|failed|cancelled`)
- `queued_at`
- `started_at`
- `finished_at`
- `result_json`

### `main_env_lease`
(single row)
- `holder_agent_id` (nullable)
- `mode` (`normal|demo`)
- `lease_token`
- `acquired_at`
- `expires_at`
- `last_heartbeat_at`

### `events`
- `id` (pk)
- `kind`
- `actor` (agent/operator/system)
- `entity_id`
- `payload_json`
- `created_at`

---

## 4) State Machines

## 4.1 Agent state machine
`idle -> working -> waiting_mainenv -> holding_mainenv -> working -> idle`

Error paths:
- any state -> `error`
- process death -> `offline`

## 4.2 Request state machine
`queued -> granted -> running -> completed|failed|cancelled`

## 4.3 Lease state machine
`free -> held(normal|demo) -> expired/released -> free`

---

## 5) Coordination API (internal contract)

## 5.1 Agent operations
- `AgentRegister(name, workspace)`
- `AgentStart(id)`
- `AgentStop(id)`
- `AgentHeartbeat(id)`
- `AgentStatus(id)`

## 5.2 Main-env request operations
- `RequestMainEnv(agent_id, type, payload, priority=normal)`
- `CancelRequest(request_id)`
- `GetQueue()`

## 5.3 Lease operations
- `AcquireIfHead(request_id)` (coordinator internal)
- `RenewLease(agent_id, lease_token)`
- `ReleaseLease(agent_id, lease_token)`
- `ForceRelease(actor=operator)`

## 5.4 Operator actions
- `PromoteAgentToMainEnv(agent_id, preempt=false)`
- `ReturnAgentToWorkspace(agent_id)`

---

## 6) Failure Handling

## 6.1 Coordinator crash/restart
- Rehydrate from SQLite.
- Re-evaluate lease expiry on boot.
- Resume queued requests.

## 6.2 Agent crash while holding lease
- Heartbeat timeout triggers lease reclaim.
- Running request marked failed with reason `holder_unreachable`.

## 6.3 Main env command failure
- Request -> `failed`, with stderr/exit code stored.
- Lease released unless explicit retry policy enabled.

---

## 7) Security and safety boundaries

- Main env actions are allowlisted to spec/dev-server operations.
- No arbitrary shell command pass-through in MVP.
- All preemptions and force-release events are audited.

---

## 8) Extension points (post-MVP)

- Priority scheduling + fairness windows.
- Multiple shared resources (e.g., staging db, device farm).
- Policy engine for auto-approval/rejection of requests.
