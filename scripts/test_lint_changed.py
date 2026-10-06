#!/usr/bin/env python3
"""Exercise lint selection and patch reporting in real temporary Git repositories."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("lint-changed.sh")
GIT = shutil.which("git")
BASH = os.environ.get("TEST_BASH", shutil.which("bash"))


class LintChangedTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.output = self.root / "lint.json"
        self.env = os.environ.copy()
        self.env.update(
            PATH=str(self.bin) + os.pathsep + self.env["PATH"],
            BASE_REF="base",
            LINT_CHANGED_CI="0",
            LINT_CHANGED_BASE_MODE="merge-base",
            LINT_OUTPUT=str(self.output),
            REAL_GIT=GIT,
        )
        self.stub(
            "git",
            """import os, sys
args = sys.argv[1:]
if os.environ.get('FAIL_GIT_DIFF') and args[:1] == ['diff']:
    sys.exit(6)
if os.environ.get('FAIL_NO_INDEX') and '--no-index' in args:
    sys.exit(7)
os.execv(os.environ['REAL_GIT'], [os.environ['REAL_GIT']] + args)
""",
        )
        self.stub(
            "go",
            """import os, pathlib, sys
if os.environ.get('FAIL_GO_LIST'):
    sys.exit(9)
assert sys.argv[1:] == ['list', '-e', '-f', '{{.Dir}}', './...']
for directory, dirs, files in os.walk('.'):
    dirs[:] = sorted(d for d in dirs if d not in ('testdata', 'vendor') and not d.startswith('.'))
    if any(f.endswith('.go') for f in files):
        print(pathlib.Path(directory).resolve())
""",
        )
        self.stub(
            "golangci-lint",
            """import json, os, pathlib, subprocess, sys
args = sys.argv[1:]
patch_arg = next((a for a in args if a.startswith('--new-from-patch=')), None)
patch_path = pathlib.Path(patch_arg.split('=', 1)[1]) if patch_arg else None
patch = patch_path.read_text() if patch_path else None
if patch:
    subprocess.run([os.environ['REAL_GIT'], 'apply', '--reverse', '--check', str(patch_path)], check=True)
pathlib.Path(os.environ['LINT_OUTPUT']).write_text(json.dumps({'args': args, 'patch': patch, 'patch_path': str(patch_path) if patch_path else None}))
issue_line = os.environ.get('LINT_ISSUE_LINE')
if issue_line:
    sys.exit(1 if patch and '+' + issue_line in patch.splitlines() else 0)
sys.exit(int(os.environ.get('LINT_EXIT', '0')))
""",
        )
        self.git("init", "-q")
        self.git("config", "user.name", "CI test")
        self.git("config", "user.email", "ci-test@example.invalid")
        self.git("config", "core.hooksPath", "/dev/null")
        self.write("go.mod", "module example.invalid/linttest\n\ngo 1.26.4\n")
        self.write("root.go", "package linttest\n\nconst Root = 1\n")
        for package in ("a", "b", "c", "deleted"):
            self.write(f"internal/{package}/code.go", f"package {package}\n\nconst Value = 1\n")
        self.commit()
        self.base = self.git("rev-parse", "HEAD").strip()
        self.git("branch", "base")

    def stub(self, name, source):
        path = self.bin / name
        path.write_text(f"#!{sys.executable}\n" + source)
        path.chmod(0o755)

    def git(self, *args):
        return subprocess.check_output([GIT, *args], cwd=self.repo, text=True)

    def write(self, name, text):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "fixture")

    def run_lint(self, *flags, **env):
        self.env.update(env)
        result = subprocess.run(
            [BASH, str(SCRIPT), *flags], cwd=self.repo, env=self.env,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        output = json.loads(self.output.read_text()) if self.output.exists() else None
        if output and output["patch_path"]:
            self.assertFalse(Path(output["patch_path"]).exists(), "temporary patch leaked")
        return result, output

    def packages(self, output):
        return [arg for arg in output["args"] if arg.startswith("./")]

    def test_local_committed_staged_unstaged_and_untracked_changes(self):
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        self.commit()
        self.write("internal/b/code.go", "package b\n\nconst Value = 3\n")
        self.git("add", "internal/b/code.go")
        self.write("internal/c/code.go", "package c\n\nconst Value = 4\n")
        self.write("internal/new/code.go", "package new\n\nconst Added = 5\n")
        result, output = self.run_lint("--timeout=2m")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a", "./internal/b", "./internal/c", "./internal/new"])
        self.assertEqual(output["args"][1:3], ["--enable", "gocritic"])
        self.assertIn("--timeout=2m", output["args"])
        for value in (2, 3, 4):
            self.assertIn(f"+const Value = {value}", output["patch"])
        self.assertIn("+const Added = 5", output["patch"])
        self.assertNotIn("root.go", output["patch"])

    def test_ci_keeps_changed_line_filter_without_duplicate_gocritic(self):
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        result, output = self.run_lint(LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("gocritic", output["args"])
        self.assertTrue(any(arg.startswith("--new-from-patch=") for arg in output["args"]))
        self.assertEqual(self.packages(output), ["./internal/a"])

    def test_forced_git_colors_do_not_hide_changed_line_findings(self):
        self.git("config", "color.diff", "always")
        issue = 'func output() { os.Stdout.Write([]byte("a")) }'
        for name in ("internal/a/code.go", "internal/a/new.go"):
            with self.subTest(name=name):
                self.git("reset", "--hard", "-q", self.base)
                self.git("clean", "-fdq")
                self.write(name, 'package a\n\nimport "os"\n\n' + issue + "\n")
                result, output = self.run_lint(LINT_CHANGED_CI="1", LINT_ISSUE_LINE=issue)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertNotIn("\x1b", output["patch"])
                self.assertIn("+" + issue, output["patch"].splitlines())
                self.assertEqual(self.packages(output), ["./internal/a"])

    def test_pr_uses_merge_base_without_upstream_changes(self):
        self.git("checkout", "-qb", "upstream")
        self.write("internal/b/code.go", "package b\n\nconst Value = 8\n")
        self.commit()
        upstream = self.git("rev-parse", "HEAD").strip()
        self.git("checkout", "-qb", "pr", self.base)
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        self.commit()
        result, output = self.run_lint(BASE_REF=upstream, LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a"])
        self.assertNotIn("internal/b", output["patch"])

    def test_push_before_sha_includes_every_commit_in_push(self):
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        self.commit()
        self.write("internal/b/code.go", "package b\n\nconst Value = 3\n")
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        result, output = self.run_lint(BASE_REF=self.base, LINT_CHANGED_BASE_MODE="revision", LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a", "./internal/b"])

    def test_force_push_available_before_revision_is_not_merge_based(self):
        self.git("checkout", "-qb", "before")
        self.write("internal/b/code.go", "package b\n\nconst Value = 8\n")
        self.commit()
        before = self.git("rev-parse", "HEAD").strip()
        self.git("checkout", "-qb", "replacement", self.base)
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        self.commit()
        result, output = self.run_lint(BASE_REF=before, LINT_CHANGED_BASE_MODE="revision", LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a", "./internal/b"])
        self.assertIn("-const Value = 8", output["patch"])

    def test_empty_missing_and_zero_before_revisions_run_unfiltered_full_tree(self):
        for base in ("", "0" * 40, "f" * 40):
            with self.subTest(base=base):
                result, output = self.run_lint(BASE_REF=base, LINT_CHANGED_BASE_MODE="revision", LINT_CHANGED_CI="1")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(output["args"], ["run", "./..."])
                self.assertIsNone(output["patch"])

    def test_missing_merge_base_runs_unfiltered_full_tree(self):
        result, output = self.run_lint(BASE_REF="missing-ref", LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output["args"], ["run", "./..."])

    def test_shared_inputs_require_full_tree_with_valid_filter(self):
        for name in ("go.mod", ".golangci.yml", "internal/routeinventory/lintrules/check.go", "internal/a/data.json", "include/config.h", "contracts/api/v2/openapi.json"):
            with self.subTest(name=name):
                self.git("reset", "--hard", "-q", self.base)
                self.git("clean", "-fdq")
                self.write(name, "changed input\n")
                result, output = self.run_lint(LINT_CHANGED_CI="1")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.packages(output), ["./..."])
                self.assertIsNotNone(output["patch"])

    def test_deleted_package_requires_full_tree_for_its_importers(self):
        (self.repo / "internal/deleted/code.go").unlink()
        result, output = self.run_lint(LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./..."])
        self.assertIn("deleted file mode", output["patch"])

    def test_embedded_markdown_requires_full_tree(self):
        self.write("internal/policy/vendor.go", 'package policy\n\nimport "embed"\n\n//go:embed vendor\nvar resources embed.FS\n')
        self.write("internal/policy/vendor/README.md", "embedded original\n")
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        self.write("internal/policy/vendor/README.md", "embedded update\n")
        result, output = self.run_lint(LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./..."])
        self.assertEqual(output["patch"], "")

    def test_unchanged_go_rename_does_not_report_inherited_findings(self):
        issue = 'func output() { os.Stdout.Write([]byte("a")) }'
        self.write("internal/a/code.go", 'package a\n\nimport "os"\n\n' + issue + "\n")
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        # Even a user preference disabling rename detection must not turn an
        # unchanged move into newly added lines for the lint filter.
        self.git("config", "diff.renames", "false")
        self.git("mv", "internal/a/code.go", "internal/a/renamed.go")
        result, output = self.run_lint(LINT_CHANGED_CI="1", LINT_ISSUE_LINE=issue)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./..."])
        self.assertIn("similarity index 100%", output["patch"])
        self.assertNotIn("+" + issue, output["patch"].splitlines())

    def test_renamed_package_still_analyzes_importers_and_reports_edits(self):
        self.git("mv", "internal/a", "internal/moved")
        issue = 'func output() { os.Stdout.Write([]byte("a")) }'
        self.write("internal/moved/code.go", 'package a\n\nimport "os"\n\nconst Value = 1\n\n' + issue + "\n")
        self.git("add", "internal/moved/code.go")
        result, output = self.run_lint(LINT_CHANGED_CI="1", LINT_ISSUE_LINE=issue)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(self.packages(output), ["./..."])
        self.assertIn("+" + issue, output["patch"].splitlines())

    def test_root_package_is_selected(self):
        self.write("root.go", "package linttest\n\nconst Root = 2\n")
        result, output = self.run_lint()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./"])

    def test_non_package_testdata_is_not_linted(self):
        self.write("internal/a/testdata/example.go", "package example\n")
        result, output = self.run_lint()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(output)
        self.assertIn("no changed Go packages", result.stdout)

    def test_documentation_only_change_does_not_run_go_or_lint(self):
        self.write("docs/release notes.md", "documentation\n")
        result, output = self.run_lint(FAIL_GO_LIST="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(output)

    def test_unusual_filenames_fail_closed(self):
        for name in ("internal/a/space name.go", "internal/a/new\nline.go", "internal/a/café.go"):
            with self.subTest(name=name):
                self.git("clean", "-fdq")
                self.write(name, "package a\n")
                result, output = self.run_lint(LINT_CHANGED_CI="1")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(output["args"], ["run", "./..."])

    def test_tool_failures_are_not_silently_ignored(self):
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        result, output = self.run_lint(FAIL_GO_LIST="1")
        self.assertEqual(result.returncode, 9, result.stderr)
        self.assertIsNone(output)
        result, output = self.run_lint(FAIL_GO_LIST="", FAIL_GIT_DIFF="1")
        self.assertEqual(result.returncode, 6, result.stderr)
        self.assertIsNone(output)
        self.write("internal/a/new.go", "package a\n")
        result, output = self.run_lint(FAIL_GIT_DIFF="", FAIL_NO_INDEX="1")
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertIsNone(output)

    def test_linter_failure_propagates_and_cleans_patch(self):
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        result, output = self.run_lint(LINT_EXIT="4")
        self.assertEqual(result.returncode, 4, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a"])


if __name__ == "__main__":
    unittest.main()
