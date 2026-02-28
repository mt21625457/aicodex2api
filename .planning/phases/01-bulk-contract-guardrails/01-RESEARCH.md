# Phase 1 Research: Bulk Contract & Guardrails

**Date:** 2026-02-28

## Current State Findings

1. Existing bulk endpoint already supports JSON merge:
- Handler request contains `Credentials map[string]any` and `Extra map[string]any`.
- Repository merges JSONB using `COALESCE(... ) || $n::jsonb`.

2. Single-account OpenAI edit uses these `extra` keys:
- `openai_passthrough`
- `openai_oauth_responses_websockets_v2_mode` / `openai_apikey_responses_websockets_v2_mode`
- `openai_oauth_responses_websockets_v2_enabled` / `openai_apikey_responses_websockets_v2_enabled`
- `codex_cli_only`

3. Gaps for this feature:
- Bulk request path lacks explicit `auto_pause_on_expired` support.
- No same-type guard when OpenAI-specific keys are sent in bulk update.
- Frontend bulk modal currently cannot edit these OpenAI options.

## Implementation Implications

- No DB migration needed.
- Risk center is behavioral safety (mixed selection writes wrong fields).
- Best guardrail is server-side validation against actual `account_ids` snapshot.

## Recommended Verification

- Handler/service unit tests for mixed-type rejection and valid-path acceptance.
- Repository/service tests confirming `auto_pause_on_expired` is persisted in bulk path.
- Preserve existing response contract (`success_ids`/`failed_ids`).
