# Change: 添加前端 Chunk 加载错误自动恢复机制

## Why

当前端应用重新部署后，用户浏览器可能缓存了旧版本的 `index.html`，其中引用的 JS chunk 文件（如 `DashboardView-CG6GXl8p.js`）在服务器上已被新版本替换。当用户导航到使用懒加载的路由时，会触发以下错误：

```
Failed to fetch dynamically imported module: https://api.aicodex.top:8443/assets/DashboardView-CG6GXl8p.js
```

这导致用户无法正常使用应用，必须手动刷新页面或清除缓存。

## What Changes

- 在 Vue Router 的 `onError` 钩子中添加 chunk 加载错误检测
- 检测到 chunk 加载失败时自动刷新页面以获取最新资源
- 使用 `sessionStorage` 防止无限刷新循环（10 秒内只允许一次自动刷新）
- 刷新仍失败时输出清晰的控制台错误提示

## Impact

- Affected specs: `frontend-routing`（新增 capability）
- Affected code:
  - `frontend/src/router/index.ts` - 增强 `router.onError` 处理逻辑
