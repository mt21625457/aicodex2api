#!/usr/bin/env python3
"""OpenAI WS v2 passthrough 灰度发布脚本。

流程对应 OpenSpec M7：
1) 先灰度少量账号切 passthrough
2) 运行 guard 观察 semantic_mutation_total 与 relay/error 指标
3) 指标异常自动回切 ctx_pool
4) 指标通过后扩量
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Dict, Iterable, List, Tuple

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_GUARD = ROOT / "tools" / "perf" / "openai_ws_v2_passthrough_gray_guard.py"


def parse_ids(raw: str) -> List[int]:
    out: List[int] = []
    for part in raw.split(","):
        p = part.strip()
        if not p:
            continue
        try:
            val = int(p)
        except ValueError as exc:
            raise ValueError(f"invalid account id: {p}") from exc
        if val <= 0:
            raise ValueError(f"invalid account id: {p}")
        out.append(val)
    return out


def build_headers(token: str) -> Dict[str, str]:
    headers = {
        "Accept": "application/json",
        "Content-Type": "application/json",
    }
    if token.strip():
        headers["Authorization"] = f"Bearer {token.strip()}"
    return headers


def request_json(method: str, url: str, headers: Dict[str, str], payload: Dict[str, Any]) -> Dict[str, Any]:
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url=url, method=method, headers=headers, data=body)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {e.code}: {body}") from e
    except urllib.error.URLError as e:
        raise RuntimeError(f"request failed: {e}") from e


def ensure_success(payload: Dict[str, Any], scene: str) -> None:
    if not isinstance(payload, dict):
        raise RuntimeError(f"{scene}: invalid response payload")
    if payload.get("code") != 0:
        raise RuntimeError(f"{scene}: api error code={payload.get('code')} message={payload.get('message')}")


def bulk_update_mode(
    base_url: str,
    headers: Dict[str, str],
    account_ids: Iterable[int],
    mode_key: str,
    mode_value: str,
    dry_run: bool,
) -> None:
    ids = list(account_ids)
    if not ids:
        return

    payload: Dict[str, Any] = {
        "account_ids": ids,
        "extra": {
            mode_key: mode_value,
        },
    }
    if dry_run:
        print(
            "[DRY-RUN] bulk-update",
            json.dumps(
                {
                    "url": f"{base_url}/api/v1/admin/accounts/bulk-update",
                    "payload": payload,
                },
                ensure_ascii=False,
            ),
        )
        return

    resp = request_json(
        method="POST",
        url=f"{base_url}/api/v1/admin/accounts/bulk-update",
        headers=headers,
        payload=payload,
    )
    ensure_success(resp, f"bulk update {mode_key}={mode_value}")


def run_guard(
    guard_script: Path,
    base_url: str,
    admin_token: str,
    time_range: str,
    group_id: str,
) -> Tuple[int, str]:
    cmd = [
        sys.executable,
        str(guard_script),
        "--base-url",
        base_url,
        "--admin-token",
        admin_token,
        "--time-range",
        time_range,
    ]
    if group_id.strip():
        cmd.extend(["--group-id", group_id.strip()])

    proc = subprocess.run(cmd, cwd=str(ROOT), capture_output=True, text=True)
    output = (proc.stdout + "\n" + proc.stderr).strip()
    return proc.returncode, output


def apply_mode_for_batches(
    base_url: str,
    headers: Dict[str, str],
    apikey_ids: List[int],
    oauth_ids: List[int],
    mode_value: str,
    dry_run: bool,
) -> None:
    bulk_update_mode(
        base_url=base_url,
        headers=headers,
        account_ids=apikey_ids,
        mode_key="openai_apikey_responses_websockets_v2_mode",
        mode_value=mode_value,
        dry_run=dry_run,
    )
    bulk_update_mode(
        base_url=base_url,
        headers=headers,
        account_ids=oauth_ids,
        mode_key="openai_oauth_responses_websockets_v2_mode",
        mode_value=mode_value,
        dry_run=dry_run,
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="OpenAI WSv2 passthrough 灰度发布")
    parser.add_argument("--base-url", required=True, help="服务地址，例如 http://127.0.0.1:5231")
    parser.add_argument("--admin-token", default="", help="Admin JWT（可选，按部署策略）")
    parser.add_argument("--apikey-canary-ids", default="", help="API Key 账号灰度列表，逗号分隔")
    parser.add_argument("--oauth-canary-ids", default="", help="OAuth 账号灰度列表，逗号分隔")
    parser.add_argument("--apikey-expand-ids", default="", help="API Key 扩量列表，逗号分隔")
    parser.add_argument("--oauth-expand-ids", default="", help="OAuth 扩量列表，逗号分隔")
    parser.add_argument("--time-range", default="30m", help="guard 时间窗口，默认 30m")
    parser.add_argument("--group-id", default="", help="可选 group_id")
    parser.add_argument(
        "--guard-script",
        default=str(DEFAULT_GUARD),
        help=f"guard 脚本路径，默认 {DEFAULT_GUARD}",
    )
    parser.add_argument("--dry-run", action="store_true", help="仅打印操作，不实际执行")
    args = parser.parse_args()

    try:
        canary_apikey_ids = parse_ids(args.apikey_canary_ids)
        canary_oauth_ids = parse_ids(args.oauth_canary_ids)
        expand_apikey_ids = parse_ids(args.apikey_expand_ids)
        expand_oauth_ids = parse_ids(args.oauth_expand_ids)
    except ValueError as exc:
        print(f"[WSv2 Rollout] 参数错误: {exc}", file=sys.stderr)
        return 1

    if not (canary_apikey_ids or canary_oauth_ids):
        print("[WSv2 Rollout] 至少需要一个 canary 账号列表", file=sys.stderr)
        return 1

    guard_script = Path(args.guard_script).resolve()
    if not guard_script.exists():
        print(f"[WSv2 Rollout] guard 脚本不存在: {guard_script}", file=sys.stderr)
        return 1

    base = args.base_url.rstrip("/")
    headers = build_headers(args.admin_token)

    try:
        print("[WSv2 Rollout] M7.1 切 canary 账号到 passthrough")
        apply_mode_for_batches(
            base_url=base,
            headers=headers,
            apikey_ids=canary_apikey_ids,
            oauth_ids=canary_oauth_ids,
            mode_value="passthrough",
            dry_run=args.dry_run,
        )

        print("[WSv2 Rollout] M7.2 运行 guard 观察关键指标")
        if args.dry_run:
            print("[WSv2 Rollout] DRY-RUN 跳过 guard 实际执行")
            return 0

        guard_code, guard_output = run_guard(
            guard_script=guard_script,
            base_url=base,
            admin_token=args.admin_token,
            time_range=args.time_range,
            group_id=args.group_id,
        )
        print(guard_output)

        if guard_code != 0:
            print("[WSv2 Rollout] M7.3 指标异常，自动回切 canary 到 ctx_pool")
            apply_mode_for_batches(
                base_url=base,
                headers=headers,
                apikey_ids=canary_apikey_ids,
                oauth_ids=canary_oauth_ids,
                mode_value="ctx_pool",
                dry_run=False,
            )
            return 2

        if expand_apikey_ids or expand_oauth_ids:
            print("[WSv2 Rollout] M7.4 指标通过，扩量切换到 passthrough")
            apply_mode_for_batches(
                base_url=base,
                headers=headers,
                apikey_ids=expand_apikey_ids,
                oauth_ids=expand_oauth_ids,
                mode_value="passthrough",
                dry_run=False,
            )

        print("[WSv2 Rollout] 完成")
        return 0

    except Exception as exc:
        print(f"[WSv2 Rollout] 执行失败: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
