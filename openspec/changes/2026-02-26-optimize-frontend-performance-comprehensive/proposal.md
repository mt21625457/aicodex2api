# Change: 前端性能全面优化与兼容升级治理

## Why

对 `frontend/` 与当前构建产物进行二次复核后，确认存在一组“高概率影响真实用户体验”的性能问题，主要集中在以下四类：

1. 首屏与公共链路负担偏高（分包与公共组件加载策略）
2. 管理后台重页面体积过大（账户页、模型映射、重弹窗）
3. 运行时存在可避免的重复/重计算（重复鉴权、冗余触发、重排序/测量）
4. 缺少可灰度、可回滚的前端性能发布门禁

构建产物关键体积（2026-02-26 本地复核）：
- `vendor-ui-CAt8eLho.js`：430,775 B（gzip 142,558 B）
- `AccountsView-BUXw2FOq.js`：417,365 B（gzip 98,447 B）
- `OpsDashboard-BtQ5fWkO.js`：218,636 B（gzip 47,615 B）
- `vendor-misc-DPWBSk0M.js`：198,727 B（gzip 70,402 B）

## What Changes

### A. 二次确认结果（含误报排除）

| 编号 | 复核结论 | 证据 | 处理优先级 |
|---|---|---|---|
| FEP-01 分包粒度粗，`vendor-misc` 进入首屏预加载 | 真问题 | `frontend/vite.config.ts:72-100`，`backend/internal/web/dist/index.html:10-13` | P0 |
| FEP-02 `@vueuse` 与 `xlsx` 混包（`vendor-ui`） | 真问题（但非“首屏阻塞”） | `frontend/vite.config.ts:85-87`，`backend/internal/web/dist/assets/vendor-ui-*.js` | P1 |
| FEP-03 路由预取过激，包含重页面（`/admin/accounts`） | 真问题 | `frontend/src/composables/useRoutePrefetch.ts:24-35` | P0 |
| FEP-04 `AppLayout` 全局挂载 onboarding（`driver.js`） | 真问题 | `frontend/src/components/layout/AppLayout.vue:30-43`，`frontend/src/composables/useOnboardingTour.ts:2-3` | P1 |
| FEP-05 `AnnouncementBell` 常驻 + 挂载即拉公告 | 真问题 | `frontend/src/components/layout/AppHeader.vue:27`，`frontend/src/components/common/AnnouncementBell.vue:317-318,439-442` | P0 |
| FEP-06 `AccountsView` 静态导入过多重组件/弹窗 | 真问题 | `frontend/src/views/admin/AccountsView.vue:254-306` | P0 |
| FEP-07 模型白名单与预设映射超大硬编码 | 真问题 | `frontend/src/composables/useModelWhitelist.ts:5-313` | P0 |
| FEP-08 `HomeView` 与 router 重复 `checkAuth` | 真问题（收益中等） | `frontend/src/views/HomeView.vue:474`，`frontend/src/router/index.ts:399` | P2 |
| FEP-09 `DataTable` 存在较重测量与客户端排序开销 | 真问题 | `frontend/src/components/common/DataTable.vue:201-243,483-499` | P1 |
| FEP-10 模板内 `sort()` 原地变异数组 | 真问题 | `frontend/src/components/account/CreateAccountModal.vue:1183`，`frontend/src/components/account/EditAccountModal.vue:332`，`frontend/src/components/account/BulkEditAccountModal.vue:372` | P1 |
| FEP-11 `AppSidebar` 内联大量 SVG render 函数 | 真问题 | `frontend/src/components/layout/AppSidebar.vue:172-461` | P1 |
| FEP-12 `adminSettingsStore.fetch` 双触发 | 真问题（低风险） | `frontend/src/components/layout/AppSidebar.vue:589-603`，`frontend/src/stores/adminSettings.ts:51-54` | P2 |

误报排除（本提案已修正）：
- “`vendor-ui` 是首屏 `modulepreload` 关键阻塞”不成立；当前首屏预加载是 `vendor-vue/vendor-misc/vendor-i18n`。因此本项降级为“共享依赖膨胀”问题处理。
- “`adminSettingsStore.fetch` 一定导致双网络请求”不严格成立；store 有 `loading` 保护。但重复触发仍增加无效调用与维护复杂度，保留优化。

### B. 最优解决方案（按阶段）

- **P0（先做）**
  - 重构 `manualChunks`：拆分 `vendor-misc`，移除非关键公共依赖的首屏绑定
  - `AccountsView` 及重弹窗改为按需异步加载
  - 预取策略从“固定邻接”改为“自适应（网络/设备/路由体积）”
  - 公告铃铛改“打开时拉取 + 轻量未读数预热”，避免挂载即重请求
  - 模型白名单改为“平台分片 + 按需加载 + 缓存兜底”

- **P1（随后）**
  - onboarding（`driver.js`）改懒加载，去公共链路静态依赖
  - `DataTable` 改轻量测量策略；大数据量默认服务端排序
  - 修复模板 `sort()` 变异（改为不可变排序）
  - `AppSidebar` 图标改为统一图标组件/资源映射，减少大段内联 render 函数

- **P2（收尾）**
  - 合并鉴权初始化入口，移除 `HomeView` 重复 `checkAuth`
  - 去除 `adminSettingsStore.fetch` 双入口触发，保留单入口

### C. 向前兼容与平滑升级（必须项）

新增灰度开关（运行时开关，默认保持旧行为，保证升级无中断）：
- `public_settings.perf_flags.prefetch_mode=legacy|adaptive|off`（默认 `legacy`）
- `public_settings.perf_flags.announcement_lazy_enabled=false`
- `public_settings.perf_flags.onboarding_lazy_enabled=false`
- `public_settings.perf_flags.accounts_async_modals_enabled=false`
- `public_settings.perf_flags.model_whitelist_remote_enabled=false`
- `public_settings.perf_flags.datatable_lightweight_enabled=false`
- `VITE_*` 仅作为本地开发与构建期默认值，不作为生产灰度主开关

发布顺序：
1. 先上可观测（仅埋点，不改行为）
2. 再开小流量灰度（10%）
3. 稳定后扩大到 50%
4. 达门禁后全量

门禁阈值（连续 24 小时满足才可进入下一阶段）：
- `/admin/accounts` 首次 JS 下载量下降 >= 30%（相对基线）
- 前端错误率相对基线上升 < 0.05%
- 关键流程（登录、账户增改删、公告、引导）回归用例通过率 100%

回滚原则：任一门禁不达标，单开关回退，不影响其余能力。

兼容矩阵：
- 新前端 + 旧配置：按默认值走旧行为（兼容）
- 新前端 + 新配置：按灰度开关启用新能力
- 旧前端 + 新配置：忽略未知 `perf_flags` 字段（兼容）

## Capabilities

### New Capabilities
- `frontend-bundle-optimization`
- `frontend-runtime-performance`
- `frontend-compatibility-rollout`

## Impact

- Affected specs:
  - `frontend-bundle-optimization`
  - `frontend-runtime-performance`
  - `frontend-compatibility-rollout`
- Affected code:
  - `frontend/vite.config.ts`
  - `frontend/src/router/*`
  - `frontend/src/components/layout/*`
  - `frontend/src/components/common/*`
  - `frontend/src/views/admin/*`
  - `frontend/src/composables/*`
  - `frontend/src/stores/*`
- 外部 API：无协议变更
- 数据兼容：保持向前兼容；新路径全部可开关回退
