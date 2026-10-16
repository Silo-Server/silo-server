#!/usr/bin/env python3
"""Exercise lint selection and patch reporting in real temporary Git repositories."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("lint-changed.sh")
REPO_ROOT = SCRIPT.parent.parent
CHECKS = REPO_ROOT / ".github/actions/ci-tools/go-checks.mk"
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
            """import json, os, pathlib, sys
if os.environ.get('FAIL_GO_LIST'):
    sys.exit(9)
if sys.argv[1:] == ['list', '-e', '-json', './...']:
    for package in json.loads(os.environ.get('GO_PACKAGES', '[]')):
        package['Dir'] = str(pathlib.Path(package['Dir']).resolve())
        print(json.dumps(package))
    sys.exit(0)
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

    def test_missing_push_before_falls_back_to_verified_target_merge_base(self):
        self.git("checkout", "-qb", "feature")
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        self.commit()
        result, output = self.run_lint(BASE_REF="f" * 40, LINT_CHANGED_BASE_MODE="revision",
                                       LINT_CHANGED_TARGET_REF="base", LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a"])
        self.assertIn("+const Value = 2", output["patch"])
        self.assertIn(self.base, result.stderr)

    def test_missing_push_before_on_target_runs_unfiltered_full_tree(self):
        self.git("checkout", "base")
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        self.commit()
        result, output = self.run_lint(BASE_REF="f" * 40, LINT_CHANGED_BASE_MODE="revision",
                                       LINT_CHANGED_TARGET_REF="base", LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNotNone(output, result.stdout + result.stderr)
        self.assertEqual(output["args"], ["run", "./..."])
        self.assertIsNone(output["patch"])

    def test_tested_pr_merge_uses_first_parent(self):
        self.git("checkout", "-qb", "feature")
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        self.commit()
        self.git("checkout", "base")
        self.write("internal/b/code.go", "package b\n\nconst Value = 8\n")
        self.commit()
        parent = self.git("rev-parse", "HEAD").strip()
        self.git("merge", "--no-ff", "-qm", "tested merge", "feature")
        result, output = self.run_lint(BASE_REF=self.base, LINT_CHANGED_BASE_MODE="pr-merge",
                                       LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a"])
        self.assertNotIn("internal/b", output["patch"])
        self.assertIn(parent, result.stderr)

    def test_ci_full_tree_keeps_changed_line_filter(self):
        self.write("internal/a/code.go", "package a\n\nconst Value = 2\n")
        for mode in ("merge-base", "revision"):
            for issue, expected_status in (("const Value = 1", 0), ("const Value = 2", 1)):
                with self.subTest(mode=mode, issue=issue):
                    result, output = self.run_lint(
                        LINT_CHANGED_FULL_TREE="1", LINT_CHANGED_CI="1",
                        LINT_CHANGED_BASE_MODE=mode, LINT_ISSUE_LINE=issue,
                    )
                    self.assertEqual(self.packages(output), ["./..."])
                    self.assertTrue(any(arg.startswith("--new-from-patch=") for arg in output["args"]), output)
                    self.assertEqual(result.returncode, expected_status, result.stderr)
                    self.assertIn("+const Value = 2", output["patch"].splitlines())
                    self.assertNotIn("+const Value = 1", output["patch"].splitlines())
                    self.assertIn(f"resolved base {self.base}", result.stderr)

    def test_go_checks_makefile_is_a_full_tree_trigger(self):
        self.write(".github/actions/ci-tools/go-checks.mk", "lint:\n\tgo vet ./...\n")
        result, output = self.run_lint(LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNotNone(output, result.stdout + result.stderr)
        self.assertEqual(self.packages(output), ["./..."])

    def test_owned_embed_selects_its_package(self):
        self.write("internal/a/data.json", "original\n")
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        self.write("internal/a/data.json", "changed\n")
        metadata = [{"Dir": "internal/a", "ImportPath": "example.invalid/linttest/internal/a", "EmbedFiles": ["data.json"]}]
        result, output = self.run_lint(LINT_CHANGED_CI="1", GO_PACKAGES=json.dumps(metadata))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a"])
        self.assertEqual(output["patch"], "")

    def test_cross_package_embed_uses_nearest_go_directory(self):
        self.write("internal/a/data.json", "changed\n")
        metadata = [{"Dir": "internal/a", "EmbedFiles": ["data.json"]},
                    {"Dir": "internal/b", "TestEmbedFiles": ["../a/data.json"]}]
        result, output = self.run_lint(LINT_CHANGED_CI="1", GO_PACKAGES=json.dumps(metadata))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/a"])

    def test_deleted_non_go_embed_remains_full_tree(self):
        self.write("internal/a/data.json", "original\n")
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        (self.repo / "internal/a/data.json").unlink()
        metadata = [{"Dir": "internal/a", "EmbedFiles": ["data.json"]}]
        result, output = self.run_lint(LINT_CHANGED_CI="1", GO_PACKAGES=json.dumps(metadata))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./..."])

    def test_workflow_and_makefile_comments_still_require_full_tree(self):
        for name in (".github/workflows/ci.yml", "Makefile"):
            self.write(name, "# original comment\n")
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        for name in (".github/workflows/ci.yml", "Makefile"):
            self.write(name, "# changed comment\n")
        result, output = self.run_lint(FAIL_GO_LIST="1", LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./..."])

    def test_non_go_inputs_map_to_nearest_go_directory(self):
        for directory in ("contracts", "migrations", "internal", "cmd", "pkg"):
            with self.subTest(directory=directory):
                self.git("reset", "--hard", "-q", self.base)
                self.git("clean", "-fdq")
                self.write(f"{directory}/owner/owner.go", "package owner\n")
                self.write(f"{directory}/owner/nested/data.json", "input\n")
                result, output = self.run_lint(LINT_CHANGED_CI="1")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.packages(output), [f"./{directory}/owner"])

    def test_no_go_ancestor_requires_full_tree(self):
        (self.repo / "root.go").unlink()
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        self.write("contracts/api/unknown.json", "input\n")
        result, output = self.run_lint(LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./..."])

    def test_workflow_toolchain_and_lint_inputs_remain_full_tree(self):
        for name in (".go-version", ".github/actions/setup-go/action.yml", ".github/workflows/ci.yml",
                     ".github/workflows/trusted-linux-ci.yml"):
            with self.subTest(name=name):
                self.write(name, "GOLANGCI_LINT_VERSION: v9.9.9\n")
                result, output = self.run_lint(LINT_CHANGED_CI="1")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.packages(output), ["./..."])

    def test_shared_inputs_require_full_tree_with_valid_filter(self):
        for name in ("go.mod", ".golangci.yml", "internal/routeinventory/lintrules/check.go", "include/config.h"):
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

    def test_embedded_markdown_maps_to_nearest_go_directory(self):
        self.write("internal/policy/vendor.go", 'package policy\n\nimport "embed"\n\n//go:embed vendor\nvar resources embed.FS\n')
        self.write("internal/policy/vendor/README.md", "embedded original\n")
        self.commit()
        self.git("branch", "-f", "base", "HEAD")
        self.write("internal/policy/vendor/README.md", "embedded update\n")
        result, output = self.run_lint(LINT_CHANGED_CI="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.packages(output), ["./internal/policy"])
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


class TestDependencyGuardTests(unittest.TestCase):
    def run_guard(self, metadata, exit_code="0"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            go = root / "go"
            go.write_text(f"#!{sys.executable}\nimport os, sys\nprint(os.environ['GUARD_METADATA'])\nsys.exit(int(os.environ.get('GUARD_EXIT', '0')))\n")
            go.chmod(0o755)
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"], GUARD_METADATA=metadata, GUARD_EXIT=exit_code)
            return subprocess.run(["make", "-f", str(CHECKS), "test-dependencies"], cwd=root,
                                  env=env, text=True, capture_output=True)

    def test_normal_test_variants_pass(self):
        result = self.run_guard("github.com/Silo-Server/silo-server/internal/api/handlers [github.com/Silo-Server/silo-server/internal/api/handlers.test]|github.com/Silo-Server/silo-server/internal/api/handlers\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_restored_lower_layer_placements_fail(self):
        for package, cause in (("handlers", "auth"), ("apiv2", "api/handlers")):
            owner = "internal/api/handlers" if package == "handlers" else "internal/apiv2"
            result = self.run_guard(f"github.com/Silo-Server/silo-server/{owner} [github.com/Silo-Server/silo-server/internal/{cause}.test]|github.com/Silo-Server/silo-server/internal/{cause}\n")
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("lower-layer external test", result.stdout + result.stderr)

    def test_empty_metadata_and_command_failures_refuse(self):
        for metadata, code in (("", "0"), ("normal package|", "9")):
            with self.subTest(code=code):
                result = self.run_guard(metadata, code)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)


class PipelineReuseTests(unittest.TestCase):
    def step(self, job, name):
        workflow = (REPO_ROOT / ".github/workflows/trusted-linux-ci.yml").read_text()
        body = workflow.split("\n  " + job + ":\n", 1)[1]
        body = re.split(r"\n  [a-z][a-z-]*:\n", body, maxsplit=1)[0]
        step = body.split("      - name: " + name + "\n", 1)[1]
        step = step.split("      - name:", 1)[0]
        if "        run: |\n" in step:
            run = step.split("        run: |\n", 1)[1]
            return "\n".join(line[10:] for line in run.splitlines() if line.startswith("          "))
        return step.split("        run: ", 1)[1].splitlines()[0]

    def execute(self, job, names, **extra_env):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            log = root / "calls.jsonl"
            packages = ("internal/watchtogether", "migrations", "internal/historyimport", "internal/api/handlers",
                        "internal/adminjob", "internal/auth", "internal/recommendations", "cmd/silo",
                        "internal/jellycompat", "internal/plugins")
            for package in packages:
                (root / package).mkdir(parents=True, exist_ok=True)
            go = root / "go"
            binary = f'''#!{sys.executable}
import json, os, pathlib, sys
with open(os.environ['CALL_LOG'], 'a') as output:
    output.write(json.dumps({{'kind': 'run', 'binary': pathlib.Path(sys.argv[0]).relative_to(os.environ['RUNNER_TEMP']).as_posix(), 'cwd': pathlib.Path.cwd().relative_to(os.environ['FIXTURE_ROOT']).as_posix(), 'args': sys.argv[1:], 'database': os.environ.get('SILO_TEST_DATABASE_URL')}}) + '\\n')
sys.exit(int(os.environ.get('TEST_BINARY_EXIT', '0')))
'''
            go.write_text(f'''#!{sys.executable}
import json, os, pathlib, sys
with open(os.environ['CALL_LOG'], 'a') as output:
    output.write(json.dumps({{'kind': 'build', 'args': sys.argv[1:]}}) + '\\n')
if os.environ.get('COMPILE_EXIT'):
    sys.exit(int(os.environ['COMPILE_EXIT']))
target = pathlib.Path(sys.argv[sys.argv.index('-o') + 1])
directory_output = sys.argv[sys.argv.index('-o') + 1].endswith('/')
if directory_output:
    target.mkdir(parents=True, exist_ok=True)
for package in (arg for arg in sys.argv[1:] if arg.startswith('./')):
    output = target / (pathlib.Path(package).name + '.test') if directory_output else target
    output.write_text({binary!r})
    output.chmod(0o755)
''')
            go.chmod(0o755)
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"], RUNNER_TEMP=str(root / "runner"),
                       CALL_LOG=str(log), FIXTURE_ROOT=str(root), SILO_TEST_DATABASE_URL="pgvector-fixture", **extra_env)
            commands = "set -euo pipefail\n" + self.step(job, "Build race test binaries") + "\n"
            workflow = (REPO_ROOT / ".github/workflows/trusted-linux-ci.yml").read_text()
            for name in names:
                step = workflow.split("      - name: " + name + "\n", 1)[1].split("      - name:", 1)[0]
                database = re.search(r"          SILO_TEST_DATABASE_URL: (.*)", step)
                commands += "\n(\n"
                if database:
                    commands += "export SILO_TEST_DATABASE_URL=" + repr(database[1]) + "\n"
                commands += self.step(job, name) + "\n)\n"
            result = subprocess.run([BASH, "-c", commands], cwd=root, env=env, text=True, capture_output=True)
            calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            return result, calls

    def test_auth_binaries_keep_race_filters_and_break_glass_order(self):
        result, calls = self.execute("go-db-auth", ["Test external sign-in against Postgres"])
        self.assertEqual(result.returncode, 0, result.stderr)
        builds = [c for c in calls if c["kind"] == "build"]
        runs = [c for c in calls if c["kind"] == "run"]
        self.assertEqual(len(builds), 1, calls)
        self.assertTrue(all("-race" in c["args"] and "-c" in c["args"] for c in builds), calls)
        self.assertEqual([c["cwd"] for c in runs], ["internal/api/handlers", "internal/auth", "internal/jellycompat", "internal/plugins"])
        self.assertIn("TestLocalLoginSettingRequiresBreakGlassDB", runs[0]["args"][0])
        self.assertTrue(all(c["database"] == "pgvector-fixture" for c in runs), calls)
        self.assertTrue(all(c["binary"] == "race/" + Path(c["cwd"]).name + ".test" for c in runs), calls)
        self.assertEqual([arg for arg in builds[0]["args"] if arg.startswith("./")],
                         ["./internal/api/handlers/", "./internal/auth/", "./internal/jellycompat/", "./internal/plugins/"])
        self.assertTrue(all("-test.count=1" in c["args"] and "-test.timeout=10m" in c["args"] for c in runs), calls)

    def test_compile_and_execution_failures_propagate(self):
        for variable in ("COMPILE_EXIT", "TEST_BINARY_EXIT"):
            with self.subTest(variable=variable):
                result, calls = self.execute("go-db-auth", ["Test external sign-in against Postgres"], **{variable: "17"})
                self.assertEqual(result.returncode, 17, result.stdout + result.stderr)
                if variable == "COMPILE_EXIT":
                    self.assertFalse(any(c["kind"] == "run" for c in calls), calls)

    def test_integration_reuses_migrations_binary_and_package_directories(self):
        result, calls = self.execute("go-integration", ["Test history import cancellation and profile authorization against Postgres",
                                                      "Test the marker claim migration against Postgres"])
        self.assertEqual(result.returncode, 0, result.stderr)
        builds = [c for c in calls if c["kind"] == "build"]
        runs = [c for c in calls if c["kind"] == "run"]
        self.assertEqual(len(builds), 1, calls)
        self.assertTrue(all("-race" in c["args"] for c in builds), calls)
        self.assertEqual([c["cwd"] for c in runs], ["migrations", "internal/historyimport", "internal/api/handlers", "migrations"])
        self.assertEqual(runs[0]["binary"], runs[-1]["binary"])

    def test_integration_keeps_database_assignments_and_all_filters(self):
        names = ["Test Watch Party coordination against Postgres",
                 "Test history import cancellation and profile authorization against Postgres",
                 "Test the marker claim migration against Postgres",
                 "Test image cache cleanup resumption against Postgres",
                 "Test device sign-in against Postgres",
                 "Test taste cluster replacement against Postgres",
                 "Test Jellyfin detail preferences against Postgres"]
        result, calls = self.execute("go-integration", names)
        self.assertEqual(result.returncode, 0, result.stderr)
        runs = [c for c in calls if c["kind"] == "run"]
        self.assertEqual([c["cwd"] for c in runs],
                         ["internal/watchtogether", "migrations", "internal/historyimport", "internal/api/handlers",
                          "migrations", "internal/adminjob", "internal/auth", "internal/recommendations", "cmd/silo"])
        self.assertTrue(all("-test.count=1" in c["args"] and "-test.timeout=10m" in c["args"] for c in runs), calls)
        # Steps override the job's pgvector URL only for the PG17 database.
        workflow = (REPO_ROOT / ".github/workflows/trusted-linux-ci.yml").read_text()
        for name in names[:3]:
            step = workflow.split("      - name: " + name + "\n", 1)[1].split("      - name:", 1)[0]
            self.assertIn("SILO_TEST_DATABASE_URL: postgres://postgres:postgres@localhost:5432/watch_party?sslmode=disable", step)
        self.assertTrue(all(c["database"] == "pgvector-fixture" for c in runs[5:]), calls)
        self.assertTrue(all(c["database"] == "postgres://postgres:postgres@localhost:5432/watch_party?sslmode=disable"
                            for c in runs[:5]), calls)

    def test_database_jobs_use_narrow_migration_runner(self):
        for job in ("go-integration", "go-db-pins", "go-db-auth"):
            command = self.step(job, "Migrate the test database")
            self.assertIn('DATABASE_URL="$SILO_TEST_DATABASE_URL"', command)
            self.assertIn("go run ./scripts/ci/migrate/ --migrate-only", command)
            self.assertNotIn("SECRET_KEY", command)
            self.assertNotIn("-race", command)

    def test_weekly_compiled_cache_never_prunes_or_saves(self):
        source = (REPO_ROOT / ".github/actions/save-go-cache/action.yml").read_text()
        steps = source.split("    - name: ")[1:]
        for step in steps[:2]:
            condition = re.search(r"      if: (.*)", step)[1]
            self.assertIn("github.event_name != 'schedule'", condition)
        for step in steps[2:]:
            self.assertNotIn("github.event_name != 'schedule'", re.search(r"      if: (.*)", step)[1])

    def test_weekly_callers_preserve_module_policy_and_deny_compiled_writes(self):
        workflow = (REPO_ROOT / ".github/workflows/trusted-linux-ci.yml").read_text()
        for step in workflow.split("      - name: Save the Go cache\n")[1:]:
            self.assertIn("github.event_name != 'schedule'", step.splitlines()[0])
        module = workflow.split("      - name: Save the shared module cache on schedule\n")[1]
        self.assertIn("github.event_name == 'schedule'", module.splitlines()[0])
        self.assertIn("module-cache-key:", module.split("\n  go-integration:")[0])


if __name__ == "__main__":
    # The repository CI already runs this entry point for lint helper checks.
    from test_check_go_lint import CheckGoLintTests

    unittest.main()
