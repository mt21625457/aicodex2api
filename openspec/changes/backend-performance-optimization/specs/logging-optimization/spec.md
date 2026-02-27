## ADDED Requirements

### Requirement: slog Handler 直接传递 fields
系统 SHALL 在 `slogZapHandler.Handle` 方法中直接调用 `h.logger.Info(msg, fields...)` / `h.logger.Error(msg, fields...)` 等对应级别方法传递字段，而非通过 `h.logger.With(fields...)` 创建临时 logger 实例。

#### Scenario: slog 日志记录
- **WHEN** 通过 slog API 记录一条日志
- **THEN** `slogZapHandler.Handle` 直接将 fields 传给 zap logger 的对应级别方法，不创建中间 logger 对象（消除 2 次堆分配）

---

### Requirement: 全局日志无锁化
系统 SHALL 将 `logger.L()` 和 `logger.S()` 的内部存储从 `sync.RWMutex` 保护的全局变量改为 `atomic.Pointer[zap.Logger]` / `atomic.Pointer[zap.SugaredLogger]`，实现无锁读取。`Reconfigure` 时通过 `Store` 原子替换。

#### Scenario: 高并发日志获取
- **WHEN** 多个 goroutine 并发调用 `logger.L()` 获取 logger 实例
- **THEN** 每次调用仅执行一次 `atomic.Pointer.Load()`（无锁），不执行 `mu.RLock()/mu.RUnlock()`

#### Scenario: 日志热重载
- **WHEN** 管理员触发日志配置热重载
- **THEN** 系统通过 `atomic.Pointer.Store()` 原子替换 logger 实例，后续 `L()` 调用立即获取新 logger

---

### Requirement: os.Getenv 初始化缓存
系统 SHALL 将 `debugModelRoutingEnabled` 和 `debugClaudeMimicEnabled` 的环境变量读取改为在 `GatewayService` 初始化时读取一次，存储为 `atomic.Bool` 字段。

#### Scenario: 网关请求中检查 debug 开关
- **WHEN** 网关请求处理中需要检查 debug 模式是否启用
- **THEN** 系统通过 `atomic.Bool.Load()` 读取缓存值，不调用 `os.Getenv` + `strings.ToLower` + `strings.TrimSpace`

---

### Requirement: RedactText 正则预编译
系统 SHALL 对 `RedactText` 中无 `extraKeys` 的默认调用路径预编译 3 个正则表达式（在 `init()` 或包初始化时），对有 `extraKeys` 的调用路径使用 `sync.Map` 按 key 组合缓存已编译正则。

#### Scenario: 默认路径（无 extraKeys）
- **WHEN** `RedactText(input)` 不传 extraKeys
- **THEN** 系统使用预编译的全局正则实例，不执行 `regexp.MustCompile`

#### Scenario: 自定义路径（有 extraKeys）
- **WHEN** `RedactText(input, "custom_key")` 传入 extraKeys
- **THEN** 系统以 extraKeys 排序后的哈希为 key 查找缓存，命中则复用，未命中则编译后缓存

---

### Requirement: Debug 日志参数延迟求值
系统 SHALL 在 TLS fingerprint dialer 的 `slog.Debug` 调用中，对 `fmt.Sprintf` 格式化操作使用 `slog.Default().Enabled(ctx, slog.LevelDebug)` 前置检查，或直接传递整数值替代格式化字符串。

#### Scenario: 生产环境（Debug 级别关闭）
- **WHEN** 日志级别高于 Debug（如 Info/Warn）
- **THEN** 系统不执行 `fmt.Sprintf("0x%04x", ...)` 格式化，零额外分配

#### Scenario: 调试环境（Debug 级别开启）
- **WHEN** 日志级别为 Debug
- **THEN** 系统正常执行格式化并输出完整 debug 信息
