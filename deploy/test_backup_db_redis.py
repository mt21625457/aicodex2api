import contextlib
import importlib.util
import io
import shutil
import tempfile
import unittest
from unittest import mock
from pathlib import Path

MODULE_PATH = Path(__file__).resolve().parent / "backup_db_redis.py"
SPEC = importlib.util.spec_from_file_location("backup_db_redis", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("failed to load backup_db_redis module")
backup = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(backup)


class BackupScriptTests(unittest.TestCase):
    def test_external_db_conn_defaults_to_loopback_and_keeps_custom_port(self) -> None:
        host, port = backup.external_db_conn({"DATABASE_PORT": "15432"})
        self.assertEqual(host, "127.0.0.1")
        self.assertEqual(port, "15432")

    def test_external_redis_conn_normalizes_localhost_to_loopback(self) -> None:
        host, port = backup.external_redis_conn({"REDIS_HOST": "localhost", "REDIS_PORT": "16379"})
        self.assertEqual(host, "127.0.0.1")
        self.assertEqual(port, "16379")

    def test_external_db_conn_rejects_non_loopback_host(self) -> None:
        with self.assertRaises(backup.BackupError) as ctx:
            backup.external_db_conn({"DATABASE_HOST": "10.0.0.8", "DATABASE_PORT": "5432"})
        self.assertIn("DATABASE_HOST 必须是 127.0.0.1", str(ctx.exception))

    def test_external_redis_conn_rejects_invalid_port(self) -> None:
        with self.assertRaises(backup.BackupError) as ctx:
            backup.external_redis_conn({"REDIS_HOST": "127.0.0.1", "REDIS_PORT": "70000"})
        self.assertIn("REDIS_PORT 端口超出范围", str(ctx.exception))

    def test_redact_cmd_masks_sensitive_values(self) -> None:
        cmd = [
            "docker",
            "exec",
            "-e",
            "PGPASSWORD=secret_pwd",
            "-e",
            "REDISCLI_AUTH=secret_redis",
            "postgres",
        ]
        logged = backup.cmd_to_log_string(cmd)
        self.assertIn("PGPASSWORD=***", logged)
        self.assertIn("REDISCLI_AUTH=***", logged)
        self.assertNotIn("secret_pwd", logged)
        self.assertNotIn("secret_redis", logged)

    def test_run_cmd_error_message_redacts_sensitive_values(self) -> None:
        with self.assertRaises(backup.BackupError) as ctx:
            backup.run_cmd(["false", "PGPASSWORD=top_secret"])
        msg = str(ctx.exception)
        self.assertIn("PGPASSWORD=***", msg)
        self.assertNotIn("top_secret", msg)

    def test_parse_env_file_unescapes_only_supported_sequences(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env_path = Path(tmp) / ".env"
            env_path.write_text(
                "export NORMAL=value\n"
                "ESCAPED=\"line\\nnext\"\n"
                "RAW='path\\nkeep'\n"
                "UNKNOWN=\"abc\\x41\"\n",
                encoding="utf-8",
            )
            env_map = backup.parse_env_file(env_path)

        self.assertEqual(env_map["NORMAL"], "value")
        self.assertEqual(env_map["ESCAPED"], "line\nnext")
        self.assertEqual(env_map["RAW"], "path\\nkeep")
        self.assertEqual(env_map["UNKNOWN"], "abc\\x41")

    def test_backup_postgres_in_container_dry_run_masks_password(self) -> None:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            backup.backup_postgres_in_container(
                container_id="postgres",
                db_user="user",
                db_password="very_secret",
                db_name="db",
                out_file=Path("/tmp/test.dump"),
                dry_run=True,
            )
        output = out.getvalue()
        self.assertIn("PGPASSWORD=***", output)
        self.assertNotIn("very_secret", output)

    def test_backup_redis_in_container_dry_run_without_password_unsets_auth(self) -> None:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            backup.backup_redis_in_container(
                container_id="redis",
                redis_password="",
                out_file=Path("/tmp/test.rdb"),
                dry_run=True,
            )
        output = out.getvalue()
        self.assertIn("env -u REDISCLI_AUTH redis-cli", output)

    def test_backup_redis_external_clears_inherited_auth_env_when_password_empty(self) -> None:
        captured = {}

        def fake_run(cmd, *, env=None, capture_output=False, check=True):  # noqa: ANN001
            captured["env"] = dict(env or {})
            return None

        with mock.patch.object(backup, "run_cmd", side_effect=fake_run):
            with mock.patch.object(backup, "ensure_tool_exists"):
                with mock.patch.dict("os.environ", {"REDISCLI_AUTH": "stale-secret"}, clear=False):
                    backup.backup_redis_external(
                        host="127.0.0.1",
                        port="6379",
                        redis_password="",
                        out_file=Path("/tmp/out.rdb"),
                        dry_run=False,
                    )
        self.assertNotIn("REDISCLI_AUTH", captured["env"])

    def test_project_name_required_message(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env_path = Path(tmp) / ".env"
            env_path.write_text("POSTGRES_USER=u\nPOSTGRES_DB=d\n", encoding="utf-8")
            with mock.patch.object(
                backup,
                "parse_args",
                return_value=type(
                    "Args",
                    (),
                    {
                        "compose_files": [],
                        "env_file": str(env_path),
                        "project_directory": str(tmp),
                        "project_name": None,
                        "output_dir": str(Path(tmp) / "backups"),
                        "zstd_level": 6,
                        "postgres_service": None,
                        "redis_service": None,
                        "no_postgres": True,
                        "no_redis": True,
                        "keep_unpacked": False,
                        "dry_run": True,
                    },
                )(),
            ):
                with self.assertRaises(backup.BackupError) as ctx:
                    backup.main()
        self.assertIn("必须显式指定 -p/--project-name", str(ctx.exception))

    def test_archive_backup_dir_dry_run_uses_streaming_pipeline(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out_root = Path(tmp)
            backup_dir = out_root / "20260101_010101"
            backup_dir.mkdir(parents=True, exist_ok=True)

            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                archive_file = backup.archive_backup_dir(
                    backup_dir=backup_dir,
                    out_root=out_root,
                    zstd_level=6,
                    dry_run=True,
                    keep_unpacked=False,
                )

        output = out.getvalue()
        self.assertTrue(str(archive_file).endswith(".tar.zst"))
        self.assertIn("tar", output)
        self.assertIn("-cf -", output)
        self.assertIn("| zstd", output)
        self.assertIn("rm -rf", output)

    def test_archive_backup_dir_creates_tar_zst_and_cleans_source(self) -> None:
        if shutil.which("tar") is None or shutil.which("zstd") is None:
            self.skipTest("tar/zstd is required for this test")

        with tempfile.TemporaryDirectory() as tmp:
            out_root = Path(tmp)
            backup_dir = out_root / "20260101_020202"
            backup_dir.mkdir(parents=True, exist_ok=True)
            (backup_dir / "sample.txt").write_text("hello\\n", encoding="utf-8")

            archive_file = backup.archive_backup_dir(
                backup_dir=backup_dir,
                out_root=out_root,
                zstd_level=3,
                dry_run=False,
                keep_unpacked=False,
            )

            self.assertTrue(archive_file.exists())
            self.assertFalse(backup_dir.exists())


if __name__ == "__main__":
    unittest.main()
