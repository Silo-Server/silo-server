#!/usr/bin/env python3
"""Exercise the pre-commit Go gate against real Git indexes and gofmt."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("check-go-lint.sh")
GIT = shutil.which("git")
BASH = os.environ.get("TEST_BASH", shutil.which("bash"))


class CheckGoLintTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.temp = self.root / "tmp"
        self.temp.mkdir()
        self.output = self.root / "lint.json"
        self.env = os.environ.copy()
        self.env.update(PATH=str(self.bin), BASE_REF="base", TMPDIR=str(self.temp))
        for tool in ("git", "gofmt", "mktemp", "rm", "env", "bash"):
            (self.bin / tool).symlink_to(shutil.which(tool))
        self.git("init", "-q")
        self.git("config", "user.name", "CI test")
        self.git("config", "user.email", "ci-test@example.invalid")
        self.git("config", "core.hooksPath", "/dev/null")
        self.write("a/code.go", "package a\n\nconst Value = 1\n")
        self.write("b/code.go", "package b\n\nconst Value = 1\n")
        self.write("go.mod", "module example.invalid/gate\n\ngo 1.26.4\n")
        self.write(".gitignore", "go.work\ngo.work.sum\n")
        delegate = self.write("scripts/lint-changed.sh", "#!/usr/bin/env bash\nexec golangci-lint run\n")
        delegate.chmod(0o755)
        self.git("add", "--all")
        self.git("commit", "-qm", "fixture")
        self.git("branch", "base")

    def git(self, *args):
        return subprocess.check_output([GIT, *args], cwd=self.repo, text=True)

    def write(self, name, text):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def linter(self, status=0):
        path = self.bin / "golangci-lint"
        path.write_text(
            f"#!{sys.executable}\n"
            "import json, os\n"
            "from pathlib import Path\n"
            f"Path({str(self.output)!r}).write_text(json.dumps({{'gowork': os.environ.get('GOWORK')}}))\n"
            f"raise SystemExit({status})\n"
        )
        path.chmod(0o755)

    def stage_go(self):
        self.write("a/code.go", "package a\n\nconst Value = 2\n")
        self.git("add", "a/code.go")

    def gate(self, *args):
        result = subprocess.run(
            [BASH, str(SCRIPT), *args], cwd=self.repo, env=self.env,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.assertEqual(list(self.temp.iterdir()), [], "temporary inputs leaked")
        return result

    def test_no_go_changes_skip_lint(self):
        self.linter(status=1)
        result = self.gate("--cached")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.output.exists())

    def test_formatting_reads_the_index_with_literal_names(self):
        self.write("café.go", "package a;const X=1\n")
        self.git("add", "café.go")
        self.write("café.go", "package a\n\nconst X = 1\n")
        result = self.gate("--cached")
        self.assertEqual(result.returncode, 1)
        self.assertIn("café.go", result.stderr)

    def test_dangling_symlink_type_change_is_checked(self):
        path = self.repo / "a/code.go"
        path.unlink()
        path.symlink_to("missing.go")
        self.git("add", "a/code.go")
        self.assertIn("T\ta/code.go", self.git("diff", "--cached", "--name-status"))
        result = self.gate("--cached")
        self.assertEqual(result.returncode, 1)
        self.assertIn("gofmt is required", result.stderr)

    def test_git_discovery_failure_cannot_pass_as_no_changes(self):
        self.stage_go()
        self.git("config", "diff.orderFile", "missing-order-file")
        result = self.gate("--cached")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("failed to read orderfile", result.stderr)

    def test_dependency_module_and_untracked_inputs_refuse_lint(self):
        self.linter()
        self.stage_go()
        for name in ("b/code.go", "go.mod", "other/new.go", "notes.txt"):
            with self.subTest(name=name):
                path = self.repo / name
                original = path.read_text() if path.exists() else None
                self.write(name, "unstaged input\n")
                result = self.gate("--cached")
                self.assertEqual(result.returncode, 1)
                self.assertIn("unstaged or untracked", result.stderr)
                self.assertIn("--include-untracked", result.stderr)
                self.assertFalse(self.output.exists())
                if original is None:
                    path.unlink()
                else:
                    path.write_text(original)

    def test_deleted_file_restored_on_disk_refuses_lint(self):
        self.linter()
        self.git("rm", "--cached", "b/code.go")
        result = self.gate("--cached")
        self.assertEqual(result.returncode, 1)
        self.assertFalse(self.output.exists())

    def test_deletion_only_commit_reaches_lint_and_preserves_failure(self):
        self.linter(status=1)
        self.git("rm", "b/code.go")
        result = self.gate("--cached")
        self.assertEqual(result.returncode, 1)
        self.assertTrue(self.output.exists())

    def test_clean_staged_commit_disables_ignored_workspaces(self):
        self.linter()
        self.stage_go()
        self.write("go.work", "invalid ignored workspace\n")
        result = self.gate("--cached")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(self.output.read_text())["gowork"], "off")

    def test_noncached_mode_keeps_local_workspace(self):
        self.linter()
        self.stage_go()
        self.write("a/code.go", "package a\n\nconst Value = 3\n")
        self.env["GOWORK"] = "auto"
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(self.output.read_text())["gowork"], "auto")

    def test_missing_linter_warns_and_keeps_formatting_gate(self):
        self.stage_go()
        result = self.gate("--cached")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("skipping golangci-lint", result.stderr)


if __name__ == "__main__":
    unittest.main()
