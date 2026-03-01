---
phase: "10"
name: "openai-wsv2-ctx-pool-normalization-hardening"
created: 2026-02-28
---

# Phase 10: openai-wsv2-ctx-pool-normalization-hardening — Context

## Decisions

- 将 WSv2 `ctx_pool` 的发送前修复逻辑收敛为统一 normalizer，避免分支散落导致回归。
- normalizer 必须在“发送上游前”统一执行三类动作：
  1) `previous_response_id` infer/align
  2) 缺失 `function_call_output` 自动补齐（`output="aborted"`）
  3) 明显 orphan output 清理
- `response -> pending_call_ids` 必须支持跨实例读取：沿用 `session_last_response_id` 的“本地热缓存 + Redis 回源”模式，TTL 与 response 粘连 TTL 对齐。
- `previous_response_id` keep/drop 判定升级为“成对不变量优先”：
  - call/output 成对关系优先于单纯锚点延续
  - 将“有 output 但 pending 状态缺失/不确定”纳入统一策略与指标
- 现有恢复矩阵保持不变：
  - `tool_output_not_found`：单次 drop `previous_response_id` 后重放
  - `previous_response_not_found`：先对齐再降级
- 回归测试向 Codex 归一化哲学对齐：覆盖 aborted 注入、orphan 清理、debug/release 行为差异。

## Discretion Areas

- normalizer 的模块边界与函数命名（独立文件或 forwarder 内聚 helper）。
- pending_call_ids 的 Redis key 设计（前缀命名与序列化格式）。
- 指标埋点位置与字段命名（保证可观测性且不污染热路径）。
- 单测与集成测试拆分方式（在保证可读性的前提下控制执行时长）。

## Deferred Ideas

- 后续可考虑把 normalizer 演进为 typed model 层（减少 JSON patch 复杂度）。
- 后续可评估将更多 session 粘连状态统一迁移到共享缓存层。
