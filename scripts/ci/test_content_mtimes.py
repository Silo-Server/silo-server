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

    def test_partial_sparse_clone_diff_name_only_after_normalize(self):
        # Modify both a checked-out file and a sparse-excluded file while
        # keeping an alphabetically earlier tracked file (go.mod) unchanged.
        self.write(self.origin / "internal/a.go", "package internal\n// v2\n")
        self.write(self.origin / "web/src/locales/fr.json", '{"v":2}\n')
        self.git(self.origin, "add", "--all")
        self.git(self.origin, "commit", "-qm", "second")

        bare = self.root / "origin.git"
        self.git(self.root, "clone", "-q", "--bare", str(self.origin), str(bare))
        self.git(bare, "config", "uploadpack.allowfilter", "true")

        clone = self.root / "partial"
        self.git(
            self.root,
            "clone",
            "-q",
            "--no-checkout",
            "--filter=blob:none",
            f"file://{bare}",
            str(clone),
        )
        self.git(clone, "sparse-checkout", "init", "--no-cone")
        self.git(clone, "sparse-checkout", "set", "/*", "!/web/assets-source/", "!/web/src/locales/")
        self.git(clone, "checkout", "-q", "HEAD")

        content_mtimes.normalize(clone)
        file_mtime = (clone / "internal/a.go").lstat().st_mtime_ns
        self.assertEqual(file_mtime, content_mtimes.timestamp((clone / "internal/a.go").read_bytes()))

        # Point origin at a nonexistent path so any unintended promisor blob
        # fetch during `git diff --name-only` fails immediately.
        self.git(clone, "remote", "set-url", "origin", f"file://{self.root / 'missing.git'}")
        diff = subprocess.check_output(
            [
                GIT,
                "diff",
                "--color=never",
                "--no-ext-diff",
                "--no-textconv",
                "--no-renames",
                "--name-only",
                "-z",
                "HEAD~1",
                "--",
            ],
            cwd=clone,
        ).decode()
        changed = [name for name in diff.split("\0") if name]
        self.assertEqual(sorted(changed), ["internal/a.go", "web/src/locales/fr.json"])

        # Excluded base and head locale blobs must remain absent locally.
        missing = self.git(clone, "rev-list", "--objects", "--missing=print", "HEAD~1", "HEAD")
        missing_oids = {line[1:] for line in missing.splitlines() if line.startswith("?")}
        for rev in ("HEAD~1", "HEAD"):
            entry = self.git(clone, "ls-tree", rev, "--", "web/src/locales/fr.json").strip()
            oid = entry.split()[2]
            self.assertIn(oid, missing_oids)


if __name__ == "__main__":
    unittest.main()
