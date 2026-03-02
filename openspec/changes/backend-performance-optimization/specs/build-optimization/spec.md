## ADDED Requirements

### Requirement: Dockerfile 多阶段构建
系统 SHALL 将 Dockerfile 改为多阶段构建：第一阶段使用 `golang:1.25.7-alpine` 编译，第二阶段使用 `alpine:3.21` 作为运行时镜像，仅包含编译产物、`ca-certificates` 和 `tzdata`；运行时健康检查 SHALL 使用 BusyBox `wget` 或等效方案，不依赖 `curl`。

#### Scenario: 构建 Docker 镜像
- **WHEN** 执行 `docker build`
- **THEN** 最终镜像不包含 Go 工具链、源代码和依赖缓存，体积不超过 30MB

---

### Requirement: 编译参数优化
系统 SHALL 在 Dockerfile 和 Makefile 的构建命令中使用以下编译参数：
- `CGO_ENABLED=0`：确保纯静态链接
- `-ldflags="-s -w"`：剥离符号表和 DWARF 调试信息
- `-trimpath`：移除编译路径信息

#### Scenario: 编译 Go binary
- **WHEN** 通过 Dockerfile 或 Makefile 编译后端程序
- **THEN** 编译命令包含 `CGO_ENABLED=0`、`-ldflags="-s -w"` 和 `-trimpath`，生成的 binary 为纯静态链接且不含调试信息

---

### Requirement: Wire cleanup 并行化
系统 SHALL 将 `provideCleanup` 中互不依赖的清理步骤并行执行，仅对有依赖关系的步骤（如 Redis/Ent 须最后关闭）保持顺序。

#### Scenario: 优雅停机
- **WHEN** 系统收到停机信号
- **THEN** 互不依赖的业务服务（如各 OAuth 服务、各定时清理服务、各 Token 刷新服务）并行关闭，基础设施服务（Redis、Ent）在所有业务服务关闭后顺序关闭

---

### Requirement: WS pool 后台 worker 生命周期管理
系统 SHALL 为 `openAIWSConnPool` 的后台 goroutine（ping worker、cleanup worker）添加 `sync.WaitGroup` 跟踪，`Close()` 时等待所有 goroutine 实际退出后再返回。

#### Scenario: WS pool 关闭
- **WHEN** 系统关闭 WS 连接池
- **THEN** `Close()` 关闭 `workerStopCh` 后通过 `WaitGroup.Wait()` 等待 ping worker 和 cleanup worker 退出，确保不存在 goroutine 泄漏

---

### Requirement: WS pool ping 并行化
系统 SHALL 将后台 ping sweep 从串行改为有限并发（如 `errgroup` 限制并发度为 10），避免 N 个 idle 连接的 ping 耗时线性增长。

#### Scenario: 后台 ping sweep
- **WHEN** 后台 ping worker 触发 sweep
- **THEN** 系统并发 ping 所有候选 idle 连接（并发度上限 10），总耗时上界从 `N × 单次 ping 超时` 降为 `ceil(N/10) × 单次 ping 超时`

---

### Requirement: ToHTTP 轻量拷贝优化
系统 SHALL 在 `errors.ToHTTP` 中用“按需轻量拷贝”替代 `Clone` 整体对象；当 `Metadata` 非空时仍 SHALL 做 map 深拷贝，保持返回语义与并发安全。

#### Scenario: HTTP 错误响应
- **WHEN** 系统将内部错误转换为 HTTP 响应
- **THEN** `ToHTTP` 返回与旧实现等价的 `Status` 数据（含 metadata 深拷贝语义），但减少不必要对象复制

---

### Requirement: failover_loop 结构化日志
系统 SHALL 将 `backend/internal/handler/failover_loop.go` 中的 `log.Printf` 调用替换为 `zap.Logger` 的结构化日志方法。

#### Scenario: failover 重试日志
- **WHEN** 网关执行 failover 重试
- **THEN** 系统使用 `zap.Logger.Warn()` 记录结构化日志（含 account_id、status_code、retry_count 等字段），而非 `log.Printf` 的文本格式
