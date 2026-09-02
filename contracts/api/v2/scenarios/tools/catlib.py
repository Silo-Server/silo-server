"""Helpers for authoring scenario catalogs. Not shipped; used once to write the
wave-1 catalogs, kept so later waves author with the same vocabulary."""
import json, sys, os

ERR_SHAPE = [
    {"pointer": "", "op": "keys_equal", "value": ["error", "message"]},
    {"pointer": "/error", "op": "type", "value": "string"},
    {"pointer": "/message", "op": "type", "value": "string"},
]
JSON_CT = [{"name": "Content-Type", "op": "equals", "value": "application/json"}]


def err(status, code, message=None, extra=None):
    body = [{"pointer": "/error", "op": "equals", "value": code}] + ERR_SHAPE
    if message:
        body.append({"pointer": "/message", "op": "equals", "value": message})
    if extra:
        body += extra
    return {"status": status, "headers": JSON_CT, "body": body}


def sc(id, category, description, principal, request, expect, requires=None, settings=None,
       fresh_state=False, notes=None, method=None):
    s = {"id": id, "category": category, "description": description,
         "principal": principal if isinstance(principal, dict) else {"class": principal},
         "request": request, "expect": expect}
    if requires:
        s["requires"] = requires
    if settings:
        s["settings"] = settings
    if fresh_state:
        s["fresh_state"] = True
    if notes:
        s["notes"] = notes
    s["v2_expectation"] = None
    return s


def row(method, path, scenarios, not_applicable=None, registration_index=0, notes=None, tier2=None):
    r = {"listener": "api", "method": method, "path": path, "registration_index": registration_index}
    if tier2:
        r["tier2_inclusion"] = tier2
    if not_applicable:
        r["not_applicable"] = not_applicable
    if notes:
        r["notes"] = notes
    if registration_index > 0:
        # The rate-limited registration variant repeats the plain row's
        # scenarios; ids stay unique per catalog.
        scenarios = [dict(s, id=f"{s['id']}.r{registration_index}") for s in scenarios]
    r["scenarios"] = scenarios
    return r


NA_SINGLE = {
    "sorting": "Single object response; nothing to order.",
    "pagination": "Single object response; nothing to page.",
    "filtering": "No filter parameters are read.",
}
NA_RAW = {"raw_protocol": "Plain JSON request/response; no ranges, redirects, multipart, or upgrades."}


def na(*dicts, **kw):
    out = {}
    for d in dicts:
        out.update(d)
    out.update(kw)
    return out


def unauth(id_prefix, method, path, body=None):
    """Missing bearer through the real RequireAuth middleware."""
    req = {"path": path}
    if body is not None:
        req["body"] = body
    return sc(f"{id_prefix}.no_token", "authorization",
              "Without a bearer the auth middleware refuses the request before the handler runs.",
              "public", req, err(401, "unauthorized", "Missing or malformed authorization header"))


def bad_token(id_prefix, path, body=None):
    req = {"path": path, "headers": {"Authorization": "Bearer not-a-jwt"}}
    if body is not None:
        req["body"] = body
    return sc(f"{id_prefix}.bad_token", "authorization",
              "A malformed bearer is rejected as an invalid token.",
              "public", req, err(401, "unauthorized", "Invalid or expired token"))


def rate_limited(id_prefix, path, endpoint_label, burst, method_body=None, principal="public", category="status_headers"):
    req = {"path": path, "repeat": burst + 1}
    if method_body is not None:
        req["body"] = method_body
    return sc(f"{id_prefix}.rate_limited", category,
              f"The rate-limited registration variant answers 429 with Retry-After once the per-IP {endpoint_label} burst ({burst}) is spent.",
              principal, req,
              {"status": 429,
               "headers": [{"name": "Retry-After", "op": "matches", "value": "^[0-9]+$"},
                           {"name": "X-RateLimit-Remaining", "op": "equals", "value": "0"},
                           {"name": "X-RateLimit-Limit", "op": "exists"},
                           {"name": "X-RateLimit-Reset", "op": "matches", "value": "^[0-9]+$"}] + JSON_CT,
               "body": [{"pointer": "/error", "op": "equals", "value": "rate_limit_exceeded"},
                        {"pointer": "/retry_after", "op": "type", "value": "integer"},
                        {"pointer": "", "op": "keys_equal", "value": ["error", "message", "retry_after"]}]},
              requires=["rate_limiter"], fresh_state=True,
              notes="The 429 body has a third key (retry_after) unlike every other error envelope; flagged for the section PR.")


def catalog(route_group, description, rows, wave=1, listener="api"):
    return {"schema_version": 1, "listener": listener, "route_group": route_group, "wave": wave,
            "description": description, "rows": rows}


def write(route_group, doc, root):
    slug = route_group.strip("/").replace("{", "").replace("}", "").replace("/", "-")
    path = os.path.join(root, doc["listener"], slug + ".json")
    with open(path, "w") as f:
        json.dump(doc, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print("wrote", path, len(doc["rows"]), "rows", sum(len(r["scenarios"]) for r in doc["rows"]), "scenarios")
