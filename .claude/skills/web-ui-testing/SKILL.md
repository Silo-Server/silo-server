---
name: web-ui-testing
description: Use when verifying a change to the Silo web frontend (`web/`) in a real browser — checking a page renders, a flow works, or a fix actually landed, and capturing screenshots for a PR. Runs the local Vite dev server against a real Silo backend so no build or deploy is needed. Not for backend-only changes, and not a substitute for unit tests in `web/src`.
---

# Verifying web UI changes

The frontend only ever calls relative `/api` URLs, so pointing it at a real backend is a
one-line config change — no build, no deploy, and hot reload still works while you
iterate.

## Point the dev server at a backend

```bash
printf 'VITE_API_PROXY_TARGET=%s\n' "$(scripts/silo-dev env | awk '/^SILO_URL/{print $2}')" > web/.env.local
make dev-frontend
```

Any reachable Silo instance works as the target — the shared `.silo-dev.env` deployment,
a colleague's server, or `http://localhost:8090` for a backend you are running yourself
with `make dev-backend`. Vite proxies `/api` (including WebSockets, such as the realtime
`scans` channel) and sets `changeOrigin` automatically for non-localhost targets, because
remote vhost proxies reject a localhost `Host` header.

`web/.env.local` is per-checkout, so a git worktree needs its own.

Before debugging anything in the UI, confirm the proxy itself:

```bash
curl -s http://localhost:5173/api/v1/health
```

Silo health JSON means the wiring is good and any remaining problem is the frontend. An
HTML page or a connection error means the proxy target is wrong or the backend is down —
`scripts/silo-dev doctor` will say which.

## Drive it

The app is at `http://localhost:5173`. Use the Playwright tooling to navigate, interact,
and screenshot.

The dev server binds `0.0.0.0` and `allowedHosts` covers `.ts.net` plus the machine's own
hostname, so it is also reachable from any device on the tailnet at
`http://<tailscale-machine>:5173`. Always give the user that URL alongside the localhost
one when starting a dev server for them — it is how they watch the UI you are working on
from another machine. `tailscale status --json | jq -r .Self.DNSName` gives the name.

Most pages need a session. Sign in through the login form using `SILO_ADMIN_USER` and
`SILO_ADMIN_PASSWORD` from `.silo-dev.env`; the session then lives in that browser
profile for the rest of the run. If those are unset, ask the user for an account on the
target rather than testing only the signed-out shell — and never type credentials the
user has not given you for that server.

## What counts as verified

A change is verified when you have seen the specific thing that was broken now behaving
correctly, on a page reached the way a user reaches it, against real data. Screenshots
belong in the PR for anything visual.

Watch for the failure modes that look like success: a page that renders because it fell
back to an empty state, a stale bundle from a dev server started before your edit, and a
console full of errors under a page that looks fine. Check the browser console before
calling it done.

## States that are hard to reach

Some UI only appears under conditions you cannot conjure on a real server — a flood of
queued scans, a specific error shape, a half-finished download. Rather than mutating the
backend, render the real page inside a temporary dev-only route wrapping it in its own
`QueryClientProvider` (queries `enabled: false`) seeded via `setQueryData`. It needs its
own client because `useAuth` calls `clear()` on the global one when auth fails, which
would wipe the fixtures. Delete the harness route once you have the screenshot.
