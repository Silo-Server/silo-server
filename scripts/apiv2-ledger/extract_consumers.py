#!/usr/bin/env python3
"""Extract first-party client call sites of Silo native routes.

Scans the web frontend source, an exported silo-apple tree, and an exported
silo-android tree for HTTP and WebSocket calls against the native API and emits
one record per call site. The output feeds match_consumers.py, which maps the
sites onto contracts/api/v2/route-inventory.json rows.

Usage:
  extract_consumers.py WEB_SRC APPLE_ROOT ANDROID_ROOT OUT_JSON

  WEB_SRC       the web/src directory of this repository; sites are recorded
                relative to web/ (src/...)
  APPLE_ROOT    a silo-apple repository root (a `git archive origin/main`
                export is enough); sites are recorded relative to it (iosApp/...)
  ANDROID_ROOT  a silo-android repository root; sites are recorded relative to
                it (shared/..., android-shared/..., androidApp/..., androidTvApp/...)

Each record: {repo, file, line, method, path, raw, types, via}. `path` is a
template with every interpolation collapsed to {x}; `method` is UNKNOWN when
the call does not name one; `via` names the extraction rule that produced the
record so a reviewer can tell a direct literal from a resolved helper.

What is resolved beyond a path literal passed straight to an HTTP call:
  * web helper functions that return a template (`return \\`/ebooks/${id}/progress\\``,
    `const p = () => \\`/devices/${id}\\``) — the call that passes helper(...) to
    api()/fetch() is credited with the helper's template;
  * web templates rooted at the API base (`${config.apiBaseUrl}/playback/${id}`,
    `${wsBase}/...`), including WebSocket URL factories;
  * Kotlin `buildString { append("/api/v1/...") ... }` blocks and `const val`
    constants passed to ktor calls;
  * Swift `let path = "/api/v1/...\\(id)..."` bindings and `static let endpoint`
    constants used by a call in the same file.
Test, fixture, mock, and diagnostics-report files are skipped.
"""
import json
import os
import re
import sys
from collections import Counter

if len(sys.argv) != 5:
    sys.stderr.write(__doc__)
    sys.exit(2)

WEB_ROOT, APPLE_ROOT, ANDROID_ROOT, OUT = sys.argv[1:5]

# Web sites are recorded relative to web/ so they read as src/...; the other
# two roots are repository roots.
WEB_BASE = os.path.dirname(WEB_ROOT.rstrip("/"))


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


def strip_comment(line):
    s = line.strip()
    if s.startswith("//") or s.startswith("*") or s.startswith("/*") or s.startswith("#"):
        return ""
    return line


def collapse_interp(p):
    out = []
    i = 0
    while i < len(p):
        if p[i] == "$" and i + 1 < len(p) and p[i + 1] == "{":
            depth = 1
            j = i + 2
            while j < len(p) and depth > 0:
                if p[j] == "{":
                    depth += 1
                elif p[j] == "}":
                    depth -= 1
                j += 1
            out.append("{x}")
            i = j
        else:
            out.append(p[i])
            i += 1
    return "".join(out)


def norm_path(p):
    """Normalize a path fragment to a template with {x} wildcards."""
    p = collapse_interp(p)  # TS/Kotlin ${...}
    p = re.sub(r"\\\([^)]*\)", "{x}", p)  # Swift \(...)
    p = re.sub(r"\$[A-Za-z_][A-Za-z0-9_]*", "{x}", p)  # Kotlin $name
    p = re.sub(r"\{[^}]*\}", "{x}", p)
    p = p.split("?")[0].split("#")[0]
    # a template glued to a non-slash character is a query-string suffix, not a segment
    p = re.sub(r"(?<=[^/])\{x\}$", "", p)
    p = re.sub(r"/+", "/", p)
    if len(p) > 1 and not p.endswith("/{x}/"):
        p = p.rstrip("/")
    return p


def balanced(src, start):
    """Return the index just past the ')' that closes the '(' before start."""
    depth = 1
    i = start
    while i < len(src) and depth > 0:
        c = src[i]
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
        i += 1
    return i


def read(fp):
    with open(fp, encoding="utf-8", errors="replace") as f:
        return f.read()


results = []


def record(repo, root, fp, line, method, path, raw, types, via):
    results.append(
        {
            "repo": repo,
            "file": os.path.relpath(fp, root),
            "line": line,
            "method": method,
            "path": norm_path(path),
            "raw": raw,
            "types": [t for t in types if t],
            "via": via,
        }
    )


def already(repo, fp, ln, np, window=4):
    base = os.path.basename(fp)
    return any(
        r["repo"] == repo and abs(r["line"] - ln) <= window and r["file"].endswith(base) and r["path"] == np
        for r in results
    )


# ---------------------------------------------------------------------------
# web
# ---------------------------------------------------------------------------
# The optional generic allows one level of nesting (api<Partial<Progress>>(...)).
WEB_CALL = re.compile(
    r"\b(api|apiResponse|apiKeepalive|apiDownload|apiBlob|apiWithProfileRequestContext|apiFetch|fetch|apiFormData|apiUpload|playerFetch)\s*(<(?:[^<>]|<[^<>]*>)*>)?\s*\("
)
# Identifiers whose value is the API origin or its ws:// form. A template that
# starts with one of these is an API path.
WEB_BASE_IDENTS = re.compile(r"^\$\{[^}]*(apiBaseUrl|apiBase|wsBase|API_BASE|apiRoot)[^}]*\}")
WEB_HELPER_DEF = re.compile(r"function\s+(\w+)\s*\(|(?:const|let)\s+(\w+)\s*=\s*(?:async\s*)?\([^)]*\)\s*(?::\s*\w+\s*)?=>")
WEB_HELPER_RETURN = re.compile(r"(?:return\s+|=>\s*)`([^`]*)`")


def web_helpers(src):
    """Map helper name -> path template for functions whose body returns a template.

    A `return \`...\`` (or an arrow body) is attributed to the nearest preceding
    function/arrow definition when the braces between the two balance to one
    open block, so a template returned from a nested callback is not credited
    to the outer function.
    """
    found = {}
    defs = [(m.start(), m.end(), m.group(1) or m.group(2)) for m in WEB_HELPER_DEF.finditer(src)]
    for rm in WEB_HELPER_RETURN.finditer(src):
        tmpl = rm.group(1)
        # Keep templates that start with a path, an API base identifier, or a
        # call to another helper (`${annotationsPath(id)}/...`); the caller
        # chains the last kind and discards anything still unresolved.
        if not (tmpl.startswith("/") or WEB_BASE_IDENTS.match(tmpl) or re.match(r"\$\{\w+\(", tmpl)):
            continue
        prev = [d for d in defs if d[0] < rm.start()]
        if not prev:
            continue
        _, dend, name = prev[-1]
        between = src[dend : rm.start()]
        depth = between.count("{") - between.count("}")
        if depth in (0, 1):
            found[name] = tmpl
    return found


def first_string_literal(args):
    """Return the body of the first string literal in args, honoring ${...} nesting in templates."""
    for i, c in enumerate(args):
        if c in "\"'":
            j = args.find(c, i + 1)
            return args[i + 1 : j] if j > 0 else None
        if c == "`":
            j = i + 1
            depth = 0
            while j < len(args):
                ch = args[j]
                if ch == "$" and j + 1 < len(args) and args[j + 1] == "{":
                    depth += 1
                    j += 2
                    continue
                if ch == "}" and depth > 0:
                    depth -= 1
                elif ch == "`" and depth == 0:
                    return args[i + 1 : j]
                j += 1
            return None
    return None


def templates_in(line):
    """Yield the body of every top-level template literal on a line (nesting-aware)."""
    i = 0
    while i < len(line):
        if line[i] == "`":
            body = first_string_literal(line[i:])
            if body is None:
                return
            yield body
            i += len(body) + 2
        else:
            i += 1


def web_api_path(lit, fn):
    """Turn a literal passed to a web HTTP helper into an /api/v1 path, or None."""
    if lit.startswith("/api/v1"):
        return lit
    if WEB_BASE_IDENTS.match(lit):
        rest = collapse_interp(lit)[3:]  # drop the leading {x}
        return "/api/v1" + rest if rest.startswith("/") else None
    if fn == "fetch":
        return lit[lit.find("/api") :] if "/api/v1" in lit else None
    if lit.startswith("/"):
        return "/api/v1" + lit
    return None


def web_method(args):
    mm = re.search(r"method:\s*[\"'`]([A-Z]+)[\"'`]", args)
    tern = re.search(r"method:\s*[^,}]*\?\s*[\"'`]([A-Z]+)[\"'`]\s*:\s*[\"'`]([A-Z]+)[\"'`]", args)
    if tern:
        return tern.group(1) + "|" + tern.group(2)
    if mm:
        return mm.group(1)
    if re.search(r"method:\s*[a-zA-Z_.]+\s*[,}]", args):
        return "UNKNOWN"
    return "GET"


def web_files():
    for root, _, files in os.walk(WEB_ROOT):
        for f in files:
            if f.endswith((".ts", ".tsx")):
                fp = os.path.join(root, f)
                if not is_test(fp):
                    yield fp


def web_scan():
    # Pass 1: helper functions that return a path template. Names are collected
    # across files because helpers are imported into the files that call them.
    helpers = {}
    for fp in web_files():
        src = read(fp)
        helpers.update(web_helpers(src))
    # A helper may delegate to another helper (`${annotationsPath(id)}/${aid}`);
    # substitute until the template starts with a path or a base identifier.
    for _ in range(3):
        for name, tmpl in list(helpers.items()):
            hm = re.match(r"\$\{(\w+)\([^}]*\)\}", tmpl)
            if hm and hm.group(1) in helpers and hm.group(1) != name:
                helpers[name] = helpers[hm.group(1)] + tmpl[hm.end():]
    helpers = {n: t for n, t in helpers.items() if t.startswith("/") or WEB_BASE_IDENTS.match(t)}
    # Pass 2: calls.
    for fp in web_files():
        src = read(fp)
        lines = src.split("\n")
        for m in WEB_CALL.finditer(src):
            fn = m.group(1)
            # Drop only the outer angle brackets: strip("<>") would also eat the
            # closing bracket of a nested generic such as api<Partial<T>>.
            generic = (m.group(2) or "")[1:-1].strip()
            start = m.end()
            end = balanced(src, start)
            args = src[start : end - 1]
            line = src.count("\n", 0, m.start()) + 1
            if strip_comment(lines[line - 1]) == "":
                continue
            lit = first_string_literal(args)
            via = fn
            head = args.lstrip()
            hm = re.match(r"(\w+)\s*\(", head)
            if hm and hm.group(1) in helpers and (lit is None or not args.lstrip().startswith(("`", '"', "'"))):
                lit = helpers[hm.group(1)]
                via = fn + "/helper:" + hm.group(1)
            if lit is None:
                continue
            path = web_api_path(lit, fn)
            if path is None:
                continue
            record("web", WEB_BASE, fp, line, web_method(args), path, lit, [generic], via)
        # Pass 3: URL templates that are not the first argument of a helper call:
        # bare /api/v1 literals (img src, window.open, EventSource) and
        # base-rooted templates (`${wsBase}/...`, `${config.apiBaseUrl}/...`).
        for ln, line in enumerate(lines, 1):
            if strip_comment(line) == "":
                continue
            cands = []
            for sm in re.finditer(r"[`\"']((?:/api/v1|/stream/)[^`\"'\s]*)[`\"']", line):
                cands.append(sm.group(1))
            for lit in templates_in(line):
                if WEB_BASE_IDENTS.match(lit):
                    p = web_api_path(lit, "template")
                    if p:
                        cands.append(p)
            for lit in cands:
                if "${path}" in lit:
                    continue
                np = norm_path(lit)
                if np in ("/api/v1", "/api/v1/{x}"):
                    continue
                if already("web", fp, ln, np):
                    continue
                method = "UNKNOWN"
                if "WebSocket(" in line or "/ws" in lit or "EventSource(" in line or "wsBase" in line:
                    method = "GET"
                record("web", WEB_BASE, fp, ln, method, lit, lit, [], "literal")


# ---------------------------------------------------------------------------
# apple
# ---------------------------------------------------------------------------
APPLE_CALL = re.compile(
    r"(?<![A-Za-z0-9_])(getUnauthenticated|get|getData|getWithHeaders|post|postVoid|postRaw|postData|put|putVoid|putRaw|putData|delete|patch|patchVoid|exists|head|sendRaw|send|requestData|sendRawBody|getRaw|download|upload|multipart|postMultipart|putMultipart)\s*\("
)
APPLE_CONST = re.compile(r"static let (\w+)\s*(?::\s*String\s*)?=\s*\"(/api/v1[^\"]*)\"")
APPLE_LET = re.compile(r"(?:let|var)\s+(\w+)\s*(?::\s*String\s*)?=\s*\"(/api/v1[^\"]*)\"")


def apple_method(fn, args):
    mm = re.search(r"method:\s*\"([A-Z]+)\"", args)
    if mm:
        return mm.group(1)
    if fn in ("get", "getData", "getWithHeaders", "exists", "getRaw", "download", "getUnauthenticated"):
        return "GET"
    if fn.startswith("post"):
        return "POST"
    if fn.startswith("put"):
        return "PUT"
    if fn.startswith("delete"):
        return "DELETE"
    if fn.startswith("patch"):
        return "PATCH"
    if fn == "head":
        return "HEAD"
    return "GET"


def apple_types(src, at):
    lm = re.search(r"let\s+\w+\s*:\s*([A-Za-z0-9_\[\]<>., ?]+)\s*=\s*$", src[max(0, at - 200) : at].rstrip())
    if lm:
        return [lm.group(1).strip()]
    back = src[max(0, at - 1500) : at]
    fm = list(re.finditer(r"func\s+\w+\s*\([^{]*?\)\s*(?:async\s+)?(?:throws\s+)?(?:->\s*([^{\n]+))?\s*\{", back))
    if fm and fm[-1].group(1):
        return [fm[-1].group(1).strip()]
    return []


def apple_scan():
    for root, _, files in os.walk(APPLE_ROOT):
        for f in files:
            if not f.endswith(".swift"):
                continue
            fp = os.path.join(root, f)
            if is_test(fp):
                continue
            src = read(fp)
            lines = src.split("\n")
            consts = {name: lit for name, lit in APPLE_CONST.findall(src)}
            # let path = "/api/v1/...\(id)" bindings, resolved when an HTTP call
            # in the file names the binding; otherwise recorded at the binding.
            lets = {}
            for m in APPLE_LET.finditer(src):
                lets[m.group(1)] = (m.group(2), src.count("\n", 0, m.start()) + 1)
            resolved_lets = set()
            for m in APPLE_CALL.finditer(src):
                fn = m.group(1)
                start = m.end()
                end = balanced(src, start)
                args = src[start : end - 1]
                line = src.count("\n", 0, m.start()) + 1
                via = fn
                sm = re.search(r"\"((?:/|\\\()[^\"]*)\"", args)
                if sm:
                    lit = sm.group(1)
                    if not lit.startswith("/") and "/api/v1" in lit:
                        lit = lit[lit.find("/api/v1") :]
                else:
                    im = re.search(r"(?:path:\s*)?(?:[A-Za-z_]\w*\.)?(\w+)\s*[,)]?", args)
                    name = im.group(1) if im else None
                    if name in consts:
                        lit = consts[name]
                        via = fn + "/const:" + name
                    elif name in lets:
                        lit = lets[name][0]
                        resolved_lets.add(name)
                        via = fn + "/let:" + name
                    else:
                        continue
                if not lit.startswith("/"):
                    continue
                record("apple", APPLE_ROOT, fp, line, apple_method(fn, args), lit, lit, apple_types(src, m.start()), via)
            for name, (lit, ln) in lets.items():
                if name in resolved_lets:
                    continue
                if already("apple", fp, ln, norm_path(lit)):
                    continue
                record("apple", APPLE_ROOT, fp, ln, "GET" if "/ws" in lit else "UNKNOWN", lit, lit, [], "let:" + name)
            for ln, line in enumerate(lines, 1):
                if strip_comment(line) == "":
                    continue
                for sm in re.finditer(r"\"([^\"]*?)(/api/v1/[^\"\s]*)\"", line):
                    lit = sm.group(2)
                    np = norm_path(lit)
                    if already("apple", fp, ln, np):
                        continue
                    record("apple", APPLE_ROOT, fp, ln, "UNKNOWN", lit, lit, [], "literal")


# ---------------------------------------------------------------------------
# android
# ---------------------------------------------------------------------------
AND_CALL = re.compile(
    r"\.(get|post|put|delete|patch|head|prepareGet|preparePost|preparePut|webSocket|submitFormWithBinaryData|submitForm|request|prepareRequest)\s*(<[^>]*>)?\s*\("
)


def android_method(fn, args):
    mm = re.search(r"method\s*=\s*HttpMethod\.(\w+)", args)
    if mm:
        return mm.group(1).upper()
    if fn in ("get", "prepareGet", "webSocket"):
        return "GET"
    if fn in ("post", "preparePost", "submitFormWithBinaryData", "submitForm"):
        return "POST"
    if fn in ("put", "preparePut"):
        return "PUT"
    if fn == "delete":
        return "DELETE"
    if fn == "patch":
        return "PATCH"
    if fn == "head":
        return "HEAD"
    return "GET"


def kotlin_build_strings(src):
    """Yield (line, template) for every buildString { append(...) ... } block."""
    for m in re.finditer(r"buildString\s*\{", src):
        depth = 1
        i = m.end()
        while i < len(src) and depth > 0:
            if src[i] == "{":
                depth += 1
            elif src[i] == "}":
                depth -= 1
            i += 1
        body = src[m.end() : i - 1]
        parts = []
        for am in re.finditer(r"append\(([^)]*(?:\([^)]*\))?[^)]*)\)", body):
            arg = am.group(1).strip()
            if arg.startswith('"') and arg.endswith('"'):
                parts.append(arg[1:-1])
            else:
                parts.append("{x}")
        tmpl = "".join(parts)
        if "/api/v1" in tmpl:
            yield src.count("\n", 0, m.start()) + 1, tmpl[tmpl.find("/api/v1") :]


KOTLIN_FUN = re.compile(r"\bfun\s+(?:<[^>]*>\s*)?(?:[\w.]+\.)?\w+\s*\(")


def kotlin_enclosing_return_type(src, at):
    """Return the declared return type of the function that encloses position at.

    Walks back to the nearest `fun name(` whose body contains `at`, skips its
    parameter list with balanced parentheses (so a default argument such as
    `scope: AuthScopeSnapshot? = null` cannot end the search early), and takes
    the `: Type` that immediately follows the closing parenthesis. Returns
    None when the enclosing function declares no return type or the call is
    not inside a function body.
    """
    back = src[max(0, at - 4000) : at]
    offset = at - len(back)
    for fm in reversed(list(KOTLIN_FUN.finditer(back))):
        close = balanced(src, offset + fm.end())
        if close > at:
            continue
        # The body must still be open at `at`: the braces between the header
        # and the call balance to at least one open block.
        between = src[close:at]
        if between.count("{") - between.count("}") < 1:
            # Expression-bodied `fun x(): T = client.get(...)` has no braces.
            if not re.match(r"\s*(?::\s*[^={\n]+)?\s*=", between):
                continue
        rm = re.match(r"\s*:\s*([^={\n]+)", src[close:at])
        if rm:
            return rm.group(1).strip()
        return None
    return None


def android_scan():
    for root, _, files in os.walk(ANDROID_ROOT):
        for f in files:
            if not f.endswith(".kt"):
                continue
            fp = os.path.join(root, f)
            if is_test(fp):
                continue
            src = read(fp)
            lines = src.split("\n")
            consts = dict(re.findall(r"const val ([A-Z_]+)\s*=\s*\"([^\"]+)\"", src))
            for m in AND_CALL.finditer(src):
                fn = m.group(1)
                start = m.end()
                end = balanced(src, start)
                args = src[start : end - 1]
                via = fn
                sm = re.search(r"\"([^\"]*)\"", args)
                if not sm:
                    cm = re.match(r"\s*([A-Z_]+)\s*[,)]?", args)
                    if cm and cm.group(1) in consts:
                        lit = consts[cm.group(1)]
                        via = fn + "/const:" + cm.group(1)
                    else:
                        continue
                else:
                    lit = sm.group(1)
                    for k, v in consts.items():
                        lit = lit.replace("$" + k, v).replace("${" + k + "}", v)
                if "/api/v1" in lit:
                    lit = lit[lit.find("/api/v1") :]
                if not lit.startswith("/"):
                    continue
                line = src.count("\n", 0, m.start()) + 1
                types = []
                bm = re.search(r"\.body<([^>]+)>\(", src[end : end + 300])
                if bm:
                    types.append(bm.group(1))
                gen = (m.group(2) or "").strip("<>")
                if gen:
                    types.append(gen)
                ret = kotlin_enclosing_return_type(src, m.start())
                if ret:
                    types.append(ret)
                record("android", ANDROID_ROOT, fp, line, android_method(fn, args), lit, lit, types, via)
            for ln, tmpl in kotlin_build_strings(src):
                np = norm_path(tmpl)
                if already("android", fp, ln, np, window=12):
                    continue
                record("android", ANDROID_ROOT, fp, ln, "GET" if "/ws" in np else "UNKNOWN", tmpl, tmpl, [], "buildString")
            for ln, line in enumerate(lines, 1):
                if strip_comment(line) == "":
                    continue
                for sm in re.finditer(r"\"([^\"]*?)(/api/v1/[^\"\s]*)\"", line):
                    lit = sm.group(2)
                    if "const val" in line:
                        continue
                    # A literal ending in "/" inside an append()/concatenation is a
                    # prefix fragment; the buildString rule above owns those.
                    if lit.endswith("/") and ("append(" in line or "+" in line):
                        continue
                    np = norm_path(lit)
                    if already("android", fp, ln, np):
                        continue
                    record("android", ANDROID_ROOT, fp, ln, "UNKNOWN", lit, lit, [], "literal")


web_scan()
apple_scan()
android_scan()
results.sort(key=lambda r: (r["repo"], r["file"], r["line"], r["method"], r["path"]))
with open(OUT, "w") as f:
    json.dump(results, f, indent=1)
    f.write("\n")
print("call sites:", len(results), dict(Counter(r["repo"] for r in results)))
print("via:", dict(Counter(r["via"].split("/")[0] if "/" not in r["via"] else r["via"].split("/")[1].split(":")[0] for r in results)))
