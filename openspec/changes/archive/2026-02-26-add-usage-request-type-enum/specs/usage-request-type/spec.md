## ADDED Requirements

### Requirement: 系统必须以 request_type 作为使用记录类型的主事实源
系统 MUST 在 `usage_logs` 中持久化 `request_type` 枚举字段，并将其作为类型展示与筛选的主事实源。

#### Scenario: 新增记录写入 request_type
- **WHEN** 网关记录一条新的 usage 日志
- **THEN** 系统 MUST 写入有效的 `request_type` 枚举值
- **AND** 枚举值 MUST 在约束集合内（`unknown/sync/stream/ws_v2`）

#### Scenario: 读取优先 request_type
- **WHEN** 系统读取 usage 日志用于 API 返回
- **THEN** 系统 MUST 优先使用 `request_type` 作为类型来源

### Requirement: 系统必须保持与旧字段兼容
系统 MUST 在迁移期保持 `stream` 与 `openai_ws_mode` 的向后兼容能力。

#### Scenario: 旧字段仍保留
- **WHEN** 新版本后端返回 usage 记录
- **THEN** 响应 MUST 继续包含 `stream` 与 `openai_ws_mode`

#### Scenario: request_type 缺失时回退
- **WHEN** 历史记录 `request_type` 为 `unknown` 或不可用
- **THEN** 系统 MUST 按旧字段推导类型
- **AND** 推导规则 MUST 与既有展示口径一致

#### Scenario: 响应字段保持兼容一致
- **WHEN** 系统返回一条包含 `request_type` 的 usage 记录
- **THEN** 响应中的 `stream` 与 `openai_ws_mode` MUST 与 `request_type` 保持一致映射
- **AND** `request_type=ws_v2` MUST 对应 `openai_ws_mode=true`
- **AND** `request_type=stream` MUST 对应 `openai_ws_mode=false && stream=true`
- **AND** `request_type=sync` MUST 对应 `openai_ws_mode=false && stream=false`

### Requirement: 系统必须支持 request_type 查询过滤并兼容 stream 参数
系统 MUST 提供 `request_type` 过滤能力，并继续兼容历史 `stream` 参数。

#### Scenario: 使用 request_type 过滤列表
- **WHEN** 客户端请求携带 `request_type`
- **THEN** 系统 MUST 按 `request_type` 执行过滤

#### Scenario: request_type 参数非法值
- **WHEN** 客户端请求携带非法 `request_type`（不在 `unknown/sync/stream/ws_v2` 中）
- **THEN** 系统 MUST 返回 `400 Bad Request`
- **AND** 错误信息 MUST 提示可接受枚举值

#### Scenario: 旧客户端使用 stream 过滤
- **WHEN** 客户端仅携带 `stream`
- **THEN** 系统 MUST 保持历史过滤行为不变

#### Scenario: 同时携带 request_type 与 stream
- **WHEN** 请求同时携带 `request_type` 与 `stream`
- **THEN** 系统 MUST 优先按 `request_type` 过滤

#### Scenario: request_type 过滤覆盖所有 usage 入口
- **WHEN** 客户端访问 usage 列表/统计/趋势/模型/清理任务入口并携带 `request_type`
- **THEN** 系统 MUST 在对应入口应用一致的 `request_type` 过滤语义

### Requirement: 历史数据迁移必须可在线执行且不破坏旧逻辑
系统 MUST 提供可在线迁移的回填方案，使历史数据具备 `request_type`，且迁移前后展示口径一致。

#### Scenario: 历史回填映射
- **WHEN** 执行历史数据回填
- **THEN** `openai_ws_mode=true` MUST 映射为 `ws_v2`
- **AND** `openai_ws_mode=false && stream=true` MUST 映射为 `stream`
- **AND** 其他情况 MUST 映射为 `sync`

#### Scenario: 分批回填
- **WHEN** 数据量较大
- **THEN** 回填 MUST 支持分批执行以降低锁与性能风险

### Requirement: 前端必须在新旧后端间保持显示一致
前端 MUST 支持 `request_type` 优先展示，并在老后端响应中自动回退旧字段推导。

#### Scenario: 新后端响应
- **WHEN** 响应包含 `request_type`
- **THEN** 前端 MUST 使用 `request_type` 渲染类型标签与样式

#### Scenario: 老后端响应
- **WHEN** 响应不包含 `request_type`
- **THEN** 前端 MUST 使用旧字段推导类型
- **AND** 渲染结果 MUST 与升级前一致

### Requirement: 升级与回滚必须可独立进行
系统 MUST 支持数据库、后端、前端分阶段升级与独立回滚，不要求一次性切换。

#### Scenario: 后端先回滚
- **WHEN** 新数据库已上线但后端回滚到旧版本
- **THEN** 系统 MUST 继续可用
- **AND** 旧字段语义 MUST 保持不变

#### Scenario: 前端先升级
- **WHEN** 前端升级但后端尚未返回 `request_type`
- **THEN** 前端 MUST 通过回退逻辑保持功能正常
