# schedule-account Specification

## Purpose
TBD - 由归档变更 refactor-sticky-session-hit-lookup 创建；后续归档后补充本规范的目的说明。
## Requirements
### Requirement: Sticky-session 命中复用可调度账号列表
调度器 SHALL 从请求已加载的“可调度账号列表”中解析 sticky-session 账号选择，并且当 sticky 账号已存在于该列表时，SHALL NOT 额外发起按账号 ID 查询数据库的请求。

#### Scenario: Sticky session 命中且不额外查询数据库
- **WHEN** 调度请求包含 sticky session，且该 sticky session 指向的账号存在于可调度账号列表中
- **THEN** 调度器复用该内存中的账号数据，并且不再按账号 ID 查询数据库
