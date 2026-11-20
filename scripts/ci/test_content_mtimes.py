#!/usr/bin/env python3
"""Exercise content_mtimes.py on full and sparse Git checkouts."""
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import content_mtimes  # noqa: E402


GIT = shutil.which("git")


class ContentMtimesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.origin = self.root / "origin"
        self.origin.mkdir()
        self.git(self.origin, "init", "-q")
        self.git(self.origin, "config", "user.name", "CI test")
        self.git(self.origin, "config", "user.email", "ci-test@example.invalid")
        self.git(self.origin, "config", "core.hooksPath", "/dev/null")
        self.write(self.origin / "go.mod", "module example.invalid/mtimes\n")
        self.write(self.origin / "internal/a.go", "package internal\n")
        self.write(self.origin / "web/assets-source/plate.png", "binary-placeholder")
        self.write(self.origin / "web/src/locales/fr.json", "{}\n")
        self.git(self.origin, "add", "--all")
        self.git(self.origin, "commit", "-qm", "fixture")

    def git(self, cwd, *args):
        return subprocess.check_output([GIT, *args], cwd=cwd, text=True)

    def write(self, path, text):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def test_sparse_checkout_skips_excluded_paths_and_stamps_present_files(self):
        clone = self.root / "sparse"
        self.git(self.root, "clone", "-q", "--no-checkout", str(self.origin), str(clone))
        self.git(clone, "sparse-checkout", "init", "--no-cone")
        self.git(clone, "sparse-checkout", "set", "/*", "!/web/assets-source/", "!/web/src/locales/")
        self.git(clone, "checkout", "-q", "HEAD")

        self.assertFalse((clone / "web/assets-source").exists())
        self.assertFalse((clone / "web/src/locales").exists())

        tracked = content_mtimes.tracked_worktree_files(clone)
        self.assertEqual(sorted(tracked), ["go.mod", "internal/a.go"])

        content_mtimes.stamp(clone, tracked)
        expected = content_mtimes.timestamp((clone / "internal/a.go").read_bytes())
        self.assertEqual((clone / "internal/a.go").lstat().st_mtime_ns, expected)

    def test_missing_non_sparse_tracked_file_still_fails(self):
        (self.origin / "internal/a.go").unlink()
        tracked = content_mtimes.tracked_worktree_files(self.origin)
        self.assertIn("internal/a.go", tracked)
        with self.assertRaises(FileNotFoundError):
            content_mtimes.stamp(self.origin, tracked)


if __name__ == "__main__":
    unittest.main()
