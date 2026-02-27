## backend-performance-optimization 多轮审核记录

### 第 1 轮：提案结构审核

- 结果：`openspec validate backend-performance-optimization --strict` 通过。
- 发现问题：
  - 多处路径表述不精确（`internal/pkg`、`internal/util`、`internal/handler` 混用）。
  - 部分场景描述有事实偏差（Google 中间件“3 次调用”实际为 4 次）。
  - 构建规范遗漏健康检查依赖约束（移除 `curl` 后可能导致 healthcheck 失效）。
- 修复动作：已在 `proposal.md`、`tasks.md`、`specs/*` 统一修正。

### 第 2 轮：问题存在性二次确认

抽样复核核心条目，均确认“问题真实存在”：

- `backend/internal/service/openai_gateway_service.go`：`getOpenAIRequestBodyMap` 未回写 `OpenAIParsedRequestBodyKey`。
- `backend/internal/server/middleware/api_key_auth_google.go`：仍为同步窗口维护路径，未与 `api_key_auth.go` 对齐。
- `backend/internal/server/middleware/security_headers.go`：全路由执行 CSP nonce 生成。
- `backend/internal/service/concurrency_service.go`：`GetAccountConcurrencyBatch` 串行调用。
- `backend/internal/pkg/errors/http.go`：`ToHTTP` 存在多余 `Clone`。
- `backend/internal/handler/failover_loop.go`：仍使用 `log.Printf`。

### 第 3 轮：修复方案最优性复审

- 调整前：`GetAccountConcurrencyBatch` 仅在 service 层“改批量”容易变成伪批量。
- 调整后：明确为“接口下沉到 repository 层，Redis Pipeline 实现，service 层委托调用”，减少重复实现并确保真实收益。
- 调整前：索引策略偏向 Ent 自动迁移，和项目现有 SQL migration runner 不一致。
- 调整后：改为“SQL migration + CONCURRENTLY 优先，Ent schema 对齐可选”，与现有迁移机制一致、风险更低。
- 调整前：Docker 运行层精简未覆盖 healthcheck 依赖。
- 调整后：明确 healthcheck 使用 BusyBox `wget` 或等效方案，避免引入 `curl`。

### 最终结论

- 二次确认结论：本提案核心性能问题存在性成立。
- 方案最优性结论：已将关键次优点修正为更符合当前仓库实现与发布流程的方案。

### 第 4 轮：向前兼容性专项修复

- 修复 `CREATE INDEX CONCURRENTLY` 与事务迁移冲突：补充“非事务迁移”能力与 `*_notx.sql` 约束。
- 修复会话哈希切换兼容性：新增“双读双写 + 兼容窗口”策略，避免滚动发布粘性失配。
- 修复 requestID 方案：改为“进程随机前缀 + 原子递增”，避免多实例碰撞。
- 修复 context 合并风险：明确兼容期保留旧 `ctxkey.*` 注入与读取回退。
- 修复 `ToHTTP` 优化语义风险：改为“按需轻量拷贝 + metadata 深拷贝保留”，不改变外部语义。

### 第 5 轮：向前兼容门禁复审（本次追加）

- 新发现 1：提案虽提到“灰度与回滚开关”，但 `design/spec/tasks` 未形成可执行门禁（缺少开关名、默认值、下线顺序、阈值）。
- 新发现 2：`database-optimization` 对 `*_notx.sql` 仅描述“非事务”，缺少幂等约束与 tx/notx 语义隔离，重复执行与回滚风险仍在。
- 新发现 3：`deleted_at` 单列索引移除缺少观察期与回滚门禁，存在误删后查询退化风险。
- 修复动作：已在 `design.md`、`specs/hotpath-optimization/spec.md`、`specs/database-optimization/spec.md`、`tasks.md` 补齐上述约束。

### 第 6 轮：问题存在性二次确认（本次追加）

- 迁移事务冲突确认：`migrations_runner.go:188-210` 仍按文件统一 `BeginTx` 包裹执行，`CONCURRENTLY` 场景确实会冲突。
- 会话哈希兼容风险确认：`openai_gateway_service.go:858-859` 与 `openai_ws_forwarder.go:544-545` 仍是 SHA-256，会在滚动发布中造成新旧 key 不一致风险。
- context 兼容风险确认：仓库内仍有大量 `ctxkey.*` 读取点（如 `middleware/logger.go`、`gemini_v1beta_handler.go` 等），一次性移除旧键会产生行为回归风险。
- 结论：第 5 轮新增问题均真实存在，不是“文档臆测”。

### 第 7 轮：方案最优性再复审（本次追加）

- 评估结果 1：会话哈希与 metadata 兼容采用“开关 + 指标门禁 + 顺序下线（关旧写→关旧读→关桥接）”优于“一次切换”，回滚路径最短。
- 评估结果 2：`*_notx.sql` 增加 `IF NOT EXISTS/IF EXISTS` 幂等约束，优于仅靠执行流程控制，能覆盖重放/灾备演练场景。
- 评估结果 3：`deleted_at` 索引移除增加“7 天观测门禁 + 可回滚恢复语句”，比“确认覆盖后立即删除”更稳健。
- 结论：本轮修复后的方案在滚动升级、回滚可用性、重复执行容错方面为当前仓库约束下的最优解。

### 最新结论

- 二次确认：本次新增兼容性问题均已确认存在。
- 最优性结论：修复方案已收敛为“可灰度、可回滚、可观测、可重放”的执行路径，优于原始提案描述。
