# Phase 1: Bulk Contract & Guardrails - Context

**Gathered:** 2026-02-28
**Status:** Ready for planning

<domain>
## Phase Boundary

本阶段只处理后端批量更新契约与安全边界，不做前端交互改版。重点是让批量接口具备 OpenAI 专属字段的同类型保护，并补齐 `auto_pause_on_expired` 的批量管道。

</domain>

<decisions>
## Implementation Decisions

### OpenAI 专属字段保护
- 当批量请求中包含 OpenAI 专属 `extra` 字段（透传、WS mode、codex_cli_only）时，`account_ids` 必须全部为 `platform=openai`。
- 且账号 `type` 必须一致（同为 `oauth` 或同为 `apikey`）。
- 不满足条件时返回明确错误，不做写入。

### 批量关闭语义
- 关闭透传、关闭 codex_cli_only 使用显式 `false`。
- 关闭 WS mode 使用 `off` 且 enabled 字段写 `false`。

### auto_pause_on_expired
- 作为顶层字段进入批量更新管道（handler -> service -> repository）。
- 仍保持“未传不改”的指针语义。

### Claude's Discretion
- 具体错误文案可按现有 API 错误风格统一。
- 校验实现层（handler 或 service）可按可测试性选择，但需单测覆盖。

</decisions>

<specifics>
## Specific Ideas

- 对齐单账号编辑使用的键：
  - `openai_passthrough`
  - `openai_oauth_responses_websockets_v2_mode`
  - `openai_apikey_responses_websockets_v2_mode`
  - `openai_oauth_responses_websockets_v2_enabled`
  - `openai_apikey_responses_websockets_v2_enabled`
  - `codex_cli_only`

</specifics>

<deferred>
## Deferred Ideas

- 批量编辑弹窗中增加“所选账号类型统计/能力预览”接口（可在后续体验优化阶段处理）。

</deferred>

---

*Phase: 01-bulk-contract-guardrails*
*Context gathered: 2026-02-28*
