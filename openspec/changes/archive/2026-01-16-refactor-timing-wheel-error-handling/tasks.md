## 1. 代码修改

- [x] 1.1 修改 `NewTimingWheelService()` 返回类型为 `(*TimingWheelService, error)`
- [x] 1.2 将 `panic(err)` 替换为 `return nil, fmt.Errorf("failed to create timing wheel: %w", err)`
- [x] 1.3 添加 `fmt` 包的 import（如果尚未导入）
- [x] 1.4 （可选增强）引入可注入的 TimingWheel factory（例如包级变量/私有构造函数），便于单测覆盖失败分支

## 2. 调用方更新

- [x] 2.1 查找所有 `NewTimingWheelService()` 的调用位置
- [x] 2.2 更新调用方以处理返回的 error
- [x] 2.3 修改 `ProvideTimingWheelService()` 返回类型为 `(*TimingWheelService, error)`，并在成功后 `Start()`
- [x] 2.4 重新生成 Wire：`cd backend && go generate ./cmd/server`（更新 `backend/cmd/server/wire_gen.go`）
- [x] 2.5 确保应用启动失败时有清晰的错误日志（当前 `backend/cmd/server/main.go` 会 `log.Fatalf("Failed to initialize application: %v", err)` 并退出）

## 3. 测试验证

- [x] 3.1 编译验证，确保没有编译错误
- [x] 3.2 运行现有测试，确保不破坏现有功能
- [x] 3.3 手动测试应用启动正常
- [x] 3.4 （可选增强）新增单测：模拟 `collection.NewTimingWheel()` 返回 error，验证 `NewTimingWheelService()` 不 panic 且返回 error
