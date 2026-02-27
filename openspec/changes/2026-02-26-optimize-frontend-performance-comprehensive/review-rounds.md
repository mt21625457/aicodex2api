## frontend-performance-comprehensive 多轮复核记录

### 第 1 轮：问题真实性复核（源码）

结论：核心问题均为真实问题，且可定位到具体文件。

已确认：
- 分包策略过粗：`frontend/vite.config.ts:72-100`
- 首屏预加载含 `vendor-misc`：`backend/internal/web/dist/index.html:10-13`
- 重页面预取：`frontend/src/composables/useRoutePrefetch.ts:24-35`
- onboarding 公共链路静态依赖：`frontend/src/components/layout/AppLayout.vue:30-43`，`frontend/src/composables/useOnboardingTour.ts:2-3`
- 公告挂载即拉数据：`frontend/src/components/common/AnnouncementBell.vue:439-442`
- `AccountsView` 静态引入过多重组件：`frontend/src/views/admin/AccountsView.vue:254-306`
- 大型模型映射硬编码：`frontend/src/composables/useModelWhitelist.ts:5-313`
- 重复鉴权：`frontend/src/views/HomeView.vue:474` 与 `frontend/src/router/index.ts:399`
- `DataTable` 重测量/排序路径：`frontend/src/components/common/DataTable.vue:201-243,483-499`
- 模板内 `sort()` 变异：
  - `frontend/src/components/account/CreateAccountModal.vue:1183`
  - `frontend/src/components/account/EditAccountModal.vue:332`
  - `frontend/src/components/account/BulkEditAccountModal.vue:372`
- `AppSidebar` 大量内联 SVG render：`frontend/src/components/layout/AppSidebar.vue:172-461`
- `adminSettingsStore.fetch` 双触发：`frontend/src/components/layout/AppSidebar.vue:589-603`

### 第 2 轮：构建产物与体积复核（二次确认）

结论：体积热点与源码问题一致，优化优先级成立。

关键体积（本地构建产物）
- `vendor-ui`: 430,775 B（gzip 142,558 B）
- `AccountsView`: 417,365 B（gzip 98,447 B）
- `OpsDashboard`: 218,636 B（gzip 47,615 B）
- `vendor-misc`: 198,727 B（gzip 70,402 B）

附加确认：
- `vendor-ui` 中确实包含 `xlsx`（构建产物可检索到 `xlsx.js` 标识）。

### 第 3 轮：误报排除与优先级校准

结论：存在两处“需要降级表述”的点，已修正进提案。

1. 关于 `vendor-ui`：
- 原判断“首屏阻塞”不准确；`index.html` 首屏 `modulepreload` 未包含 `vendor-ui`。
- 修正为“共享依赖膨胀风险”，优先级从 P0 下调为 P1。

2. 关于 `adminSettingsStore.fetch`：
- 原判断“双请求”不严格；store 的 `loading`/`loaded` 保护可避免重复请求。
- 仍保留为低优先级问题（重复触发/可维护性）。

### 第 4 轮：最优方案与兼容性复审

结论：方案已收敛为“收益最大且兼容风险最低”的路径。

- 采用 feature flag 渐进发布，而非一次性切换
- 默认保持旧行为，确保向前兼容
- 对高风险点（预取策略、模型清单来源、重页面异步化）提供单项回滚开关
- 设定量化门禁（体积、错误率、回归）后再推进全量

### 最新结论

- 二次确认结果：问题真实性成立，误报已排除。
- 最优性结论：当前提案满足“可灰度、可回滚、向前兼容、升级可控”的要求。

### 第 5 轮：灰度机制可执行性复审（本次）

发现问题：
- 提案将灰度开关写为 `VITE_*`，这属于构建期变量，不适合作为生产运行时灰度与快速回滚主路径。

修复动作：
- 将开关主路径统一调整为 `public_settings.perf_flags.*`（运行时）。
- 明确 `VITE_*` 仅作为开发/构建默认值兜底。
- 在 `proposal.md`、`design.md`、`tasks.md`、`frontend-compatibility-rollout/spec.md` 同步修复。

### 第 6 轮：最优方案收敛复审（本次）

发现问题：
- `design.md` 中“模型白名单分片或远程”表述存在策略歧义，且留有开放问题，不满足“给出最优方案并可直接执行”的要求。

修复动作：
- 收敛为“静态分片主路径 + 远程增量覆盖 + 失败回退静态快照”的确定性方案。
- 移除不必要歧义，开放问题改为“无”，并补齐迁移阶段。

### 第 7 轮：向前兼容与门禁严格性复审（本次）

发现问题：
- 原文对混部兼容（旧前端+新配置）与推进阻断条件（门禁连续性）描述不够可验证。

修复动作：
- 新增兼容矩阵（新前端+旧配置、旧前端+新配置、新前端+新配置）。
- 新增“连续 24h 门禁阈值”与“不达标禁止推进”要求。
- 在 `tasks.md` 增加混部兼容验证任务与量化门禁任务。

### 复审后结论

- 已完成 3 轮增量复审并修复全部发现问题。
- 当前提案具备：问题真实性证据、确定性最优方案、运行时灰度回滚能力、向前兼容与混部可验证门禁。
