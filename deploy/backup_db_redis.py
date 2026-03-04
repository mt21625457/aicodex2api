#!/usr/bin/env python3
"""Backup PostgreSQL and Redis using docker compose + .env configuration.

Features:
- Parse .env file for variables (without third-party dependencies).
- Parse docker compose configuration via `docker compose config --format json`.
- Auto-detect postgres/redis services from compose file.
- Backup only the explicitly specified docker compose project (-p).
- Backup PostgreSQL to custom dump file (*.dump).
- Backup Redis to RDB snapshot (*.rdb).
- Generate metadata JSON for traceability.
- Package backup directory into a single tar.zst artifact.

Examples:
  python3 deploy/backup_db_redis.py -p myproject
  python3 deploy/backup_db_redis.py -f deploy/docker-compose.local.yml -e deploy/.env -p myproject
  python3 deploy/backup_db_redis.py --no-redis
  python3 deploy/backup_db_redis.py --dry-run
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple


class BackupError(RuntimeError):
    """Raised when backup process fails."""


SENSITIVE_ENV_KEYS = {"PGPASSWORD", "REDISCLI_AUTH"}


def now_utc() -> dt.datetime:
    return dt.datetime.now(tz=dt.timezone.utc)


def timestamp_local() -> str:
    return dt.datetime.now().strftime("%Y%m%d_%H%M%S")


def redact_cmd(cmd: List[str]) -> List[str]:
    redacted: List[str] = []
    for token in cmd:
        item = str(token)
        if "=" in item:
            key, _ = item.split("=", 1)
            if key.upper() in SENSITIVE_ENV_KEYS:
                redacted.append(f"{key}=***")
                continue
        redacted.append(item)
    return redacted


def cmd_to_log_string(cmd: List[str]) -> str:
    return " ".join(redact_cmd(cmd))


def print_dry_run_cmd(cmd: List[str], out_file: Optional[Path] = None) -> None:
    msg = f"[dry-run] {cmd_to_log_string(cmd)}"
    if out_file is not None:
        msg += f" > {out_file}"
    print(msg)


def run_cmd(
    cmd: List[str],
    *,
    env: Optional[Dict[str, str]] = None,
    capture_output: bool = False,
    check: bool = True,
) -> subprocess.CompletedProcess:
    try:
        return subprocess.run(
            cmd,
            env=env,
            check=check,
            capture_output=capture_output,
            text=True,
        )
    except FileNotFoundError as exc:
        raise BackupError(f"命令不存在: {cmd[0]}") from exc
    except subprocess.CalledProcessError as exc:
        stdout = exc.stdout.strip() if exc.stdout else ""
        stderr = exc.stderr.strip() if exc.stderr else ""
        msg = f"命令执行失败: {cmd_to_log_string(cmd)}"
        if stdout:
            msg += f"\nstdout: {stdout}"
        if stderr:
            msg += f"\nstderr: {stderr}"
        raise BackupError(msg) from exc


def unescape_env_double_quoted(value: str) -> str:
    out: List[str] = []
    idx = 0
    while idx < len(value):
        ch = value[idx]
        if ch != "\\":
            out.append(ch)
            idx += 1
            continue
        if idx + 1 >= len(value):
            out.append("\\")
            break
        nxt = value[idx + 1]
        if nxt == "n":
            out.append("\n")
        elif nxt == "r":
            out.append("\r")
        elif nxt == "t":
            out.append("\t")
        elif nxt == '"':
            out.append('"')
        elif nxt == "\\":
            out.append("\\")
        else:
            out.append("\\")
            out.append(nxt)
        idx += 2
    return "".join(out)


def parse_env_file(env_file: Path) -> Dict[str, str]:
    if not env_file.exists():
        raise BackupError(f".env 文件不存在: {env_file}")
    env_map: Dict[str, str] = {}
    pattern = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
    for idx, raw_line in enumerate(env_file.read_text(encoding="utf-8").splitlines(), start=1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[len("export ") :].strip()
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if not pattern.match(key):
            raise BackupError(f".env 键名不合法: {key} (line {idx})")
        if len(value) >= 2 and ((value[0] == value[-1] == '"') or (value[0] == value[-1] == "'")):
            quote = value[0]
            value = value[1:-1]
            if quote == '"':
                value = unescape_env_double_quoted(value)
        env_map[key] = value
    return env_map


def normalize_environment(env_value: Any) -> Dict[str, str]:
    if env_value is None:
        return {}
    if isinstance(env_value, dict):
        return {str(k): "" if v is None else str(v) for k, v in env_value.items()}
    if isinstance(env_value, list):
        result: Dict[str, str] = {}
        for item in env_value:
            if not isinstance(item, str):
                continue
            if "=" in item:
                k, v = item.split("=", 1)
                result[k] = v
        return result
    return {}


def compose_base_cmd(
    compose_files: List[Path],
    env_file: Path,
    project_dir: Path,
    project_name: Optional[str] = None,
) -> List[str]:
    cmd = ["docker", "compose", "--project-directory", str(project_dir), "--env-file", str(env_file)]
    if project_name:
        cmd.extend(["-p", project_name])
    for item in compose_files:
        cmd.extend(["-f", str(item)])
    return cmd


def load_compose_config(base_cmd: List[str]) -> Dict[str, Any]:
    result = run_cmd(base_cmd + ["config", "--format", "json"], capture_output=True)
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise BackupError("无法解析 docker compose config 的 JSON 输出") from exc


def choose_postgres_service(services: Dict[str, Dict[str, Any]], preferred: Optional[str]) -> Optional[str]:
    if preferred:
        return preferred if preferred in services else None
    best_name = None
    best_score = 0
    for name, svc in services.items():
        score = 0
        image = str(svc.get("image", "")).lower()
        env_map = normalize_environment(svc.get("environment"))
        hc = json.dumps(svc.get("healthcheck", {})).lower()
        if "postgres" in image:
            score += 100
        if any(k.startswith("POSTGRES_") for k in env_map):
            score += 80
        if "pg_isready" in hc:
            score += 40
        if score > best_score:
            best_score = score
            best_name = name
    return best_name


def choose_redis_service(services: Dict[str, Dict[str, Any]], preferred: Optional[str]) -> Optional[str]:
    if preferred:
        return preferred if preferred in services else None
    best_name = None
    best_score = 0
    for name, svc in services.items():
        score = 0
        image = str(svc.get("image", "")).lower()
        env_map = normalize_environment(svc.get("environment"))
        cmd = json.dumps(svc.get("command", "")).lower()
        hc = json.dumps(svc.get("healthcheck", {})).lower()
        if "redis" in image:
            score += 100
        if "redis-server" in cmd:
            score += 60
        if "rediscli_auth" in {k.lower() for k in env_map}:
            score += 40
        if "redis-cli" in hc:
            score += 20
        if score > best_score:
            best_score = score
            best_name = name
    return best_name


def get_compose_container_id(base_cmd: List[str], service_name: str) -> str:
    result = run_cmd(base_cmd + ["ps", "-q", service_name], capture_output=True)
    container_id = result.stdout.strip()
    if not container_id:
        raise BackupError(
            f"服务 {service_name} 未运行（无法获取容器 ID）。请先执行 docker compose up -d。"
        )
    return container_id


def dry_run_container_ref(services: Dict[str, Dict[str, Any]], service_name: str) -> str:
    service_cfg = services.get(service_name, {})
    container_name = str(service_cfg.get("container_name") or "").strip()
    if container_name:
        return container_name
    return f"<{service_name}>"


def archive_member_path(backup_dir: Path, file_path: Path) -> str:
    return f"{backup_dir.name}/{file_path.name}"


def ensure_tool_exists(tool: str) -> None:
    if shutil.which(tool) is None:
        raise BackupError(f"未找到命令: {tool}")


def postgres_creds(service_env: Dict[str, str], env_map: Dict[str, str]) -> Tuple[str, str, str]:
    user = service_env.get("POSTGRES_USER") or env_map.get("POSTGRES_USER") or env_map.get("DATABASE_USER")
    password = (
        service_env.get("POSTGRES_PASSWORD")
        or env_map.get("POSTGRES_PASSWORD")
        or env_map.get("DATABASE_PASSWORD")
        or ""
    )
    dbname = service_env.get("POSTGRES_DB") or env_map.get("POSTGRES_DB") or env_map.get("DATABASE_DBNAME")
    if not user:
        raise BackupError("未找到 PostgreSQL 用户（POSTGRES_USER / DATABASE_USER）")
    if not dbname:
        raise BackupError("未找到 PostgreSQL 数据库名（POSTGRES_DB / DATABASE_DBNAME）")
    return user, password, dbname


def external_db_conn(env_map: Dict[str, str]) -> Tuple[str, str]:
    host = env_map.get("DATABASE_HOST", "")
    port = env_map.get("DATABASE_PORT", "5432")
    if not host:
        raise BackupError("未找到外部数据库主机（DATABASE_HOST）")
    return host, port


def external_redis_conn(env_map: Dict[str, str]) -> Tuple[str, str]:
    host = env_map.get("REDIS_HOST", "")
    port = env_map.get("REDIS_PORT", "6379")
    if not host:
        raise BackupError("未找到外部 Redis 主机（REDIS_HOST）")
    return host, port


def backup_postgres_in_container(
    container_id: str,
    db_user: str,
    db_password: str,
    db_name: str,
    out_file: Path,
    dry_run: bool,
) -> None:
    cmd = ["docker", "exec", "-i"]
    if db_password:
        cmd.extend(["-e", f"PGPASSWORD={db_password}"])
        cmd.append(container_id)
        cmd.extend(
            [
                "pg_dump",
                "-U",
                db_user,
                "-d",
                db_name,
                "-F",
                "c",
                "--no-owner",
                "--no-privileges",
            ]
        )
    else:
        cmd.append(container_id)
        cmd.extend(
            [
                "env",
                "-u",
                "PGPASSWORD",
                "pg_dump",
                "-U",
                db_user,
                "-d",
                db_name,
                "-F",
                "c",
                "--no-owner",
                "--no-privileges",
            ]
        )
    if dry_run:
        print_dry_run_cmd(cmd, out_file)
        return
    with out_file.open("wb") as fp:
        try:
            subprocess.run(cmd, check=True, stdout=fp)
        except subprocess.CalledProcessError as exc:
            raise BackupError(f"PostgreSQL 备份失败（容器模式）: {exc}") from exc


def backup_postgres_external(
    host: str,
    port: str,
    db_user: str,
    db_password: str,
    db_name: str,
    out_file: Path,
    dry_run: bool,
) -> None:
    ensure_tool_exists("pg_dump")
    cmd = [
        "pg_dump",
        "-h",
        host,
        "-p",
        str(port),
        "-U",
        db_user,
        "-d",
        db_name,
        "-F",
        "c",
        "--no-owner",
        "--no-privileges",
    ]
    if dry_run:
        print_dry_run_cmd(cmd, out_file)
        return
    env = os.environ.copy()
    if db_password:
        env["PGPASSWORD"] = db_password
    else:
        env.pop("PGPASSWORD", None)
    with out_file.open("wb") as fp:
        try:
            subprocess.run(cmd, env=env, check=True, stdout=fp)
        except subprocess.CalledProcessError as exc:
            raise BackupError(f"PostgreSQL 备份失败（外部模式）: {exc}") from exc


def backup_redis_in_container(
    container_id: str,
    redis_password: str,
    out_file: Path,
    dry_run: bool,
) -> None:
    stamp = timestamp_local()
    tmp_file = f"/tmp/redis_backup_{stamp}.rdb"
    cmd = ["docker", "exec"]
    if redis_password:
        cmd.extend(["-e", f"REDISCLI_AUTH={redis_password}", container_id, "redis-cli", "--rdb", tmp_file])
    else:
        cmd.extend([container_id, "env", "-u", "REDISCLI_AUTH", "redis-cli", "--rdb", tmp_file])
    cp_cmd = ["docker", "cp", f"{container_id}:{tmp_file}", str(out_file)]
    rm_cmd = ["docker", "exec", container_id, "rm", "-f", tmp_file]

    if dry_run:
        print_dry_run_cmd(cmd)
        print_dry_run_cmd(cp_cmd)
        print_dry_run_cmd(rm_cmd)
        return

    run_cmd(cmd)
    try:
        run_cmd(cp_cmd)
    finally:
        try:
            run_cmd(rm_cmd)
        except BackupError:
            pass


def backup_redis_external(
    host: str,
    port: str,
    redis_password: str,
    out_file: Path,
    dry_run: bool,
) -> None:
    ensure_tool_exists("redis-cli")
    cmd = ["redis-cli", "-h", host, "-p", str(port), "--rdb", str(out_file)]
    env = os.environ.copy()
    if redis_password:
        env["REDISCLI_AUTH"] = redis_password
    else:
        env.pop("REDISCLI_AUTH", None)
    if dry_run:
        print_dry_run_cmd(cmd)
        return
    run_cmd(cmd, env=env)


def save_metadata(meta_file: Path, payload: Dict[str, Any], dry_run: bool) -> None:
    if dry_run:
        print(f"[dry-run] 写入元数据: {meta_file}")
        print(json.dumps(payload, indent=2, ensure_ascii=False))
        return
    meta_file.write_text(json.dumps(payload, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def archive_backup_dir(
    backup_dir: Path,
    out_root: Path,
    zstd_level: int,
    dry_run: bool,
    keep_unpacked: bool,
) -> Path:
    archive_file = out_root / f"{backup_dir.name}.tar.zst"
    tar_cmd = ["tar", "-C", str(out_root), "-cf", "-", backup_dir.name]
    zstd_cmd = ["zstd", "-T0", f"-{zstd_level}", "-f", "-o", str(archive_file)]

    if dry_run:
        print(f"[dry-run] {cmd_to_log_string(tar_cmd)} | {cmd_to_log_string(zstd_cmd)}")
        if not keep_unpacked:
            print(f"[dry-run] rm -rf {backup_dir}")
        return archive_file

    ensure_tool_exists("tar")
    ensure_tool_exists("zstd")

    tar_stderr = b""
    zstd_stderr = b""
    tar_return_code = 0
    zstd_return_code = 0
    try:
        with subprocess.Popen(tar_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE) as tar_proc:
            if tar_proc.stdout is None:
                raise BackupError("tar 输出管道创建失败")
            with subprocess.Popen(
                zstd_cmd,
                stdin=tar_proc.stdout,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
            ) as zstd_proc:
                tar_proc.stdout.close()
                _, zstd_stderr = zstd_proc.communicate()
            if tar_proc.stderr is not None:
                tar_stderr = tar_proc.stderr.read()
            tar_return_code = tar_proc.wait()
            zstd_return_code = zstd_proc.returncode or 0
    except FileNotFoundError as exc:
        raise BackupError(f"命令不存在: {exc.filename}") from exc

    if tar_return_code != 0 or zstd_return_code != 0:
        if archive_file.exists():
            archive_file.unlink()
        errors: List[str] = []
        if tar_return_code != 0:
            errors.append(f"tar 失败({tar_return_code}): {tar_stderr.decode('utf-8', errors='ignore').strip()}")
        if zstd_return_code != 0:
            errors.append(f"zstd 失败({zstd_return_code}): {zstd_stderr.decode('utf-8', errors='ignore').strip()}")
        raise BackupError("压缩备份失败: " + " | ".join(errors))

    if not keep_unpacked:
        shutil.rmtree(backup_dir, ignore_errors=True)
    return archive_file


def parse_args() -> argparse.Namespace:
    script_dir = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description="根据 .env + docker compose 配置备份 PostgreSQL 和 Redis")
    parser.add_argument(
        "-f",
        "--compose-file",
        action="append",
        dest="compose_files",
        help="docker compose 文件路径，可重复指定。默认 deploy/docker-compose.yml",
    )
    parser.add_argument(
        "-e",
        "--env-file",
        default=str(script_dir / ".env"),
        help="环境变量文件路径，默认 deploy/.env",
    )
    parser.add_argument(
        "--project-directory",
        default=str(script_dir),
        help="docker compose project directory，默认 deploy/",
    )
    parser.add_argument(
        "-p",
        "--project-name",
        default=None,
        help="docker compose project name（等价 -p）。必填，仅备份该 project 的容器",
    )
    parser.add_argument(
        "--output-dir",
        default=str(script_dir / "backups"),
        help="备份输出根目录，默认 deploy/backups",
    )
    parser.add_argument("--postgres-service", default=None, help="显式指定 PostgreSQL 服务名")
    parser.add_argument("--redis-service", default=None, help="显式指定 Redis 服务名")
    parser.add_argument("--no-postgres", action="store_true", help="跳过 PostgreSQL 备份")
    parser.add_argument("--no-redis", action="store_true", help="跳过 Redis 备份")
    parser.add_argument(
        "--zstd-level",
        type=int,
        default=6,
        help="zstd 压缩等级（1-19），默认 6",
    )
    parser.add_argument(
        "--keep-unpacked",
        action="store_true",
        help="保留未压缩备份目录（默认仅保留 tar.zst）",
    )
    parser.add_argument("--dry-run", action="store_true", help="仅打印计划，不执行备份")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    script_dir = Path(__file__).resolve().parent
    compose_files = [Path(p).resolve() for p in (args.compose_files or [script_dir / "docker-compose.yml"])]
    env_file = Path(args.env_file).resolve()
    project_dir = Path(args.project_directory).resolve()
    out_root = Path(args.output_dir).resolve()
    if args.zstd_level < 1 or args.zstd_level > 19:
        raise BackupError("--zstd-level 必须在 1 到 19 之间")

    env_map = parse_env_file(env_file)
    project_name = args.project_name
    if not project_name:
        raise BackupError("必须显式指定 -p/--project-name，脚本仅备份该 compose project 的容器")

    for file_path in compose_files:
        if not file_path.exists():
            raise BackupError(f"compose 文件不存在: {file_path}")

    base_cmd = compose_base_cmd(compose_files, env_file, project_dir, project_name)
    cfg = load_compose_config(base_cmd)
    services: Dict[str, Dict[str, Any]] = cfg.get("services", {})
    if not services:
        raise BackupError("compose 配置中未找到 services")

    backup_dir = out_root / timestamp_local()
    if not args.dry_run:
        backup_dir.mkdir(parents=True, exist_ok=True)

    meta: Dict[str, Any] = {
        "created_at_utc": now_utc().isoformat(),
        "compose_files": [str(p) for p in compose_files],
        "env_file": str(env_file),
        "project_directory": str(project_dir),
        "project_name": project_name or "",
        "backup_dir": str(backup_dir),
        "archive_file": str(out_root / f"{backup_dir.name}.tar.zst"),
        "metadata_archive_member": f"{backup_dir.name}/backup_meta.json",
        "compression": "tar.zst",
        "zstd_level": args.zstd_level,
        "keep_unpacked": args.keep_unpacked,
        "postgres": {},
        "redis": {},
    }

    if not args.no_postgres:
        pg_service = choose_postgres_service(services, args.postgres_service)
        pg_service_env = normalize_environment(services.get(pg_service, {}).get("environment")) if pg_service else {}
        pg_user, pg_password, pg_db = postgres_creds(pg_service_env, env_map)
        pg_file = backup_dir / f"postgres_{pg_db}_{timestamp_local()}.dump"
        pg_member = archive_member_path(backup_dir, pg_file)

        if pg_service:
            pg_container = (
                dry_run_container_ref(services, pg_service)
                if args.dry_run
                else get_compose_container_id(base_cmd, pg_service)
            )
            backup_postgres_in_container(pg_container, pg_user, pg_password, pg_db, pg_file, args.dry_run)
            meta["postgres"] = {
                "mode": "container",
                "service": pg_service,
                "container_id": pg_container,
                "database": pg_db,
                "user": pg_user,
                "backup_file": str(pg_file) if args.keep_unpacked else pg_member,
                "backup_archive_member": pg_member,
            }
        else:
            db_host, db_port = external_db_conn(env_map)
            backup_postgres_external(db_host, db_port, pg_user, pg_password, pg_db, pg_file, args.dry_run)
            meta["postgres"] = {
                "mode": "external",
                "host": db_host,
                "port": db_port,
                "database": pg_db,
                "user": pg_user,
                "backup_file": str(pg_file) if args.keep_unpacked else pg_member,
                "backup_archive_member": pg_member,
            }

    if not args.no_redis:
        redis_service = choose_redis_service(services, args.redis_service)
        redis_service_env = (
            normalize_environment(services.get(redis_service, {}).get("environment")) if redis_service else {}
        )
        redis_password = (
            redis_service_env.get("REDISCLI_AUTH")
            or env_map.get("REDIS_PASSWORD")
            or env_map.get("REDISCLI_AUTH")
            or ""
        )
        redis_file = backup_dir / f"redis_{timestamp_local()}.rdb"
        redis_member = archive_member_path(backup_dir, redis_file)

        if redis_service:
            redis_container = (
                dry_run_container_ref(services, redis_service)
                if args.dry_run
                else get_compose_container_id(base_cmd, redis_service)
            )
            backup_redis_in_container(redis_container, redis_password, redis_file, args.dry_run)
            meta["redis"] = {
                "mode": "container",
                "service": redis_service,
                "container_id": redis_container,
                "backup_file": str(redis_file) if args.keep_unpacked else redis_member,
                "backup_archive_member": redis_member,
            }
        else:
            redis_host, redis_port = external_redis_conn(env_map)
            backup_redis_external(redis_host, redis_port, redis_password, redis_file, args.dry_run)
            meta["redis"] = {
                "mode": "external",
                "host": redis_host,
                "port": redis_port,
                "backup_file": str(redis_file) if args.keep_unpacked else redis_member,
                "backup_archive_member": redis_member,
            }

    meta_file = backup_dir / "backup_meta.json"
    save_metadata(meta_file, meta, args.dry_run)

    archive_file = archive_backup_dir(
        backup_dir=backup_dir,
        out_root=out_root,
        zstd_level=args.zstd_level,
        dry_run=args.dry_run,
        keep_unpacked=args.keep_unpacked,
    )

    print(f"备份完成: {archive_file}")
    if meta.get("postgres"):
        if args.keep_unpacked:
            print(f"- PostgreSQL: {meta['postgres'].get('backup_file', '-')}")
        else:
            print(f"- PostgreSQL(压缩包内): {meta['postgres'].get('backup_archive_member', '-')}")
    if meta.get("redis"):
        if args.keep_unpacked:
            print(f"- Redis: {meta['redis'].get('backup_file', '-')}")
        else:
            print(f"- Redis(压缩包内): {meta['redis'].get('backup_archive_member', '-')}")
    if args.keep_unpacked:
        print(f"- 元数据: {meta_file}")
        print(f"- 未压缩目录: {backup_dir}")
    else:
        print(f"- 元数据(压缩包内): {backup_dir.name}/backup_meta.json")
        if args.dry_run:
            print(f"- [dry-run] 未压缩目录将被清理: {backup_dir}")
        else:
            print(f"- 未压缩目录已清理: {backup_dir}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except BackupError as exc:
        print(f"[ERROR] {exc}", file=sys.stderr)
        raise SystemExit(1)
