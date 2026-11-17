# nanobus

只运消息：NanoSec 平台的队列、事件流与隧道回退。

它是八个一级服务之一。每个渠道、每个智能体容器、每一条要等回答的控制台读请求，
都从这里过。

## 它是什么

- **按用户分的队列。** 入站与出站各一条，用一次原子写入领取，同一条消息不会交给两个人。
- **一条事件流。** 流式分片写进集合再从集合扇出，所以副本重启不会丢掉一段回答的尾巴。
- **隧道回退。** 隧道在 provisioner 终止；总线保留旧端点，给镜像还没更新的容器用。
- **集群账本。** runner 注册表、心跳清理、微信客服的游标。
- **不碰业务数据。** 配置、目录、智能体、会话、审计都归各自的模块，总线走内部 HTTP 去问。

## 它在哪

| | |
|---|---|
| 集合 | 只有 `bus_*` 与 `sys_*` |
| 端口 | 网络内 `9000`，宿主机 `14400`（绑 loopback） |
| 与谁对话 | 网关、智能体容器、控制台、供给服务，以及四个模块主人 |
| 只写 | `bus_inbound_queue`、`bus_outbound_queue`、`bus_stream_events`、`bus_platform_events`、`bus_voice_sessions` |
| 指标 | `GET /metrics` 与 `GET /healthz`，内网匿名 |

## 快速开始

```bash
go build ./cmd/nanobus
NANOSEC_BUS_DATABASE_URI="mongodb://localhost:27017/nanosec" \
NANOSEC_BUS_INTERNAL_TOKEN="$(openssl rand -hex 24)" \
  ./nanobus
```

先应用清单 —— 三组集合与它们的 TTL 都声明在清单里，不在代码里：

```bash
nanobus migrate up
nanobus migrate status
```

整套栈在超仓的 `deploy/` 里：`docker compose up -d` 会把总线与它要调的模块一起起来。

## 配置

| 变量 | 含义 |
|---|---|
| `NANOSEC_BUS_DATABASE_URI` | 那唯一一个库；总线在里面写自己的前缀 |
| `NANOSEC_BUS_INTERNAL_TOKEN` | 全平台共享密钥，给 `/api/v1/internal/*` |
| `NANOSEC_BUS_ADMIN_BASE_URL` | 目录数据去哪儿问 |
| `NANOSEC_BUS_PORTAL_BASE_URL` | 智能体行与分组在哪儿 |
| `NANOSEC_BUS_PROVISIONER_BASE_URL` | 容器请求往哪儿入队 |
| `NANOSEC_BUS_NANOAUTH_BASE_URL` | 开户与令牌校验用的身份服务 |

运维参数（端口、限流、时序）放在 `admin_config` 里，在控制台编辑。
`server.rateLimitPerMinute` 是**全平台一个预算**：每个副本分到自己那一份，
五个副本 600 就是每人 120。

## HTTP 面

| 接口 | 做什么 |
|---|---|
| `POST /api/v1/inbound`、`POST /api/v1/inbound/poll` | 投递用户消息、领取一条 |
| `POST /api/v1/outbound`、`…/chunk`、`…/complete` | 写一条回复、一个分片、一轮结束 |
| `GET /api/v1/outbound/stream`、`GET /api/v1/events/stream` | SSE：单个会话、平台事件 |
| `POST /api/v1/rpc` | 协议消息分发，所有服务都走它 |
| `GET /ws/agent`、`GET /ws/terminal` | 智能体 socket 与终端桥 |
| `GET /ws/hypervisor`、`POST /api/v1/internal/tunnels/{name}/request` | 隧道回退与转发 |
| `POST /api/v1/runners/register`、`GET /api/v1/runners` | runner 注册表 |

## 设计决策

- **领取只有一次写。** 对 `status` 做 `findOneAndUpdate`，同一毫秒轮询的两个智能体
  不可能都拿到同一条消息。
- **分片进集合，不进环形缓冲。** `bus_stream_events` 以渠道 + 会话为键，600 秒过期，
  每个会话修剪到 1000 条；订阅按 `ObjectId` 回放，这个 id 跨进程可比。
- **`complete` 先落盘。** 分片先写完再确认结束，消费方不会看到「回答结束了但最后一段没来」。
- **清理从不删除。** runner 静默两分钟只标 `offline`；真要清理由运维显式
  `DELETE /api/v1/runners/{id}`。

## 文档

- [`docs/ARCHITECTURE.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/docs/ARCHITECTURE.md) —— 它所在的整个平台
- [`docs/DEPLOYMENT.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/docs/DEPLOYMENT.md) —— 端口、环境、运维
- [`docs/README.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/docs/README.md) —— 全部文档索引

## 状态

随 `v0.1.0` 发布；本模块的版本钉在超仓的 `versions.lock` 里。

## 贡献与安全

开发环境与提交规则见
[`CONTRIBUTING.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/CONTRIBUTING.md)。
漏洞请按
[`SECURITY.md`](https://gitee.com/kobeyoung81/nanosec/blob/master/SECURITY.md)
私下报告 —— 不要开公开 issue。

## 许可证

[AGPL-3.0](./LICENSE)。名称与标志另有说明 —— 见 [`NOTICE`](./NOTICE)。
