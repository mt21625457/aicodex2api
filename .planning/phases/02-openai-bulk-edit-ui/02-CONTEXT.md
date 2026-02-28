# Phase 2: OpenAI Bulk Edit UI - Context

**Gathered:** 2026-02-28
**Status:** Ready for planning

<domain>
## Phase Boundary

本阶段仅改造管理端批量编辑弹窗与相关前端提交逻辑，让 OpenAI 单账号编辑中的关键开关可被批量编辑。后端契约以 Phase 1 为前提，不在此阶段重复改后端能力。

</domain>

<decisions>
## Implementation Decisions

### OpenAI 批量字段范围
- 自动透传（开/关）
- WS mode（off/shared/dedicated）
- codex_cli_only（仅 OAuth）
- auto_pause_on_expired（通用开关）

### 交互与提交语义
- 继续沿用“字段开关勾选后才写入请求体”。
- 关闭类字段要显式发 false/off，确保可从已开启状态回滚。
- 发生后端同类型校验错误时，在弹窗内给出明确反馈。

### Claude's Discretion
- 字段布局可按当前 `BulkEditAccountModal` 分区风格调整。
- 文案可精简，但必须明确“OpenAI 专属且需同类型选择”。

</decisions>

<specifics>
## Specific Ideas

- 与单编辑页键保持一致，减少后端解释分歧。
- `codex_cli_only` 控件只在 OAuth 批量场景启用。

</specifics>

<deferred>
## Deferred Ideas

- 在弹窗内展示所选账号类型分布与可编辑能力矩阵。

</deferred>

---

*Phase: 02-openai-bulk-edit-ui*
*Context gathered: 2026-02-28*
