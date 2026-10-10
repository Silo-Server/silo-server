#!/usr/bin/env python3
"""Exercise job admission with real Git diffs and final Actions outcomes."""

import copy
from contextlib import contextmanager
import errno
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("select-jobs.py").resolve()
sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("select_jobs", SCRIPT)
selector = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(selector)
DEFAULT_EVENT = object()
# Go regression tests read these Web inputs directly from the checkout.
GO_READ_WEB_INPUTS = (
    "web/index.html",
    "web/src/pages/admin-settings/settingsWorkerDefaults.ts",
    "web/public/images/collection-templates/tmdb_franchise_monsterverse.jpg",
    "web/assets-source/collection-templates/raw/tmdb_franchise_monsterverse.png",
    "web/public/vendor/pdfjs/standard_fonts/LiberationSans-Regular.ttf",
)


@contextmanager
def working_directory(path):
    previous = Path.cwd()
    try:
        os.chdir(path)
        yield
    finally:
        os.chdir(previous)


class Repository:
    def __init__(self, path):
        self.path = Path(path)
        self.path.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "CI selection test")
        self.git("config", "user.email", "ci@example.test")
        self.git("config", "core.hooksPath", "/dev/null")
        self.write("README.md", "initial documentation\n")
        self.base = self.commit()

    def git(self, *args):
        return subprocess.run(
            ["git", *args], cwd=self.path, check=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        ).stdout.decode().strip()

    def write(self, name, contents="changed input\n"):
        file = self.path / name
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(contents)

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "Change test input")
        return self.git("rev-parse", "HEAD")

    def event(self, head=None, count=1):
        return {"pull_request": {
            "base": {"sha": self.base},
            "head": {"sha": head or self.git("rev-parse", "HEAD")},
            "changed_files": count,
        }}

    def plan(self, event=DEFAULT_EVENT, event_name="pull_request"):
        with working_directory(self.path):
            return selector.plan(event_name, self.event() if event is DEFAULT_EVENT else event)


class GitSelectionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.repo = Repository(Path(self.temporary.name) / "repo")

    def assert_groups(self, result, go, web):
        self.assertIs(result["go"], go, result)
        self.assertIs(result["web"], web, result)

    def test_understood_inputs_choose_only_required_groups(self):
        cases = [
            ("README.md", False, False),
            ("docs/architecture/ci.md", False, False),
            ("docs/design/diagnostics.md", False, False),
            ("docs/design/schemas-other/notes.md", False, False),
            ("LICENSE", False, False),
            ("NOTICE", False, False),
            ("web/src/components/Toast.tsx", False, True),
            ("web/package.json", False, True),
            ("web/pnpm-lock.yaml", False, True),
            ("web/scripts/generate-apiv2.mjs", False, True),
            ("web/src/pages/admin-settings/AdminSettings.tsx", False, True),
            ("web/src/pages/admin-settings/settingsWorkerDefaults.test.ts", False, True),
            ("web/public/images/icon.svg", False, True),
            ("web/public/images/collection-templates-other/action.png", False, True),
            ("web/assets-source/banner.svg", False, True),
            ("web/assets-source/collection-templates-other/action.svg", False, True),
            ("web/public/vendor/pdfjs/pdf.worker.mjs", False, True),
            ("web/public/vendor/pdfjs/standard_fonts_other/font.pfb", False, True),
            ("internal/catalog/history.go", True, False),
            ("internal/scenariocatalog/testdata/fixture.json", True, False),
            ("internal/policy/vendor/README.md", True, False),
            ("internal/userdb/testdata/schema.sql", True, False),
            ("internal/ebookconvert/mobitool.wasm", True, False),
            ("migrations/sql/20261001000000_change.sql", True, False),
            ("pkg/client/client.go", True, False),
            ("example.go", True, False),
            ("docs/example.go", True, False),
        ]
        for number, (path, go, web) in enumerate(cases):
            with self.subTest(path=path):
                self.repo.base = self.repo.git("rev-parse", "HEAD")
                self.repo.write(path, "change %s\n" % number)
                self.repo.commit()
                event = self.repo.event()
                snapshot = copy.deepcopy(event)
                self.assert_groups(self.repo.plan(event), go, web)
                self.assertEqual(event, snapshot, "PR metadata must remain immutable")

    def test_shared_generation_and_validation_inputs_require_both_groups(self):
        cases = [
            ".github/workflows/ci.yml",
            ".github/actions/README.md",
            "scripts/check-local-path-leaks.sh",
            "contracts/api/v2/openapi.json",
            "contracts/api/v2/README.md",
            "contracts/settings/v1/conformance.json",
            "tools/generate.py",
            "internal/apiv2/registry.go",
            "internal/apiv2/docsui/LICENSE",
            "internal/settingscontract/contract.go",
            "cmd/settingsgen/main.go",
            "Makefile",
            "go.mod",
            "go.sum",
            "go.work",
            "go.work.sum",
            ".golangci.yml",
            "web/embed.go",
            "web/example_test.go",
            "web/src/api/v2/schema.ts",
            "web/src/api/v2/operations.ts",
            "web/src/lib/settingsContract.ts",
            "web/src/lib/settingsConformance.json",
        ]
        cases.extend(GO_READ_WEB_INPUTS)
        for path in cases:
            with self.subTest(path=path):
                self.repo.base = self.repo.git("rev-parse", "HEAD")
                self.repo.write(path)
                self.repo.commit()
                self.assert_groups(self.repo.plan(), True, True)

    def test_markdown_inside_schema_inputs_is_not_skipped_as_documentation(self):
        for path in (
            "docs/design/schemas/README.md",
            "docs/design/schemas/client-diagnostics/v1/fixtures/valid/notes.md",
            "docs/design/schemas/client-diagnostics/v1/fixtures/invalid/notes.md",
        ):
            with self.subTest(path=path):
                self.repo.base = self.repo.git("rev-parse", "HEAD")
                self.repo.write(path)
                self.repo.commit()
                self.assert_groups(self.repo.plan(), True, True)

    def test_unknown_inputs_run_both_groups(self):
        for path in ("Dockerfile", "docker-compose.yml", ".env.example",
                     "docs/schema.json", "new-input/config.yaml"):
            with self.subTest(path=path):
                self.repo.base = self.repo.git("rev-parse", "HEAD")
                self.repo.write(path)
                self.repo.commit()
                self.assert_groups(self.repo.plan(), True, True)

    def test_mixed_frontend_and_backend_changes_require_both_groups(self):
        self.repo.write("web/src/Toast.tsx")
        self.repo.write("internal/catalog/history.go")
        self.repo.commit()
        self.assert_groups(self.repo.plan(self.repo.event(count=2)), True, True)

    def test_modified_shared_web_input_and_normal_web_change_require_both_groups(self):
        self.repo.write("web/index.html", "original shell\n")
        self.repo.base = self.repo.commit()
        self.repo.write("web/index.html", "changed shell\n")
        self.repo.write("web/src/components/Toast.tsx")
        self.repo.commit()
        self.assert_groups(self.repo.plan(self.repo.event(count=2)), True, True)

    def test_deleted_go_file_still_requires_go_jobs(self):
        self.repo.write("internal/catalog/deleted.go")
        self.repo.base = self.repo.commit()
        (self.repo.path / "internal/catalog/deleted.go").unlink()
        self.repo.commit()
        self.assert_groups(self.repo.plan(), True, False)

    def test_deleted_web_inputs_read_by_go_still_require_both_groups(self):
        for path in GO_READ_WEB_INPUTS:
            with self.subTest(path=path):
                self.repo.write(path)
                self.repo.base = self.repo.commit()
                (self.repo.path / path).unlink()
                self.repo.commit()
                self.assert_groups(self.repo.plan(), True, True)

    def test_rename_checks_both_sides_even_when_github_counts_one_file(self):
        for source, target, go, web in (
            ("internal/catalog/source.go", "docs/renamed.md", True, False),
            ("docs/source.md", "web/src/renamed.ts", False, True),
            ("web/src/source.ts", "internal/catalog/renamed.go", True, True),
            ("web/index.html", "docs/retired-shell.md", True, True),
            ("docs/worker-defaults.md", "web/src/pages/admin-settings/settingsWorkerDefaults.ts", True, True),
            ("web/public/images/collection-templates/retired.jpg", "web/public/images/ordinary.jpg", True, True),
            ("web/assets-source/ordinary.png", "web/assets-source/collection-templates/raw/new.png", True, True),
            ("web/public/vendor/pdfjs/standard_fonts/retired.ttf", "docs/font-retired.md", True, True),
            ("docs/fixture-notes.md", "docs/design/schemas/client-diagnostics/v1/fixtures/valid/notes.md", True, True),
            ("docs/design/schemas/client-diagnostics/v1/fixtures/valid/old-notes.md", "docs/retired-fixture-notes.md", True, True),
        ):
            with self.subTest(source=source, target=target):
                self.repo.write(source, "same rename contents\n")
                self.repo.base = self.repo.commit()
                destination = self.repo.path / target
                destination.parent.mkdir(parents=True, exist_ok=True)
                (self.repo.path / source).rename(destination)
                self.repo.commit()
                self.assert_groups(self.repo.plan(self.repo.event(count=1)), go, web)

    def test_spaces_quotes_and_unicode_are_complete_git_paths(self):
        for path in ("docs/a file.md", "docs/a'quoted\"file.md", "docs/caf\u00e9.md"):
            with self.subTest(path=path):
                self.repo.base = self.repo.git("rev-parse", "HEAD")
                self.repo.write(path)
                self.repo.commit()
                self.assert_groups(self.repo.plan(), False, False)

    def test_control_character_filename_forces_all_jobs(self):
        self.repo.write("docs/line\nbreak.md")
        self.repo.commit()
        self.assert_groups(self.repo.plan(), True, True)

    def test_non_utf8_filename_forces_all_jobs(self):
        file = os.fsencode(self.repo.path) + b"/invalid-\xff.md"
        try:
            with open(file, "wb") as output:
                output.write(b"test\n")
        except OSError as error:
            if error.errno != errno.EILSEQ:
                raise
            # Filesystems that require UTF-8 cannot create this Git input.
            # Keep real commit/history checks and substitute only the diff bytes.
            self.repo.write("docs/ci.md")
            self.repo.commit()
            real_git = selector.git

            def invalid_diff(*args):
                return b"invalid-\xff.md\0" if args[0] == "diff" else real_git(*args)

            with patch.object(selector, "git", invalid_diff):
                self.assert_groups(self.repo.plan(), True, True)
            return
        self.repo.commit()
        self.assert_groups(self.repo.plan(), True, True)

    def test_invalid_path_shapes_are_not_allowlisted(self):
        for path in ("", "/docs/file.md", "docs/../file.md", "docs/\x00file.md"):
            with self.subTest(path=path):
                self.assert_groups(selector.classify([path]), True, True)

    def test_empty_diff_with_positive_file_count_runs_everything(self):
        self.assert_groups(self.repo.plan(self.repo.event(count=1)), True, True)

    def test_incomplete_file_count_runs_everything(self):
        self.repo.write("docs/ci.md")
        self.repo.commit()
        self.assert_groups(self.repo.plan(self.repo.event(count=2)), True, True)

    def test_invalid_metadata_runs_everything(self):
        self.repo.write("docs/ci.md")
        self.repo.commit()
        event = self.repo.event()
        cases = [None, [], {}, {"pull_request": {}}, {"pull_request": None}]
        for field in ("base", "head"):
            for sha in (None, 42, "main", "f" * 39, "z" * 40, "a" * 40 + "\n"):
                invalid = copy.deepcopy(event)
                invalid["pull_request"][field]["sha"] = sha
                cases.append(invalid)
        for count in (None, True, False, 0, -1, "1", 1.0):
            invalid = copy.deepcopy(event)
            invalid["pull_request"]["changed_files"] = count
            cases.append(invalid)
        for invalid in cases:
            with self.subTest(event=invalid):
                self.assert_groups(self.repo.plan(invalid), True, True)

    def test_missing_commit_runs_everything(self):
        self.repo.write("docs/ci.md")
        self.repo.commit()
        for field in ("base", "head"):
            with self.subTest(field=field):
                event = self.repo.event()
                event["pull_request"][field]["sha"] = "f" * 40
                self.assert_groups(self.repo.plan(event), True, True)

    def test_unrelated_histories_do_not_allow_selection(self):
        self.repo.git("checkout", "--orphan", "unrelated")
        self.repo.git("rm", "-qrf", ".")
        self.repo.write("docs/ci.md")
        self.repo.commit()
        self.assert_groups(self.repo.plan(), True, True)

    def test_missing_checkout_runs_everything(self):
        event = self.repo.event()
        with working_directory(self.temporary.name):
            self.assert_groups(selector.plan("pull_request", event), True, True)

    def test_checkout_must_contain_the_requested_pr_head(self):
        self.repo.write("docs/ci.md")
        head = self.repo.commit()
        self.repo.git("checkout", "-q", self.repo.base)
        self.assert_groups(self.repo.plan(self.repo.event(head=head)), True, True)

    def test_merge_checkout_uses_pr_diff_not_base_only_changes(self):
        self.repo.git("checkout", "-qb", "feature")
        self.repo.write("docs/ci.md")
        head = self.repo.commit()
        self.repo.git("checkout", "-qb", "base-update", self.repo.base)
        self.repo.write("internal/catalog/unrelated.go")
        updated_base = self.repo.commit()
        self.repo.git("merge", "--no-ff", "-qm", "Merge feature", "feature")
        event = self.repo.event(head=head)
        event["pull_request"]["base"]["sha"] = updated_base
        self.assert_groups(self.repo.plan(event), False, False)

    def test_shallow_checkout_does_not_skip_jobs(self):
        self.repo.write("docs/ci.md")
        self.repo.commit()
        event = self.repo.event()
        shallow = Path(self.temporary.name) / "shallow"
        subprocess.run(
            ["git", "clone", "-q", "--depth", "1", self.repo.path.as_uri(), str(shallow)],
            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        with working_directory(shallow):
            self.assert_groups(selector.plan("pull_request", event), True, True)

    def test_blobless_checkout_selects_from_trees_without_fetching_blobs(self):
        # CI checks out full history with --filter=blob:none to keep this job
        # fast. A delete and a similar add would need both blobs if the diff
        # ever looked for renames.
        text = "shared documentation text\n" * 20
        self.repo.write("docs/old.md", text)
        base = self.repo.commit()
        (self.repo.path / "docs/old.md").unlink()
        self.repo.write("docs/new.md", text + "one more line\n")
        self.repo.commit()
        blobs = [self.repo.git("rev-parse", base + ":docs/old.md"),
                 self.repo.git("rev-parse", "HEAD:docs/new.md")]
        self.repo.git("config", "uploadpack.allowFilter", "true")
        event = self.repo.event(count=2)
        event["pull_request"]["base"]["sha"] = base
        blobless = Path(self.temporary.name) / "blobless"
        subprocess.run(
            ["git", "clone", "-q", "--filter=blob:none", "--no-checkout",
             self.repo.path.as_uri(), str(blobless)],
            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        with working_directory(blobless):
            self.assert_groups(selector.plan("pull_request", event), False, False)
        missing = subprocess.run(
            ["git", "rev-list", "--objects", "--all", "--missing=print"], cwd=blobless,
            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        ).stdout.decode().splitlines()
        for blob in blobs:
            self.assertIn("?" + blob, missing)

    def test_push_force_push_and_manual_validation_always_run_everything(self):
        self.repo.write("docs/ci.md")
        self.repo.commit()
        for name, event in (("push", {"forced": False}), ("push", {"forced": True}),
                            ("workflow_dispatch", {}), ("workflow_call", {}),
                            ("pull_request_target", self.repo.event()), ("", {})):
            with self.subTest(event=name, payload=event):
                self.assert_groups(self.repo.plan(event, name), True, True)

    def test_cli_emits_only_boolean_group_outputs(self):
        self.repo.write("docs/ci.md")
        self.repo.commit()
        event_path = Path(self.temporary.name) / "event.json"
        output_path = Path(self.temporary.name) / "outputs"
        event_path.write_text(json.dumps(self.repo.event()))
        environment = dict(os.environ, GITHUB_EVENT_NAME="pull_request",
                           GITHUB_EVENT_PATH=str(event_path), GITHUB_OUTPUT=str(output_path))
        run = subprocess.run([sys.executable, str(SCRIPT), "plan"], cwd=self.repo.path,
                             env=environment, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assert_groups(json.loads(run.stdout), False, False)
        self.assertEqual(output_path.read_text(), "go=false\nweb=false\n")
        event_path.write_text("not JSON")
        output_path.unlink()
        run = subprocess.run([sys.executable, str(SCRIPT), "plan"], cwd=self.repo.path,
                             env=environment, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(output_path.read_text(), "go=true\nweb=true\n")


class AggregateResultTests(unittest.TestCase):
    def results(self, go="true", web="true"):
        needs = {"select": {"result": "success", "outputs": {"go": go, "web": web}}}
        for job in selector.ALWAYS_JOBS:
            needs[job] = {"result": "success"}
        for group, jobs in ((go, selector.GO_JOBS), (web, selector.WEB_JOBS)):
            for job in jobs:
                needs[job] = {"result": "success" if group == "true" else "skipped"}
        return needs

    def test_all_required_jobs_succeed(self):
        self.assertEqual(selector.check_results(self.results()), [])

    def test_only_unselected_groups_may_skip(self):
        for go, web in (("false", "false"), ("true", "false"), ("false", "true")):
            with self.subTest(go=go, web=web):
                self.assertEqual(selector.check_results(self.results(go, web)), [])

    def test_failure_cancel_and_unexpected_skip_block_the_aggregate(self):
        for job in selector.GO_JOBS + selector.WEB_JOBS:
            for result in ("failure", "cancelled", "skipped", "timed_out", None):
                with self.subTest(job=job, result=result):
                    needs = self.results()
                    needs[job]["result"] = result
                    self.assertTrue(selector.check_results(needs))

    def test_unselected_failed_or_cancelled_job_still_blocks(self):
        for job in selector.GO_JOBS + selector.WEB_JOBS:
            for result in ("failure", "cancelled"):
                with self.subTest(job=job, result=result):
                    needs = self.results("false", "false")
                    needs[job]["result"] = result
                    self.assertTrue(selector.check_results(needs))

    def test_unselected_jobs_that_ran_successfully_are_accepted(self):
        needs = self.results("false", "false")
        for job in selector.GO_JOBS + selector.WEB_JOBS:
            needs[job]["result"] = "success"
        self.assertEqual(selector.check_results(needs), [])

    def test_missing_selection_or_outputs_block_the_aggregate(self):
        for remove in ("select", "outputs", "go", "web"):
            with self.subTest(remove=remove):
                needs = self.results()
                if remove == "select":
                    del needs["select"]
                elif remove == "outputs":
                    del needs["select"]["outputs"]
                else:
                    del needs["select"]["outputs"][remove]
                self.assertTrue(selector.check_results(needs))

    def test_unsuccessful_selection_blocks_even_if_every_job_succeeds(self):
        for result in ("failure", "cancelled", "skipped", None):
            with self.subTest(result=result):
                needs = self.results()
                needs["select"]["result"] = result
                self.assertTrue(selector.check_results(needs))

    def test_selection_requires_lowercase_boolean_strings(self):
        for group in ("go", "web"):
            for value in (True, False, "True", "False", "", "yes", None):
                with self.subTest(group=group, value=value):
                    needs = self.results()
                    needs["select"]["outputs"][group] = value
                    self.assertTrue(selector.check_results(needs))

    def test_missing_required_job_outcome_blocks_even_when_unselected(self):
        for selected in ("true", "false"):
            for job in selector.GO_JOBS + selector.WEB_JOBS:
                with self.subTest(selected=selected, job=job):
                    needs = self.results(selected, selected)
                    del needs[job]
                    self.assertTrue(selector.check_results(needs))

    def test_always_required_jobs_block_whatever_was_selected(self):
        for selected in ("true", "false"):
            for job in selector.ALWAYS_JOBS:
                for result in ("failure", "cancelled", "skipped", "timed_out", None):
                    with self.subTest(selected=selected, job=job, result=result):
                        needs = self.results(selected, selected)
                        needs[job]["result"] = result
                        self.assertTrue(selector.check_results(needs))
                with self.subTest(selected=selected, job=job, result="missing"):
                    needs = self.results(selected, selected)
                    del needs[job]
                    self.assertTrue(selector.check_results(needs))

    def test_check_cli_returns_failure_and_identifies_the_failed_job(self):
        for failed in (False, True):
            with self.subTest(failed=failed):
                needs = self.results()
                if failed:
                    needs["go-integration"]["result"] = "failure"
                run = subprocess.run(
                    [sys.executable, str(SCRIPT), "check"], capture_output=True, text=True,
                    env=dict(os.environ, CI_JOB_RESULTS=json.dumps(needs)),
                )
                self.assertEqual(run.returncode, 1 if failed else 0, run.stderr)
                if failed:
                    self.assertIn("::error::go-integration ended with failure", run.stdout)


if __name__ == "__main__":
    unittest.main()
