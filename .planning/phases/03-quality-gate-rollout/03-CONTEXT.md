# Phase 3: Quality Gate & Rollout - Context

**Gathered:** 2026-02-28
**Status:** Ready for planning

<domain>
## Phase Boundary

本阶段只做质量收敛：测试、回归清单和上线前验收，不再新增功能字段。

</domain>

<decisions>
## Implementation Decisions

### 测试策略
- 后端覆盖混选校验与新增字段通路。
- 前端覆盖 payload 构建关键路径（开启、关闭、未勾选）。

### 验收策略
- 以管理员真实操作路径为核心：筛选同类型 -> 勾选多账号 -> 批量提交 -> 列表复核。

### Claude's Discretion
- 前端单测可通过抽取纯函数降低组件测试成本。

</decisions>

<specifics>
## Specific Ideas

- 增加手工验收清单：
  - 同类型 OpenAI OAuth 成功
  - OpenAI OAuth + API Key 混选失败
  - 开启后再关闭回滚成功

</specifics>

<deferred>
## Deferred Ideas

- 加入 E2E 自动化（Playwright）覆盖管理员批量编辑流程。

</deferred>

---

*Phase: 03-quality-gate-rollout*
*Context gathered: 2026-02-28*
