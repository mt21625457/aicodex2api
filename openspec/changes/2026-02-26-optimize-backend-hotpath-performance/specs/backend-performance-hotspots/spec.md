## ADDED Requirements

### Requirement: 批量账号更新必须使用批量快照同步
系统 MUST 在批量账号更新场景使用批量快照同步接口，避免逐账号深查询同步。

#### Scenario: 批量状态变更触发快照同步
- **WHEN** 一次请求更新多个账号且触发调度快照同步
- **THEN** 系统 MUST 使用批量读取与批量写缓存路径
- **AND** 系统 MUST NOT 对每个账号循环执行 `GetByID -> accountsToService`

### Requirement: Outbox 批量账号事件必须批量消费
系统 MUST 将批量账号 outbox 事件按批处理，并对 rebuild 目标进行去重。

#### Scenario: 收到 account_ids 批量事件
- **WHEN** outbox 事件包含多个 `account_ids`
- **THEN** 系统 MUST 批量加载账号并批量更新缓存
- **AND** 系统 SHALL 对 `platform + group` 维度去重 rebuild

### Requirement: Gemini 预检必须避免候选循环内逐账号查库
系统 MUST 在 Gemini 账号筛选链路中使用请求级批量 usage 预取，避免在候选循环中逐账号查询分钟配额。

#### Scenario: 账号筛选执行 RPM 预检
- **WHEN** 请求需要从候选账号中选择可用账号
- **THEN** 系统 MUST 先执行批量 usage 预取并复用结果
- **AND** 候选循环 MUST NOT 触发逐账号分钟窗口 SQL 查询

### Requirement: Gemini 批量预检结果必须保持语义一致
系统 MUST 确保批量预检结果与原有逐账号预检在同一时间窗口口径下语义一致。

#### Scenario: 相同输入下预检结果一致
- **WHEN** 对同一账号集合、同一时间窗口执行逐账号预检与批量预检
- **THEN** 通过/拒绝结论 MUST 保持一致
- **AND** 除缓存时效差异外 MUST NOT 引入额外误判

### Requirement: 批量混合渠道检查必须单次预加载分组成员
系统 MUST 在批量账号更新中的混合渠道检查里复用单次预加载的分组成员索引。

#### Scenario: 批量更新含 groupIDs 且启用混合渠道检查
- **WHEN** 一个请求内需要对多个账号执行混合渠道检查
- **THEN** 系统 SHALL 一次性加载相关分组成员数据
- **AND** 系统 MUST NOT 对每个账号重复调用 `ListByGroup`

### Requirement: Ops Dashboard raw 查询必须收敛重复扫描
系统 SHALL 在 raw 查询路径复用公共时间窗与过滤 CTE，降低同请求重复扫描成本。

#### Scenario: raw 模式查询概览指标
- **WHEN** 看板请求走 raw 查询模式
- **THEN** 系统 SHALL 通过共享 CTE 收敛 `usage_logs/ops_error_logs` 重复扫描
- **AND** 在高负载下 SHALL 提供可降级返回而非长时间阻塞

### Requirement: SSE 事件处理必须避免重复 JSON 解析
系统 MUST 在流式事件处理中做到单事件单次解析，并复用解析结果提取 usage。

#### Scenario: 处理 message_start/message_delta 事件
- **WHEN** 网关收到可解析的 SSE 事件
- **THEN** 系统 MUST 至多执行一次结构化解析
- **AND** usage 提取 MUST 复用该解析结果

### Requirement: 用户列表倍率加载必须批量化
系统 MUST 在用户列表场景使用批量接口加载用户分组倍率，避免 N+1 查询。

#### Scenario: 列表页加载用户与倍率
- **WHEN** 管理端请求用户列表并需要展示 `GroupRates`
- **THEN** 系统 MUST 使用批量倍率查询接口
- **AND** 系统 MUST NOT 对每个用户逐条查询倍率

### Requirement: 用户分组倍率同步必须批量写入
系统 MUST 在同步用户分组倍率时使用单事务批量 upsert/delete。

#### Scenario: 一次请求同步多个 group 的倍率
- **WHEN** 后端收到 `SyncUserGroupRates` 请求
- **THEN** 系统 MUST 采用批量 SQL 完成 upsert 与 delete
- **AND** 系统 MUST NOT 对每个 group 执行独立 SQL 往返

### Requirement: 模型映射解析必须具备缓存机制
系统 SHALL 为账号模型映射提供惰性缓存机制，避免热点路径重复构建 map。

#### Scenario: 高频模型匹配调用
- **WHEN** 同一账号在一次请求/短窗口内多次调用 `IsModelSupported` 或 `GetMappedModel`
- **THEN** 系统 SHALL 复用已解析映射
- **AND** 在凭证更新后 MUST 正确失效缓存

### Requirement: 分组存在性校验必须支持批量查询
系统 MUST 在账号创建/更新/批量更新场景提供分组批量存在性校验能力。

#### Scenario: 请求携带多个 groupIDs
- **WHEN** 请求需要校验多个分组是否存在
- **THEN** 系统 MUST 使用批量存在性查询
- **AND** 系统 MUST NOT 对每个 groupID 逐条 `GetByID`

### Requirement: 性能优化必须具备可观测指标
系统 MUST 暴露并记录与本提案对应的关键性能指标，支持发布决策与回归定位。

#### Scenario: 发布前后性能对比
- **WHEN** 团队执行性能优化发布评审
- **THEN** 系统 MUST 提供优化前后同口径指标对比
- **AND** 指标至少包含 DB 查询数、SSE 解析次数、raw 查询耗时、allocs/op

### Requirement: 性能优化必须支持分域灰度与回滚
系统 MUST 为各优化子域提供独立开关，并支持按子域回滚。

#### Scenario: 指标越界触发回滚
- **WHEN** 任一子域灰度期间出现关键指标越界
- **THEN** 系统 MUST 能独立回滚该子域优化
- **AND** 其他子域优化 SHALL 可继续保持开启

### Requirement: 性能优化不得改变业务语义
系统 MUST 在优化后保持原有调度、配额、网关协议与管理语义一致。

#### Scenario: 语义回归验证
- **WHEN** 执行回归测试（调度、配额、流式协议、管理接口）
- **THEN** 业务行为 MUST 与优化前一致
- **AND** 仅允许性能指标变化，不允许功能语义变化
