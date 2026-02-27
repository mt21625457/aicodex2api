## ADDED Requirements

### Requirement: WSv2 转发热路径必须避免重复序列化与重复字段解析
系统 MUST 在单次 WSv2 请求处理过程中避免可消除的 payload 重复序列化、重复字符串解析与重复大对象拷贝。

#### Scenario: 单请求仅进行必要序列化
- **WHEN** 网关处理一次合法的 OpenAI WSv2 请求
- **THEN** payload 编码与字段提取 SHALL 采用单次快照策略
- **AND** 系统 MUST NOT 在同一请求中重复执行可避免的全量 JSON 编码

### Requirement: WSv2 日志必须受预算与采样控制
系统 MUST 对 WSv2 热路径日志与 payload 统计执行预算控制，避免日志计算放大主流程开销。

#### Scenario: 大 payload 场景日志成本受控
- **WHEN** 请求包含大型 `tools` 或 `input` 字段
- **THEN** 系统 SHALL 使用采样和截断策略记录诊断信息
- **AND** 系统 MUST NOT 对所有字段每次都执行高开销序列化统计

### Requirement: WS 事件循环必须最小化字节与字符串往返转换
系统 SHALL 在 WS 事件处理循环中优先使用字节路径，降低 `[]byte <-> string` 的频繁转换成本。

#### Scenario: 高频 token 事件下保持低分配
- **WHEN** 流式请求持续输出高频 token 事件
- **THEN** 事件处理路径 MUST 使用字节优先处理与选择性解析
- **AND** 在不影响协议语义前提下 MUST 减少每事件的临时对象分配

### Requirement: 连接池获取路径必须使用低复杂度连接选择策略
系统 MUST 为账号连接池提供低复杂度连接选择机制，避免在每次 `Acquire` 上执行全量排序。

#### Scenario: 账号连接数增加时获取开销受控
- **WHEN** 同一账号连接池中连接数量上升
- **THEN** `Acquire` 延迟 SHALL 维持稳定并接近 O(1)/O(log n) 复杂度
- **AND** `preferred_conn_id` 命中时 MUST 走快速路径

### Requirement: 代理建连必须复用 HTTP 传输资源
系统 MUST 复用代理建连使用的 HTTP client/transport，避免按请求重复创建传输对象。

#### Scenario: 同代理地址连续建连
- **WHEN** 同一 `proxyURL` 在短时间内多次用于 WS 建连
- **THEN** 系统 SHALL 复用同一传输资源池
- **AND** 握手延迟与建连 CPU 开销 MUST 低于未复用基线

### Requirement: WS 重试策略必须具备分类、退避与熔断能力
系统 MUST 将 WS 失败分为可重试与不可重试两类，并对可重试路径应用退避与抖动策略。

#### Scenario: 策略类失败快速回退
- **WHEN** 上游返回策略违规类关闭状态（例如 `1008`）
- **THEN** 系统 MUST 在一次失败后快速回退到 HTTP
- **AND** 系统 MUST NOT 连续进行多次无效 WS 重试

#### Scenario: 可重试失败执行退避
- **WHEN** 发生可重试的瞬时错误（如网络抖动、上游 5xx）
- **THEN** 系统 SHALL 使用指数退避并附加 jitter 控制重试节奏
- **AND** 重试次数与等待时长 MUST 受配置上限约束

### Requirement: 预热与扩容策略必须防抖并避免建连风暴
系统 SHALL 对连接预热和扩容触发执行防抖控制，避免瞬时负载波动触发过量后台建连。

#### Scenario: 高频 Acquire 下预热触发受控
- **WHEN** 同账号在短窗口内出现大量 Acquire 调用
- **THEN** 系统 MUST 保证同一账号预热线程/任务数量有界
- **AND** 预热触发 MUST 受 cooldown 与失败率门限控制

### Requirement: WSv2 性能优化不得改变“默认开启”产品策略
系统 MUST 在性能优化实施后保持 OpenAI Responses WebSocket 的默认开启策略不变，不得通过性能提案将默认行为回退为关闭。

#### Scenario: 配置默认值保持开启
- **WHEN** 系统加载默认网关配置
- **THEN** `gateway.openai_ws.enabled` MUST 保持为 `true`
- **AND** 性能优化开关 MUST 只影响实现细节，不改变 WS 默认启用语义

### Requirement: WSv2 性能优化发布必须满足量化验收与回滚保障
系统 MUST 在 WSv2 性能优化上线前后提供统一口径基线对比，并具备阈值触发回滚能力。

#### Scenario: 发布验收材料完整
- **WHEN** 团队评审 WSv2 性能优化发布
- **THEN** 材料 MUST 包含 `TTFT`、`P95/P99`、`CPU`、`allocs/op`、`retry_attempts`、`fallback_rate` 的前后对比

### Requirement: WSv2 性能优化必须达到明确阈值
系统 MUST 基于统一压测口径达到本提案定义的性能阈值，未达标不得全量发布。

#### Scenario: 延迟与资源阈值达标
- **WHEN** 在统一基线环境完成 WSv2 优化回归压测
- **THEN** 网关附加延迟 `P95` MUST 至少降低 25%
- **AND** 网关附加延迟 `P99` MUST 至少降低 20%
- **AND** 热路径 `allocs/op` MUST 至少降低 30%
- **AND** 热路径 `B/op` MUST 至少降低 25%

#### Scenario: 重试与连接复用阈值达标
- **WHEN** 在统一基线环境完成失败注入与稳态压测
- **THEN** 单请求平均 `retry_attempts` MUST 小于等于 1.2
- **AND** `retry_exhausted` 比例 MUST 小于等于 0.5%
- **AND** 连接池复用率 MUST 大于等于 75%

#### Scenario: 指标越界可快速回滚
- **WHEN** 灰度阶段关键指标超出预设阈值
- **THEN** 系统 MUST 支持按开关快速回滚到稳定路径
- **AND** 回滚后行为 MUST 与回滚前基线兼容
