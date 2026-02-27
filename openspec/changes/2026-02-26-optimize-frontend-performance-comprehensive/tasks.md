## 0. 基线与门禁准备

- [ ] 0.1 在 `frontend/` 执行构建并产出基线报告（chunk 原始体积 + gzip 体积 + 路由首进加载链路）
- [ ] 0.2 增加前端性能观测指标（页面级加载耗时、路由跳转耗时、JS 错误率）
- [ ] 0.3 定义并固化灰度门禁阈值（`/admin/accounts` 体积、错误率、回归用例）
- [ ] 0.4 固化推进门禁（连续 24h 满足：`/admin/accounts` 首次 JS 下载量下降 >= 30%、错误率增幅 < 0.05%、关键回归用例 100% 通过）

## 1. 分包与重页面优化（P0）

- [ ] 1.1 `frontend/vite.config.ts`：重构 `manualChunks`，拆分 `vendor-misc` 与 `vendor-ui`，将 `xlsx` 独立为低频 chunk
- [ ] 1.2 `frontend/src/views/admin/AccountsView.vue`：将低频重组件（弹窗/图表）改为异步加载
- [ ] 1.3 `frontend/src/composables/useModelWhitelist.ts`：改造为平台分片加载；保留本地快照兜底
- [ ] 1.4 `frontend/src/components/common/AnnouncementBell.vue`：移除挂载即全量拉取，改为打开时加载详情、可选轻量未读数预热
- [ ] 1.5 重新构建并对比：确认 `AccountsView` 与公共 chunk 体积下降达到目标

## 2. 运行时性能优化（P1）

- [ ] 2.1 `frontend/src/composables/useRoutePrefetch.ts`：新增 `adaptive` 模式，低资源设备跳过重页面预取
- [ ] 2.2 `frontend/src/components/layout/AppLayout.vue` + `frontend/src/composables/useOnboardingTour.ts`：引导能力改惰性初始化
- [ ] 2.3 `frontend/src/components/common/DataTable.vue`：限制重测量触发条件并节流；大数据量默认服务端排序
- [ ] 2.4 `frontend/src/components/layout/AppSidebar.vue`：图标定义从内联 render 函数迁移到统一图标组件/映射
- [ ] 2.5 `frontend/src/components/account/CreateAccountModal.vue`：修复模板 `sort()` 原地变异
- [ ] 2.6 `frontend/src/components/account/EditAccountModal.vue`：修复模板 `sort()` 原地变异
- [ ] 2.7 `frontend/src/components/account/BulkEditAccountModal.vue`：修复模板 `sort()` 原地变异

## 3. 冗余调用与兼容治理（P2）

- [ ] 3.1 `frontend/src/views/HomeView.vue`：移除与 router 重复的 `authStore.checkAuth()` 初始化路径
- [ ] 3.2 `frontend/src/components/layout/AppSidebar.vue`：保留单入口触发 `adminSettingsStore.fetch()`（去除重复触发）
- [ ] 3.3 为关键优化增加运行时开关读取（`public_settings.perf_flags.*`），并保留 `VITE_*` 作为默认值兜底
- [ ] 3.4 完善“新路径失败回退旧路径”的兜底逻辑（特别是模型清单加载）
- [ ] 3.5 `backend` 公共设置接口新增可选字段 `perf_flags`（非必填、非破坏性），验证旧前端忽略未知字段

## 4. 灰度发布与验收

- [ ] 4.1 10% 灰度开启：`public_settings.perf_flags.accounts_async_modals_enabled=true` + `public_settings.perf_flags.announcement_lazy_enabled=true`
- [ ] 4.2 达标后扩大到 50%，再全量；期间持续监控错误率与关键页面指标
- [ ] 4.3 灰度稳定后启用 `public_settings.perf_flags.prefetch_mode=adaptive`
- [ ] 4.4 完成回归测试：`pnpm --dir frontend run lint:check`、`pnpm --dir frontend run typecheck`、关键路由手工冒烟
- [ ] 4.5 记录回滚手册（按开关逐项回退）并验证回退有效
- [ ] 4.6 混部兼容验证：新前端+旧配置、旧前端+新配置、新前端+新配置三种组合均通过冒烟

## 5. 收尾与文档

- [ ] 5.1 更新性能优化文档（含“已修复问题清单、收益、风险、回滚命令”）
- [ ] 5.2 执行 `openspec validate 2026-02-26-optimize-frontend-performance-comprehensive --strict`
- [ ] 5.3 提交最终验收结论（是否满足“向前兼容、升级无中断”）
