## ADDED Requirements

### Requirement: 并发查询 Pipeline 批量化
系统 SHALL 将 `GetAccountConcurrencyBatch` 从串行 N 次 Redis GET 改为 Redis Pipeline 批量查询，单次 RTT 获取所有账号的并发数。

#### Scenario: 批量查询账号并发数
- **WHEN** 调度器需要查询 N 个账号的当前并发数
- **THEN** 系统通过单次 Redis Pipeline 发送 N 个 EVAL 命令并批量接收结果，而非 N 次独立 Redis 往返

---

### Requirement: 余额缓存防击穿保护
系统 SHALL 在余额缓存回源查询中使用 `singleflight.Group` 合并并发请求，同一 `userID` 的缓存穿透只执行一次数据库查询。

#### Scenario: 高并发缓存过期
- **WHEN** 同一用户的余额缓存过期，多个并发请求同时穿透
- **THEN** 仅第一个请求执行数据库查询，其余请求等待并共享结果

#### Scenario: singleflight 超时保护
- **WHEN** 数据库查询耗时超过 3 秒
- **THEN** 等待中的请求超时放弃 singleflight 等待，各自独立回源（防止一个慢查询阻塞所有请求）

---

### Requirement: IP 规则预编译缓存
系统 SHALL 在 API Key 加载/缓存时预编译 IP 白名单/黑名单规则为 `[]*net.IPNet` 和 `[]net.IP`，认证时 SHALL 使用预编译结果进行匹配，不再每次调用 `net.ParseCIDR`/`net.ParseIP`。

#### Scenario: API Key 认证中的 IP 检查
- **WHEN** 请求通过 API Key 认证且该 Key 配置了 IP 限制规则
- **THEN** 系统使用预编译的 `*net.IPNet` 执行 `Contains()` 检查，不执行字符串解析

#### Scenario: API Key 规则变更
- **WHEN** 管理员修改 API Key 的 IP 限制规则
- **THEN** 系统重新编译该 Key 的 IP 规则并更新缓存

---

### Requirement: generateRequestID 轻量化
系统 SHALL 将并发控制的内部 `generateRequestID` 从每次调用 `crypto/rand` 改为“进程随机前缀 + 原子计数器”，生成格式为 `<prefix>-<base36_counter>`，在降低开销的同时保持跨实例唯一性。

#### Scenario: 获取新 slot 时生成 requestID
- **WHEN** `AcquireAccountSlot` 或 `AcquireUserSlot` 需要生成 requestID
- **THEN** 系统使用 `atomic.Uint64.Add(1)` 生成递增序号并拼接进程级前缀，不在每次请求中调用 `crypto/rand`

#### Scenario: 多实例部署
- **WHEN** 多个网关实例同时生成 requestID
- **THEN** 不同实例通过各自前缀隔离，避免 Redis 槽位 member 冲突

---

### Requirement: enqueueCacheWrite 安全关闭检查
系统 SHALL 在 `BillingCacheService` 中使用 `atomic.Bool` 标记服务是否已停止，`enqueueCacheWrite` 在发送前检查该标记，替代当前的 `panic-recover` 模式。

#### Scenario: 服务运行中入队
- **WHEN** 服务运行中调用 `enqueueCacheWrite`
- **THEN** 系统检查 `stopped.Load() == false` 后正常入队，无 defer/recover 开销

#### Scenario: 服务已停止时入队
- **WHEN** 服务已停止后调用 `enqueueCacheWrite`
- **THEN** 系统通过 `stopped.Load() == true` 快速返回 false，不触发 panic
