## Why

当前 OpenAI Responses WebSocket v2 已进入主路径，但从线上日志和代码热路径看，仍存在可观的性能浪费与放大效应：在高并发、长会话、失败重试场景下，网关附加延迟、CPU/GC 压力和无效重试次数偏高。

本提案目标是“修复所有已识别的 WS v2 性能问题”，并形成可量化、可灰度、可回滚的闭环。

### 三轮分析结论（汇总）

1. 热路径 CPU/分配开销偏高（forwarder）
- `backend/internal/service/openai_ws_forwarder.go:668`、`backend/internal/service/openai_ws_forwarder.go:703`：同一次请求对 payload 重复序列化，且基于字符串再次提取字段。
- `backend/internal/service/openai_ws_forwarder.go:177`：日志统计 `payload_key_sizes` 对每个字段执行 `json.Marshal`，在大 `tools/input` 场景放大 CPU。
- `backend/internal/service/openai_ws_forwarder.go:895`、`backend/internal/service/openai_ws_forwarder.go:998`、`backend/internal/service/openai_ws_forwarder.go:1001`：流事件循环里存在 `[]byte <-> string` 频繁转换、逐事件 flush 与 usage 解析，导致分配和系统调用开销上升。

2. 连接池与客户端路径存在调度/建连额外成本
- `backend/internal/service/openai_ws_pool.go:527`、`backend/internal/service/openai_ws_pool.go:719`、`backend/internal/service/openai_ws_pool.go:727`：`Acquire` 频繁全量排序连接（O(n log n)），账号连接数增大后成本上升。
- `backend/internal/service/openai_ws_pool.go:267`、`backend/internal/service/openai_ws_pool.go:295`：每次读写都创建 `context.WithTimeout`，计时器对象和取消函数在热点下产生分配压力。
- `backend/internal/service/openai_ws_client.go:56`、`backend/internal/service/openai_ws_client.go:57`：按请求新建 `http.Client/Transport`（代理场景），连接复用能力弱，握手和 TLS 成本高。

3. 重试与降级策略产生失败放大效应
- `backend/internal/service/openai_gateway_service.go:1350`、`backend/internal/service/openai_gateway_service.go:1375`：重试循环未引入指数退避+jitter，失败时容易形成重连风暴。
- `backend/internal/service/openai_gateway_service.go:337`、`backend/internal/service/openai_gateway_service.go:350`：重试分类偏粗，`1008` 等策略类失败仍会被重复尝试，导致“无效重试 + payload 裁剪 + 重日志”叠加放大。

## What Changes

- 建立 WS v2 性能修复三层方案：
  - 热路径优化：单次序列化、低开销字段提取、日志预算化、事件写出批量策略。
  - 连接与调度优化：连接选择从全量排序改为低复杂度策略，代理建连复用 transport，减少热点计时器分配。
  - 失败控制优化：非重试错误快速降级，重试路径引入指数退避+jitter+熔断冷却，抑制失败放大。
- 补齐专项性能观测与压测验收：
  - `网关附加延迟 / TTFT / P95/P99 / CPU / allocs / WS 复用率 / 重试分布 / fallback rate` 全量纳入。
- 明确发布策略：
  - 保持对外 API 不变，按账号灰度，阈值触发一键回退 HTTP。

## Performance Targets

- WSv2 流式请求网关附加延迟：P95 降低 >= 25%，P99 降低 >= 20%（相对基线）。
- WSv2 热路径 CPU 时间：每千请求 CPU 降低 >= 20%。
- WSv2 热路径内存分配：`allocs/op` 降低 >= 30%，`B/op` 降低 >= 25%。
- 单请求平均 WS 尝试次数：<= 1.2；`retry_exhausted` 比例 <= 0.5%。
- 连接池复用率：>= 75%；同账号建连速率峰值较基线下降 >= 30%。
- 失败放大抑制：`close_status=1008` 场景不超过 1 次 WS 尝试后必须进入 HTTP 回退。

## Scope / Constraints

- 保持外部接口与协议兼容：客户端仍走 `POST /v1/responses`。
- 本提案不引入新的外部基础设施（如新增 MQ）。
- 保持“OpenAI Responses WebSocket 默认开启”策略，不在本提案中调整默认开关语义。

## Capabilities

### New Capabilities

- `openai-ws-v2-performance`: 定义并约束 OpenAI Responses WebSocket v2 的性能目标、失败控制策略、连接调度策略与验收标准。

### Modified Capabilities

- （无）

## Impact

- Backend
  - `backend/internal/service/openai_ws_forwarder.go`
  - `backend/internal/service/openai_ws_pool.go`
  - `backend/internal/service/openai_ws_client.go`
  - `backend/internal/service/openai_gateway_service.go`
  - `backend/internal/service/openai_ws_state_store.go`
  - `backend/internal/config/config.go`
- Tests / Perf
  - `backend/internal/service/*_test.go`（forwarder/pool/retry/fallback）
  - `tools/perf/*`（新增 WSv2 基线与回归脚本）
- Ops
  - 监控与告警面板新增 WSv2 性能指标与重试分布。

## Risks

- 低开销日志与选择性解析改造可能引入可观测性盲区。
- 重试收敛后，短时成功率可能下降但整体延迟与资源效率提升。
- 连接池选择策略改造若实现不当可能导致局部热点。

## Rollout

1. 基线采样：先冻结当前指标与流量模型。
2. 小流量灰度：按账号 allowlist 分批启用优化开关。
3. 阈值守护：任何一项关键指标越界立即回退。
4. 全量发布：达成验收指标后全量启用。
