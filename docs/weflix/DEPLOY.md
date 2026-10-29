# Deploying the WeFlix fork

How to build your own Silo image from this fork, point your deployment at it,
and pull upstream updates in later. Nothing here changes the NAS; it describes
what to run when you decide to.

## What the deployment runs today

- `ghcr.io/silo-server/silo-server:latest` on Synology Container Manager, in the
  compose project directory alongside the other container stacks there.
- `:latest` is the regular image, built from the repository-root `Dockerfile`:
  a three-stage build (frontend on Node 24 with pnpm 10.32.1, Go 1.26 with CGO
  and libvips, then a Debian trixie runtime carrying Jellyfin's FFmpeg).
- `:aio` is the omnibus image. `omnibus/Dockerfile` starts `FROM ${SILO_IMAGE}`
  and adds PostgreSQL, optional Redis and Meilisearch, and s6-overlay, so it
  layers on top of the regular image rather than replacing it.

## Build your own image

From the repository root:

```sh
docker build -t ghcr.io/heylucifurr/silo-server:weflix .
```

For a Synology, build for the NAS architecture. Building on an Apple Silicon or
ARM machine needs an explicit platform, or the image will not start there:

```sh
docker buildx build --platform linux/amd64 \
  -t ghcr.io/heylucifurr/silo-server:weflix --push .
```

Push it to your fork's package namespace. Use a personal access token with
`write:packages`; keep it in your shell, never in the repository:

```sh
echo "$GHCR_TOKEN" | docker login ghcr.io -u heylucifurr --password-stdin
docker push ghcr.io/heylucifurr/silo-server:weflix
```

Building directly on the NAS with `docker build` also works and skips the
registry, at the cost of a long build on that hardware.

### If you use the omnibus image

Build the regular image first, then layer the omnibus on top of yours:

```sh
docker buildx build --platform linux/amd64 \
  --build-arg SILO_IMAGE=ghcr.io/heylucifurr/silo-server:weflix \
  -f omnibus/Dockerfile \
  -t ghcr.io/heylucifurr/silo-server:weflix-aio --push .
```

## Point the deployment at it

Change the image reference in the compose file on the NAS:

```yaml
services:
  silo:
    image: ghcr.io/heylucifurr/silo-server:weflix   # was ghcr.io/silo-server/silo-server:latest
```

Then:

```sh
docker compose pull
docker compose up -d
```

If the stack drives its tag through `.env` (`SILO_OMNIBUS_IMAGE`, or the
`SILO_IMAGE` build argument when rebuilding the omnibus), change it there
instead so one value stays authoritative.

The database schema is unchanged by this fork, so no migration step is needed
beyond what the image already runs at startup.

## Pulling upstream updates into the fork

Keep `main` a mirror of upstream and do WeFlix work on `Development`. That
keeps every update a fast-forward and confines conflicts to the branch.

```sh
git remote add upstream https://github.com/Silo-Server/silo-server.git   # once
git fetch upstream
git checkout main
git merge --ff-only upstream/main
git push origin main
git checkout Development
git rebase main
```

The change is deliberately isolated to make that rebase small:

- New files: `web/src/themes/theme.ts`, `web/src/themes/appThemeContext.ts`,
  `web/src/themes/AppThemeProvider.tsx`, `web/src/themes/weflix.css`,
  `web/src/components/TopNav.tsx`, `web/src/components/primaryMenuLinks.tsx`.
- Edited: `web/index.html`, `web/src/main.tsx`, `web/src/themeBoot.js`,
  `web/src/App.tsx`, `web/src/components/Layout.tsx`,
  `web/src/components/AppSidebar.tsx`, `web/src/components/GlobalSearch.tsx`,
  `web/src/pages/settings/InterfaceSettings.tsx`.

Conflicts will concentrate in `Layout.tsx` and `AppSidebar.tsx`, which are the
two files upstream changes most often. Everything else is either new or a
one-line hook-up.

No backend, API, settings-contract or playback code is touched, so an upstream
server change cannot conflict with the theme.

## Licensing

Silo is AGPL-3.0-or-later. Running a modified Silo over a network obliges you to
offer the corresponding source to the people using that service. Keeping this
fork public and pushing the branch satisfies that directly; if the fork is ever
made private, publish the source or a written offer alongside the deployment.

The Silo name, logo and wordmark are trademarks of Silo Media L.L.C. and are not
covered by the AGPL — see `TRADEMARK.md`. A WeFlix-branded deployment should
present itself as its own thing rather than as an official Silo release.
