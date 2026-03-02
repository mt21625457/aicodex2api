- [x] 0.1 `backend/internal/repository/migrations_runner.go`: 增加“非事务迁移”执行能力（如按文件后缀 `_notx.sql` 分支执行，不包事务），并补充对应测试，确保 `CREATE INDEX CONCURRENTLY` 可被安全执行
- [x] 0.2 `backend/migrations/`: 约定并文档化 `*_notx.sql` 命名规范、回滚策略与执行顺序，禁止在同一文件混用 tx/notx 语义
- [x] 0.3 `backend/internal/repository/migrations_runner_test.go`: 增加 `*_notx.sql` 幂等测试与语义校验测试（重复执行不报错、混用语义时阻断）

## 1. Phase 1 — 日志系统优化（logging-optimization）

- [x] 1.1 `backend/internal/pkg/logger/slog_handler.go`: 将 `Handle` 方法中的 `h.logger.With(fields...)` 改为直接调用 `h.logger.Info(msg, fields...)` / `Error(msg, fields...)` 等对应级别方法，消除每条日志的临时 logger 分配
- [x] 1.2 `backend/internal/pkg/logger/logger.go`: 将 `global` 和 `sugar` 全局变量从 `sync.RWMutex` 保护改为 `atomic.Pointer[zap.Logger]` / `atomic.Pointer[zap.SugaredLogger]`，同步修改 `L()`、`S()`、`Reconfigure()` 和 `sinkCore.Write` 中的 `currentSink` 为 `atomic.Value`
- [x] 1.3 `backend/internal/service/gateway_service.go`: 在 `GatewayService` 构造函数中读取 `SUB2API_DEBUG_MODEL_ROUTING` 和 `SUB2API_DEBUG_CLAUDE_MIMIC` 环境变量，存储为 `atomic.Bool` 字段，替换 `debugModelRoutingEnabled()` / `debugClaudeMimicEnabled()` 中的 `os.Getenv` 调用
- [x] 1.4 `backend/internal/util/logredact/redact.go`: 在包初始化时对默认 key 列表预编译 3 个全局正则；对有 `extraKeys` 的调用路径，以排序后 key 组合为 cache key 使用 `sync.Map` 缓存已编译正则
- [x] 1.5 `backend/internal/pkg/tlsfingerprint/dialer.go`: 对所有 `slog.Debug` 调用中的 `fmt.Sprintf("0x%04x", ...)` 参数，改为直接传整数值 `spec.TLSVersMax`，或添加 `slog.Default().Enabled(ctx, slog.LevelDebug)` 前置检查
- [x] 1.6 `backend/internal/handler/failover_loop.go`: 将所有 `log.Printf` 调用替换为 `zap.Logger.Warn()` 结构化日志，接受 context 传入的 logger 实例

## 2. Phase 1 — 中间件优化（middleware-optimization）

- [x] 2.1 `backend/internal/server/middleware/security_headers.go`: 在 `SecurityHeaders` 中间件中添加 API 路由前缀检查（`/v1/`、`/v1beta/`、`/antigravity/`、`/sora/`、`/responses`），命中时跳过 CSP nonce 生成，仅设置基础安全头
- [x] 2.2 `backend/internal/server/middleware/api_key_auth_google.go`: 将同步 4 次调用（`ValidateSubscription` + `CheckAndActivateWindow` + `CheckAndResetWindows` + `CheckUsageLimits`）替换为 `ValidateAndCheckLimits` 合并调用 + `needsMaintenance` 时异步 `DoWindowMaintenance`，与 `api_key_auth.go` 对齐
- [x] 2.3 `backend/internal/handler/ops_error_logger.go`: 创建 `var opsCaptureWriterPool = sync.Pool{...}`，在 `OpsErrorLoggerMiddleware` 中从 pool 获取 `opsCaptureWriter`，请求结束后 `Reset()` buffer 并归还 pool
- [x] 2.4 `backend/internal/util/responseheaders/responseheaders.go`: 新增 `CompileHeaderFilter(cfg)` 函数返回 `*compiledHeaderFilter`（预构建 `allowed` 和 `forceRemove` map），在 service 初始化时调用；修改 `FilterHeaders` / `WriteFilteredHeaders` 接受预编译结果

## 3. Phase 1 — 缓存与并发优化（cache-optimization）

- [x] 3.1 `backend/internal/service/concurrency_service.go` + `backend/internal/repository/concurrency_cache.go`: 在 `ConcurrencyCache` 增加批量查询接口（如 `GetAccountConcurrencyBatch`），由 repository 层使用 Redis Pipeline 实现，service 层改为单次委托调用
- [x] 3.2 `backend/internal/repository/billing_cache.go` / `backend/internal/service/billing_service.go`: 在余额缓存回源路径中引入 `singleflight.Group`，以 `userID` 为 key 合并并发穿透；为 singleflight 调用设置独立 3 秒 context 超时
- [x] 3.3 `backend/internal/service/concurrency_service.go`: 将 `generateRequestID` 改为“进程随机前缀 + 原子计数器（base36）”，避免跨实例碰撞且减少 `crypto/rand` 热路径开销
- [x] 3.4 `backend/internal/service/billing_cache_service.go`: 在 `BillingCacheService` 中添加 `stopped atomic.Bool` 字段，`Stop()` 时设置为 true；`enqueueCacheWrite` 中先检查 `s.stopped.Load()` 再入队，移除 `defer func() { recover() }` 模式
- [x] 3.5 `backend/internal/pkg/ip/ip.go` + `backend/internal/service/api_key_service.go`: 新增 `CompiledIPRules` 结构体（含 `[]*net.IPNet` 和 `[]net.IP`）和 `CompileIPRules(patterns []string)` 函数；在 API Key 加载/缓存时预编译；修改 `CheckIPRestriction` 使用预编译规则

## 4. Phase 2 — 网关热路径优化（hotpath-optimization）

- [x] 4.1 `openai_ws_forwarder.go`: 将第 1890 行的 `json.Unmarshal(trimmed, &payload)` 改为 `gjson.GetBytes` 按需提取 `type`、`model`、`prompt_cache_key`、`previous_response_id` 等字段；仅在需要修改 payload 时退回 Unmarshal
- [x] 4.2 `openai_gateway_service.go`: 在 `getOpenAIRequestBodyMap` 中首次 `json.Unmarshal` 成功后添加 `c.Set(OpenAIParsedRequestBodyKey, reqBody)` 回写 gin context 缓存
- [x] 4.3 `openai_gateway_service.go`: 在 `SelectAccountWithLoadAwareness` 入口处查询 `GetSessionAccountID` 后，将 `stickyAccountID` 写入 `OpenAIAccountScheduleRequest` 结构体新增字段；在 `selectBySessionHash` / `tryStickySessionHit` 中优先使用已传入的值，非空时跳过 Redis 查询
- [x] 4.4 `openai_gateway_service.go` + `openai_ws_forwarder.go`: 将会话哈希切换到 xxhash，并实现兼容期“双读双写”策略：读新 key 未命中回退读旧 SHA key；绑定时同时刷新新旧 key（旧 key 带短 TTL）
- [x] 4.5 `httpclient/pool.go`: 在 `validatedTransport` 中新增 `validatedHosts sync.Map`（key: string, value: time.Time），`RoundTrip` 中先检查缓存（30 秒 TTL），命中则跳过 DNS 查询；未命中或过期时执行 `ValidateResolvedIP` 并写入缓存
- [x] 4.6 `gateway_handler.go` + `gemini_v1beta_handler.go` + `sora_gateway_handler.go`: 将 `readRequestBodyWithPrealloc` 从 `openai_gateway_handler.go` 提取到公共包（如 `pkg/httputil/body.go`），替换这三个文件中的 `io.ReadAll(c.Request.Body)` 调用
- [x] 4.7 `gateway_handler.go` + `service/*`: 定义 `RequestMetadata` 结构体并单次注入；兼容期保留旧 `ctxkey.*` 注入，读取侧优先读新结构体、回退读旧 key
- [x] 4.8 `openai_gateway_service.go`: 在 `bodyModified` 路径中，对单字段删除场景使用 `sjson.DeleteBytes`，对单字段修改场景使用 `sjson.SetBytes`；仅在多字段复杂修改时保留 `json.Marshal` 全量路径
- [x] 4.9 `gateway_helper.go`: 修改 `SetClaudeCodeClientContext` 接受已解析的请求结构或从 gin context 中读取缓存结果，替换内部的 `json.Unmarshal(body, &bodyMap)` 调用
- [x] 4.10 `backend/internal/config/*` + `config.example.yaml`: 增加兼容开关配置项并设默认值（`session_hash_read_old_fallback=true`、`session_hash_dual_write_old=true`、`metadata_bridge_enabled=true`）
- [x] 4.11 `openai_gateway_service.go` + `gateway_handler.go`: 增加兼容路径观测指标（旧 key 回退命中率、旧 ctxkey 回退命中率），作为下线门禁依据

## 5. Phase 2 — 数据库查询优化（database-optimization，代码层）

- [x] 5.1 `backend/ent/schema/account.go`: 先补齐 `temp_unschedulable_until`、`temp_unschedulable_reason` 字段定义（与现有 DB 列对齐，不改列类型）
- [x] 5.2 `backend/internal/repository/account_repo.go`: 在 `accountsToService` 和 `GetByIDs` 中复用首次查询字段，移除 `loadTempUnschedStates` 二次查询
- [x] 5.3 `usage_log_repo.go`: 将 `fillDashboardUsageStatsFromUsageLogs` 中 4 次独立 SQL 合并为 1-2 个 CTE 查询（total + today 统计合并，today active + hourly active 合并）
- [x] 5.4 `group_repo.go`: 将 `UpdateSortOrders` 从 `for range` 逐条 `UpdateOneID` 改为单条 SQL `UPDATE groups SET sort_order = CASE id WHEN $1 THEN $2 ... END WHERE id = ANY($N)`

## 6. Phase 3 — 数据库索引（database-optimization，Schema 层）

- [x] 6.1 `backend/migrations/*_notx.sql`：新增索引迁移 SQL，使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 创建 `accounts(platform, priority)` 与 `accounts(priority, status)` 的业务部分索引
- [x] 6.2 `backend/migrations/*_notx.sql`：新增索引迁移 SQL，使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 创建 `user_subscriptions(user_id, status, expires_at) WHERE deleted_at IS NULL`
- [x] 6.3 `backend/migrations/*_notx.sql`：新增索引迁移 SQL，使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 创建 `usage_logs(group_id, created_at) WHERE group_id IS NOT NULL`
- [x] 6.4 `backend/ent/schema/*.go`：按需同步普通索引声明（文档一致性目的），不依赖 Ent 自动迁移落地部分索引
- [ ] 6.5 评估并移除 `accounts`、`users`、`api_keys`、`groups`、`user_subscriptions`、`proxies` 表上无效的 `deleted_at` 单列索引（确认已被业务复合部分索引覆盖后）
- [x] 6.6 在 `backend/` 目录运行 `go generate ./ent && go generate ./cmd/server` 重新生成代码，验证编译通过
- [ ] 6.7 在测试/预发布环境执行 `EXPLAIN ANALYZE` 验证新索引被调度热路径查询使用
- [ ] 6.8 `backend/migrations/*_notx.sql`：对 planned 删除索引使用 `DROP INDEX CONCURRENTLY IF EXISTS`，并仅在完成 7 天慢 SQL/查询计划观测后执行

## 7. Phase 1 — 构建与基础设施优化（build-optimization）

- [x] 7.1 `Dockerfile`: 改为多阶段构建 — Stage 1 `golang:1.25.7-alpine` 编译（含 `CGO_ENABLED=0 -ldflags="-s -w" -trimpath`），Stage 2 `alpine:3.21` 运行时（`ca-certificates` + `tzdata` + binary + resources），并将 healthcheck 命令改为 BusyBox `wget`（避免引入 `curl`）
- [x] 7.2 `backend/Makefile`: 更新 `build` 目标添加 `CGO_ENABLED=0 -ldflags="-s -w -X main.Version=$(VERSION)" -trimpath`；新增 `generate` 目标（`go generate ./ent && go generate ./cmd/server`）
- [x] 7.3 `openai_ws_pool.go`: 在 `openAIWSConnPool` 中添加 `workerWg sync.WaitGroup`，`startBackgroundWorkers` 中 `wg.Add(2)` + goroutine 内 `defer wg.Done()`，`Close()` 中 `close(workerStopCh)` 后 `wg.Wait()`
- [x] 7.4 `openai_ws_pool.go`: 将 `runBackgroundPingSweep` 改为使用 `errgroup.Group` 并设置并发度上限（`SetLimit(10)`），并发 ping 所有候选 idle 连接
- [x] 7.5 `backend/internal/pkg/errors/http.go`: 用“按需轻量拷贝”替代 `Clone(appErr)`（仅在 `Metadata != nil` 时拷贝 map），保持 `ToHTTP` 返回语义与线程安全不变
- [x] 7.6 `backend/cmd/server/wire.go`：将 `provideCleanup` 中互不依赖的清理步骤分组并行执行（使用 `sync.WaitGroup`），基础设施步骤（Redis、Ent）保持最后顺序执行；随后重新生成 `backend/cmd/server/wire_gen.go`

## 8. 验证与收尾

- [x] 8.1 运行完整单元测试套件 `go test ./...` 确保所有修改不破坏现有功能
- [ ] 8.2 运行集成测试（特别是 `internal/integration/` 下的 E2E 测试）验证网关热路径修改
- [ ] 8.3 构建 Docker 镜像并验证体积 < 30MB、启动正常、API 功能正常
- [x] 8.4 使用 `go test -bench=.` 对关键路径（WS 消息解析、会话哈希、日志写入）做基准测试对比
- [ ] 8.5 在预发布环境执行 `EXPLAIN ANALYZE` 验证所有新索引的查询计划
