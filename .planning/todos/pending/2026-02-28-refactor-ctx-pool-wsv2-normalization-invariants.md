---
created: 2026-02-28T23:39:28.232Z
title: Refactor ctx_pool WSv2 normalization invariants
area: api
files:
  - backend/internal/service/openai_ws_forwarder.go
  - backend/internal/service/openai_ws_state_store.go
  - backend/internal/service/openai_ws_forwarder_ingress_session_test.go
  - backend/internal/service/openai_ws_forwarder_ingress_test.go
  - backend/internal/service/openai_ws_state_store_test.go
---

## Problem

Current `ctx_pool` WSv2 continuation handling is correct in many paths, but logic is still scattered across multiple branches in ingress before-turn/recovery flows. This increases maintenance cost and can leave edge-case gaps.

Specific gaps to close:

1. Pre-send normalization is not a single unified module. `align/infer previous_response_id`, missing `function_call_output` injection, and orphan cleanup are implemented in distributed branches.
2. `response -> pending_call_ids` is currently local-memory only, so cross-instance routing can lose pending call context.
3. `previous_response_id` keep/drop decisions are call-id aware, but ambiguous states such as "has output but no pending state" need unified invariant policy and metrics.
4. Existing recovery matrix (`tool_output_not_found` single drop-prev replay, `previous_response_not_found` align then degrade) must remain unchanged.
5. Regression coverage should follow Codex normalization philosophy, especially for `aborted` synthesis, orphan cleanup, and debug/release behavior differences.

## Solution

Implement a dedicated pre-send normalizer and migration plan for `ctx_pool` WSv2:

1. Extract a single normalizer module invoked before upstream write:
   - infer/align `previous_response_id`
   - ensure missing outputs for pending calls (`output="aborted"`)
   - remove obvious orphan outputs
2. Extend state store so `response -> pending_call_ids` supports cross-instance fetch/backfill (same pattern as `session_last_response_id` with local hot cache + Redis fallback, TTL aligned with response stickiness).
3. Upgrade keep/drop evaluator to invariant-first policy:
   - prioritize call/output pairing invariants
   - define strategy for "output present but pending unknown" state
   - add metrics/log tags for invariant state and decision reason
4. Preserve current recovery matrix and existing fallback semantics.
5. Expand ingress tests to codify normalization invariants and edge paths.
