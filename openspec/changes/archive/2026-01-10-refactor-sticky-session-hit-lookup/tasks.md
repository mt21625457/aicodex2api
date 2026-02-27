## 1. Implementation
- [x] 1.1 Build an account lookup map in `SelectAccountWithLoadAwareness` for sticky-session checks.
- [x] 1.2 Use the map on sticky-session hit and remove the `GetByID` query.
- [x] 1.3 Add a `SelectAccountWithLoadAwareness` unit test that asserts sticky-session hit does not call `accountRepo.GetByID` (use a mock/stub repo with call counting).
