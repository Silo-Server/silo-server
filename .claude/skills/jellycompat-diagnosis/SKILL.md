---
name: jellycompat-diagnosis
description: Use when a Jellyfin-protocol client (JellyCon, Findroid, Infuse, Swiftfin) misbehaves against Silo's jellycompat API — wrong or missing data, ignored sort/filter/pagination parameters, oversized responses, or slow browse queries. Covers comparing Silo's responses against a real Jellyfin server field-by-field and reading the jellycompat debug log. Not for Silo's own `/api/v1` clients.
---

# Diagnosing jellycompat responses

Jellyfin clients are unforgiving about response shape: a missing field or an extra one
changes what they render, and they rarely report why. The reliable method is to make the
identical request against Silo and against a real Jellyfin, then diff the JSON.

Set `JELLYFIN_COMPARE_URL` and credentials in `.silo-dev.env` — without a reference
server you are guessing at what the client expected.

## Start from what the client actually sent

The debug log records full request/response pairs, but only when the server runs with
`JELLYCOMPAT_DEBUG_LOG` set to a path. If it is unset, that is the first fix.

```bash
scripts/silo-dev sh 'grep -E "^(===|Status:|Remote:|Response \()" /opt/silo/jellycompat-debug.log | tail -60'
```

That overview is usually enough to localize the problem before reading any bodies. Look
for response sizes (a 20-item list over ~50KB is wrong), latency (browse over a second is
wrong), non-200 statuses, and — most importantly — which query parameters the client
actually sent, which is often not what you assumed. `grep -n "^==="` gives request
boundaries to `sed` a specific body out of.

## Compare against real Jellyfin

Authenticate to both and request the same path. Jellyfin's auth is a POST to
`/Users/AuthenticateByName` with an `X-Emby-Authorization` header; the response carries
`AccessToken` and `User.Id`, which you need for subsequent `/Users/{id}/Items` calls.
Silo's jellycompat listens on port 8096 alongside the main API.

Diff the sorted key lists of one item from each side before diffing values — a field that
is present on one and absent on the other explains most client bugs on its own, and
`omitempty` in `dto.go` means an empty value and a missing field are the same thing on
the wire.

[references/response-parity.md](references/response-parity.md) has the queries JellyCon
sends, the fields real Jellyfin returns with and without an explicit `Fields` parameter,
the parameter-handling behaviors worth asserting, and a symptom-to-cause table for the
failures already found once — check it before re-deriving a diagnosis.

## After a fix

Redeploy, then re-authenticate — restarting the container invalidates the tokens — and
re-run every query you were comparing, not just the one you fixed. Field gating is shared
across handlers, so a change that corrects a list view frequently perturbs detail views.
