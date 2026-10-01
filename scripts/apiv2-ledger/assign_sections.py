#!/usr/bin/env python3
"""Assign every migration-ledger entry to an API section.

A section is the Phase 4 delivery unit: one cohesive route group that one
section PR ports to /api/v2 (docs/architecture/api-contract.md, "Migration
ledger"). The assignment is a pure function of the entry's listener,
disposition and path, so re-running this script on an unchanged ledger rewrites
the file byte for byte. Rows that need no v2 work (removed, documented
exclusions) still carry a section so the gate can say which PR retires or
documents them.

Usage: scripts/apiv2-ledger/assign_sections.py [--check] [contracts/api/v2/migration.json]
"""

import json
import sys
from collections import OrderedDict

LEDGER = "contracts/api/v2/migration.json"

# Sections whose rows live outside /api/v1 on the API listener.
NODE_SECTIONS = {"proxy": "node-proxy", "transcode_node": "node-transcode"}

# /api/v1/admin/<b> -> section
ADMIN = {
    "users": "admin-users", "access-groups": "admin-users", "ips": "admin-users",
    "sessions": "admin-sessions", "node-sessions": "admin-sessions", "devices": "admin-sessions",
    "api-keys": "admin-sessions",
    "policy": "admin-policy", "invitations": "admin-invitations", "invite-codes": "admin-invitations",
    "settings": "admin-settings", "branding": "admin-settings", "server": "admin-settings",
    "system": "admin-settings", "email": "admin-settings", "rate-limits": "admin-settings",
    "jellyfin-compat": "admin-settings", "playback-routing": "admin-settings",
    "logs": "admin-observability", "dashboard": "admin-observability", "stats": "admin-observability",
    "stream-telemetry": "admin-observability", "playback-history": "admin-observability",
    "diagnostics": "admin-observability",
    "items": "admin-catalog", "markers": "admin-catalog", "catalog": "admin-catalog",
    "people": "admin-catalog", "unmatched": "admin-catalog", "files": "admin-catalog",
    "filesystem": "admin-catalog", "literary-works": "admin-catalog",
    "autoscan": "admin-autoscan",
    "plugins": "admin-plugins",
    "collections": "admin-collections", "collection-groups": "admin-collections",
    "libraries": "admin-collections", "sections": "admin-sections",
    "history-imports": "admin-history-imports", "history-import-sources": "admin-history-imports",
    "notifications": "admin-notifications",
    "requests": "admin-requests", "request-integrations": "admin-requests",
    "request-settings": "admin-requests", "request-users": "admin-requests",
    "tasks": "admin-tasks", "jobs": "admin-tasks", "nodes": "admin-nodes",
    "recommendations": "admin-recommendations",
    "subtitles": "admin-subtitles", "subtitle-providers": "admin-subtitles",
}

# /api/v1/<a> -> section
TOP = {
    "auth": "auth-core", "user": "auth-core", "onboarding": "auth-core", "policy": "auth-core",
    "invitations": "auth-invitations", "api-keys": "auth-devices", "devices": "auth-devices",
    "profiles": "profiles", "profile": "profiles",
    "settings": "settings", "audio-prefs": "settings-prefs", "subtitle-prefs": "settings-prefs",
    "library-playback-prefs": "settings-prefs", "theme": "settings-branding", "branding": "settings-branding",
    "libraries": "catalog-libraries", "library": "catalog-libraries",
    "catalog": "catalog-items", "items": "catalog-items", "people": "catalog-items",
    "works": "catalog-items", "search": "catalog-items", "metadata": "catalog-items",
    "sections": "catalog-home", "home": "catalog-home", "calendar": "catalog-home",
    "recommendations": "catalog-recommendations",
    "progress": "personal-progress", "history": "personal-progress", "watched": "personal-progress",
    "watch": "personal-progress", "sync": "personal-progress",
    "favorites": "personal-lists", "watchlist": "personal-lists", "ratings": "personal-lists",
    "collections": "personal-collections",
    "requests": "personal-requests", "watch-providers": "personal-requests",
    "imports": "personal-imports", "history-imports": "personal-imports",
    "plex-sync": "personal-imports", "webhook-sync": "personal-imports",
    "notifications": "notifications", "events": "realtime", "watch-together": "realtime",
    "playback": "playback-control", "markers": "playback-control",
    "subtitles": "playback-subtitles", "stream": "playback-delivery",
    "direct-download": "playback-delivery", "direct-download-proxy": "playback-delivery",
    "transcode": "playback-delivery", "downloads": "downloads",
    "ebooks": "ebooks", "images": "raw-assets",
    "compat": "operational", "autoscan": "operational", "scan": "operational",
    "diagnostics": "operational", "health": "operational", "ready": "operational",
    "plugins": "plugin-proxy", "plugin-assets": "plugin-proxy",
}


def section_for(entry):
    listener = entry["listener"]
    if listener in NODE_SECTIONS:
        return NODE_SECTIONS[listener]
    if listener == "root":
        return "root-operational"
    if entry["namespace"] == "api_v2":
        return "v2-delegation"
    parts = [s for s in entry["path"].split("/") if s]
    if len(parts) < 3 or parts[0] != "api" or parts[1] != "v1":
        return "operational"
    a = parts[2]
    if a == "admin":
        if len(parts) == 3:
            return "admin-settings"
        b = parts[3]
        if b in ADMIN:
            return ADMIN[b]
        raise SystemExit(f"unmapped admin route: {entry['method']} {entry['path']}")
    if a in TOP:
        return TOP[a]
    raise SystemExit(f"unmapped route: {entry['method']} {entry['path']}")


def main(argv):
    check = "--check" in argv
    args = [a for a in argv[1:] if not a.startswith("--")]
    path = args[0] if args else LEDGER
    with open(path, encoding="utf-8") as f:
        raw = f.read()
    doc = json.loads(raw, object_pairs_hook=OrderedDict)
    changed = 0
    for entry in doc["entries"]:
        want = section_for(entry)
        if entry.get("section") != want:
            changed += 1
        # Keep the field next to the other curated scheduling fields.
        items = list(entry.items())
        items = [(k, v) for k, v in items if k != "section"]
        idx = next(i for i, (k, _) in enumerate(items) if k == "release_flow")
        items.insert(idx, ("section", want))
        entry.clear()
        entry.update(items)
    out = json.dumps(doc, indent=2, ensure_ascii=False) + "\n"
    if check:
        if out != raw:
            print(f"{path}: {changed} entries would change; run assign_sections.py", file=sys.stderr)
            return 1
        print(f"{path}: sections current")
        return 0
    with open(path, "w", encoding="utf-8") as f:
        f.write(out)
    print(f"{path}: {changed} entries updated")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
