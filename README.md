# nanobus

Messages, and nothing else: the queues, the event stream and the tunnel fallback
of the NanoSec platform.

It is one of the eight first-class services. Every channel, every agent container
and every console read that has to wait for an answer passes through it.

## What it is

- **Per-user queues.** Inbound and outbound, claimed with one atomic write so a
  message is never handed to two consumers.
- **One event stream.** Streamed chunks are written to a collection and fanned out
  from there, so a replica restart does not lose the tail of an answer.
- **Tunnel fallback.** Tunnels terminate at the provisioner; the bus keeps the old
  endpoint for containers whose image still dials it.
- **Fleet bookkeeping.** The runner registry, heartbeat sweeps and the WeChat
  customer-service cursor.
- **No domain data.** Configuration, the catalogue, agents, sessions and the audit
  trail belong to their modules; the bus asks them over internal HTTP.

## Where it sits

| | |
|---|---|
| Collections | `bus_*` and `sys_*` — nothing else |
| Port | `9000` in the network, `14400` on the host (loopback) |
| Talks to | gateway, agent containers, the console, the provisioner, and the four module owners |
| Owned by | `nanobus` is the only writer of `bus_inbound_queue`, `bus_outbound_queue`, `bus_stream_events`, `bus_platform_events`, `bus_voice_sessions` |
| Metrics | `GET /metrics` and `GET /healthz`, anonymous on the internal network |

## Quick start

```bash
go build ./cmd/nanobus
NANOSEC_BUS_DATABASE_URI="mongodb://localhost:27017/nanosec" \
NANOSEC_BUS_INTERNAL_TOKEN="$(openssl rand -hex 24)" \
  ./nanobus
```

Apply the manifests first — the three collection families and their TTLs are
declared there, not in code:

```bash
nanobus migrate up
nanobus migrate status
```

The rest of the stack is in `deploy/` of the super-repo:
`docker compose up -d` brings the bus up together with the modules it calls.

## Configuration

| Variable | Meaning |
|---|---|
| `NANOSEC_BUS_DATABASE_URI` | The one database; the bus writes its prefixes in it |
| `NANOSEC_BUS_INTERNAL_TOKEN` | Platform-wide shared secret for `/api/v1/internal/*` |
| `NANOSEC_BUS_ADMIN_BASE_URL` | Where the catalogue is asked |
| `NANOSEC_BUS_PORTAL_BASE_URL` | Where agent rows and their groups live |
| `NANOSEC_BUS_PROVISIONER_BASE_URL` | Where a container request is enqueued |
| `NANOSEC_BUS_NANOAUTH_BASE_URL` | Identity for onboarding and token validation |

Operational settings (ports, rate limits, timings) live in `admin_config` and are
edited in the console. `server.rateLimitPerMinute` is one platform-wide budget: each
replica is given its share, so five replicas at 600 means 120 each.

## HTTP surface

| Surface | What it does |
|---|---|
| `POST /api/v1/inbound`, `POST /api/v1/inbound/poll` | Queue a user message, claim one |
| `POST /api/v1/outbound`, `…/chunk`, `…/complete` | Write a reply, a streamed piece, the end of a turn |
| `GET /api/v1/outbound/stream`, `GET /api/v1/events/stream` | SSE: one conversation, and the platform events |
| `POST /api/v1/rpc` | The protocol message dispatch used by every service |
| `GET /ws/agent`, `GET /ws/terminal` | The agent socket and the terminal bridge |
| `GET /ws/hypervisor`, `POST /api/v1/internal/tunnels/{name}/request` | The tunnel fallback and the relay |
| `POST /api/v1/runners/register`, `GET /api/v1/runners` | The runner registry |

## Design decisions

- **Claiming is one write.** `findOneAndUpdate` on `status`, so two agents polling
  at the same millisecond cannot both take the message.
- **Chunks go to a collection, not a ring buffer.** `bus_stream_events` is keyed by
  channel and conversation, expires after 600 seconds, and is trimmed to 1,000
  events per conversation; a subscription replays by `ObjectId`, which is comparable
  across processes.
- **A `complete` flushes first.** The streamed pieces are written before the
  completion is acknowledged, so a consumer never sees an answer end before its last
  piece.
- **The sweeper never deletes.** A runner silent for two minutes is marked
  `offline`; pruning is a deliberate `DELETE /api/v1/runners/{id}`.

## Docs

- [`docs/ARCHITECTURE.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/docs/ARCHITECTURE.md) — the platform around this module
- [`docs/DEPLOYMENT.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/docs/DEPLOYMENT.md) — ports, environment, operations
- [`docs/README.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/docs/README.md) — every document

## Status

Released as part of `v0.1.0`; the version of this module is pinned in the
super-repo's `versions.lock`.

## Contributing and security

Development setup and the commit rules are in
[`CONTRIBUTING.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/CONTRIBUTING.md).
Report a vulnerability privately as described in
[`SECURITY.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/SECURITY.md) —
never in a public issue.

## License

[AGPL-3.0](./LICENSE). The name and logo are covered separately — see
[`NOTICE`](./NOTICE).
