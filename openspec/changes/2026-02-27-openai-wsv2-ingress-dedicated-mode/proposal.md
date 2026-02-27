## Why

你当前的核心诉求已经明确为 4 点：

1. WS mode 需要 3 种模式：`off/shared/dedicated`。
2. 协议必须对称：只允许 `ws->ws`、`http->http`，禁止 `ws->http`、`http->ws`。
3. 新增实现不能破坏原有实现（默认保持旧行为，按开关启用新逻辑）。
4. 账号“并发数”字段就是该账号连接池最大连接数。

现有提案仍偏向“布尔 dedicated 开关”，且对“旧实现隔离”与“并发数即池上限”的约束不够硬，需要补齐。

## What Changes

本提案升级为“**增量并行实现**”：新增 v2 路径，不替换旧路径。

### 1. 新增总开关，确保不改旧实现

新增：`gateway.openai_ws.mode_router_v2_enabled`（默认 `false`）。

- `false`：100% 走原实现（legacy 路径），行为不变。
- `true`：仅指定请求进入新模式路由（tri-mode + symmetry + dedicated 强化）。

### 2. WS mode 三态

新增统一模式：`off|shared|dedicated`。

- 全局默认：`gateway.openai_ws.ingress_mode_default`（默认 `shared`）
- 账号级（按类型）：
  - `accounts.extra.openai_oauth_responses_websockets_v2_mode`
  - `accounts.extra.openai_apikey_responses_websockets_v2_mode`

### 3. 协议对称硬约束（在 v2 路径生效）

- WS 入站仅允许 `ws->ws`
- HTTP 入站仅允许 `http->http`
- 明确拒绝 `ws->http`、`http->ws`

### 4. dedicated + store=false 三层稳定策略

- Layer 1：发送前治理 `previous_response_id`
- Layer 2：连接硬亲和（同会话固定同连接）
- Layer 3：连接中断时 input replay 单次恢复

### 5. 账号并发数即连接池上限（v2 路径）

在 v2 路径中，账号连接池上限由账号并发数直接决定：

- `max_conns_for_account = account.concurrency`
- `dedicated` 模式下可同时承载的会话上限也受该值约束

说明：legacy 路径维持现有全局池参数语义，不被本设计改变。

## Non-Goals

- 不改 HTTP/WSv1 业务协议。
- 不移除或重写 legacy 逻辑。
- 不在本提案实现跨实例会话绑定共享。

## Capabilities

### Modified Capabilities

- `openai-ws-v2-performance`

## Impact

- 影响模块（预期）：
  - `backend/internal/config/config.go`
  - `backend/internal/config/config_test.go`
  - `backend/internal/service/account.go`
  - `backend/internal/service/openai_ws_protocol_resolver.go`
  - `backend/internal/service/openai_ws_forwarder.go`
  - `backend/internal/service/openai_ws_pool.go`
  - `backend/internal/service/openai_ws_forwarder_ingress_session_test.go`
  - `frontend/src/components/account/CreateAccountModal.vue`
  - `frontend/src/components/account/EditAccountModal.vue`
  - `frontend/src/i18n/locales/zh.ts`
  - `frontend/src/i18n/locales/en.ts`

- 兼容性：默认 `mode_router_v2_enabled=false`，原行为不变。
- 风险级别：中（开启 `dedicated` 后资源消耗上升）。
