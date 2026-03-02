## Why

后端代码经全量性能审计发现 30+ 个已确认的性能问题，覆盖网关热路径、数据库查询、Redis 缓存、中间件开销、日志系统等关键模块。部分问题（如每请求 DNS 查询、WS 消息全量反序列化、缺失复合索引）直接影响请求延迟和吞吐量，在高并发场景下会成为瓶颈。需要系统性修复以提升整体性能。

## What Changes

### P0 — 关键热路径优化

- **[P0-1] WS 消息反序列化优化**：`openai_ws_forwarder.go:1890` 每条 WS 消息 `json.Unmarshal` 到 `map[string]any`，改用 `gjson.GetBytes` 按需提取只读字段，仅在需要修改 payload 时才全量解析
- **[P0-2] HTTP 请求体解析缓存回写**：`openai_gateway_service.go:3615-3629` `getOpenAIRequestBodyMap` 首次解析后缺少 `c.Set` 回写 gin context 缓存，导致同一请求内可能多次 `json.Unmarshal`
- **[P0-3] Google 认证中间件订阅验证对齐**：`backend/internal/server/middleware/api_key_auth_google.go:79-90` 仍使用旧的 4 次同步调用（`ValidateSubscription`、`CheckAndActivateWindow`、`CheckAndResetWindows`、`CheckUsageLimits`），需更新为 `ValidateAndCheckLimits` 合并 + 异步维护模式
- **[P0-4] CSP nonce 全局中间件优化**：`security_headers.go` 的 CSP nonce 生成（`crypto/rand` 系统调用）作为全局中间件对 API 路由（`/v1/*`、`/v1beta/*`）无意义执行，需限制为仅前端路由
- **[P0-5] 数据库复合索引补充**：`accounts` 表调度热路径缺少 `(platform, priority) WHERE deleted_at IS NULL AND status='active' AND schedulable=true` 复合部分索引；`user_subscriptions` 缺少 `(user_id, status, expires_at) WHERE deleted_at IS NULL` 复合部分索引；`usage_logs` 缺少 `(group_id, created_at)` 复合索引
- **[P0-6] Dockerfile 运行时镜像精简**：当前已是多阶段构建，但运行时镜像仍包含 `curl` 且基础镜像版本偏旧（`alpine:3.20`），需在不破坏健康检查的前提下精简依赖并升级基线

### P1 — 高优先级优化

- **[P1-1] 请求体预分配读取统一化**：`gateway_handler.go:115` 等多处使用 `io.ReadAll` 无预分配，而 `openai_gateway_handler.go` 已有 `readRequestBodyWithPrealloc` 优化方案，需提升到公共层统一使用
- **[P1-2] 双重 Redis 粘性会话查询消除**：`openai_gateway_service.go` 中 `SelectAccountWithLoadAwareness` 和 `selectBySessionHash` 对同一 key 执行两次 `GetSessionAccountID` Redis 查询
- **[P1-3] 并发查询批量化**：`backend/internal/service/concurrency_service.go:323-335` `GetAccountConcurrencyBatch` 名为 Batch 但实现为串行 N 次 Redis GET，需在 `ConcurrencyCache` 增加批量接口并在 `backend/internal/repository/concurrency_cache.go` 用 Pipeline 实现
- **[P1-4] 会话哈希算法降级（含兼容过渡）**：`openai_gateway_service.go:858` 使用 SHA-256 做会话映射，项目已引入 xxhash，对非密码学场景改用 `xxhash.Sum64String` 可提速 10-20 倍；需提供“新 hash 优先读取 + 旧 SHA-256 回退读取 + 兼容期双写”机制，避免升级瞬间粘性会话失配
- **[P1-5] IP 规则匹配预编译**：`ip/ip.go:105-126` `MatchesPattern` 每次都重新 `net.ParseCIDR`/`net.ParseIP`，需在 API Key 创建时预编译为 `[]*net.IPNet`
- **[P1-6] 余额缓存防击穿保护**：`billing_cache.go:86` 热点用户缓存失效瞬间并发穿透到数据库，需添加 `singleflight` 合并回源
- **[P1-7] ResponseHeaders 预编译**：`backend/internal/util/responseheaders/responseheaders.go:44` `FilterHeaders` 每请求重建 allowed map（~20 条目），需在 service 初始化时预构建
- **[P1-8] accounts 表多余查询消除**：`account_repo.go:1386-1426` `loadTempUnschedStates` 仅取 2 列却对 accounts 表做第二次完整查询，可在首次 ORM 查询时一并 Select
- **[P1-9] 仪表盘 SQL 查询合并**：`usage_log_repo.go:500-587` `fillDashboardUsageStatsFromUsageLogs` 4 次独立 SQL 扫描 usage_logs，可合并为 1-2 个 CTE 查询
- **[P1-10] 每请求 DNS 查询缓存**：`httpclient/pool.go:155-165` `validatedTransport.RoundTrip` 对每个 HTTP 请求执行 `ValidateResolvedIP` 完整 DNS 查询，需添加带 TTL 的已验证主机缓存

### P2 — 中优先级优化

- **[P2-1] slog Handler 临时 logger 消除**：`slog_handler.go:51` `h.logger.With(fields...)` 每次创建临时 logger 实例（2 次堆分配），改为直接传 fields 调用对应级别方法
- **[P2-2] os.Getenv 缓存**：`gateway_service.go:130,135` `debugModelRoutingEnabled`/`debugClaudeMimicEnabled` 每次调用 `os.Getenv`（单请求路径可能触发 16 次），改为 `atomic.Bool` 初始化时读取
- **[P2-3] 全局日志无锁化**：`logger.go:171-186` `L()`/`S()` 每次获取 `mu.RLock` 全局读锁，改为 `atomic.Pointer[zap.Logger]`
- **[P2-4] opsCaptureWriter 对象池化**：`ops_error_logger.go:308-336` 每请求堆分配 `opsCaptureWriter`（含 `bytes.Buffer`），改用 `sync.Pool` 复用
- **[P2-5] generateRequestID 轻量化（跨实例唯一）**：`concurrency_service.go:47-53` 内部 slot ID 使用 `crypto/rand`，改为“进程随机前缀 + 原子递增计数器”，在保留跨实例唯一性的同时降低热路径开销
- **[P2-6] body 二次 Unmarshal 消除**：`gateway_helper.go:38-45` `SetClaudeCodeClientContext` 对已解析的 body 再做一次完整 `json.Unmarshal`，复用首次解析结果
- **[P2-7] 请求体增量 patch**：`openai_gateway_service.go:1543` `bodyModified` 时 `json.Marshal` 全量重序列化，改用 `sjson.SetBytes`/`sjson.DeleteBytes` 做增量修改
- **[P2-8] context.WithValue 合并（兼容桥接）**：`gateway_handler.go:144-266` Messages() 中 5+ 次 `context.WithValue` 链式调用，合并为单个请求属性结构体一次注入；兼容期保留旧 key 注入与读取回退，避免现有 service/handler 读取点行为回归
- **[P2-9] WS pool ping 并行化**：`openai_ws_pool.go:635-647` 后台 ping sweep 串行执行，改为有限并发并行 ping
- **[P2-10] WS pool 后台 worker WaitGroup 跟踪**：`openai_ws_pool.go:606-612` `startBackgroundWorkers` 缺少 `sync.WaitGroup`，关闭时无法等待 goroutine 退出
- **[P2-11] Debug 日志参数延迟求值**：`backend/internal/pkg/tlsfingerprint/dialer.go:270` `fmt.Sprintf("0x%04x", ...)` 在 `slog.Debug` 参数中提前求值，改用 `slog.Enabled` 守卫或直接传整数
- **[P2-12] RedactText 正则预编译**：`backend/internal/util/logredact/redact.go:88-92` 每次调用编译 3 个正则，对无 extraKeys 的默认路径预编译缓存
- **[P2-13] enqueueCacheWrite panic-recover 改 atomic.Bool**：`billing_cache_service.go:125-145` 用 panic-recover 检测已关闭 channel，改为 `atomic.Bool` 标记 + 前置检查
- **[P2-14] 软删除 deleted_at 单列索引优化**：所有软删除表上 `deleted_at` 单列索引对 99% NULL 值无效，改为在业务复合索引上添加 `WHERE deleted_at IS NULL` 部分索引条件

### P3 — 低优先级优化

- **[P3-1] ToHTTP 轻量拷贝优化（保留语义）**：`backend/internal/pkg/errors/http.go:19` 避免 `Clone` 整体对象开销，改为按需构造 `Status` 并仅在 `Metadata != nil` 时做 map 深拷贝，保持对外语义不变
- **[P3-2] failover_loop log.Printf**：`backend/internal/handler/failover_loop.go:81` 使用标准库 `log.Printf` 而非结构化日志
- **[P3-3] UpdateSortOrders 逐条 UPDATE**：`group_repo.go:570` N 个分组 N 条 UPDATE，可用 CASE WHEN 批量化
- **[P3-4] cleanupAccountLocked evicted 切片无容量**：`openai_ws_pool.go:992` `make([]*openAIWSConn, 0)` 无初始容量
- **[P3-5] conn.touch() 降频**：`openai_ws_pool.go:408` 每条 WS 消息触发 `atomic.Store + time.Now()`，可增加 1 秒内去重
- **[P3-6] Wire cleanup 并行化**：`wire_gen.go` 24 个清理步骤串行执行（10 秒超时），互不依赖的步骤可并行

### 七轮审核修复记录

- **第 1 轮（结构审核）**：OpenSpec 语法通过，但发现若干条目路径不精确，已统一修正为仓库真实路径（`internal/pkg`、`internal/util`、`internal/handler`）。
- **第 2 轮（存在性复核）**：对关键条目逐项源码核对，确认问题真实存在（如 `getOpenAIRequestBodyMap` 未回写缓存、Google 中间件同步窗口维护、`GetAccountConcurrencyBatch` 串行查询、`ToHTTP` 多余 Clone、`SecurityHeaders` 全路由 nonce）。
- **第 3 轮（方案最优性审核）**：将批量并发查询方案收敛为“接口下沉到 repository 层 Pipeline 实现”；避免仅在 service 层做伪批量，确保改动方向可落地且收益稳定。
- **第 4 轮（兼容性修复）**：补齐 `CONCURRENTLY` 非事务迁移、会话哈希双读双写、context 兼容桥接、requestID 跨实例唯一。
- **第 5 轮（兼容门禁复审）**：补齐兼容开关命名、默认值、下线顺序与阈值门禁，避免“一次性切换”。
- **第 6 轮（二次确认）**：再次核对源码，确认事务迁移包裹、SHA-256 会话哈希、旧 `ctxkey.*` 读取点均真实存在。
- **第 7 轮（最优性再评估）**：将方案收敛为“开关可回退 + 指标门禁 + notx 幂等 + 索引移除观察期”的最优落地路径。

### 兼容性修复补充

- **向前兼容迁移机制**：`CREATE INDEX CONCURRENTLY` 相关迁移需通过“非事务迁移路径”执行，避免被当前 migration runner 的事务包装直接失败。
- **粘性会话键兼容**：会话哈希算法切换采用双读双写过渡窗口，确保滚动发布期间新旧版本共存可用。
- **上下文键兼容**：`RequestMetadata` 引入后保留旧 `ctxkey.*` 键的写入与读取兜底，分阶段下线。
- **并发槽位 ID 兼容**：`requestID` 新格式保持字符串形态与 Redis member 语义兼容，不修改 key 前缀与数据结构。
- **灰度与回滚开关**：会话哈希双写、旧 key 回退读取、context 兼容桥接均需配置开关控制，支持“关旧写 → 关旧读 → 关桥接”的可回退发布路径（每步满足命中率门禁后再推进）。

## Capabilities

### New Capabilities
- `hotpath-optimization`: 网关热路径性能优化（WS 消息解析、请求体缓存、会话哈希、DNS 缓存、预分配读取、context 合并）
- `middleware-optimization`: 中间件性能优化（CSP nonce 路由限制、Google 订阅验证对齐、opsCaptureWriter 池化）
- `database-optimization`: 数据库查询与索引优化（复合部分索引、多余查询消除、SQL 合并、批量化更新）
- `cache-optimization`: Redis 缓存与并发控制优化（防击穿保护、批量查询、粘性会话去重、requestID 轻量化）
- `logging-optimization`: 日志系统性能优化（slog handler、全局日志无锁化、正则预编译、debug 日志延迟求值）
- `build-optimization`: 构建产物优化（Dockerfile 多阶段构建、编译参数优化）

### Modified Capabilities
<!-- 无现有 spec 需要修改 -->

## Impact

- **代码影响范围**：约 30 个文件，涵盖 handler、service、repository、middleware、pkg、ent/schema、Dockerfile 层
- **API 行为**：无 API 接口变更，仅内部实现优化
- **数据库**：新增 3-5 个复合部分索引（需数据库迁移），优化已有索引策略
- **Redis**：无 key 格式变更，仅优化查询模式和缓存策略
- **风险评估**：所有优化为内部实现变更，不影响外部接口；索引变更使用 `CREATE INDEX CONCURRENTLY` 不阻塞读写；Dockerfile 变更需验证镜像运行正确性
