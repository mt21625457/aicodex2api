import importlib.util
import io
import tempfile
import unittest
from pathlib import Path
from unittest import mock

MODULE_PATH = Path(__file__).resolve().parent / "restore_db_redis.py"
SPEC = importlib.util.spec_from_file_location("restore_db_redis", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("failed to load restore_db_redis module")
restore = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(restore)


class RestoreScriptTests(unittest.TestCase):
    def test_safe_archive_member_accepts_normal_path(self) -> None:
        self.assertEqual(
            restore.safe_archive_member("20260304_120000/postgres_sub2api.dump"),
            "20260304_120000/postgres_sub2api.dump",
        )

    def test_safe_archive_member_rejects_parent_traversal(self) -> None:
        with self.assertRaises(restore.BackupError):
            restore.safe_archive_member("../secrets.txt")

    def test_find_metadata_member_prefers_latest_sorted_path(self) -> None:
        members = [
            "20260304_010101/backup_meta.json",
            "20260304_020202/backup_meta.json",
            "20260304_020202/postgres.dump",
        ]
        self.assertEqual(restore.find_metadata_member(members), "20260304_020202/backup_meta.json")

    def test_derive_backup_dir_name_uses_meta_backup_dir(self) -> None:
        meta = {"backup_dir": "/tmp/backups/20260304_120000"}
        name = restore.derive_backup_dir_name(meta, "20260304_120000/backup_meta.json")
        self.assertEqual(name, "20260304_120000")

    def test_resolve_data_member_prefers_backup_archive_member(self) -> None:
        section = {"backup_archive_member": "20260304_120000/redis_1.rdb"}
        member = restore.resolve_data_member(section, "20260304_120000")
        self.assertEqual(member, "20260304_120000/redis_1.rdb")

    def test_resolve_data_member_legacy_absolute_path(self) -> None:
        section = {"backup_file": "/abs/path/to/postgres_sub2api.dump"}
        member = restore.resolve_data_member(section, "20260304_120000")
        self.assertEqual(member, "20260304_120000/postgres_sub2api.dump")

    def test_resolve_data_member_legacy_basename(self) -> None:
        section = {"backup_file": "postgres_sub2api.dump"}
        member = restore.resolve_data_member(section, "20260304_120000")
        self.assertEqual(member, "20260304_120000/postgres_sub2api.dump")

    def test_main_external_redis_rejects_non_loopback_host(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            archive = root / "backup.tar.zst"
            archive.write_bytes(b"fake")
            compose_file = root / "docker-compose.yml"
            compose_file.write_text("services: {}", encoding="utf-8")
            env_file = root / ".env"
            env_file.write_text("REDIS_HOST=10.0.0.8\n", encoding="utf-8")

            args = type(
                "Args",
                (),
                {
                    "archive_file": str(archive),
                    "compose_files": [str(compose_file)],
                    "env_file": str(env_file),
                    "project_directory": str(root),
                    "project_name": "testproj",
                    "postgres_service": None,
                    "redis_service": None,
                    "no_postgres": True,
                    "no_redis": False,
                    "postgres_clean": True,
                    "redis_rdb_path": "/data/dump.rdb",
                    "no_redis_restart": False,
                    "redis_external_rdb_path": str(root / "restore.rdb"),
                    "redis_external_restart_cmd": None,
                    "dry_run": True,
                    "force": False,
                },
            )()

            with mock.patch.object(restore, "parse_args", return_value=args):
                with mock.patch.object(restore, "list_archive_members", return_value=["20260304/backup_meta.json"]):
                    with mock.patch.object(restore, "find_metadata_member", return_value="20260304/backup_meta.json"):
                        with mock.patch.object(
                            restore,
                            "read_archive_member_json",
                            return_value={
                                "backup_dir": "/tmp/20260304",
                                "postgres": {},
                                "redis": {"backup_archive_member": "20260304/redis.rdb"},
                            },
                        ):
                            with mock.patch.object(restore.common, "compose_base_cmd", return_value=["docker", "compose"]):
                                with mock.patch.object(
                                    restore.common,
                                    "load_compose_config",
                                    return_value={"services": {"app": {"image": "busybox"}}},
                                ):
                                    with mock.patch.object(restore.common, "parse_env_file", return_value={"REDIS_HOST": "10.0.0.8"}):
                                        with self.assertRaises(restore.BackupError) as ctx:
                                            restore.main()

        self.assertIn("REDIS_HOST 必须是 127.0.0.1", str(ctx.exception))

    def test_main_external_redis_prints_loopback_target(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            archive = root / "backup.tar.zst"
            archive.write_bytes(b"fake")
            compose_file = root / "docker-compose.yml"
            compose_file.write_text("services: {}", encoding="utf-8")
            env_file = root / ".env"
            env_file.write_text("REDIS_HOST=127.0.0.1\nREDIS_PORT=16379\n", encoding="utf-8")

            args = type(
                "Args",
                (),
                {
                    "archive_file": str(archive),
                    "compose_files": [str(compose_file)],
                    "env_file": str(env_file),
                    "project_directory": str(root),
                    "project_name": "testproj",
                    "postgres_service": None,
                    "redis_service": None,
                    "no_postgres": True,
                    "no_redis": False,
                    "postgres_clean": True,
                    "redis_rdb_path": "/data/dump.rdb",
                    "no_redis_restart": False,
                    "redis_external_rdb_path": str(root / "restore.rdb"),
                    "redis_external_restart_cmd": None,
                    "dry_run": True,
                    "force": False,
                },
            )()

            stdout = io.StringIO()
            with mock.patch.object(restore, "parse_args", return_value=args):
                with mock.patch.object(restore, "list_archive_members", return_value=["20260304/backup_meta.json"]):
                    with mock.patch.object(restore, "find_metadata_member", return_value="20260304/backup_meta.json"):
                        with mock.patch.object(
                            restore,
                            "read_archive_member_json",
                            return_value={
                                "backup_dir": "/tmp/20260304",
                                "postgres": {},
                                "redis": {"backup_archive_member": "20260304/redis.rdb"},
                            },
                        ):
                            with mock.patch.object(restore.common, "compose_base_cmd", return_value=["docker", "compose"]):
                                with mock.patch.object(
                                    restore.common,
                                    "load_compose_config",
                                    return_value={"services": {"app": {"image": "busybox"}}},
                                ):
                                    with mock.patch.object(
                                        restore.common,
                                        "parse_env_file",
                                        return_value={"REDIS_HOST": "127.0.0.1", "REDIS_PORT": "16379"},
                                    ):
                                        with mock.patch.object(
                                            restore,
                                            "restore_redis_external",
                                            return_value=None,
                                        ) as restore_external_mock:
                                            with mock.patch("sys.stdout", stdout):
                                                rc = restore.main()

        self.assertEqual(rc, 0)
        restore_external_mock.assert_called_once()
        self.assertIn("Redis 恢复目标: 127.0.0.1:16379", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
