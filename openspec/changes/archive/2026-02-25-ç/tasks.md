## 0. 签核门禁

- [ ] 0.1 冻结 WSv2 当前基线（P50/P95/P99、TTFT、CPU、allocs、重试分布、fallback rate）
- [ ] 0.2 确认并签字性能目标阈值与回滚阈值
- [ ] 0.3 确认灰度账号清单与分批比例
- [x] 0.4 执行 `openspec validate ç --strict` 并留档

## 1. 热路径优化（forwarder）

- [x] 1.1 重构 `forwardOpenAIWSV2` 为单次 payload 序列化快照，移除重复 `payloadAsJSON`
- [x] 1.2 `previous_response_id/prompt_cache_key/type` 改为 map 直接读取，减少 `gjson.Get(string)`
- [x] 1.3 `summarizeOpenAIWSPayloadKeySizes` 改为采样+预算模式，避免全字段 marshal
- [x] 1.4 引入 `toolCorrector` 字节接口，减少 `[]byte <-> string` 转换
- [x] 1.5 流式写出增加轻量批量 flush 策略（终态事件强制 flush）
- [x] 1.6 增加基准测试：`BenchmarkOpenAIWSForwarderHotPath`

## 2. 连接池与客户端优化（pool/client）

- [x] 2.1 将连接选择从全量排序改为低复杂度增量结构（堆或等效策略）
- [x] 2.2 `Acquire` 路径保留 `preferred_conn_id` O(1) 快速命中
- [x] 2.3 引入 read/write timeout 上下文复用，减少热点 `WithTimeout` 分配
- [x] 2.4 代理建连改造为 transport/client 复用缓存（含 TTL/LRU）
- [x] 2.5 `ensureTargetIdleAsync` 增加账号级 cooldown 与失败率抑制
- [x] 2.6 增加基准测试：`BenchmarkOpenAIWSPoolAcquire`

## 3. 重试与降级优化（gateway）

- [x] 3.1 完善重试分类：策略类/鉴权类/参数类失败标记为不可重试
- [x] 3.2 对可重试错误引入指数退避+jitter（带最大上限）
- [x] 3.3 对 `close_status=1008` 路径改为单次尝试后快速 HTTP fallback
- [x] 3.4 增加账号级熔断冷却窗口，避免失败风暴期间反复打 WS
- [x] 3.5 增加重试策略单测与故障注入测试

## 4. 可观测性与压测

- [x] 4.1 增加 WSv2 专项指标：`conn_pick_ms`、`retry_attempts`、`backoff_ms`、`transport_reuse_ratio`
- [x] 4.2 增加日志采样配置与运行时校验，防止日志放大
- [x] 4.3 补充压测脚本（短请求/长请求/错误注入/热点账号）
- [ ] 4.4 输出优化前后对比报告并纳入发布评审材料
- [ ] 4.5 按统一口径校验阈值：`P95`/`P99`/`allocs-op`/`B-op`/`retry_attempts`/`retry_exhausted`/`reuse_ratio`

## 5. 回归与发布

- [x] 5.1 HTTP/SSE 路径回归，确保无行为退化
- [x] 5.2 WSv2 流式协议兼容回归（事件顺序、DONE、usage）
- [ ] 5.3 按账号灰度发布并持续观测 24h
- [ ] 5.4 达成阈值后全量，否则按开关回滚并复盘
- [ ] 5.5 进行一次“阈值越界自动回滚”演练并记录结果
- [x] 5.6 同步更新 `deploy/config.example.yaml` 与运行手册中的新配置项说明
- [ ] 5.7 输出最终验收报告（含指标、风险、回滚演练结果）并归档到 `openspec/changes/.../final-acceptance-report.md`
