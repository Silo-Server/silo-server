#!/usr/bin/env python3
"""Match extracted client call sites to route-inventory rows.

Usage:
  match_consumers.py ROUTE_INVENTORY CALL_SITES OUT_MAP OUT_UNMATCHED

  ROUTE_INVENTORY  contracts/api/v2/route-inventory.json
  CALL_SITES       output of extract_consumers.py
  OUT_MAP          JSON object: "<listener> <METHOD> <path>" -> [call site, ...]
  OUT_UNMATCHED    JSON list of call sites no API-listener row accepted, each
                   with a `reason` a reviewer resolves by hand

Only API-listener rows are candidates: first-party clients never address the
proxy or transcode-node listeners directly, they follow server-issued URLs.
Matching is by normalized path template, then by HTTP method:
  exact      the templates are identical (after collapsing interpolations);
  param      same segment count and every differing segment is an inventory
             parameter (a client that hard-codes a value such as
             /settings/values/nav.shortcuts is a param match);
  wildcard   the inventory row ends in /* and the client path extends it.
A site whose method is UNKNOWN is credited only when exactly one method is
registered on the matched template; otherwise it is reported as
ambiguous-method for manual resolution.
"""
import json
import re
import sys
from collections import Counter, defaultdict

if len(sys.argv) != 5:
    sys.stderr.write(__doc__)
    sys.exit(2)

INV, CALLS, OUT_MAP, OUT_UNMATCHED = sys.argv[1:5]
with open(INV) as f:
    inv = json.load(f)["routes"]
with open(CALLS) as f:
    cs = json.load(f)


def tmpl(p):
    return re.sub(r"\{[^}]*\}", "{x}", p)


bypath = defaultdict(list)
for r in inv:
    if r["listener"] == "api":
        bypath[tmpl(r["path"])].append(r)


def key(r):
    return f"{r['listener']} {r['method']} {r['path']}"


def match(method, path):
    p = path
    # Clients build a few stream/transcode URLs without the /api/v1 prefix; the
    # server later prefixes them.
    if p.startswith("/stream/") or p.startswith("/playback/transcode/"):
        p = "/api/v1" + p
    cands = list(bypath.get(p) or bypath.get(p.rstrip("/")) or bypath.get(p + "/") or [])
    how = "exact"
    if not cands:
        segs = p.split("/")
        for t, rows in bypath.items():
            rs = t.split("/")
            if len(rs) == len(segs) and all(a == b or a == "{x}" for a, b in zip(rs, segs)):
                cands += rows
        how = "param"
    if not cands:
        for t, rows in bypath.items():
            if t.endswith("/*") and p.startswith(t[:-1]):
                cands += rows
                how = "wildcard"
    if not cands:
        return [], "none"
    if method == "UNKNOWN":
        ms = {r["method"] for r in cands}
        if len(ms) == 1:
            return cands, how + "/method-inferred"
        return [], "ambiguous-method"
    methods = set(method.split("|"))
    hits = [r for r in cands if r["method"] in methods]
    return (hits, how) if hits else ([], "method-mismatch")


consumers = defaultdict(list)
unmatched = []
stats = Counter()
for c in cs:
    hits, how = match(c["method"], c["path"])
    stats[how] += 1
    if not hits:
        unmatched.append(dict(c, reason=how))
        continue
    for h in hits:
        consumers[key(h)].append(
            {"repo": c["repo"], "file": c["file"], "line": c["line"], "types": c["types"], "match": how, "via": c["via"]}
        )

with open(OUT_MAP, "w") as f:
    json.dump({k: v for k, v in sorted(consumers.items())}, f, indent=1)
    f.write("\n")
with open(OUT_UNMATCHED, "w") as f:
    json.dump(unmatched, f, indent=1)
    f.write("\n")
print(dict(stats))
print("rows with client consumers:", len(consumers), "unmatched call sites:", len(unmatched))
