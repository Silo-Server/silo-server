---
name: dev-environment-debugging
description: Use when diagnosing a running Silo server — playback, streaming, or transcode failures, unhealthy nodes, stuck sessions, wrong database state, errors after a deploy, or anything needing the deployment's logs, health endpoints, or SQL. Covers both a local docker compose install and a remote host over SSH. Not for building or testing code in this repo, and not for client-side bugs that reproduce without a server.
---

# Debugging a running Silo deployment

Everything here goes through `scripts/silo-dev`, which reads `.silo-dev.env` and runs
commands against the deployment whether it is on this machine or behind SSH. Start with:

```bash
scripts/silo-dev doctor
```

`doctor` reports which of API, host shell, compose stack, database, and login actually
work. Fix what it flags before diagnosing anything else — most confusing symptoms here
are really a misconfigured target. If `.silo-dev.env` is missing, copy
`.silo-dev.env.example` and fill it in; ask the user for hosts and credentials rather
than guessing, and never move those values into a committed file.

## Orienting

```bash
scripts/silo-dev compose ps                  # what is running
scripts/silo-dev api /api/v1/health          # API up
scripts/silo-dev api /api/v1/ready           # PostgreSQL and optional S3 reachable
scripts/silo-dev logs --tail 200             # recent server logs
scripts/silo-dev logs -f                     # follow
```

`/ready` failing usually means the database, not the API — read the service logs before
concluding Postgres is down.

Silo also records structured logs in the database itself, which survive container
restarts and are easier to filter than stdout. `operational_logs` holds server events,
`activity_log` holds HTTP requests; both are keyed by `"timestamp"` (quoted — it is a
reserved word). See [references/queries.md](references/queries.md) for the diagnostic
queries worth having on hand, along with the schema traps that bite most often.

Log level lives in `server_settings` under `server.log_level` and is read at startup, so
raising it to `debug` means updating the row and recreating the service. Put it back
afterwards unless the task needs it to persist.

## Search

Meilisearch is optional — Silo runs without it — but it is recommended, and it is what
makes search results good. It ships as a compose service behind the `search` profile and
needs `MEILI_MASTER_KEY` set, so a stack brought up without that profile simply has no
`meilisearch` service.

The provider is chosen from the `catalog.search.provider` setting rather than from
whether the container is running, and the failure mode is quiet: if Meilisearch is
selected but the URL is empty, unreachable, or the provider fails to initialize, Silo
logs a warning at startup and serves Postgres results instead. Nothing surfaces to the
client. So "search got worse" or "search stopped matching the way it used to" is usually
the fallback being active, not a ranking regression — check which provider is live before
touching query code:

```bash
scripts/silo-dev psql "SELECT key, value FROM server_settings WHERE key LIKE 'catalog.search%' ORDER BY key;"
scripts/silo-dev logs --tail 500 | grep -i 'catalog search'
```

Provider settings, index name, timeouts, batch sizes, and the semantic-search options all
live under `catalog.search.meilisearch.*` in `server_settings`. Changing the provider
needs a restart to take effect.

## Playback

Work outward from the failure the user actually saw.

**Direct play** — confirm the file exists at the path stored in `media_files`
(`scripts/silo-dev sh 'ls -la …'`, under `SILO_MEDIA_ROOT`), then compare the file's
codecs against what the client advertised. A mismatch should have fallen back to remux
or transcode; if it did not, that is the bug.

**Remux and transcode** — both run ffmpeg inside the server container, so check ffmpeg
is present and, for hardware paths, that `/dev/dri` is passed through:

```bash
scripts/silo-dev compose "exec -T silo sh -lc 'ffmpeg -hwaccels; ls -la /dev/dri'"
```

Transcode output lands in a bind-mounted temp directory; a full disk there looks like a
transcode bug. If `SILO_TRANSCODE_SSH` is set, the node is a separate host with its own
`/api/v1/health` and logs — check it before blaming the main server.

**Stuck sessions** — query `playback_sessions_sync` for rows whose `updated_at` has gone
stale. The reconciler and shutdown path clean these up; when Redis is unavailable,
cross-node coordination degrades and stale rows are a symptom rather than the cause.

## Deploying a fix

`make dev-deploy` and friends come from `Makefile.local`, which is gitignored and
per-developer — do not assume they exist. Confirm with `make -n dev-deploy` first, and
otherwise deploy however the user's setup does. Afterwards, verify rather than assume:

```bash
scripts/silo-dev compose ps
scripts/silo-dev api /api/v1/ready
scripts/silo-dev logs --tail 100
```

Deploys restart the container, which invalidates any token you were holding.

## Rename drift

Silo was formerly Continuum. Names like `continuum-postgres`, `/opt/continuum`,
`CONTINUUM_IMAGE`, or a stray `docker-compose.dev.yml` are usually legacy artifacts, but
on a long-lived host they are occasionally still the live stack. Check what compose
actually reports before deciding which one you are looking at.
