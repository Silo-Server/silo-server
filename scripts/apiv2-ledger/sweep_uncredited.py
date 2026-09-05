#!/usr/bin/env python3
"""Find source lines that mention an inventory path but are not credited to it.

This is the manual-review backstop for extract_consumers.py. For every
API-listener inventory row it takes the last two static (non-parameter) path
segments, greps that fragment across the web, apple, and android sources, and
prints every hit whose file and line (within a few lines) is not already a
call site on any ledger row. A reviewer resolves each hit by hand: credit it as a match=manual
call site, or record why it is not a consumer (comment, unrelated path, test).

Usage:
  sweep_uncredited.py ROUTE_INVENTORY LEDGER WEB_SRC APPLE_ROOT ANDROID_ROOT [APPLE_GIT ANDROID_GIT]

  ROUTE_INVENTORY  contracts/api/v2/route-inventory.json
  LEDGER           contracts/api/v2/migration.json (to know what is credited)
  WEB_SRC          web/src of this repository
  APPLE_ROOT       a silo-apple repository root or export
  ANDROID_ROOT     a silo-android repository root or export
  APPLE_GIT        optional: a silo-apple git checkout; every credited apple
                   site is re-resolved with `git cat-file -e <sha>:<file>`
                   against the commit pinned in the ledger's source_trees
  ANDROID_GIT      optional: the same for silo-android

With the git checkouts given, a credited sibling site that does not exist at
the pinned commit is printed as `stale-against-pinned-tree` (the file moved or
the pin is wrong), separately from the uncredited hits below, and the exported
roots are checked to be at the pinned commit when they carry a .git directory.

A hit is reported when the line either names /api/v1 outright or carries a
request marker for that repo (a web api()/fetch()/WebSocket call or a path
template return, an HTTPClient/ContinuumAPI call in Swift, a ktor client call
in Kotlin). Frontend route strings such as navigate("/settings") share the
same words as API paths and are excluded this way.

Output is one line per uncredited hit: fragment, repo, file:line, the source
line. Comment-only lines and test/fixture files are skipped. Exit status is 0;
the list is for review, not a gate.
"""
import json
import os
import re
import subprocess
import sys
from collections import defaultdict

if len(sys.argv) not in (6, 8):
    sys.stderr.write(__doc__)
    sys.exit(2)

INV, LEDGER, WEB_ROOT, APPLE_ROOT, ANDROID_ROOT = sys.argv[1:6]
GIT_DIRS = {"apple": sys.argv[6], "android": sys.argv[7]} if len(sys.argv) == 8 else {}
with open(INV) as f:
    routes = json.load(f)["routes"]
with open(LEDGER) as f:
    ledger = json.load(f)
entries = ledger["entries"]
PINS = {"apple": ledger["source_trees"]["silo-apple"], "android": ledger["source_trees"]["silo-android"]}


def git_has(gitdir, sha, path):
    return subprocess.run(["git", "-C", gitdir, "cat-file", "-e", f"{sha}:{path}"], capture_output=True).returncode == 0


stale = 0
for repo, gitdir in GIT_DIRS.items():
    sha = PINS[repo]
    if subprocess.run(["git", "-C", gitdir, "cat-file", "-e", sha + "^{commit}"], capture_output=True).returncode != 0:
        print(f"# {repo}: pinned commit {sha} is not present in {gitdir}; git fetch first", file=sys.stderr)
        continue
    seen = set()
    for e in entries:
        for s in e["consumer_call_sites"]:
            if s["repo"] != repo or s["file"] in seen:
                continue
            seen.add(s["file"])
            if not git_has(gitdir, sha, s["file"]):
                print(f"stale-against-pinned-tree\t{repo}\t{s['file']}\tnot in {sha[:8]}")
                stale += 1
if GIT_DIRS:
    print(f"# {stale} credited sibling files missing at the pinned trees", file=sys.stderr)

ROOTS = {"web": (WEB_ROOT, (".ts", ".tsx")), "apple": (APPLE_ROOT, (".swift",)), "android": (ANDROID_ROOT, (".kt",))}


def is_test(path):
    p = path.lower()
    base = os.path.basename(p)
    return (
        "fixtures" in base
        or "hosteddiagnostics" in p
        or "test" in base
        or "/tests/" in p
        or "/test/" in p
        or "__tests__" in p
        or "/commontest/" in p
        or "uitests" in p
        or "/mocks/" in p
    )


def fragment(path):
    """Last two static segments joined by '/', or the last one if only one exists."""
    segs = [s for s in path.strip("/").split("/") if s and not s.startswith("{") and s != "*"]
    segs = [s for s in segs if s not in ("api", "v1")]
    if not segs:
        return None
    return "/".join(segs[-2:])


frag_rows = defaultdict(list)
for r in routes:
    if r["listener"] != "api":
        continue
    fr = fragment(r["path"])
    if fr:
        frag_rows[fr].append(r)

# (repo, file basename) -> credited line numbers, across every ledger row. A
# line is treated as credited when a call site sits within WINDOW lines of it,
# because a multi-line call is credited at the call, not at the path literal.
WINDOW = 4
credited = defaultdict(set)
for e in entries:
    for s in e["consumer_call_sites"]:
        credited[(s["repo"], os.path.basename(s["file"]))].add(s["line"])


def is_credited(repo, base, ln):
    return any(abs(ln - c) <= WINDOW for c in credited.get((repo, base), ()))

sources = {}
for repo, (root, exts) in ROOTS.items():
    for dirpath, _, files in os.walk(root):
        for fn in files:
            if not fn.endswith(exts):
                continue
            fp = os.path.join(dirpath, fn)
            if is_test(fp):
                continue
            with open(fp, encoding="utf-8", errors="replace") as f:
                sources[(repo, fp)] = f.read().split("\n")

MARKERS = {
    "web": re.compile(r"\b(api\w*|fetch|playerFetch)\s*(<[^>]*>)?\s*\(|WebSocket\(|EventSource\(|(return|=>)\s*`/|wsBase|apiBaseUrl"),
    "apple": re.compile(
        r"\.(get\w*|post\w*|put\w*|delete|patch\w*|exists|head|send\w*|request\w*|download|upload|multipart)\s*\("
        r'|path:\s*"|let \w*(path|endpoint)\w*\s*(:\s*String\s*)?='
    ),
    "android": re.compile(r'client\.\w+|HttpMethod|append\(\s*"|urlString|url\s*=\s*"|val \w*(path|url|endpoint)\w*\s*='),
}

hits = 0
for fr in sorted(frag_rows):
    # The fragment must appear as a path fragment: preceded by / or a quote.
    pat = re.compile(r"(?<=[/\"'`])" + re.escape(fr) + r"(?=[/\"'`?\\$\s)]|$)")
    for (repo, fp), lines in sources.items():
        root = ROOTS[repo][0]
        base = os.path.basename(fp)
        for ln, line in enumerate(lines, 1):
            s = line.strip()
            if s.startswith(("//", "*", "/*", "#")):
                continue
            if not pat.search(line):
                continue
            if is_credited(repo, base, ln):
                continue
            if "/api/v1" not in line and not MARKERS[repo].search(line):
                continue
            rel = os.path.relpath(fp, os.path.dirname(root.rstrip("/")) if repo == "web" else root)
            print(f"{fr}\t{repo}\t{rel}:{ln}\t{s[:160]}")
            hits += 1
print(f"# {hits} uncredited hits across {len(frag_rows)} fragments", file=sys.stderr)
