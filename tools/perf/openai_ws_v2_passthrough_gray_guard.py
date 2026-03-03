#!/usr/bin/env python3
"""OpenAI WS v2 passthrough 灰度守护脚本。

用途：
- 拉取 passthrough 运行时指标（semantic_mutation_total）
- 拉取 Ops overview 错误率指标
- 拉取 request-errors 作为 relay error 指标
- 输出门禁判定结果（用于灰度扩量/回切）

退出码：
- 0: 指标通过
- 1: 请求失败或参数错误
- 2: 指标超阈值（建议停止扩量并回切）
"""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Dict, List, Optional


@dataclass
class GuardSnapshot:
    semantic_mutation_total: Optional[int]
    usage_parse_failure_total: Optional[int]
    request_error_rate_percent: Optional[float]
    upstream_error_rate_percent: Optional[float]
    relay_error_count: Optional[int]


@dataclass
class GuardThresholds:
    request_error_rate_percent_max: Optional[float]
    upstream_error_rate_percent_max: Optional[float]
    relay_error_count_max: Optional[int]


def build_headers(token: str) -> Dict[str, str]:
    headers = {"Accept": "application/json"}
    if token.strip():
        headers["Authorization"] = f"Bearer {token.strip()}"
    return headers


def request_json(url: str, headers: Dict[str, str]) -> Dict[str, Any]:
    req = urllib.request.Request(url=url, method="GET", headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {e.code}: {body}") from e
    except urllib.error.URLError as e:
        raise RuntimeError(f"request failed: {e}") from e


def parse_envelope_data(payload: Dict[str, Any]) -> Dict[str, Any]:
    if not isinstance(payload, dict):
        raise RuntimeError("invalid response payload")
    if payload.get("code") != 0:
        raise RuntimeError(f"api error: code={payload.get('code')} message={payload.get('message')}")
    data = payload.get("data")
    if not isinstance(data, dict):
        raise RuntimeError("invalid response data")
    return data


def to_float_or_none(v: Any) -> Optional[float]:
    if v is None:
        return None
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def to_int_or_none(v: Any) -> Optional[int]:
    if v is None:
        return None
    try:
        return int(v)
    except (TypeError, ValueError):
        return None


def evaluate(snapshot: GuardSnapshot, thresholds: GuardThresholds) -> List[str]:
    violations: List[str] = []

    if snapshot.semantic_mutation_total is not None and snapshot.semantic_mutation_total != 0:
        violations.append(
            "semantic_mutation_total 非 0: "
            f"actual={snapshot.semantic_mutation_total} expected=0"
        )

    if (
        thresholds.request_error_rate_percent_max is not None
        and snapshot.request_error_rate_percent is not None
        and snapshot.request_error_rate_percent > thresholds.request_error_rate_percent_max
    ):
        violations.append(
            "请求错误率超阈值: "
            f"actual={snapshot.request_error_rate_percent:.2f}% "
            f"threshold={thresholds.request_error_rate_percent_max:.2f}%"
        )

    if (
        thresholds.upstream_error_rate_percent_max is not None
        and snapshot.upstream_error_rate_percent is not None
        and snapshot.upstream_error_rate_percent > thresholds.upstream_error_rate_percent_max
    ):
        violations.append(
            "上游错误率超阈值: "
            f"actual={snapshot.upstream_error_rate_percent:.2f}% "
            f"threshold={thresholds.upstream_error_rate_percent_max:.2f}%"
        )

    if (
        thresholds.relay_error_count_max is not None
        and snapshot.relay_error_count is not None
        and snapshot.relay_error_count > thresholds.relay_error_count_max
    ):
        violations.append(
            "relay 错误条数超阈值: "
            f"actual={snapshot.relay_error_count} "
            f"threshold={thresholds.relay_error_count_max}"
        )

    return violations


def main() -> int:
    parser = argparse.ArgumentParser(description="OpenAI WS v2 passthrough 灰度守护")
    parser.add_argument("--base-url", required=True, help="服务地址，例如 http://127.0.0.1:5231")
    parser.add_argument("--admin-token", default="", help="Admin JWT（可选，按部署策略）")
    parser.add_argument("--platform", default="openai", help="平台过滤，默认 openai")
    parser.add_argument("--time-range", default="30m", help="时间窗口: 5m/30m/1h/6h/24h/7d/30d")
    parser.add_argument("--group-id", default="", help="可选 group_id")
    parser.add_argument(
        "--relay-error-query",
        default="openai ws passthrough",
        help="relay 错误关键字查询（ops request-errors 的 q 参数）",
    )
    parser.add_argument(
        "--request-error-rate-max",
        type=float,
        default=2.0,
        help="请求错误率阈值（百分比）",
    )
    parser.add_argument(
        "--upstream-error-rate-max",
        type=float,
        default=2.0,
        help="上游错误率阈值（百分比）",
    )
    parser.add_argument(
        "--relay-error-count-max",
        type=int,
        default=0,
        help="relay 错误条数阈值",
    )
    args = parser.parse_args()

    base = args.base_url.rstrip("/")
    headers = build_headers(args.admin_token)

    try:
        metrics_url = f"{base}/api/v1/admin/ops/openai-ws-v2/passthrough-metrics"
        metrics_data = parse_envelope_data(request_json(metrics_url, headers))
        passthrough_metrics = metrics_data.get("passthrough")
        if not isinstance(passthrough_metrics, dict):
            raise RuntimeError("invalid passthrough metrics payload")

        overview_query = {
            "platform": args.platform,
            "time_range": args.time_range,
        }
        if args.group_id.strip():
            overview_query["group_id"] = args.group_id.strip()
        overview_url = (
            f"{base}/api/v1/admin/ops/dashboard/overview?"
            + urllib.parse.urlencode(overview_query)
        )
        overview_data = parse_envelope_data(request_json(overview_url, headers))

        request_errors_query = {
            "platform": args.platform,
            "time_range": args.time_range,
            "page": "1",
            "page_size": "50",
            "q": args.relay_error_query,
        }
        if args.group_id.strip():
            request_errors_query["group_id"] = args.group_id.strip()
        request_errors_url = (
            f"{base}/api/v1/admin/ops/request-errors?"
            + urllib.parse.urlencode(request_errors_query)
        )
        request_errors_data = parse_envelope_data(request_json(request_errors_url, headers))
        items = request_errors_data.get("items")
        relay_error_count = len(items) if isinstance(items, list) else None

        snapshot = GuardSnapshot(
            semantic_mutation_total=to_int_or_none(passthrough_metrics.get("semantic_mutation_total")),
            usage_parse_failure_total=to_int_or_none(passthrough_metrics.get("usage_parse_failure_total")),
            request_error_rate_percent=to_float_or_none(overview_data.get("error_rate")),
            upstream_error_rate_percent=to_float_or_none(overview_data.get("upstream_error_rate")),
            relay_error_count=relay_error_count,
        )
        thresholds = GuardThresholds(
            request_error_rate_percent_max=args.request_error_rate_max,
            upstream_error_rate_percent_max=args.upstream_error_rate_max,
            relay_error_count_max=args.relay_error_count_max,
        )

        print("[OpenAI WSv2 Passthrough Guard] 当前快照:")
        print(
            json.dumps(
                {
                    "semantic_mutation_total": snapshot.semantic_mutation_total,
                    "usage_parse_failure_total": snapshot.usage_parse_failure_total,
                    "request_error_rate_percent": snapshot.request_error_rate_percent,
                    "upstream_error_rate_percent": snapshot.upstream_error_rate_percent,
                    "relay_error_count": snapshot.relay_error_count,
                },
                ensure_ascii=False,
                indent=2,
            )
        )
        print("[OpenAI WSv2 Passthrough Guard] 阈值:")
        print(
            json.dumps(
                {
                    "request_error_rate_percent_max": thresholds.request_error_rate_percent_max,
                    "upstream_error_rate_percent_max": thresholds.upstream_error_rate_percent_max,
                    "relay_error_count_max": thresholds.relay_error_count_max,
                },
                ensure_ascii=False,
                indent=2,
            )
        )

        violations = evaluate(snapshot, thresholds)
        if violations:
            print("[OpenAI WSv2 Passthrough Guard] 检测到阈值违例：")
            for idx, line in enumerate(violations, start=1):
                print(f"  {idx}. {line}")
            print("[OpenAI WSv2 Passthrough Guard] 建议：停止扩量并执行回切。")
            return 2

        print("[OpenAI WSv2 Passthrough Guard] 指标通过，可继续观察或按计划扩量。")
        return 0

    except Exception as exc:
        print(f"[OpenAI WSv2 Passthrough Guard] 执行失败: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())

