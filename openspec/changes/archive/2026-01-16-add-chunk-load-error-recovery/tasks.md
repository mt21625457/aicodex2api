## 1. 代码实现

- [x] 1.1 在 `router.onError` 中添加 chunk 加载错误检测逻辑
- [x] 1.2 检测多种错误消息模式（`Failed to fetch dynamically imported module`、`Loading chunk`、`Loading CSS chunk`、`ChunkLoadError`）
- [x] 1.3 使用 `sessionStorage` 记录刷新时间戳，防止无限刷新循环
- [x] 1.4 设置 10 秒冷却时间，避免网络问题导致的快速重复刷新

## 2. 测试验证

- [x] 2.1 TypeScript 类型检查通过
- [x] 2.2 手动测试：模拟 chunk 加载失败，验证自动刷新行为
- [x] 2.3 手动测试：验证 10 秒内不会重复刷新
