---
created: 2026-03-01T00:22:02.661Z
title: Harden WSv2 ctx_pool usage billing minimal fixes
area: api
files:
  - backend/internal/config/config.go:1353
  - backend/internal/handler/openai_gateway_handler.go:895
  - backend/internal/service/openai_ws_forwarder.go:452
  - backend/internal/service/openai_gateway_service.go:3476
  - backend/internal/repository/usage_log_repo.go:149
---

## Problem

OpenAI WSv2 + ctx_pool session path has billing consistency risks:
- Usage record tasks can be dropped under queue overflow when policy is `sample`, causing under-record and under-charge.
- Usage parsing only targets `response.completed`, which can miss terminal usage data on other terminal events.
- `RecordUsage` currently bills when usage log insert errors occur (`shouldBill := inserted || err != nil`), creating potential over-charge or ledger mismatch.

This affects both OAuth and API Key flows because both share the same AfterTurn -> RecordUsage pipeline.

## Solution

Implement minimal, low-risk changes first:
1. Immediate stop-loss via config: set `gateway.usage_record.overflow_policy=sync`.
2. In `submitUsageRecordTask`, inspect worker pool submit mode; if dropped, execute the task synchronously with timeout and warning log.
3. Expand WS usage parsing trigger from `response.completed` to include `response.done` and `response.failed`.
4. In `RecordUsage`, return early when usage log insert fails; only bill when insert succeeds.
5. Optional minimal enhancement: add deterministic fallback request id when missing to improve idempotency.

Validation:
- Add/adjust unit tests for submit fallback behavior, WS terminal usage parsing, and RecordUsage no-bill-on-insert-error branch.
- Run targeted backend tests for touched modules.
