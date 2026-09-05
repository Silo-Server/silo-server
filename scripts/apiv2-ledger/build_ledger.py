#!/usr/bin/env python3
"""Assemble contracts/api/v2/migration.json from the route inventory, consumer
evidence, and the existing ledger.

Usage:
  build_ledger.py ROUTE_INVENTORY CONSUMER_MAP LEDGER APPLE_SHA ANDROID_SHA

  ROUTE_INVENTORY  contracts/api/v2/route-inventory.json
  CONSUMER_MAP     output of match_consumers.py (mechanical client matches)
  LEDGER           contracts/api/v2/migration.json; read if present, then
                   rewritten in inventory order
  APPLE_SHA        the silo-apple commit the apple call sites were extracted
                   from (git rev-parse origin/main after git fetch)
  ANDROID_SHA      the silo-android commit the android call sites were
                   extracted from

The two SHAs are written to the ledger's source_trees header so every sibling
call site can be re-resolved against the exact tree its line number refers to.

Merge rule (by key: listener + method + path + registration_index):
  * fields copied from the inventory (listener, namespace, method, path,
    handler, handler_kind, source_file, route_group, middleware_chain,
    auth_class, auth_traits, conditional, conditions, delegates_to,
    request_kind, response_media_kind, streams, upgrades_websocket) and the
    derived profile_required/admin_required are always refreshed from the
    inventory row;
  * mechanical call sites are always refreshed from CONSUMER_MAP; a
    path_literal_line annotation on an existing mechanical site (the line
    where the path constant is declared when the call line only names it) is
    carried over to the refreshed site at the same repo/file/line;
  * everything curated by hand on an existing entry is preserved verbatim:
    match=manual and match=follower call sites (a follower is the resolver or
    allowlist that admits a server-supplied URL, so the route's path is never
    spelled there), capability_endpoint, release_flow, tier, disposition,
    disposition_rule, disposition_rationale, owner, review_state, v2, and
    notes. consumers is recomputed from the merged call sites;
  * a row with no existing entry receives the seed defaults below (flow and
    tier from the path, disposition from the ratified rules, notes from the
    templates) and the manual and follower sites recorded in this file for
    its key.

Re-running on an unchanged inventory and consumer map is a no-op. The ledger is
gated, not regenerated: CI runs make verify-migration-ledger, which reconciles
the committed file against the inventory; this script only helps a maintainer
add rows for new routes or refresh consumer evidence without losing decisions.
"""
import json
from assign_sections import section_for
import os
import re
import sys
from collections import Counter, OrderedDict

if len(sys.argv) != 6:
    sys.stderr.write(__doc__)
    sys.exit(2)

INV, CMAP, OUT, APPLE_SHA, ANDROID_SHA = sys.argv[1:6]
for sha in (APPLE_SHA, ANDROID_SHA):
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        sys.stderr.write(f"expected a full 40-hex commit SHA, got {sha!r}\n")
        sys.exit(2)
with open(INV, encoding='utf-8') as f:
    inv = json.load(f)
rows = inv['routes']
with open(CMAP, encoding='utf-8') as f:
    cmap = json.load(f)
existing = {}
if os.path.exists(OUT):
    with open(OUT, encoding='utf-8') as f:
        for e in json.load(f)['entries']:
            existing[(e['listener'], e['method'], e['path'], e['registration_index'])] = e

CURATED = ("capability_endpoint", "section", "release_flow", "tier", "disposition", "disposition_rule",
           "disposition_rationale", "owner", "review_state", "v2", "notes")
# Optional curated fields: preserved when the prior entry carries them, never
# seeded. "concurrency" (if_match) marks a row whose v2 operation is Guarded.
# "retry_safety" classifies a tier-1 ported mutation row by the contract's
# "Mutation retry safety" strategy; "retry_safety_note" explains the choice.
CURATED_OPTIONAL = ("concurrency", "retry_safety", "retry_safety_note")

def key(r): return f"{r['listener']} {r['method']} {r['path']}"

# ---------------------------------------------------------------------------
# Manual consumer overrides: call sites the extractor could not resolve
# mechanically (variable path segments, URL builders, WebSocket URL assembly,
# server-provided URLs that clients follow). Each is a (repo, file, line, types)
# tuple attached to an API-listener route key. Reviewed by hand.
# ---------------------------------------------------------------------------
W = "web"; A = "apple"; N = "android"
manual_clients = {
    # web: variable action segment
    "api POST /api/v1/admin/sessions/{session_id}/pause": [(W, "src/components/AdminSessionActions.tsx", 92, ["SessionCommandResponse"])],
    "api POST /api/v1/admin/sessions/{session_id}/resume": [(W, "src/components/AdminSessionActions.tsx", 92, ["SessionCommandResponse"])],
    "api POST /api/v1/admin/sessions/{session_id}/stop": [(W, "src/components/AdminSessionActions.tsx", 92, ["SessionCommandResponse"])],
    "api POST /api/v1/admin/sessions/{session_id}/terminate": [(W, "src/components/AdminSessionActions.tsx", 92, ["SessionCommandResponse"])],
    "api POST /api/v1/auth/device/approve": [(W, "src/pages/ActivateDevice.tsx", 77, [])],
    "api POST /api/v1/auth/device/deny": [(W, "src/pages/ActivateDevice.tsx", 77, [])],
    "api GET /api/v1/requests/discover/browse/genre/{slug}": [(W, "src/hooks/queries/useRequests.ts", 144, ["DiscoverBrowseResponse"])],
    "api GET /api/v1/requests/discover/browse/network/{slug}": [(W, "src/hooks/queries/useRequests.ts", 144, ["DiscoverBrowseResponse"])],
    "api GET /api/v1/requests/discover/browse/studio/{slug}": [(W, "src/hooks/queries/useRequests.ts", 144, ["DiscoverBrowseResponse"])],
    # web: path built by a helper/const and passed as an identifier
    "api GET /api/v1/admin/stats/timeseries": [(W, "src/hooks/queries/admin/dashboardInsights.ts", 92, ["AdminTimeseries"])],
    "api GET /api/v1/admin/stats/playback-activity": [(W, "src/hooks/queries/admin/dashboardInsights.ts", 102, ["AdminPlaybackActivity"])],
    "api GET /api/v1/admin/stats/top-activity": [(W, "src/hooks/queries/admin/dashboardInsights.ts", 112, ["AdminTopActivity"])],
    "api GET /api/v1/admin/stats/downloads": [(W, "src/hooks/queries/admin/dashboardInsights.ts", 122, ["AdminDownloadsStats"])],
    "api GET /api/v1/admin/dashboard/layout": [(W, "src/hooks/queries/admin/dashboardLayout.ts", 37, ["AdminDashboardLayoutResponse"])],
    "api PUT /api/v1/admin/dashboard/layout": [(W, "src/hooks/queries/admin/dashboardLayout.ts", 48, ["void"])],
    "api DELETE /api/v1/admin/dashboard/layout": [(W, "src/hooks/queries/admin/dashboardLayout.ts", 69, [])],
    "api GET /api/v1/admin/autoscan/events": [(W, "src/hooks/queries/useAutoscan.ts", 324, ["AutoscanEventsResponse"])],
    "api GET /api/v1/admin/autoscan/scans": [(W, "src/hooks/queries/useAutoscan.ts", 354, ["AutoscanScansResponse"])],
    "api GET /api/v1/recommendations/section/{kind}": [(W, "src/hooks/queries/recommendations.ts", 121, ["RecommendationSectionResponse"])],
    "api GET /api/v1/recommendations/section/{kind}/{key}": [(W, "src/hooks/queries/recommendations.ts", 121, ["RecommendationSectionResponse"])],
    "api GET /api/v1/admin/history-imports/runs": [(W, "src/hooks/queries/admin/history-import-admin.ts", 129, ["HistoryImportRun[]"])],
    "api GET /api/v1/admin/subtitles/": [(W, "src/hooks/queries/admin/subtitles.ts", 35, ["AdminDownloadedSubtitlesResponse"])],
    "api PUT /api/v1/admin/users/{id}/settings/values/{key}": [(W, "src/hooks/queries/admin/users.ts", 290, []), (W, "src/hooks/queries/admin/users.ts", 320, []), (W, "src/hooks/queries/admin/users.ts", 417, [])],
    "api DELETE /api/v1/admin/users/{id}/settings/values/{key}": [(W, "src/hooks/queries/admin/users.ts", 325, []), (W, "src/hooks/queries/admin/users.ts", 448, []), (W, "src/hooks/queries/admin/users.ts", 486, [])],
    "api PUT /api/v1/collections/{id}/items/{item_id}": [(W, "src/hooks/queries/collections.ts", 174, [])],
    "api PUT /api/v1/admin/collections/{id}/items/{item_id}": [(W, "src/hooks/queries/collections.ts", 174, [])],
    "api POST /api/v1/admin/plugins/uploads/chunked": [(W, "src/hooks/queries/admin/plugins.ts", 198, ["ChunkedUploadSession"])],
    "api PUT /api/v1/admin/plugins/uploads/chunked/{upload_id}/chunks/{chunk_index}": [(W, "src/hooks/queries/admin/plugins.ts", 200, ["ChunkedUploadSession"])],
    "api POST /api/v1/admin/plugins/uploads/chunked/{upload_id}/complete": [(W, "src/hooks/queries/admin/plugins.ts", 202, ["PluginInstallation"])],
    "api DELETE /api/v1/admin/plugins/uploads/chunked/{upload_id}": [(W, "src/hooks/queries/admin/plugins.ts", 204, [])],
    "api PUT /api/v1/settings/values/{key}": [(W, "src/lib/seriesSubtitleSettings.ts", 49, [])],
    # web: WebSocket / navigation URLs
    "api GET /api/v1/events/ws": [(W, "src/components/RealtimeEventsProvider.tsx", 92, [])],
    "api GET /api/v1/admin/logs/ws": [(W, "src/hooks/admin/useAdminLogStream.ts", 63, [])],
    "api GET /api/v1/watch-together/rooms/{room_id}/ws": [(W, "src/player/hooks/useWatchTogetherRoomConnection.ts", 236, [])],
    "api GET /api/v1/direct-download": [(W, "src/hooks/queries/downloads.ts", 95, [])],
    "api GET /api/v1/ebooks/{content_id}/files/{file_id}/read": [(W, "src/reader/FoliateBookReader.tsx", 180, []), (N, "shared/src/commonMain/kotlin/org/siloserver/silo/network/api/EbookReaderApi.kt", 27, [])],
    # web: plugin UI navigation (documented exclusion, still a first-party consumer)
    "api GET /api/v1/plugins/{installation_id}/*": [(W, "src/lib/pluginRouteHref.ts", 6, [])],
    "api POST /api/v1/collections/preview": [(W, "src/hooks/queries/collectionPreviews.ts", 46, ["CollectionPreviewResponse"])],
    "api POST /api/v1/admin/collections/preview": [(W, "src/hooks/queries/collectionPreviews.ts", 46, ["CollectionPreviewResponse"])],
    "api DELETE /api/v1/devices/{device_id}": [(W, "src/hooks/queries/devices.ts", 57, [])],
    "api DELETE /api/v1/devices/{device_id}/settings": [(W, "src/hooks/queries/devices.ts", 64, [])],
    # apple
    "api PUT /api/v1/settings/values/nav.shortcuts/item": [(A, "iosApp/iosApp/Networking/ContinuumAPI+Settings.swift", 200, ["NavigationShortcutItemWriteRequest"])],
    "api GET /api/v1/downloads/{id}/file": [(A, "iosApp/iosApp/Downloads/DownloadAPI.swift", 71, [])],
    "api GET /api/v1/notifications/push/apple/display/{delivery_id}": [(A, "iosApp/iosApp/Notifications/ApplePushDisplayMetadata.swift", 68, ["ApplePushDisplayResponse"])],
    "api GET /api/v1/downloads/{id}/artwork/{kind}": [(A, "iosApp/iosApp/Downloads/DownloadManager.swift", 805, [])],
    "api GET /api/v1/downloads/{id}/subtitles/{ref}": [(A, "iosApp/iosApp/Downloads/DownloadManager.swift", 828, [])],
}
# Clients build subtitle URLs from the sidecar descriptor the playback plan
# carries; these are the builders that assemble them.
subtitle_url_builders = [
    (A, "iosApp/iosApp/Networking/AIModels.swift", 351, ["SidecarSubtitleDescriptor"]),
    (N, "shared/src/commonMain/kotlin/org/siloserver/silo/model/playback/SubtitleTrackMerge.kt", 56, []),
]
for k in ["api GET /api/v1/stream/{session_id}/subtitles/{track}", "api HEAD /api/v1/stream/{session_id}/subtitles/{track}"]:
    manual_clients.setdefault(k, []).extend(subtitle_url_builders)
# Clients follow server-provided stream/manifest URLs from the playback plan.
# The Apple and Android sites are the validator and resolver that admit those
# URLs and are recorded as match=follower: the route's path is never spelled
# there, so the pinned-tree test checks only that the file and line exist. The
# web site is the preconnect hint on the same URL and stays match=manual.
stream_routes = [
    "api GET /api/v1/stream/{session_id}", "api HEAD /api/v1/stream/{session_id}",
    "api GET /api/v1/stream/{session_id}/subtitles/{track}", "api HEAD /api/v1/stream/{session_id}/subtitles/{track}",
    "api GET /api/v1/stream/{session_id}/subtitles/{track}/fonts",
    "api GET /api/v1/playback/transcode/{session_id}/master.m3u8", "api GET /api/v1/playback/transcode/{session_id}/segment/{name}",
]
stream_followers = [
    (A, "iosApp/iosApp/Screens/Player/StreamRequest.swift", 205, []),
    (N, "android-shared/src/androidMain/kotlin/org/siloserver/silo/common/player/SiloPlayerFactory.kt", 731, []),
]
follower_clients = {}
for k in stream_routes:
    manual_clients.setdefault(k, []).append((W, "src/player/stream-url.ts", 9, []))
    follower_clients.setdefault(k, []).extend(stream_followers)

# ---------------------------------------------------------------------------
# Server-internal consumers: Go code in this repo that builds the URL or calls
# the route. (repo, file, line, kind) where kind names the role.
# ---------------------------------------------------------------------------
S = "server"
def go(file, line): return (S, file, line, [])
internal = {
    # api listener
    "api GET /api/v1/health": [go("Dockerfile", 90), go("docker-compose.dev.yml", 133)],
    "api POST /api/v1/autoscan/webhooks/{token}": [go("internal/api/handlers/autoscan.go", 389)],
    "api POST /api/v1/plex-sync/webhooks/{secret}": [go("internal/api/handlers/webhook_sync.go", 569)],
    "api POST /api/v1/webhook-sync/webhooks/{secret}": [go("internal/api/handlers/webhook_sync.go", 568), go("internal/webhooksync/types.go", 11)],
    "api GET /api/v1/auth/oauth/{install_id}/callback": [go("internal/auth/oauth_handler.go", 117)],
    "api GET /api/v1/notifications/discord/link/callback": [go("internal/api/handlers/notifications_discord.go", 40)],
    "api GET /api/v1/notifications/email/verify": [go("internal/notifications/email_address.go", 99)],
    "api GET /api/v1/notifications/email/unsubscribe": [go("internal/notifications/email_digest.go", 134)],
    "api POST /api/v1/notifications/email/unsubscribe": [go("internal/notifications/email_digest.go", 134)],
    "api GET /api/v1/branding/assets/{kind}": [go("internal/branding/branding.go", 57), go("internal/branding/render.go", 40)],
    "api GET /api/v1/downloads/{id}/artwork/{kind}": [go("internal/downloads/manifest.go", 362)],
    "api GET /api/v1/downloads/{id}/subtitles/{ref}": [go("internal/downloads/manifest.go", 366)],
    "api GET /api/v1/stream/{session_id}": [go("internal/api/handlers/playback.go", 757), go("internal/api/handlers/playback_v3.go", 2585)],
    "api HEAD /api/v1/stream/{session_id}": [go("internal/api/handlers/playback.go", 757), go("internal/api/handlers/playback_v3.go", 2585)],
    "api GET /api/v1/stream/{session_id}/subtitles/{track}": [go("internal/playback/subtitle_inventory_v3.go", 214)],
    "api HEAD /api/v1/stream/{session_id}/subtitles/{track}": [go("internal/playback/subtitle_inventory_v3.go", 214)],
    "api GET /api/v1/stream/{session_id}/subtitles/{track}/fonts": [go("internal/playback/subtitle_inventory_v3.go", 230)],
    "api GET /api/v1/playback/transcode/{session_id}/master.m3u8": [go("internal/api/handlers/playback.go", 755), go("internal/api/handlers/playback_v3.go", 3567)],
    "api GET /api/v1/playback/transcode/{session_id}/segment/{name}": [go("internal/playback/transcode.go", 3113)],
    "api GET /api/v1/plugins/{installation_id}/*": [go("cmd/silo/main.go", 2781), go("internal/audiobooks/abs/handler.go", 787)],
    # proxy listener
    # The same image (ENTRYPOINT silo, mode flag in cmd/silo/main.go) runs in
    # proxy and transcode mode, so the container HEALTHCHECK sites apply to
    # the node listeners as well as to the API row.
    "proxy GET /api/v1/health": [go("internal/nodepool/health.go", 61), go("Dockerfile", 90), go("docker-compose.dev.yml", 133)],
    "proxy GET /stream/direct/{token}": [go("internal/api/handlers/playback_v3.go", 3245), go("internal/jellycompat/handlers_playback.go", 1285)],
    "proxy HEAD /stream/direct/{token}": [go("internal/api/handlers/playback_v3.go", 3245), go("internal/jellycompat/handlers_playback.go", 1285)],
    "proxy GET /stream/remux/{token}": [go("internal/api/handlers/playback_v3.go", 3243), go("internal/jellycompat/handlers_playback.go", 1287)],
    "proxy HEAD /stream/remux/{token}": [go("internal/api/handlers/playback_v3.go", 3243), go("internal/jellycompat/handlers_playback.go", 1287)],
    "proxy GET /stream/remux/audio-v2/{token}": [go("internal/api/handlers/playback_v3.go", 3241), go("internal/jellycompat/handlers_playback.go", 1289)],
    "proxy HEAD /stream/remux/audio-v2/{token}": [go("internal/api/handlers/playback_v3.go", 3241), go("internal/jellycompat/handlers_playback.go", 1289)],
    "proxy GET /stream/transcode/{token}/master.m3u8": [go("internal/api/handlers/playback.go", 1932), go("internal/jellycompat/handlers_playback.go", 1298)],
    "proxy HEAD /stream/transcode/{token}/master.m3u8": [go("internal/api/handlers/playback.go", 1932), go("internal/jellycompat/handlers_playback.go", 1298)],
    "proxy GET /stream/transcode/{token}/segment/{name}": [go("internal/playback/transcode.go", 3113), go("internal/proxy/server.go", 906)],
    "proxy GET /stream/v3/{session_id}": [go("internal/api/handlers/playback_v3.go", 2787)],
    "proxy HEAD /stream/v3/{session_id}": [go("internal/api/handlers/playback_v3.go", 2787)],
    "proxy GET /stream/v3/{session_id}/master.m3u8": [go("internal/api/handlers/playback_v3.go", 3903)],
    "proxy HEAD /stream/v3/{session_id}/master.m3u8": [go("internal/api/handlers/playback_v3.go", 3903)],
    "proxy GET /stream/v3/{session_id}/segment/{name}": [go("internal/playback/transcode.go", 3113), go("internal/proxy/mediagrant.go", 194)],
    "proxy GET /downloads/file/{token}": [go("internal/api/handlers/downloads.go", 650)],
    "proxy HEAD /downloads/file/{token}": [go("internal/api/handlers/downloads.go", 650)],
    "proxy GET /hw-capabilities": [go("internal/transcodenode/capability_client.go", 39), go("internal/api/handlers/playback_transport.go", 94), go("internal/downloads/remote_preparer.go", 466), go("internal/jellycompat/handlers_playback.go", 523)],
    "proxy POST /admin/reload-config": [go("internal/api/handlers/nodes.go", 395)],
    "proxy POST /admin/force-reload": [go("internal/api/handlers/nodes.go", 532), go("internal/api/handlers/nodes.go", 581)],
    "proxy POST /admin/reprobe-capabilities": [go("internal/api/handlers/nodes.go", 807)],
    # transcode node listener
    "transcode_node GET /api/v1/health": [go("internal/nodepool/health.go", 61), go("Dockerfile", 90), go("docker-compose.dev.yml", 133)],
    "transcode_node POST /transcode/start": [go("internal/api/handlers/playback_transport.go", 40), go("internal/jellycompat/handlers_playback.go", 1538)],
    "transcode_node DELETE /transcode/{session_id}": [go("internal/playback/transcode_manager.go", 1058)],
    "transcode_node GET /transcode/{session_id}/master.m3u8": [go("internal/api/handlers/playback.go", 1636), go("internal/proxy/server.go", 895), go("internal/proxy/mediagrant.go", 181), go("internal/jellycompat/streams.go", 1227)],
    "transcode_node GET /transcode/{session_id}/segment/{name}": [go("internal/api/handlers/playback.go", 1748), go("internal/proxy/server.go", 906), go("internal/proxy/mediagrant.go", 194), go("internal/jellycompat/streams.go", 1248)],
    "transcode_node POST /transcode/{session_id}/segment/{name}/downloaded": [go("internal/transcodeproxy/completion.go", 94), go("internal/api/handlers/playback.go", 2034), go("internal/proxy/server.go", 1104), go("internal/jellycompat/streams.go", 1271)],
    "transcode_node GET /remux/{session_id}": [go("internal/proxy/server.go", 845), go("internal/proxy/mediagrant.go", 160)],
    "transcode_node HEAD /remux/{session_id}": [go("internal/proxy/server.go", 845), go("internal/proxy/mediagrant.go", 160)],
    "transcode_node POST /downloads/prepare": [go("internal/downloadprepare/transport.go", 321)],
    "transcode_node GET /downloads/artifacts/{artifact_id}": [go("internal/downloadprepare/transport.go", 409)],
    "transcode_node HEAD /downloads/artifacts/{artifact_id}": [go("internal/downloadprepare/transport.go", 343)],
    "transcode_node DELETE /downloads/artifacts/{artifact_id}": [go("internal/downloadprepare/transport.go", 378)],
    "transcode_node POST /chapter-thumbnails/extract": [go("internal/chapterthumbs/remote.go", 73)],
    "transcode_node GET /hw-capabilities": [go("internal/transcodenode/capability_client.go", 39), go("internal/api/handlers/playback_transport.go", 94), go("internal/downloads/remote_preparer.go", 466), go("internal/jellycompat/handlers_playback.go", 523)],
    "transcode_node POST /admin/reload-config": [go("internal/api/handlers/nodes.go", 395)],
    "transcode_node POST /admin/force-reload": [go("internal/api/handlers/nodes.go", 532), go("internal/api/handlers/nodes.go", 581)],
    "transcode_node POST /admin/reprobe-capabilities": [go("internal/api/handlers/nodes.go", 807)],
}
# Rows where the calling party is an external system or a browser following a
# server-issued link, not a first-party client build.
external = {
    "api POST /api/v1/autoscan/webhooks/{token}": "autoscan webhook delivery from an external scanner integration",
    "api POST /api/v1/plex-sync/webhooks/{secret}": "legacy Plex webhook delivery; the URL is still issued to Plex by the webhook-sync handler",
    "api POST /api/v1/webhook-sync/webhooks/{secret}": "webhook delivery from an external media server",
    "api GET /api/v1/auth/oauth/{install_id}/callback": "OAuth provider browser redirect",
    "api GET /api/v1/notifications/discord/link/callback": "Discord OAuth browser redirect",
    "api GET /api/v1/notifications/email/verify": "tokenized link in an outbound email",
    "api GET /api/v1/notifications/email/unsubscribe": "tokenized link in an outbound email",
    "api POST /api/v1/notifications/email/unsubscribe": "RFC 8058 one-click unsubscribe POST from a mail client",
    "api GET /api/v1/ready": "orchestrator readiness probe (docs/continuum-to-silo-docker-migration.md)",
    "api GET /api/v1/plugin-assets/{installation_id}/*": "asset references inside plugin-served pages",
    "api GET /api/v1/plugins/{installation_id}/*": "plugin-served pages and their own fetches",
    "api GET /api/v1/branding/assets/{kind}": "browser loads of server-rendered asset URLs (favicon, PWA manifest, email)",
    "root GET /metrics": "Prometheus scraper",
    "proxy GET /metrics": "Prometheus scraper",
    "transcode_node GET /metrics": "Prometheus scraper",
}
# Compat-implementation consumers (jellycompat / ABS) — recorded as a consumer
# class so the compatibility_only rule can be evaluated.
compat_files = ("internal/jellycompat/", "internal/audiobooks/abs/")

# ---------------------------------------------------------------------------
# Flow, tier, capability endpoint, disposition.
#
# release_flow is derived from route intent (who calls it and what for), not
# from the path prefix alone: the acting_admin library-management routes under
# /api/v1/libraries/, the admin scan triggers, and the theme catalog refresh
# are core_admin even though their prefixes look like browse paths, while the
# viewer-facing /api/v1/library/{id}/* reads stay in browse_search.
# ---------------------------------------------------------------------------
FLOW_RULES = [
    (r"^/api/v1/libraries(/|$)", "core_admin"),
    (r"^/api/v1/scan(/|$)", "core_admin"),
    (r"^/api/v1/theme/catalog/refresh$", "core_admin"),
    (r"^/api/v1/auth/(setup|signup)", "setup"),
    (r"^/api/v1/onboarding", "setup"),
    (r"^/api/v1/invitations/", "setup"),
    (r"^/api/v1/auth/", "login"),
    (r"^/api/v1/profiles?(/|$)", "profiles"),
    (r"^/api/v1/(catalog|libraries|library|items|people|home|search|calendar|works|sections|recommendations|collections|audiobooks|ebooks|requests|watch-providers|favorites|watchlist|ratings|markers|subtitles|subtitle-prefs|audio-prefs|library-playback-prefs|theme|branding|images|metadata)(/|$)", "browse_search"),
    (r"^/api/v1/user/libraries$", "browse_search"),
    (r"^/api/v1/events/ws(-ticket)?$", "notifications"),
    (r"^/api/v1/(playback|stream|watch-together|direct-download|direct-download-proxy)(/|$)", "playback_lifecycle"),
    (r"^/api/v1/(progress|sync|watched|watch|history)(/|$)", "progress"),
    (r"^/api/v1/(settings|devices|api-keys)(/|$)", "settings"),
    (r"^/api/v1/notifications(/|$)", "notifications"),
    (r"^/api/v1/downloads(/|$)", "downloads"),
    (r"^/api/v1/admin/(users|libraries|nodes|node-sessions|settings|tasks|jobs|sessions|stats|system|access-groups|policy|invitations|invite-codes|api-keys|devices|dashboard|logs|diagnostics|rate-limits|playback-routing|stream-telemetry)(/|$)", "core_admin"),
    (r"^/api/v1/admin/", "other_admin"),
    (r"^/api/v1/(events|diagnostics|history-imports|plex-sync|webhook-sync|autoscan|policy|plugins|plugin-assets)(/|$)", "background_refresh"),
    (r"^/api/v1/(health|ready)$", "operational"),
]
TIER1_FLOWS = {"login", "setup", "profiles", "browse_search", "playback_lifecycle", "progress", "settings", "notifications", "downloads", "core_admin"}
CAP_RULES = [
    (r"^/api/v1/auth/device(/|$)", "GET /api/v1/auth/device/capability"),
    (r"^/api/v1/collections(/|$)", "GET /api/v1/collections/capabilities"),
    (r"^/api/v1/downloads(/|$)", "GET /api/v1/downloads/capability"),
    (r"^/api/v1/direct-download", "GET /api/v1/downloads/capability"),
    (r"^/api/v1/ebooks(/|$)", "GET /api/v1/ebooks/capability"),
    (r"^/api/v1/events(/|$)", "GET /api/v1/events/capability"),
    (r"^/api/v1/items/(\{id\}/)?trailers", "GET /api/v1/items/trailers/capability"),
    (r"^/api/v1/notifications(/|$)", "GET /api/v1/notifications/capability"),
    (r"^/api/v1/playback(/|$)", "GET /api/v1/playback/capability"),
    (r"^/api/v1/stream(/|$)", "GET /api/v1/playback/capability"),
    (r"^/api/v1/policy(/|$)", "GET /api/v1/policy/capability"),
    (r"^/api/v1/settings(/|$)", "GET /api/v1/settings/capability"),
    (r"^/api/v1/admin/dashboard(/|$)", "GET /api/v1/admin/dashboard/capabilities"),
    (r"^/api/v1/admin/playback-routing(/|$)", "GET /api/v1/admin/playback-routing/capabilities"),
    (r"^/api/v1/admin/sessions(/|$)", "GET /api/v1/admin/sessions/capabilities"),
    (r"^/api/v1/api-keys(/|$)", "GET /api/v1/api-keys/scopes"),
    (r"^/api/v1/libraries(/|$)", "GET /api/v1/libraries/provider-defaults"),
    (r"^/api/v1/images(/|$)", "GET /api/v1/images/capability"),
]
CAP_SELF = {"/api/v1/auth/device/capability", "/api/v1/collections/capabilities", "/api/v1/downloads/capability", "/api/v1/ebooks/capability", "/api/v1/events/capability", "/api/v1/items/trailers/capability", "/api/v1/notifications/capability", "/api/v1/playback/capability", "/api/v1/policy/capability", "/api/v1/settings/capability", "/api/v1/settings/contract/capabilities", "/api/v1/admin/dashboard/capabilities", "/api/v1/admin/playback-routing/capabilities", "/api/v1/admin/sessions/capabilities", "/api/v1/api-keys/scopes", "/api/v1/libraries/provider-defaults", "/api/v1/images/capability"}

# v1-scope removals table: "String GET/PUT/DELETE /api/v1/settings…" still registered on the bridge.
REMOVED_BY_SCOPE_TABLE = {
    "api GET /api/v1/settings/", "api GET /api/v1/settings/{key}", "api PUT /api/v1/settings/{key}", "api DELETE /api/v1/settings/{key}",
    "api GET /api/v1/settings/device/{key}", "api PUT /api/v1/settings/device/{key}", "api DELETE /api/v1/settings/device/{key}",
    "api GET /api/v1/settings/effective",
}

def flow_for(r):
    if r['listener'] in ('proxy', 'transcode_node'):
        p = r['path']
        if p == '/api/v1/health' or p in ('/status', '/metrics', '/hw-capabilities') or p.startswith('/admin/'):
            return 'operational'
        if p.startswith('/downloads/') or p.startswith('/chapter-thumbnails/'):
            return 'downloads' if p.startswith('/downloads/') else 'background_refresh'
        return 'playback_lifecycle'
    if r['listener'] == 'root':
        return 'operational' if r['path'] in ('/metrics', '/api/') else 'other'
    for pat, flow in FLOW_RULES:
        if re.search(pat, r['path']): return flow
    return 'other'

def cap_for(r):
    if r['listener'] != 'api' or r['path'] in CAP_SELF: return None
    for pat, cap in CAP_RULES:
        if re.search(pat, r['path']): return cap
    return None

def disposition_for(r, consumers):
    k = key(r); p = r['path']
    if r['listener'] == 'root':
        if p == '/metrics': return 'documented_exclusion', 'operator_metrics', 'Unauthenticated operator-facing /metrics stays outside the native client contract (api-contract.md, "What Huma covers").'
        if p == '/api/': return 'documented_exclusion', 'listener_delegation', 'Root-mux delegation to the API listener; the operations behind it are the API listener rows.'
        return 'documented_exclusion', 'maintainer_decision', 'Bundled web frontend catch-all on the root listener; an HTML/asset response, not a native API operation. No ratified document names this exclusion, so it is a judgment call for maintainer review.'
    if p == '/metrics': return 'documented_exclusion', 'operator_metrics', 'Unauthenticated operator-facing /metrics stays outside the native client contract (api-contract.md, "What Huma covers").'
    if r['delegates_to'] == 'api_v2': return 'documented_exclusion', 'listener_delegation', 'API-listener delegation to the native v2 listener; the operations behind it are described by contracts/api/v2/openapi.json, not by legacy ledger rows.'
    if p.startswith('/api/v1/plugins/{installation_id}/*') or p.startswith('/api/v1/plugin-assets/'): return 'documented_exclusion', 'dynamic_plugin_proxy', 'Dynamic plugin HTTP proxy path; cannot have a finite static response schema (api-contract.md, "What Huma covers").'
    if k in REMOVED_BY_SCOPE_TABLE: return 'removed', 'v1_scope_removals_table', 'Listed in docs/architecture/v1-scope.md removals ("String GET/PUT/DELETE /api/v1/settings…"); still registered on the bridge surface, retired by the tombstone. Successor: the typed settings contract (/api/v1/settings/values*).'
    if r['listener'] == 'api' and p in ('/api/v1/health', '/api/v1/ready'): return 'redesigned', 'contract_root_probes', 'api-contract.md: liveness/readiness move to version-neutral root /health and /ready and stay out of the OpenAPI artifact.'
    if r['listener'] in ('proxy', 'transcode_node') and p == '/api/v1/health': return 'redesigned', 'contract_root_probes', 'Plan ratified constraint 4 moves operational probes to root /health and /ready without listener qualification, and decision register Decision 2 (cluster and remote-node upgrade contract) requires the API-to-worker control-plane probe to move under the supported worker protocol rather than survive as a v1 alias. The v2 probe is GET /health on this same listener; internal/nodepool/health.go is the internal consumer that must follow.'
    if k == 'api POST /api/v1/playback/start': return 'redesigned', 'contract_playback_start_status', 'api-contract.md: the legacy/draft-body 426 becomes the v2 422 protocol-version/input-schema problem; v3 payload semantics unchanged.'
    if k == 'api POST /api/v1/auth/plugin-launch': return 'redesigned', 'contract_plugin_cookie_path', 'api-contract.md "Credential continuity": the plugin access cookie is reissued on a narrow v2 plugin-content parent path, never broadened to /.'
    if consumers and all(c in ('compat',) for c in consumers): return 'compatibility_only', 'only_compat_consumer', 'Only consumer is the jellycompat/ABS implementation.'
    if r['listener'] in ('proxy', 'transcode_node'): return 'ported', 'default_ported', NODE_PORTED_RATIONALE
    return 'ported', 'default_ported', 'No exclusion, removal, or ratified redesign applies; port to v2 in its section PR.'

NODE_PORTED_RATIONALE = ("No exclusion, removal, or ratified redesign applies. The node listeners have no /api/v2 namespace: "
    "this route is retained at its version-neutral path on the node listener and described through the typed manual registry, "
    "with no /api/v2 alias (plan constraint 4; api-contract.md section 8, version-neutral legacy routes are retired individually "
    "and are not aliases into v2). Its section PR confirms retention or retires it individually.")
NODE_PORTED_NOTE = "v2 is null by design for node-listener rows: the route keeps its version-neutral path and is described in the manual registry, not aliased under /api/v2."

def media_kind(r, which):
    v = r['request_kind'] if which == 'request' else r['response_media_kind']
    if r['path'].startswith('/api/v1/plugins/{installation_id}/*') or r['path'].startswith('/api/v1/plugin-assets/'):
        return 'dynamic_proxy'
    return v

entries = []
seen = {}
for r in rows:
    k = key(r)
    seen[k] = seen.get(k, 0)
    reg_index = seen[k]; seen[k] += 1
    prior = existing.get((r['listener'], r['method'], r['path'], reg_index))
    sites = []
    prior_by_loc = {}
    if prior is not None:
        prior_by_loc = {(s['repo'], s['file'], s['line']): s for s in prior['consumer_call_sites']}
        sites.extend(s for s in prior['consumer_call_sites'] if s['match'] in ('manual', 'follower'))
    else:
        for (repo, f, line, types) in manual_clients.get(k, []):
            sites.append({"repo": repo, "file": f, "line": line, "types": sorted(set(types)), "match": "manual"})
        for (repo, f, line, types) in follower_clients.get(k, []):
            sites.append({"repo": repo, "file": f, "line": line, "types": sorted(set(types)), "match": "follower"})
        for (repo, f, line, types) in internal.get(k, []):
            sites.append({"repo": repo, "file": f, "line": line, "types": [], "match": "manual"})
    for c in cmap.get(k, []):
        site = OrderedDict([("repo", c['repo']), ("file", c['file']), ("line", c['line'])])
        # The extractor does not know where a path constant is declared; that
        # annotation is curated on the site and survives a refresh by location.
        literal_line = prior_by_loc.get((c['repo'], c['file'], c['line']), {}).get('path_literal_line')
        if literal_line is not None:
            site["path_literal_line"] = literal_line
        site["types"] = sorted(set(t for t in c['types'] if t))
        site["match"] = "mechanical"
        sites.append(site)
    # de-dup: a manual site and a mechanical site at the same place are one site
    uniq = OrderedDict()
    for s in sites: uniq[(s['repo'], s['file'], s['line'])] = s
    sites = sorted(uniq.values(), key=lambda s: (s['repo'], s['file'], s['line']))
    consumers = set()
    for s in sites:
        if s['repo'] == 'server':
            consumers.add('compat' if s['file'].startswith(compat_files) else 'internal')
        else:
            consumers.add(s['repo'])
    if k in external: consumers.add('external_unknown')
    if r['listener'] == 'root' and r['path'] == '/': consumers.add('web')
    if r['listener'] == 'root' and r['path'] == '/api/': consumers.add('internal')
    # The API listener's /api/v2/* hand-off is consumed by the server itself,
    # like the root /api/ delegation; there is no call site to credit.
    if r['delegates_to'] == 'api_v2': consumers.add('internal')
    if not consumers: consumers = {'unused'}
    consumers = sorted(consumers)
    flow = flow_for(r)
    disp, rule, why = disposition_for(r, consumers)
    tier = 1 if (flow in TIER1_FLOWS and disp != 'removed') else 2
    v2 = {"method": None, "path": None, "operation_id": None}
    if disp == 'redesigned' and r['path'] in ('/api/v1/health', '/api/v1/ready'):
        v2 = {"method": "GET", "path": r['path'].replace('/api/v1', ''), "operation_id": None}
    owner = "pending:#135/execution-input-1" if disp in ('removed', 'redesigned', 'replaced') else None
    notes = []
    if k in external: notes.append("External caller: " + external[k] + ".")
    if r['listener'] == 'root' and r['path'] == '/': notes.append("Serves the bundled web frontend; the browser is the consumer.")
    if disp == 'removed': notes.append("Administrator action: ship the bridge builds of the first-party clients before upgrading the server to 1.0; the Android and Apple builds cited in consumer_call_sites still call this route and must move to the typed settings contract (GET/PUT/DELETE /api/v1/settings/values/{key}, GET /api/v1/settings/values/effective) first. On a 1.0 server this route answers 410 client_upgrade_required.")
    if r['listener'] == 'api' and r['path'] == '/api/v1/health': notes.append("Administrator action: rewrite the container HEALTHCHECK (Dockerfile, docker-compose.dev.yml) and every orchestrator liveness probe from /api/v1/health to /health before the 1.0 cutover; /api/v1/health answers 410 afterwards. Client action: Apple (ServerIdentityResolver, ConnectionMonitor) and Android (HealthApi, ServerReachabilityMonitor, PlaybackSessionLifecycle) probe this route for server identity and reachability; they move to the v2 discovery document GET /api/v2/system/info (decision register Decision 3), not to the operator probe.")
    if r['listener'] == 'api' and r['path'] == '/api/v1/ready': notes.append("Administrator action: rewrite every orchestrator readiness probe from /api/v1/ready to /ready before the 1.0 cutover.")
    if r['listener'] in ('proxy', 'transcode_node') and r['path'] == '/api/v1/health': notes.append("Administrator action: rewrite the proxy/transcode-node container HEALTHCHECK and orchestrator probes from /api/v1/health to /health on the node listener, and upgrade API replicas and nodes together inside the homogeneous-version maintenance window (Decision 2). Internal consumer: internal/nodepool/health.go CheckNode builds NodeEndpoint(n.URL, \"/api/v1/health\") and must switch to /health in the same change; the node-listener /api/v1/health route is then retired individually (it is not under the main-API tombstone).")
    if k == 'api POST /api/v1/playback/start': notes.append("Administrator action: none beyond the coordinated upgrade. Client action: the v2 start body must declare protocol_version 3; a legacy or draft body receives 422 (protocol-version/input-schema problem) on v2 instead of the v1 426. The v3 payload semantics are unchanged and remain tier 1.")
    if k == 'api POST /api/v1/auth/plugin-launch': notes.append("Administrator action: none beyond the coordinated upgrade; plugin pages open before the cutover need one fresh launch afterwards. Client action: the v2 launch reissues the five-minute HttpOnly SameSite=Lax plugin access cookie on the narrow v2 plugin-content parent path; the /api/v1-path cookie is expired on launch/logout where practical and otherwise dies within its five-minute maximum. Never broadened to /.")
    if disp == 'ported' and r['listener'] in ('proxy', 'transcode_node'): notes.append(NODE_PORTED_NOTE)
    if r['path'] == '/status' : notes.append("No caller found in this repository or the first-party clients; verify before treating as unused.")
    if r['path'].startswith('/stream/subtitles/'): notes.append("No URL builder for the proxy subtitle path was found in server or client code; verify whether v3 subtitle inventory still emits proxy subtitle URLs.")
    if r['path'] in ('/api/v1/downloads/{id}/file-proxy', '/api/v1/direct-download-proxy'): notes.append("No producer or client reference found; candidate for a maintainer removal decision.")
    if 'unused' in consumers: notes.append("No first-party or internal consumer found. Absence from first-party clients is not proof of disuse: third-party clients exist, and removal needs an affirmative product decision.")
    if rule == 'dynamic_plugin_proxy':
        notes.append("Media kind deliberately overrides the inventory's heuristic value: a dynamic plugin proxy has no static request or response kind, so both are recorded as dynamic_proxy (the gate allows this override only under this rule).")
    elif r['request_kind'] == 'unknown' or r['response_media_kind'] == 'unknown': notes.append("Inventory media kind is heuristic and unresolved for this row; confirm in the section PR.")
    if r['conditional']: notes.append("Conditionally registered: " + "; ".join(r['conditions']) + ". v2 registers the operation unconditionally and reports capability state instead.")
    if sum(1 for x in rows if key(x) == k) > 1: notes.append("One of several registrations of this method+path with different middleware/conditions; see registration_index and conditions.")
    entry = OrderedDict([
        ("listener", r['listener']), ("namespace", r['namespace']), ("method", r['method']), ("path", r['path']),
        ("registration_index", reg_index),
        ("handler", r['handler']), ("handler_kind", r['handler_kind']), ("source_file", r['source_file']),
        ("route_group", r['route_group']), ("middleware_chain", r['middleware_chain']),
        ("auth_class", r['auth_class']), ("auth_traits", list(r['auth_traits'])),
        ("profile_required", 'profile_required' in r['auth_traits']),
        ("admin_required", 'acting_admin' in r['auth_traits']),
        ("conditional", r['conditional']), ("conditions", list(r['conditions'])),
        ("delegates_to", r['delegates_to'] or None),
        ("request_kind", media_kind(r, 'request')), ("response_media_kind", media_kind(r, 'response')),
        ("streams", r['streams']), ("upgrades_websocket", r['upgrades_websocket']),
        ("capability_endpoint", cap_for(r)),
        ("consumers", consumers), ("consumer_call_sites", sites),
        ("section", section_for({"listener": r["listener"], "namespace": r["namespace"], "method": r["method"], "path": r["path"]})), ("release_flow", flow), ("tier", tier),
        ("disposition", disp), ("disposition_rule", rule), ("disposition_rationale", why),
        ("owner", owner),
        ("review_state", "proposed"),
        ("v2", v2),
        ("notes", " ".join(notes)),
    ])
    if prior is not None:
        for field in CURATED:
            entry[field] = prior[field]
        for field in CURATED_OPTIONAL:
            if field in prior:
                entry[field] = prior[field]
    entries.append(entry)

doc = OrderedDict([
    ("schema_version", 1),
    ("description", "Pre-1.0 native surface to /api/v2 migration ledger: one entry per contracts/api/v2/route-inventory.json row, keyed by listener + method + path + registration_index (the inventory registers a few method+path pairs more than once under different middleware/conditions). Dispositions are proposals until review_state is ratified. release_flow is derived from route intent (who calls the route and for what), not from the path prefix: acting_admin library management under /api/v1/libraries/, the admin scan triggers, and the theme catalog refresh are core_admin while the viewer-facing /api/v1/library/{id}/* reads are browse_search. Tier rule: tier 1 when release_flow is one of the plan's release-critical flows (login, setup, profiles, browse_search, playback_lifecycle, progress, settings, notifications, downloads, core_admin) and the disposition is not removed; removed rows are tier 2 because there is no v2 behavior to baseline; every other row is tier 2. owner is required on removed, redesigned, and replaced rows and holds the literal pending:#135/execution-input-1 until named reviewers are recorded; a row cannot be ratified while its owner is pending. section is the Phase 4 delivery unit (one section PR per value), assigned by scripts/apiv2-ledger/assign_sections.py from listener, namespace and path."),
    ("source_inventory", "contracts/api/v2/route-inventory.json"),
    ("source_trees", OrderedDict([("silo-apple", APPLE_SHA), ("silo-android", ANDROID_SHA), ("silo-server", "this repository")])),
    ("consumer_method", "Client call sites were extracted by scripts/apiv2-ledger/extract_consumers.py from web/src (this repo) and the origin/main trees of silo-apple and silo-android at the commits pinned in source_trees (re-resolve a sibling site with git show <sha>:<file> in that repo, never against a moving branch): path literals and templates passed to HTTP/WebSocket calls, plus paths built by helper functions that return a template, templates rooted at the API base URL, Kotlin buildString/const val builders, and Swift let/static let path bindings, with interpolations normalized to a wildcard and matched against inventory paths by method (scripts/apiv2-ledger/match_consumers.py). scripts/apiv2-ledger/sweep_uncredited.py then greps the last two static segments of every inventory path across all three trees and every hit not within four lines of a credited site was resolved by hand. Server-internal and compat consumers are Go URL builders and callers in this repo. Sites marked match=manual were resolved by hand where the path is assembled from variables the scripts cannot follow. Call-site files are repository-root-relative for their repo (web: src/... under web/; apple: iosApp/...; android: shared/, android-shared/, androidApp/, androidTvApp/; server: this repository root). Absence of a first-party consumer is not proof of disuse."),
    ("field_reference", "internal/contractledger validates this file against migration.schema.json and reconciles it against the route inventory: listener, namespace, method, path, registration_index, handler, handler_kind, source_file, route_group, middleware_chain, auth_class, auth_traits, conditional, conditions, delegates_to (inventory empty string is null here), request_kind, response_media_kind, streams, and upgrades_websocket are copied from the inventory row and must match it, except that dynamic_plugin_proxy rows record both media kinds as dynamic_proxy; profile_required and admin_required are derived from auth_traits. Every other field is curated by hand and preserved when scripts/apiv2-ledger/build_ledger.py merges a regenerated inventory into this file."),
    ("totals", OrderedDict([("entries", len(entries))])),
    ("entries", entries),
])
# ensure_ascii=False keeps the committed file's non-ASCII prose (an ellipsis
# in a rationale) as UTF-8, so a re-run on an unchanged input is byte-identical.
with open(OUT, 'w', encoding='utf-8') as f:
    json.dump(doc, f, indent=2, ensure_ascii=False)
    f.write("\n")
merged = sum(1 for e in entries if (e['listener'], e['method'], e['path'], e['registration_index']) in existing)
print("entries:", len(entries), "merged from existing:", merged, "seeded:", len(entries) - merged)
print("disposition:", Counter(e['disposition'] for e in entries))
print("tier:", Counter(e['tier'] for e in entries))
c = Counter()
for e in entries:
    for x in e['consumers']: c[x] += 1
print("consumers:", c)
print("flow:", Counter(e['release_flow'] for e in entries))
