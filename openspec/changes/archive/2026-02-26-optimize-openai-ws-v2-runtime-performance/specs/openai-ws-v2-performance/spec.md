## ADDED Requirements

### Requirement: WSv2 error 事件后的连接必须不可复用
系统 MUST 在收到上游 `type=error` 事件后将当前连接标记为损坏，避免回池复用。

#### Scenario: error 事件触发统一损坏标记
- **WHEN** 上游返回 `error` 事件
- **THEN** 系统 MUST 执行连接损坏标记
- **AND** 不得因“是否可回退”分支差异而漏标记

### Requirement: WSv2 写上游超时必须继承父 context
系统 MUST 在写上游 WS 时继承调用方父 `context`，避免客户端已断开时仍长时间阻塞。

#### Scenario: 父 context 已取消
- **WHEN** 父 `context` 已取消
- **THEN** 写上游操作 MUST 立即感知取消并返回
- **AND** MUST NOT 阻塞到默认写超时

### Requirement: 连接池必须具备后台 ping 与后台清理
系统 MUST 在 `Acquire` 之外提供后台连接维护能力。

#### Scenario: 空闲连接后台心跳
- **WHEN** 连接处于空闲状态
- **THEN** 系统 SHALL 按周期对空闲连接执行 ping
- **AND** ping 失败连接 MUST 被回收

#### Scenario: 长时间无请求账号
- **WHEN** 某账号长时间无新请求
- **THEN** 系统 SHALL 仍执行后台清理
- **AND** 过期/无效连接 MUST 被回收

### Requirement: 连接 I/O 必须支持并发一读一写
系统 MUST 避免将 WS 读写串行化到同一把锁上。

#### Scenario: 读阻塞期间执行写/Ping
- **WHEN** 读路径处于阻塞等待
- **THEN** 写路径 SHOULD 仍可独立推进
- **AND** 不得因单锁竞争导致心跳/写入长时间饥饿

### Requirement: ingress WS 客户端断连后应继续 drain 上游
系统 MUST 在 ingress WS 模式下对客户端断连采用“继续 drain 到 terminal”的策略。

#### Scenario: 客户端中途断开
- **WHEN** 向客户端写事件返回断连错误
- **THEN** 系统 SHALL 继续读取上游直到 terminal
- **AND** 连接不得因该断连被立即标记损坏

### Requirement: 状态存储 Redis 操作必须有独立短超时
系统 MUST 为 WS 状态存储的 Redis 操作设置独立短超时，避免长上下文阻塞。

#### Scenario: Redis 网络异常
- **WHEN** Redis 操作发生网络抖动/分区
- **THEN** `set/get/delete` MUST 在短超时内返回
- **AND** 不得无限依赖上层长连接 context

### Requirement: 协议决策必须对未知认证类型显式回退 HTTP
系统 MUST 在未知 OpenAI 认证类型下显式回退 HTTP。

#### Scenario: 非 OAuth 且非 API Key 账号
- **WHEN** 账号认证类型不在已知集合内
- **THEN** 协议决策 MUST 返回 HTTP
- **AND** MUST NOT 进入 WS 子开关判定路径

### Requirement: WS 消息读取上限必须受控
系统 MUST 对 ingress 与上游 WS 客户端统一设置合理读取上限，降低异常大包内存风险。

#### Scenario: 默认读取上限
- **WHEN** 系统创建 ingress/上游 WS 连接
- **THEN** 读取上限 MUST 为受控值（16MB）
- **AND** ingress 与上游配置 MUST 保持一致

### Requirement: 粘连绑定失败必须可观测
系统 MUST 对 `BindResponseAccount` 失败记录警告日志。

#### Scenario: 粘连绑定异常
- **WHEN** 状态存储返回绑定错误
- **THEN** 系统 MUST 记录 `warn` 级别日志
- **AND** 日志 MUST 包含 group/account/response 标识

