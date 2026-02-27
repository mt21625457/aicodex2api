## ADDED Requirements

### Requirement: WS 消息按需解析
系统 SHALL 对 WebSocket 客户端消息使用 `gjson.GetBytes` 按需提取只读字段（`type`、`model`、`prompt_cache_key`、`previous_response_id` 等），而非每条消息都 `json.Unmarshal` 到 `map[string]any`。仅在需要修改 payload 字段时才退回到全量反序列化。

#### Scenario: 只读消息（不修改 payload）
- **WHEN** WebSocket 收到客户端消息且不需要修改任何字段
- **THEN** 系统仅通过 `gjson.GetBytes` 提取所需字段，不执行 `json.Unmarshal`，零额外堆分配

#### Scenario: 需要修改 payload 的消息（如 model 字段映射）
- **WHEN** WebSocket 收到客户端消息且需要修改 `model` 字段或其他字段
- **THEN** 系统使用 `sjson.SetBytes` 做增量修改，或在多字段修改时退回到 `json.Unmarshal` + `json.Marshal`

---

### Requirement: HTTP 请求体解析结果缓存回写
系统 SHALL 在 `getOpenAIRequestBodyMap` 首次解析请求体后，将结果通过 `c.Set(OpenAIParsedRequestBodyKey, reqBody)` 回写到 gin context 缓存，确保同一请求内的后续调用可命中缓存。

#### Scenario: 首次解析请求体
- **WHEN** `getOpenAIRequestBodyMap` 被调用且 gin context 中无缓存
- **THEN** 系统执行 `json.Unmarshal` 解析，并将结果 `c.Set` 写入 context 缓存后返回

#### Scenario: 后续调用命中缓存
- **WHEN** 同一请求中第二次调用 `getOpenAIRequestBodyMap`
- **THEN** 系统直接从 gin context 缓存返回，不执行 `json.Unmarshal`

---

### Requirement: 双重粘性会话查询消除
系统 SHALL 在 `SelectAccountWithLoadAwareness` 入口处查询 `GetSessionAccountID` 后，将结果通过 `OpenAIAccountScheduleRequest` 传递给下游调度器，下游 SHALL 优先使用已传入的结果而非再次查询 Redis。

#### Scenario: 入口已获取粘性会话 accountID
- **WHEN** `SelectAccountWithLoadAwareness` 入口查询到 `stickyAccountID`
- **THEN** 该 ID 通过请求参数传递到 `selectBySessionHash`/`tryStickySessionHit`，下游不再重复查询 Redis

#### Scenario: 入口未查到粘性会话
- **WHEN** 入口处 `GetSessionAccountID` 返回空
- **THEN** 下游调度器按正常负载均衡流程选择账号，不执行额外 Redis 查询

---

### Requirement: 会话哈希使用非密码学算法
系统 SHALL 对用于会话粘性映射的哈希使用 `xxhash.Sum64String` 替代 `sha256.Sum256`。仅保留密码学场景（API Key 哈希、幂等键）使用 SHA-256。为保证滚动发布兼容性，系统 SHALL 实现“新 key 优先读取 + 旧 key 回退读取 + 兼容期双写”。

#### Scenario: 会话哈希生成
- **WHEN** 需要为 sessionID 生成粘性会话哈希
- **THEN** 系统使用 `xxhash.Sum64String(sessionID)` 并转为十六进制字符串，不使用 `crypto/sha256`

#### Scenario: 滚动升级兼容读取
- **WHEN** 新 hash key 未命中粘性会话，但旧版本仍可能写入 SHA-256 key
- **THEN** 系统回退读取旧 SHA-256 key，避免升级窗口内粘性会话失配

#### Scenario: 兼容期写入
- **WHEN** 系统绑定或刷新粘性会话
- **THEN** 系统同时写入新 hash key 与旧 SHA-256 key（旧 key 使用较短 TTL），兼容期结束后下线旧 key 写入

---

### Requirement: 会话哈希与 metadata 兼容开关门禁
系统 SHALL 为兼容路径提供可回退特性开关（`session_hash_read_old_fallback`、`session_hash_dual_write_old`、`metadata_bridge_enabled`），默认值均为 `true`。系统 SHALL 按“先关旧写、后关旧读、最后关 metadata 桥接”的顺序下线；每一步下线前 SHALL 满足门禁条件：旧路径命中率连续 7 天 `< 0.1%` 且无兼容性告警。

#### Scenario: 兼容开关回滚
- **WHEN** 灰度期间出现粘性会话失配或旧读取点异常
- **THEN** 运维可立即重新开启对应兼容开关，系统恢复旧路径读取/写入能力

#### Scenario: 关闭旧写门禁
- **WHEN** `session_hash_dual_write_old` 准备关闭
- **THEN** 系统先确认旧 key 回退命中率连续 7 天 `< 0.1%`，关闭后继续保留 `session_hash_read_old_fallback=true`

#### Scenario: 关闭 metadata 兼容桥接门禁
- **WHEN** `metadata_bridge_enabled` 准备关闭
- **THEN** 系统确认旧 `ctxkey.*` 回退读取命中率连续 7 天 `< 0.1%` 且无错误回归，再执行下线

---

### Requirement: 每请求 DNS 查询缓存
系统 SHALL 在 `validatedTransport.RoundTrip` 中对已通过 `ValidateResolvedIP` 验证的主机名缓存验证结果（TTL 30 秒），避免每请求都执行完整 DNS 查询。

#### Scenario: 已缓存的主机名
- **WHEN** 请求目标主机在 30 秒内已通过 IP 验证
- **THEN** 系统直接放行请求，不执行 DNS 查询

#### Scenario: 缓存过期或未知主机
- **WHEN** 请求目标主机不在缓存中或缓存已过期
- **THEN** 系统执行 `ValidateResolvedIP` 完整 DNS 查询，验证通过后写入缓存

#### Scenario: 验证失败清除缓存
- **WHEN** 某主机名此前缓存为通过，但新一次 DNS 查询返回内网 IP
- **THEN** 系统拒绝请求并清除该主机的缓存条目

---

### Requirement: 请求体预分配读取统一化
系统 SHALL 将 `readRequestBodyWithPrealloc` 提升为公共函数，所有 Handler 的请求体读取 SHALL 使用该函数替代 `io.ReadAll`，根据 `Content-Length` 预分配 buffer 容量。

#### Scenario: 已知 Content-Length 的请求
- **WHEN** 请求带有 `Content-Length` 头
- **THEN** 系统以 `Content-Length` 值预分配 buffer（上限 `openAIRequestBodyReadMaxInitCap`），一次性读取无需扩容

#### Scenario: 未知 Content-Length 的请求
- **WHEN** 请求无 `Content-Length` 头（如 chunked transfer）
- **THEN** 系统以 `openAIRequestBodyReadInitCap`（512 字节）为初始容量，按需扩容

---

### Requirement: context.WithValue 调用合并
系统 SHALL 在 `Messages()` 方法中将多个请求属性（`IsMaxTokensOneHaikuRequest`、`ThinkingEnabled`、`PrefetchedStickyAccountID`、`PrefetchedStickyGroupID`、`SingleAccountRetry` 等）合并为一个 `RequestMetadata` 结构体，通过单次 `context.WithValue` 注入。兼容期 SHALL 保留旧 `ctxkey.*` 键写入与读取回退。

#### Scenario: Messages 请求属性注入
- **WHEN** `Messages()` 方法需要向 context 注入多个请求属性
- **THEN** 系统构建一个 `RequestMetadata` 结构体并通过一次 `context.WithValue` 注入，context 链深度增加 1 而非 5+

#### Scenario: 旧读取点兼容
- **WHEN** 下游代码仍按旧 `ctxkey.*` 读取请求属性
- **THEN** 系统可通过兼容注入或读取回退获取正确值，不出现行为回归

---

### Requirement: 请求体增量 patch
系统 SHALL 在 `bodyModified` 场景中优先使用 `sjson.SetBytes`/`sjson.DeleteBytes` 对原始 `[]byte` 做增量修改，而非 `json.Marshal(map[string]any)` 全量重序列化。仅在多字段复杂修改时退回到全量序列化。

#### Scenario: 单字段删除
- **WHEN** 仅需删除 `max_output_tokens` 字段
- **THEN** 系统使用 `sjson.DeleteBytes(body, "max_output_tokens")` 直接操作原始 bytes

#### Scenario: 单字段修改
- **WHEN** 仅需修改 `model` 字段值
- **THEN** 系统使用 `sjson.SetBytes(body, "model", newModel)` 直接操作原始 bytes

#### Scenario: 多字段复杂修改
- **WHEN** 需要修改 3 个以上字段或涉及嵌套结构修改
- **THEN** 系统退回到 `json.Unmarshal` + 修改 + `json.Marshal` 的全量路径

---

### Requirement: body 二次 Unmarshal 消除
系统 SHALL 在 `SetClaudeCodeClientContext` 中复用上游已解析的请求体结果（通过 gin context 传递），而非对同一 `body` 再做一次 `json.Unmarshal`。

#### Scenario: Claude Code 客户端请求验证
- **WHEN** `SetClaudeCodeClientContext` 需要验证请求体内容
- **THEN** 系统从 gin context 中读取已解析的结果，或使用 `gjson` 按需提取验证所需字段，不执行完整 `json.Unmarshal`
