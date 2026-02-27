## ADDED Requirements

### Requirement: OpenAI OAuth 链路性能目标可量化
系统 MUST 为 OpenAI OAuth `/v1/responses` 链路定义并维护可量化的性能目标，至少覆盖网关附加延迟、TTFT、P95/P99、错误率与资源开销基线，并以统一口径输出对比结果。

#### Scenario: 发布前具备性能基线与目标对比
- **WHEN** 团队发起 OpenAI OAuth 性能优化发布评审
- **THEN** 评审材料 MUST 包含优化前后同口径压测结果与目标达成情况

### Requirement: 请求热路径避免重复解析与不必要拷贝
系统 SHALL 在 OpenAI OAuth 请求处理热路径中避免对同一请求体进行重复解析与不必要数据拷贝，保证常态请求不引入额外的可避免 CPU/内存开销。

#### Scenario: 常态请求路径不发生多次完整解析
- **WHEN** 网关处理一个合法的 OpenAI OAuth 非异常请求
- **THEN** 热路径实现 SHALL 不重复执行可避免的全量 JSON 解析与大对象拷贝

### Requirement: 并发控制快速路径最小化额外存储往返
系统 SHALL 对并发控制采用快速路径策略：在可直接获得并发槽位时，不执行不必要的等待队列写入，并最小化常态请求的额外 Redis 往返。

#### Scenario: 可立即获得槽位时跳过等待队列写入
- **WHEN** 请求到达且用户与账号并发槽位均可立即获取
- **THEN** 系统 SHALL 直接进入上游转发路径而不执行等待队列计数写入

### Requirement: 流式转发热路径降低逐行处理成本
系统 MUST 优化 OpenAI OAuth SSE 流式转发热路径，降低逐行处理中的高频字符串与 JSON 操作成本，并保持与 OpenAI Responses 流式协议兼容。

#### Scenario: 流式协议兼容且处理开销降低
- **WHEN** 客户端发起 OpenAI OAuth 流式请求并持续接收事件
- **THEN** 系统 SHALL 保持事件语义兼容，同时逐行处理不应依赖可替代的高开销通用解析手段

### Requirement: Token 竞争路径控制尾延迟放大
系统 SHALL 在 OpenAI OAuth token 获取的锁竞争场景中采用低抖动等待策略，避免固定大步长等待导致的尾延迟放大。

#### Scenario: 锁竞争下请求不出现固定等待台阶
- **WHEN** 多个并发请求同时命中同一 OAuth 账号的 token 刷新竞争
- **THEN** 请求等待策略 SHALL 使用短周期可回退机制，并避免固定长等待造成显著延迟台阶

### Requirement: 优化发布必须具备可灰度与可回滚保障
系统 MUST 为 OpenAI OAuth 性能优化提供灰度发布、关键指标监控与回滚策略，确保在异常时可快速恢复到稳定状态。

#### Scenario: 灰度阶段触发阈值时可快速回滚
- **WHEN** 灰度期间关键指标（错误率或 P99）超出预设阈值
- **THEN** 运行策略 MUST 支持按批次或按开关回滚，并恢复至优化前稳定行为
