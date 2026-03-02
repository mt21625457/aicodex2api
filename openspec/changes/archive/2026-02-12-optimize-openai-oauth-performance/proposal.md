## Why

当前 OpenAI OAuth 线路（Codex CLI → `/v1/responses` → 网关转发 ChatGPT/OpenAI）在高并发下存在多处非必要开销（重复解析、额外 Redis 往返、热路径日志与 goroutine 开销、SSE 逐行处理成本），导致网关附加延迟与尾延迟（p95/p99）偏高。随着 Codex/VSCode 客户端流量增长，这条链路已成为核心体验路径，需要以“极致性能”为目标进行系统性优化。

## What Changes

- 为 OpenAI OAuth 线路建立明确的性能目标与验收口径（重点关注网关附加延迟、TTFT、p95/p99、CPU/内存分配、错误率）。
- 优化请求热路径：减少请求体重复解析与不必要拷贝，收敛中间件与上下文写入的额外成本。
- 优化并发与调度路径：减少常态请求的 Redis 往返次数，降低等待队列与槽位管理的额外开销。
- 优化流式转发路径：降低 SSE 逐行处理中的正则与 JSON 解析成本，减少流式场景的 CPU 和 GC 压力。
- 优化 OAuth token 获取路径：降低锁竞争时的等待成本，避免固定 sleep 放大尾延迟。
- 增强可观测性：补齐性能指标与压测基线，确保优化收益可量化、可回归验证。

## Capabilities

### New Capabilities

- `openai-oauth-performance`: 定义并约束 OpenAI OAuth 端到端高性能网关能力，包括请求热路径、调度并发路径、流式转发路径和 token 获取路径的性能要求与验收标准。

### Modified Capabilities

- （无）

## Impact

- 影响模块：`backend/internal/handler/openai_gateway_handler.go`、`backend/internal/handler/gateway_helper.go`、`backend/internal/service/openai_gateway_service.go`、`backend/internal/service/openai_token_provider.go`、`backend/internal/repository/http_upstream.go`、`backend/internal/server/middleware/*`、相关 `config` 默认值与校验。
- 影响系统：Redis（并发/等待队列/调度缓存访问模式）、上游连接池行为、日志与监控指标。
- API 兼容性：对外 API 路由与协议保持兼容，不引入 Breaking Change；主要是性能与内部行为优化。
- 风险与收益：需要通过压测与灰度验证来控制回归风险；预期显著降低网关附加延迟与尾延迟，提升高并发稳定性。
