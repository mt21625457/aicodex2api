import importlib.util
import unittest
from pathlib import Path

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


if __name__ == "__main__":
    unittest.main()
