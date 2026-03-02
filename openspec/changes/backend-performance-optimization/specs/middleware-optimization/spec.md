## ADDED Requirements

### Requirement: CSP nonce 仅对前端路由生效
系统 SHALL 将 `SecurityHeaders` 中间件的 CSP nonce 生成逻辑限制为仅对前端路由（返回 HTML 的路由）执行。API 路由（`/v1/*`、`/v1beta/*`、`/antigravity/*`、`/sora/*`、`/responses`）SHALL 跳过 CSP nonce 生成，仅设置基础安全头（`X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`）。

#### Scenario: API 路由请求
- **WHEN** 请求路径以 `/v1/`、`/v1beta/`、`/antigravity/`、`/sora/`、`/responses` 开头
- **THEN** 系统设置基础安全头但跳过 CSP nonce 生成，不调用 `crypto/rand`

#### Scenario: 前端路由请求
- **WHEN** 请求路径为前端页面路由（如 `/`、`/admin`、`/settings` 等返回 HTML 的路由）
- **THEN** 系统正常生成 CSP nonce 并设置 `Content-Security-Policy` 头

---

### Requirement: Google 认证中间件订阅验证对齐
`api_key_auth_google.go` SHALL 使用与 `api_key_auth.go` 相同的合并验证模式：调用 `ValidateAndCheckLimits`（纯内存操作）进行订阅验证和限额检查，将窗口维护操作（`CheckAndActivateWindow`、`CheckAndResetWindows`）改为异步执行。

#### Scenario: Google 格式 API Key 认证
- **WHEN** 请求通过 Google 格式 API Key 认证且用户有活跃订阅
- **THEN** 系统调用 `ValidateAndCheckLimits` 进行合并验证（纯内存操作），不再同步执行 4 次独立调用

#### Scenario: 订阅需要窗口维护
- **WHEN** `ValidateAndCheckLimits` 返回 `needsMaintenance=true`
- **THEN** 系统异步调用 `DoWindowMaintenance`，不阻塞请求处理

---

### Requirement: opsCaptureWriter 对象池化
系统 SHALL 使用 `sync.Pool` 复用 `opsCaptureWriter` 实例，避免每请求堆分配。

#### Scenario: 请求进入 OpsErrorLoggerMiddleware
- **WHEN** 新请求进入错误日志中间件
- **THEN** 系统从 `sync.Pool` 获取 `opsCaptureWriter`（命中时零分配），设置 `ResponseWriter` 和 `limit` 后使用

#### Scenario: 请求结束
- **WHEN** 请求处理完毕
- **THEN** 系统 `Reset()` opsCaptureWriter 的 `bytes.Buffer` 并归还到 `sync.Pool`

---

### Requirement: ResponseHeaders 预编译
系统 SHALL 在 service 初始化时预构建 `compiledHeaderFilter`（包含合并后的 `allowed` set 和 `forceRemove` set），`FilterHeaders`/`WriteFilteredHeaders` 在运行时直接使用预编译结果，不每次重建 map。

#### Scenario: 代理响应头过滤
- **WHEN** 网关将上游响应转发给客户端
- **THEN** 系统使用预编译的 `compiledHeaderFilter` 过滤响应头，不分配新的 `allowed` / `forceRemove` map
