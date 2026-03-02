## Context

本项目是一个多平台 AI API 网关（支持 Claude/OpenAI/Gemini/Sora/Antigravity），使用 Go + Gin + Ent ORM + Redis + PostgreSQL 技术栈。经全量代码审计确认 34 个性能问题，涉及网关热路径（每请求必经）、数据库查询、Redis 缓存策略、中间件开销、日志系统和构建产物等层面。

当前系统处理链路：HTTP 请求 → 全局中间件（Recovery/Logger/CORS/SecurityHeaders）→ 认证中间件（API Key Auth）→ Handler → Service（调度/网关/计费）→ Repository（DB/Redis/HTTP 上游）→ 响应。

约束条件：
- 所有优化必须为内部实现变更，不改变外部 API 接口
- 数据库索引变更必须使用 `CONCURRENTLY` 避免阻塞
- 不修改配置级参数（连接池大小、TTL 等已由团队调优）

## Goals / Non-Goals

**Goals:**
- 降低网关热路径的每请求内存分配和 CPU 开销
- 消除数据库层的冗余查询和缺失索引
- 优化 Redis 访问模式（消除重复查询、串行批量化）
- 减少中间件在 API 路由上的无效开销
- 提升日志系统在高并发下的吞吐效率
- 将 Docker 运行时镜像稳定在 `<30MB` 且保持可运维能力（健康检查/时区/TLS 证书）

**Non-Goals:**
- 不做架构层面重构（保持现有分层结构不变）
- 不修改配置级参数（连接池、TTL、Worker 数量等）
- 不引入新的外部依赖（使用项目已有的 gjson/sjson/xxhash 等）
- 不修改 API 接口行为或响应格式
- 不引入新的业务字段语义变更；仅允许补齐与现有数据库已存在列一致的 Ent schema 声明

## Decisions

### D1: WS 消息解析策略 — gjson 按需提取 vs 定义结构体

**选择**: gjson 按需提取

**理由**: WS 消息 payload 字段多且随 API 版本变化，定义完整结构体维护成本高。`gjson.GetBytes` 零分配提取所需字段（`type`、`model`、`prompt_cache_key` 等），仅在需要修改 payload 时才退回到 `json.Unmarshal`。项目已广泛使用 gjson（`openai_gateway_service.go` 中有 50+ 处），模式一致。

**替代方案**: `jsoniter` 或 `sonic` — 需引入新依赖，收益不如 gjson 按需提取大。

### D2: DNS 查询缓存实现 — sync.Map + TTL vs 独立缓存库

**选择**: `sync.Map` + `time.Time` TTL（30 秒）

**理由**: 缓存条目极少（仅上游 API 主机，如 `api.anthropic.com`、`api.openai.com` 等不超过 10 个），用 `sync.Map` 实现最简、无额外依赖。定期过期通过写入时间戳 + 读取时判断实现，无需后台清理 goroutine。

**替代方案**: ristretto / go-cache — 项目已有这两个依赖，但对 <10 个条目的场景引入 LRU 过度设计。

### D3: 全局日志无锁化 — atomic.Pointer vs sync.Once

**选择**: `atomic.Pointer[zap.Logger]` + `atomic.Pointer[zap.SugaredLogger]`

**理由**: 日志对象在启动后基本不变（仅热重载时变更），`atomic.Pointer` 的 Load 操作是单次 CPU 指令，比 `RWMutex.RLock/RUnlock` 高效一个数量级。`Reconfigure` 时通过 `Store` 原子替换，保证线程安全。`sync.Once` 不支持后续的 Reconfigure 场景。

### D4: 索引策略 — Ent schema 定义 vs 手写迁移 SQL

**选择**: 手写迁移 SQL（`backend/migrations/*.sql`）优先 + Ent schema 对齐

**理由**: 当前项目使用内置 migration runner 执行 `backend/migrations` SQL，并依赖 checksum 保证不可变性。对于 `PARTIAL INDEX` + `CONCURRENTLY` 这类在线索引场景，手写 SQL 更可控、风险更低。Ent schema 可在后续补充普通索引定义以保持模型可读性，但不作为线上索引落地主路径。

**注意**: migration runner 当前按文件事务执行，`CREATE INDEX CONCURRENTLY` 不能直接放在默认事务迁移中；需要先提供非事务迁移能力（或等效的独立执行流程）。对生产库新增索引统一采用 `CREATE INDEX CONCURRENTLY`；回滚采用 `DROP INDEX CONCURRENTLY`。

### D5: 请求体增量 patch — sjson vs 手动拼接

**选择**: `sjson.SetBytes` / `sjson.DeleteBytes`

**理由**: 项目已引入 `sjson`，API 稳定。对于仅修改/删除少量字段的场景（如删除 `max_output_tokens`、修改 `model`），sjson 直接操作原始 `[]byte`，避免全量 `json.Marshal(map[string]any)` 的分配开销。

### D6: Dockerfile 多阶段构建目标镜像

**选择**: `alpine:3.21` 作为运行时基础镜像

**理由**: 需要 `ca-certificates`（TLS 连接上游 API）和 `tzdata`（时区支持），`scratch` 镜像不含这些。`distroless` 也可行但 alpine 调试更方便。使用 `CGO_ENABLED=0` 确保静态链接。为避免新增 `curl` 依赖，healthcheck 改用 BusyBox 自带 `wget`。

### D7: 会话哈希切换兼容策略 — 直接替换 vs 双读双写过渡

**选择**: 双读双写过渡

**理由**: 粘性会话 key 由 `openai:<hash(sessionID)>` 组成。若从 SHA-256 直接替换为 xxhash，会导致滚动发布期间新旧实例命中不同 key，出现短期粘性失效。采用“读新回退旧、写新同时写旧（兼容窗口）”可平滑过渡，不影响在线请求。

### D8: context 元数据注入演进 — 一次性替换 vs 兼容桥接

**选择**: 兼容桥接分阶段替换

**理由**: 当前大量逻辑直接读取 `ctxkey.IsMaxTokensOneHaikuRequest`、`ctxkey.ThinkingEnabled`、`ctxkey.PrefetchedStickyAccountID` 等旧键。若一次性替换为 `RequestMetadata`，存在行为回归风险。先“新结构体注入 + 旧键保留写入/回退读取”，待全链路切换后再移除旧键，风险最低。

### D9: 兼容开关与下线门禁 — 一次切换 vs 分阶段收敛

**选择**: 分阶段开关 + 指标门禁

**理由**: 会话哈希与 context 键演进都涉及滚动发布期间的新旧版本共存。一次性关闭旧路径会放大回滚风险。采用显式开关并绑定观测阈值，可实现“可回退发布”：
- `session_hash_read_old_fallback`（默认 `true`）：新 key 未命中时是否回退读旧 key
- `session_hash_dual_write_old`（默认 `true`）：写入时是否同时写旧 key
- `metadata_bridge_enabled`（默认 `true`）：是否保留旧 `ctxkey.*` 兼容注入/读取桥接

**下线顺序**:
1. 先关闭 `session_hash_dual_write_old`（保留旧读回退）
2. 观测稳定后再关闭 `session_hash_read_old_fallback`
3. 最后关闭 `metadata_bridge_enabled`

**门禁条件**: 旧 key 回退命中率连续 7 天 `< 0.1%` 且无兼容性告警，方可进入下一步下线。

**回滚策略**: 任一步骤出现粘性失配或行为回归，立即重新开启对应开关并回退到上一步。

## Risks / Trade-offs

**[Risk] gjson 按需提取可能遗漏需要处理的字段** → 通过代码审查确认所有需提取的字段列表，并在修改处保留 fallback 到全量 Unmarshal 的路径

**[Risk] DNS 缓存可能导致 IP 变更延迟感知** → TTL 设为 30 秒，足够短以跟随 DNS 变更；安全校验失败时立即清除对应缓存条目

**[Risk] atomic.Pointer 替换全局日志后 Reconfigure 时的短暂不一致** → Reconfigure 本身就是低频操作（管理员触发），Store 是原子操作，不一致窗口为纳秒级

**[Risk] CONCURRENTLY 创建索引在高写入负载下可能耗时较长** → 在低峰期执行迁移；索引创建不阻塞读写，仅消耗额外 I/O

**[Risk] sjson 修改嵌套字段时路径语法与 gjson 不完全一致** → 仅对顶层字段使用 sjson（如 `model`、`max_output_tokens`），嵌套修改仍走全量 Marshal

**[Risk] singleflight 在余额缓存中可能导致一个慢查询阻塞同 key 所有请求** → 对 singleflight 调用设置独立的 context 超时（3 秒），超时后放弃等待直接回源

**[Risk] 运行时镜像移除 curl 后健康检查失效** → 将 Docker `HEALTHCHECK` 命令改为 `wget -q -O -` 或显式保留 curl（二选一并在任务中固定）

**[Risk] 并发槽位 requestID 改为纯原子递增会跨实例碰撞** → 使用“进程随机前缀 + 原子递增”组合，保证跨实例/重启场景下仍具备足够唯一性

**[Risk] 兼容路径过早下线导致滚动升级抖动** → 兼容开关默认开启，按“关旧写→关旧读→关桥接”顺序执行，并用命中率阈值做门禁

## Migration Plan

1. **Phase 1 — 无风险纯代码优化**（无需迁移）
   - P2/P3 级别的代码优化（日志无锁化、对象池化、正则预编译、Debug 日志守卫等）
   - Dockerfile 多阶段构建
   - 与兼容桥接无关的改动可直接合并；涉及兼容开关默认值调整的改动需灰度

2. **Phase 2 — 热路径优化**（需要充分测试）
   - WS 消息 gjson 解析、请求体缓存回写、会话哈希改 xxhash
   - DNS 查询缓存、IP 规则预编译
   - Google 认证中间件对齐
   - 会话哈希/metadata 兼容开关默认保持开启，按门禁分阶段下线旧路径
   - 需要完整的集成测试覆盖后合并

3. **Phase 3 — 数据库索引**（需要低峰期执行）
   - 先完成 migration runner 非事务迁移能力（或明确独立执行机制）
   - 在 `backend/migrations` 新增 `*_notx.sql` 迁移文件，使用 `CREATE INDEX CONCURRENTLY` 在线创建
   - `*_notx.sql` 使用 `IF NOT EXISTS`/`IF EXISTS` 保证幂等，不与事务迁移语句混用
   - 视需要在 Ent schema 对齐普通索引定义（不依赖 Ent 自动迁移落地）
   - 验证查询计划（`EXPLAIN ANALYZE`）确认索引被使用

**回滚策略**: 所有变更按 Phase 分批提交为独立 commit，任一 Phase 出现问题可独立回滚。索引回滚通过 `DROP INDEX CONCURRENTLY` 执行。

## Open Questions

- Q1: `openai_ws_forwarder.go` 中 WS 消息修改场景（需要全量 Unmarshal 的情况）的具体触发条件和频率，以评估 gjson-only 路径的覆盖率。
