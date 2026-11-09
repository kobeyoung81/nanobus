# nanobus

WebSocket message broker with MongoDB storage for MyriadMind.

## Overview

`nanobus` is the central message routing service. It provides:
- **User registry** — onboard and manage users with per-channel identity linking
- **Message queues** — per-user inbound/outbound queues with atomic claiming
- **WebSocket server** — single endpoint handling all protocol message types
- **HTTP REST + SSE surface** — `/api/v1/*` RPC endpoints plus `/auth/v1/*` proxied to nanoauth
- **Service configuration** — key-value config store for all cluster services
- **Runner registry** — track Docker / Swarm / Kubernetes provisioning instances
- **WeChat KF sync state** — cursor and token persistence for chat platform sync
- **Agent registry & sessions** — per-user agents and their `portal_sessions` projection
- **Enterprise data** — audit log, alert rules

## Architecture

```
Gateway --WebSocket/HTTP--> nanobus (server)
Agent   --WebSocket/HTTP-->   |-- handler.go (protocol type dispatch)
Runner  --WebSocket/HTTP-->   |-- handler_rest.go (REST /api/v1/rpc)
Portal  --HTTP---------->     |-- proxy_auth.go  (/auth/v1/* -> nanoauth)
                              |-- stream/        (SSE chunk buffer, events)
                              |-- store/
                              |   |-- user.go      (user registry, auto-onboarding)
                              |   |-- queue.go     (inbound/outbound queues, FOR UPDATE)
                              |   |-- config.go    (admin_config CRUD)
                              |   |-- agent.go     (agent registry)
                              |   |-- session.go   (portal_sessions)
                              |   |-- audit.go     (audit_log)
                              |   |-- runner.go    (runner registration + heartbeat)
                              |   |-- wechatkf_state.go (sync cursor + callback token)
                              |   |-- migrate_mongo.go (declarative index manifests)
                              |
                            MongoDB
```

## Data model

MongoDB is the only store. The collections and their indexes are declared by the
manifests in `migrations-mongo/`, embedded with `go:embed` and applied in filename
order by `nanobus migrate up`; the applied names are recorded in the
`sys_migrations` collection, one document per service (bus and nanoartifacts share it).

| Manifest | Collections |
|----------|-------------|
| 001 | `admin_config` |
| 002 | `portal_agents` |
| 003 | `admin_audit_log`, `admin_alert_rules` |
| 004 | `provisioner_runners` |
| 005 | `admin_agent_types` |
| 006 | `bus_inbound_queue`, `bus_outbound_queue` |
| 007 | `auth_users`, `provisioner_container_requests` |
| 008 | `admin_models`, `admin_model_providers` |
| 009 | `admin_agent_type_overrides` |
| 010 | `admin_mcp_servers`, `admin_skills`, `auth_secrets`, `admin_user_skill_acks` |
| 011 | `portal_sessions` |
| 012 | `admin_prompts` |
| 013 | `auth_oauth_providers`, `auth_git_shortcuts`, `auth_refresh_tokens` |
| 014 | `portal_agent_groups` |

A manifest is declarative — it names the indexes a collection must have (and
optionally a validator). Creating an index that already exists is a no-op, so a
partially applied manifest is harmless.

The document shapes live next to the code that reads them (`storedUser`,
`storedAgentSession`, … in `internal/store/*.go`); this file does not duplicate them.

## Key Design Decisions

### Atomic Message Claiming
`ClaimInbound` and `ClaimOutbound` each claim with a single atomic
`findOneAndUpdate`. A message claimed by one agent/gateway cannot be handed to
another.

### Auto-Onboarding
When `ResolveUser`/`ResolveUserBinding` encounters an unknown platform identity, it automatically:
1. Creates the user with the channel identity embedded in it, in one `insertOne`
   with status `pending` and tier `free`
2. Enqueues a container request (`status=pending`)
3. Returns the new user with `is_new=true`

The second write is deliberately not in a transaction with the first: a failure
there leaves a working binding whose container has not been asked for, which the
provisioning path recovers from.

### Migrations
MongoDB manifests are embedded via `go:embed` and applied in filename order; the
applied names are recorded in the `sys_migrations` collection, scoped by the `service` field. `migrate up`
also fills the denormalized `portal_sessions.title`.

### Runner registry & stale runners
The bus owns the `provisioner_runners` collection. A runner registers with a stable `id` (pass `--runner-id`
or set the `runner.runnerID` config item) so restarts update the same document instead of
inserting a new one. The bus:
- touches `last_heartbeat` on every container poll (WebSocket and REST paths),
- runs a **sweeper** (every 60s) that marks `status='offline'` where
  `last_heartbeat` is older than 2 minutes — it never deletes documents,
- exposes `DELETE /api/v1/runners/{id}` so operators can prune dead runners by hand.

### Gateway web channel RPC
The gateway web channel authenticates browsers and lists agents through two RPC
message types the bus implements: `validate_token` (delegates to nanoauth
`/auth/v1/userinfo`) and `list_agents` (reads the agent registry). The bus REST
surface also carries agent CRUD (`POST/PUT/DELETE /api/v1/agents`).

## CLI Reference

```bash
nanobus                                     # Start server (WS + REST + SSE + auth proxy)
nanobus --config ~/.nanosec/bus.json        # Start with a config file
nanobus version                             # Print version info
nanobus user add <user-id>                  # Register a user
nanobus user list                           # List all users
nanobus user link <user-id> <channel> <sender-id>  # Link channel identity
nanoadmin config seed                       # Seed the platform defaults (admin module)
nanobus config get <component-type> [key]   # Get config item(s)
nanobus config set <component-type> <key> <value>  # Set a config item
nanobus migrate up                          # Apply pending migrations
nanobus migrate status                      # Show migration status
```

## Configuration

Loaded from file or environment variables (prefix: `NANOSEC_BUS_`).

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 9000,
    "max_content_bytes": 16777215
  },
  "database": {
    "uri": "mongodb://nanobus:…@mongo:27017/nanobus?authSource=admin"
  },
  "nanoauth": {
    "baseURL": "http://nanoauth:8081"
  }
}
```

`nanoauth.baseURL` (env `NANOSEC_BUS_NANOAUTH_BASE_URL`) enables the `/auth/v1/*` reverse proxy to the nanoauth service.

`database.uri` (env `NANOSEC_BUS_DATABASE_URI`) must name the database the bus
owns; it is the only connection string the service takes.

`server.max_content_bytes` (env `NANOSEC_BUS_SERVER_MAX_CONTENT_BYTES`) bounds a
single inbound/outbound message body. Messages above it are rejected with HTTP
413 and a clear error rather than by the store. It defaults to 16,777,215
(16 MiB - 1).

## Deployment

### Docker
```bash
docker build -t nanobus .
docker run -p 9000:9000 -e NANOSEC_BUS_DATABASE_URI="mongodb://nanobus:…@mongo:27017/nanobus?authSource=admin" nanobus
```

### Docker Compose
Part of the top-level `docker-compose.yml`. The `bus-init` one-shot service applies
the collection manifests and seeds the built-in catalogs before the server starts.

### Standalone
```bash
go build ./cmd/nanobus
./nanobus --config ~/.nanosec/bus.json
```

## Packages

| Package | Purpose |
|---------|---------|
| `cmd/nanobus` | CLI entry point (cobra commands) |
| `internal/server` | WebSocket server, REST surface, auth proxy, message dispatch |
| `internal/stream` | SSE chunk buffer for streaming outbound |
| `internal/store` | MongoDB data access layer |
| `internal/config` | Configuration types and loading |
| `internal/log` | Custom slog log level |
| `migrations-mongo/` | Embedded index/validator manifests (one per domain) |
