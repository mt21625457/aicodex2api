## Context

OpenAI WS v2 当前实现已经具备功能完整性，但在“长会话 + 大 payload + 高频事件 + 失败重试”组合场景下出现以下结构性性能瓶颈：

- 热路径重复序列化与日志重计算（forwarder）。
- 连接池 `Acquire` 的排序与计时器分配成本（pool/client）。
- 重试分类与节奏控制不足导致的放大效应（gateway）。

这些问题叠加后，会在失败场景产生额外建连、额外日志、额外 payload 处理，最终拖慢成功请求并放大 P99 抖动。

## Goals / Non-Goals

**Goals**

- 让 WS v2 请求在热路径上做到“低分配、低复制、低系统调用”。
- 让连接池在高并发下保持稳定复杂度和高复用。
- 让失败处理从“重试放大”转为“收敛控制”，避免重连风暴。
- 提供可量化的性能验收与回归门禁。

**Non-Goals**

- 不改变对外 API 协议与客户端调用方式。
- 不引入新基础设施依赖。
- 不改变“WS 默认开启”既有产品策略。

## Decisions

### 决策 1：WS payload 构建改为单次序列化快照

- 现状问题：`payloadAsJSON` 在同一请求内多次执行，且日志提取依赖反复解析字符串。
- 方案：在 `forwardOpenAIWSV2` 中引入一次性 `payloadSnapshot`：
  - 同时持有 `[]byte`（发送）和 `map`（按需修改）。
  - `previous_response_id`、`prompt_cache_key`、`event.type` 等字段直接从 map 读取，避免 `gjson.Get(string)`。
  - `setOpenAIWSTurnMetadata` 后仅在 payload 实际变化时重编码。
- 收益：减少 JSON 编码/解码、降低字符串分配。

### 决策 2：日志预算化，重统计改为采样+上限

- 现状问题：`summarizeOpenAIWSPayloadKeySizes` 对每个字段 `json.Marshal`，在大 `tools` 下昂贵。
- 方案：
  - 新增采样开关和采样率（默认低频）。
  - `payload_key_sizes` 改为“估算+截断”策略，不对超大字段做完整 marshal。
  - 事件日志保留关键里程碑（connect/write/read_fail/terminal），常规 token 事件按采样记录。
- 收益：显著降低日志计算 CPU 与日志 I/O。

### 决策 3：事件流处理改为“字节优先”，减少字符串往返

- 现状问题：事件循环中 `string(message)` 多次重复，且每事件立即 flush。
- 方案：
  - `toolCorrector` 增加 `[]byte` 版本入口，避免频繁字节转字符串再转回字节。
  - usage 解析改为按事件类型 gating（仅 `response.completed` 进入字段解析）。
  - 流式写出支持轻量批量 flush（例如 token 事件 N 条或 T 毫秒一刷），终态事件强制 flush。
- 收益：减少分配和 syscall 次数，改善吞吐与 P99。

### 决策 4：连接池选择从全量排序改为低复杂度结构

- 现状问题：`Acquire` 每次排序连接数组，账号连接数上升后代价明显。
- 方案：
  - 引入“最小等待者优先”的增量结构（小根堆或有界桶），避免每次全量 `sort`。
  - 保留 `preferred_conn_id` 快速路径，命中时 O(1)。
  - 定时惰性重平衡，避免每次 `Acquire` 触发重排。
- 收益：连接选择成本从 O(n log n) 降到近 O(log n)/O(1)。

### 决策 5：读写超时上下文复用，削减计时器分配

- 现状问题：每次 read/write/ping 都 `context.WithTimeout`，热点场景分配多。
- 方案：
  - 请求级创建一次父 deadline，上下游读写复用。
  - 连接级 read/write 在必要时复用 timer（或使用统一 deadline context）。
  - 仅 ping 健康检查保留独立短超时。
- 收益：减少 timer 与 context 对象分配，降低 GC 压力。

### 决策 6：代理建连复用 HTTP Transport

- 现状问题：代理路径按 dial 动态 new `http.Client/Transport`，连接池无法复用。
- 方案：
  - 建立 `proxyURL -> *http.Client/*http.Transport` 复用缓存（带 LRU/TTL）。
  - 设置合理的 `MaxIdleConns`, `MaxIdleConnsPerHost`, `IdleConnTimeout`。
- 收益：减少 TLS 握手和短连接抖动，提升建连效率。

### 决策 7：重试策略改为“可重试白名单 + 指数退避 + jitter”

- 现状问题：失败重试无退避，策略类失败（如 1008）仍重复尝试。
- 方案：
  - 非重试错误（策略违规、鉴权类、参数类）直接 HTTP fallback，不再重复 WS。
  - 可重试错误才进入指数退避：`base * 2^n + jitter`，设置最大上限。
  - 引入账号级短路熔断：连续失败达到阈值后在冷却窗口内直走 HTTP。
- 收益：抑制重连风暴，降低失败路径资源放大。

### 决策 8：预热触发去抖，防止后台建连风暴

- 现状问题：`ensureTargetIdleAsync` 触发频繁，峰值时可能造成额外 prewarm 压力。
- 方案：
  - 增加账号级 prewarm cooldown（毫秒级）。
  - 当最近失败率高于阈值时暂停预热，优先维持现有连接健康。
  - `targetConnCount` 采用 EWMA 负载而非瞬时 waiters 峰值。
- 收益：降低无效预热和建连抖动。

## Architecture Changes

### 模块改造

1. `openai_ws_forwarder.go`
- 引入 payload 快照结构与字节优先处理链。
- 日志采样、payload 大字段预算和流式 flush 策略。

2. `openai_ws_pool.go`
- `Acquire` 连接选择器改为增量结构。
- `ensureTargetIdleAsync` 增加触发去抖与失败保护。

3. `openai_ws_client.go`
- 代理 HTTP client/transport 复用池。
- 建连参数支持 keep-alive 与空闲连接上限。

4. `openai_gateway_service.go`
- 重试分类细化，加入 backoff+jitter 与熔断冷却。
- `1008` 等策略类失败快速回退。

5. `config.go`
- 新增性能治理配置项：
  - `gateway.openai_ws.retry_backoff_initial_ms`
  - `gateway.openai_ws.retry_backoff_max_ms`
  - `gateway.openai_ws.retry_jitter_ratio`
  - `gateway.openai_ws.non_retryable_close_statuses`
  - `gateway.openai_ws.payload_log_sample_rate`
  - `gateway.openai_ws.prewarm_cooldown_ms`
  - `gateway.openai_ws.event_flush_batch_size`
  - `gateway.openai_ws.event_flush_interval_ms`

## Observability & Benchmarks

- 指标新增：
  - `openai_ws_payload_analyze_ms`
  - `openai_ws_retry_attempts`
  - `openai_ws_backoff_ms`
  - `openai_ws_conn_pick_ms`
  - `openai_ws_transport_reuse_ratio`
  - `openai_ws_non_retryable_fast_fallback_total`
- 压测维度：
  - 短请求（小 payload）/长请求（大 tools）/高失败率注入（1008/5xx/timeout）
  - 流式与非流式分开对比
  - 单账号热点与多账号均衡场景

## Migration Plan

1. 阶段 A（安全收益优先）
- 上线重试分类 + backoff + 1008 快速 fallback + 基础日志采样。

2. 阶段 B（热路径减负）
- 上线 payload 单次序列化、字节优先处理、usage 选择性解析。

3. 阶段 C（连接与建连优化）
- 上线连接选择器优化、prewarm 去抖、代理 transport 复用。

4. 阶段 D（门禁与固化）
- 固化压测基线到回归门禁，未达标不得发布。

## Rollback Strategy

- 任一阶段都可通过独立开关回退到旧逻辑：
  - `openai_ws_retry_policy_v2_enabled`
  - `openai_ws_fast_payload_path_enabled`
  - `openai_ws_pool_picker_v2_enabled`
  - `openai_ws_transport_cache_enabled`
- 指标越界（错误率、P99、fallback rate）时自动触发回退。
