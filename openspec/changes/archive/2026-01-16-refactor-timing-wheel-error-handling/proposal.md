# Change: 重构 TimingWheelService 错误处理

## Why

`NewTimingWheelService()` 初始化失败时直接 `panic(err)`，与项目中其他模块"返回 error + 上层处理"的错误处理风格不一致。这种做法在极端情况下会导致进程崩溃，且不给上层调用者处理错误的机会。

**问题代码位置**：`backend/internal/service/timing_wheel_service.go:27`

```go
tw, err := collection.NewTimingWheel(...)
if err != nil {
    panic(err)  // 问题所在
}
```

## What Changes

- 修改 `NewTimingWheelService()` 函数签名为 `(*TimingWheelService, error)`
- 移除 panic 调用，改为返回 error
- 更新所有调用方以处理返回的 error
- 确保应用启动时正确处理 TimingWheel 初始化失败的情况

## Impact

- Affected specs: `timing-wheel`
- Affected code:
  - `backend/internal/service/timing_wheel_service.go` - 核心修改
  - `backend/internal/service/wire.go` - Provider 签名/返回值需要调整以透传 error
  - `backend/cmd/server/wire_gen.go` - Wire 生成文件会随 Provider 变化而更新（需要重新生成）
  - `backend/cmd/server/main.go` - `initializeApplication(...)` 返回 error 时会 `log.Fatalf(...)` 并退出（非 0）
  - 任何其他直接调用 `NewTimingWheelService()` 的代码（需统一处理返回的 error）

**生成文件注意事项**：修改 `backend/internal/service/wire.go` 后，需要运行 `cd backend && go generate ./cmd/server` 重新生成 `backend/cmd/server/wire_gen.go`。
