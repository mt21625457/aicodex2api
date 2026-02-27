# Change: Avoid extra DB lookup on sticky session hit

## Why
Sticky-session hits in `SelectAccountWithLoadAwareness` currently call `accountRepo.GetByID` even though the candidate accounts are already loaded in `listSchedulableAccounts`. This adds a redundant DB query on the hot path, increasing latency and DB load.

## What Changes
- Build a map of `accountID -> *Account` from the schedulable accounts list.
- On sticky-session hit, use the in-memory map to validate group/platform/model support and attempt slot acquisition without an extra DB lookup.
- Keep behavior unchanged when the sticky account is not in the candidate set (fall back to load-aware selection).

## Impact
- Affected specs: `schedule-account`
- Affected code: `backend/internal/service/gateway_service.go`
