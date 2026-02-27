## ADDED Requirements

### Requirement: New implementation MUST NOT change legacy behavior by default
系统 MUST 以新增路径方式实现，不得默认改变既有行为。

#### Scenario: v2 router disabled
- **WHEN** `gateway.openai_ws.mode_router_v2_enabled=false`
- **THEN** 系统 MUST 继续执行 legacy 路径
- **AND** 行为 MUST 与当前实现保持一致

### Requirement: WS mode MUST support off/shared/dedicated in v2 router
系统 MUST 在 v2 路径支持三态 WS mode。

#### Scenario: mode off
- **WHEN** v2 路径解析模式为 `off`
- **THEN** 系统 MUST 禁止该账号类型使用 WS mode

#### Scenario: mode shared
- **WHEN** v2 路径解析模式为 `shared`
- **THEN** 系统 MUST 使用共享连接池语义

#### Scenario: mode dedicated
- **WHEN** v2 路径解析模式为 `dedicated`
- **THEN** 系统 MUST 为每个客户端 WS 会话分配独占上游连接
- **AND** 同会话内所有 turn MUST 复用该连接

### Requirement: V2 router MUST be backward compatible with legacy WS flags
系统 MUST 支持旧布尔字段映射到新三态模式。

#### Scenario: legacy enabled
- **WHEN** 新 `*_mode` 缺失且旧 `*_enabled=true`
- **THEN** 系统 MUST 解析为 `shared`

#### Scenario: legacy disabled
- **WHEN** 新 `*_mode` 缺失且旧 `*_enabled=false`
- **THEN** 系统 MUST 解析为 `off`

#### Scenario: no account flags
- **WHEN** 账号新旧字段均缺失
- **THEN** 系统 MUST 使用 `gateway.openai_ws.ingress_mode_default`

### Requirement: Protocol symmetry MUST be enforced in v2 router
系统 MUST 在 v2 路径强制协议对称。

#### Scenario: websocket ingress
- **WHEN** 客户端以 WS 入站并走 v2 路径
- **THEN** 系统 MUST 仅允许 `ws->ws`
- **AND** MUST NOT fallback to HTTP

#### Scenario: http ingress
- **WHEN** 客户端以 HTTP 入站并走 v2 路径
- **THEN** 系统 MUST 仅允许 `http->http`
- **AND** MUST NOT upgrade to WS

### Requirement: Account concurrency MUST define per-account pool max in v2 router
系统 MUST 在 v2 路径将账号并发数作为该账号连接池上限。

#### Scenario: positive account concurrency
- **WHEN** 账号 `concurrency > 0`
- **THEN** 系统 MUST 使用 `account.concurrency` 作为该账号 `max_conns`

#### Scenario: non-positive account concurrency
- **WHEN** 账号 `concurrency <= 0`
- **THEN** 系统 MUST 拒绝该账号的 WS 调度
- **AND** MUST 记录可观测日志与指标

### Requirement: Dedicated store=false path MUST support chain governance and replay recovery
系统 MUST 在 dedicated + store=false 路径支持前置治理与重放恢复。

#### Scenario: proactive previous_response governance
- **WHEN** `store=false` 请求包含不可信 `previous_response_id`
- **THEN** 系统 SHALL 在发送前执行治理（如剥离）

#### Scenario: dedicated connection loss
- **WHEN** dedicated 会话中连接中断
- **THEN** 系统 SHALL 执行一次 input replay 重建
- **AND** 失败时 MUST 返回明确错误并提示重启会话

### Requirement: V2 router MUST expose mode and symmetry observability
系统 MUST 输出 v2 路径关键观测指标。

#### Scenario: mixed traffic with v2 enabled
- **WHEN** v2 开启且流量包含多个 mode
- **THEN** 系统 MUST 输出按 `ws_mode` 分桶的会话与失败指标
- **AND** MUST 输出协议对称拒绝计数与连接池上限命中计数
