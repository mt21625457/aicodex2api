## ADDED Requirements

### Requirement: TimingWheel Initialization Error Handling

当 TimingWheel 初始化失败时，`NewTimingWheelService()` SHALL 返回 error 而不是触发 panic。函数签名 MUST 为 `(*TimingWheelService, error)`，以便调用方能够感知初始化失败并按“启动失败”路径处理。

#### Scenario: TimingWheel 初始化失败时不触发 panic
- **WHEN** 底层 `collection.NewTimingWheel()` 返回 error
- **THEN** `NewTimingWheelService()` 返回 `nil` 和包装后的 error（例如使用 `%w` 包装）
- **AND** 不发生 panic（进程不应因该错误直接崩溃）

#### Scenario: TimingWheel 初始化成功
- **WHEN** 底层 `collection.NewTimingWheel()` 初始化成功
- **THEN** `NewTimingWheelService()` 返回有效的 `*TimingWheelService` 和 `nil` error

#### Scenario: 初始化失败导致应用启动失败并退出（非 0）
- **WHEN** `initializeApplication(...)` 调用 TimingWheel 的 provider/constructor 并收到 error
- **THEN** `initializeApplication(...)` 将该 error 返回给调用方
- **AND** `backend/cmd/server/main.go` 记录 fatal 日志并以非 0 状态码退出进程
