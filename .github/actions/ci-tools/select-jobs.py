#!/usr/bin/env python3
"""Skip only understood PR inputs; unknown or incomplete diffs run every job."""

import json
import os
from pathlib import PurePosixPath, Path
import re
import subprocess
import sys


GO_JOBS = ("go-lint", "go-verify", "go-test", "go-integration", "go-db-pins", "go-db-auth")
WEB_JOBS = ("web", "web-test")
# Run on every event regardless of what the diff touched.
ALWAYS_JOBS = ("repo-checks",)
SHA = re.compile(r"[0-9a-fA-F]{40}\Z")


def all_jobs(reason):
    return {"go": True, "web": True, "reason": reason}


def classify(paths):
    if not paths:
        return all_jobs("empty diff")
    go = web = False
    for path in paths:
        parts = PurePosixPath(path).parts
        if (not parts or path.startswith("/") or ".." in parts
                or any(ord(char) < 32 for char in path)):
            return all_jobs("unrecognized path")
        # These feed both generated Go artifacts and their Web bindings, or
        # change how validation runs. Keep the entire gate for them.
        # Schemas under docs include executable fixtures whose tests glob all
        # extensions, so Markdown there must also retain validation.
        if (path.startswith((".github/", "scripts/", "contracts/", "tools/", "docs/design/schemas/"))
                or path.startswith(("internal/apiv2/", "internal/settingscontract/", "cmd/"))
                or path in {"Makefile", "go.mod", "go.sum", "go.work", "go.work.sum"}
                or path.startswith(".golangci")):
            go = web = True
        elif path.startswith("web/"):
            # Bindings and assets read by Go tests require both sets of checks.
            if (path.endswith(".go") or path.startswith((
                    "web/src/api/v2/", "web/src/settings/",
                    "web/public/images/collection-templates/",
                    "web/assets-source/collection-templates/",
                    "web/public/vendor/pdfjs/standard_fonts/"))
                    or path in {"web/index.html", "web/src/lib/settingsContract.ts",
                                "web/src/lib/settingsConformance.json",
                                "web/src/pages/admin-settings/settingsWorkerDefaults.ts"}):
                go = True
            web = True
        elif path.startswith(("internal/", "migrations/", "pkg/")) or path.endswith(".go"):
            go = True
        elif ((path.startswith("docs/") or len(parts) == 1) and path.endswith(".md")):
            pass
        elif path in {"LICENSE", "NOTICE"}:
            pass
        else:
            return all_jobs("unknown input")
    return {"go": go, "web": web, "reason": "complete PR diff"}


def git(*args):
    return subprocess.run(["git", *args], check=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE).stdout


def plan(event_name, event):
    if event_name != "pull_request":
        return all_jobs("full push or manual validation")
    try:
        pr = event["pull_request"]
        base, head = pr["base"]["sha"], pr["head"]["sha"]
        count = pr["changed_files"]
        if (not isinstance(base, str) or not isinstance(head, str)
                or not SHA.fullmatch(base) or not SHA.fullmatch(head)
                or type(count) is not int or count <= 0):
            return all_jobs("incomplete PR metadata")
        if git("rev-parse", "--is-shallow-repository").strip() != b"false":
            return all_jobs("incomplete checkout history")
        git("cat-file", "-e", base + "^{commit}")
        git("cat-file", "-e", head + "^{commit}")
        git("merge-base", "--is-ancestor", head, "HEAD")
        diff = git("diff", "--name-only", "--no-renames", "-z", base + "..." + head, "--")
        if not diff.endswith(b"\0"):
            return all_jobs("empty or incomplete diff")
        paths = diff[:-1].decode("utf-8").split("\0")
        # --no-renames includes both the deleted and added sides. GitHub can
        # count a rename once, so a larger local count is still complete.
        if len(paths) < count:
            return all_jobs("incomplete changed-file count")
        return classify(paths)
    except (KeyError, TypeError, ValueError, UnicodeError, OSError, subprocess.CalledProcessError):
        return all_jobs("PR comparison unavailable")


def check_results(needs):
    errors = []
    selection = needs.get("select", {})
    if selection.get("result") != "success":
        errors.append("job selection did not succeed")
    for job in ALWAYS_JOBS:
        result = needs.get(job, {}).get("result")
        if result != "success":
            errors.append(job + " ended with " + str(result))
    outputs = selection.get("outputs", {})
    for group, jobs in (("go", GO_JOBS), ("web", WEB_JOBS)):
        selected = outputs.get(group)
        if selected not in {"true", "false"}:
            errors.append(group + " selection is missing or invalid")
        for job in jobs:
            result = needs.get(job, {}).get("result")
            if result != "success" and not (selected == "false" and result == "skipped"):
                errors.append(job + " ended with " + str(result))
    return errors


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in {"plan", "check"}:
        raise SystemExit("expected plan or check")
    if sys.argv[1] == "check":
        errors = check_results(json.loads(os.environ["CI_JOB_RESULTS"]))
        if errors:
            for error in errors:
                print("::error::" + error)
            return 1
        print("Every selected CI job passed.")
        return 0
    try:
        event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
        result = plan(os.environ.get("GITHUB_EVENT_NAME", ""), event)
    except (KeyError, OSError, ValueError, TypeError):
        result = all_jobs("event metadata unavailable")
    print(json.dumps(result, sort_keys=True))
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        for group in ("go", "web"):
            output.write(group + "=" + str(result[group]).lower() + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
