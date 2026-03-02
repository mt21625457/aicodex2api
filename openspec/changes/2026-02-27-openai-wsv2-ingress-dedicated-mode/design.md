## Context

本设计采用“并行双路径”原则：

- legacy 路径：原实现，保持不变
- v2 路径：新增实现，由开关启用

v2 路径承载以下新能力：

1. WS mode 三态（`off/shared/dedicated`）
2. 协议对称（`ws->ws`、`http->http`）
3. dedicated 会话稳定性增强
4. 账号并发数即连接池上限

## Goals

1. 新增实现不破坏原行为（默认不开启）。
2. 明确三态 WS mode 的配置、优先级和兼容迁移。
3. 将协议对称变为可测试的硬约束。
4. 将账号并发数绑定为账号连接池上限。
5. 提供 dedicated 在高并发下的容量与拒绝语义。

## Non-Goals

1. 不替换 legacy 实现。
2. 不修改客户端协议格式。
3. 不引入跨实例共享会话状态。

## Configuration Design

### A. New Master Switch (Legacy Isolation)

新增：`gateway.openai_ws.mode_router_v2_enabled: bool`（默认 `false`）

- `false`：保持原实现完整运行。
- `true`：启用 v2 mode 路由能力。

### B. Tri-Mode Configuration

新增：`gateway.openai_ws.ingress_mode_default: string`（`off|shared|dedicated`，默认 `shared`）

账号新增（按类型）：

- `accounts.extra.openai_oauth_responses_websockets_v2_mode`
- `accounts.extra.openai_apikey_responses_websockets_v2_mode`

取值均为：`off|shared|dedicated`

### C. Backward Compatibility

在 v2 路径中，账号模式按如下顺序解析：

1. 新模式字段（`*_mode`）
2. 旧布尔字段：
   - `openai_oauth_responses_websockets_v2_enabled`
   - `openai_apikey_responses_websockets_v2_enabled`
   - `responses_websockets_v2_enabled`
   - `openai_ws_enabled`
3. 全局默认 `ingress_mode_default`

映射规则：`true => shared`，`false => off`。

## Protocol Symmetry (V2 Path)

当 `mode_router_v2_enabled=true` 时，执行硬约束：

1. 入站 WS：仅允许上游 WS（`ws->ws`）
2. 入站 HTTP：仅允许上游 HTTP（`http->http`）
3. 禁止跨协议：`ws->http`、`http->ws`

失败语义：

- WS 路径不 fallback 到 HTTP；直接返回可诊断 close 错误。
- HTTP 路径不 upgrade 到 WS；保持 HTTP 内部重试逻辑。

## Mode Resolution and Lifecycle

### Step 1: Router Branching

1. `mode_router_v2_enabled=false` => legacy
2. `mode_router_v2_enabled=true` => v2

### Step 2: Effective Mode (V2)

在 v2 路径中，`effectiveMode` 受以下门禁约束：

1. 全局门禁：`enabled/force_http/responses_websockets_v2`
2. 账号类型门禁：`oauth_enabled/apikey_enabled`
3. 账号模式解析（新字段优先，旧字段回退）

### Step 3: Per Mode Behavior

- `off`：拒绝 WS mode；HTTP 正常走 HTTP。
- `shared`：复用现有共享池策略。
- `dedicated`：每个客户端 WS 会话独占上游连接并强亲和。

## Account Concurrency = Pool Max (V2)

### Rule

v2 路径中，账号池上限由账号并发数决定：

- `max_conns_for_account = account.concurrency`

约束：

1. `account.concurrency <= 0` 视为不可调度（直接拒绝 WS）。
2. `dedicated` 下活跃会话数不得超过 `account.concurrency`。
3. `shared` 下连接总数也不得超过该上限。

说明：legacy 路径继续使用当前池参数计算逻辑，不受本规则影响。

## `store=false` Three-Layer Strategy (Dedicated)

### Layer 1

发送前治理 `previous_response_id`，减少 `previous_response_not_found` 无效往返。

### Layer 2

会话内强亲和，仅允许 `sessionConnID`。

### Layer 3

连接不可用时，剥离失效续链锚点并执行 input replay 单次恢复。

## State Model (V2 Dedicated)

会话态：

1. `effectiveMode`
2. `sessionConnID`
3. `lastTurnResponseID`
4. `lastTurnReplayInput`

会话结束后全部销毁；连接标记不可复用。

## High-Concurrency Model

定义：

- `U`: 活跃 WS 会话
- `A`: 账号数
- `Ci`: 第 i 个账号并发数
- `S = ΣCi`

dedicated 满足：`U <= S`。

示例：50 账号 * 20 并发 => `S=1000`。

若目标是 1000 同时在线会话，建议按 20% 冗余评估到 1200 级容量（否则高峰会出现拒绝）。

## Observability

新增指标（v2 打标）：

1. `openai_ws_mode_router_v2_requests_total{protocol_path,mode}`
2. `openai_ws_protocol_symmetry_reject_total{from,to}`
3. `openai_ws_ingress_sessions_active{mode}`
4. `openai_ws_ingress_acquire_fail_total{mode,reason}`
5. `openai_ws_ingress_replay_total{mode,result}`
6. `openai_ws_account_pool_limit_hits_total{account_id}`

关键日志字段：

1. `router_version=legacy|v2`
2. `ws_mode=off|shared|dedicated`
3. `protocol_path=ws->ws|http->http`
4. `account_concurrency`
5. `account_pool_max`

## Three-Round Proposal Review and Fixes

### Review Round 1

问题：早期方案会直接改现有路径，违反“新增不能改原实现”。

修复：引入 `mode_router_v2_enabled`，明确 legacy/v2 双路径并存，默认 legacy。

### Review Round 2

问题：协议对称规则缺少作用域，可能误伤 legacy 行为。

修复：将协议对称约束限定在 v2 路径；legacy 路径保持现状。

### Review Round 3

问题：连接池上限来源不明确（全局参数 vs 账号并发数）。

修复：在 v2 路径将 `account.concurrency` 定义为账号池硬上限，并补充拒绝语义与观测指标。

## Rollout Plan

1. 上线但不启用 v2（开关默认 `false`）。
2. 小流量启用 v2 + shared 观察。
3. 再灰度 dedicated 账号组。
4. 指标越界即回退为 legacy（仅关开关）。

## Test Strategy

### Unit

1. `mode_router_v2_enabled=false` 与当前基线一致。
2. 三态模式解析正确（新字段/旧字段/默认值）。
3. 协议对称拒绝 `ws->http`、`http->ws`。
4. v2 路径账号并发数池上限生效。

### Integration

1. v2 shared 与 dedicated 行为验证。
2. dedicated 连接中断 replay 恢复验证。
3. 1000 会话压测下拒绝率和时延验证。
