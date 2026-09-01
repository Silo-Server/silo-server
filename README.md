<p align="center">
  <img src="assets/icon.png" alt="Silo logo" width="112" height="112">
</p>

<h1 align="center">Silo</h1>

<p align="center">
  A self-hosted media server for films, series, audiobooks, ebooks, podcasts, and manga.
</p>

<p align="center">
  <a href="https://github.com/Silo-Server/silo-server/releases"><img alt="Latest GitHub release" src="https://img.shields.io/github/v/release/Silo-Server/silo-server?include_prereleases&amp;sort=semver&amp;display_name=tag&amp;style=flat-square&amp;label=release"></a>
  <a href="https://github.com/orgs/Silo-Server/packages/container/package/silo-server"><img alt="Container image on GHCR" src="https://img.shields.io/badge/container-GHCR-2496ED?style=flat-square&amp;logo=docker&amp;logoColor=white"></a>
  <a href="https://github.com/Silo-Server/silo-server/actions/workflows/ci.yml"><img alt="Continuous integration" src="https://img.shields.io/github/actions/workflow/status/Silo-Server/silo-server/ci.yml?branch=main&amp;style=flat-square&amp;label=CI"></a>
  <img alt="Go 1.26" src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&amp;logo=go&amp;logoColor=white">
  <img alt="React 19" src="https://img.shields.io/badge/React-19-149ECA?style=flat-square&amp;logo=react&amp;logoColor=white">
  <a href="LICENSE"><img alt="AGPL-3.0-or-later license" src="https://img.shields.io/badge/license-AGPL--3.0--or--later-555555?style=flat-square"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a>
  · <a href="docs/wiki/index.md">Documentation</a>
  · <a href="docs/release-versioning.md">Builds &amp; releases</a>
  · <a href="https://discord.gg/siloserver">Discord</a>
  · <a href="#supporting-silo">Support Silo</a>
  · <a href="CONTRIBUTING.md">Contributing</a>
</p>

---

> [!WARNING]
> Silo is pre-release. APIs, configuration, and database migrations may change
> before the first stable release. Back up your deployment before updating.

## What Silo does

<table>
  <tr>
    <td width="33%" valign="top">
      <strong>Play</strong><br><br>
      Direct play when possible, remux when needed, transcode otherwise, with
      VA-API, Quick Sync, and NVENC hardware acceleration.
    </td>
    <td width="33%" valign="top">
      <strong>Organize</strong><br><br>
      One catalog for films, series, audiobooks, ebooks, podcasts, and manga,
      matched through metadata plugins such as TMDB and TVDB.
    </td>
    <td width="33%" valign="top">
      <strong>Connect</strong><br><br>
      Use the included web app, or the Jellyfin/Emby-compatible API with clients
      such as <a href="https://vidhub.okaapps.com/what-does-vidhub-do/">VidHub</a>,
      <a href="https://github.com/jarnedemeulemeester/findroid">Findroid</a>, and
      <a href="https://firecore.com/infuse">Infuse</a>. Client coverage varies.
    </td>
  </tr>
  <tr>
    <td width="33%" valign="top">
      <strong>Share</strong><br><br>
      Household profiles with their own watch state, library access, and
      parental controls.
    </td>
    <td width="33%" valign="top">
      <strong>Manage</strong><br><br>
      Libraries, users, providers, storage, search, and playback are configured
      in the admin interface, not in config files.
    </td>
    <td width="33%" valign="top">
      <strong>Scale</strong><br><br>
      Start with one integrated server; split proxy and transcode roles across
      shared PostgreSQL and Redis when you need to.
    </td>
  </tr>
</table>

## Quick start

Requires Docker Compose 2.24 or newer. The default stack runs Silo, PostgreSQL
with pgvector, and Redis.

```sh
git clone https://github.com/Silo-Server/silo-server.git
cd silo-server
cp .env.example .env
chmod 600 .env
printf '\nPOSTGRES_PASSWORD=%s\nSECRET_KEY=%s\n' \
  "$(openssl rand -hex 24)" "$(openssl rand -base64 48)" >> .env
```

Set `MEDIA_ROOT` in `.env` to the absolute path of your media, then:

```sh
docker compose up -d
```

Open <http://localhost:8090> and complete onboarding.

The [Docker deployment guide](docs/wiki/deployment/docker.md) covers the
`SECRET_KEY` backup requirement, storage paths, GPU acceleration, Meilisearch,
external PostgreSQL and Redis, distributed roles, PostgreSQL tuning, backups,
and updates. Migrating from Continuum? Use the
[cutover guide](docs/continuum-to-silo-docker-migration.md).

## Builds and releases

Until the first release, default-branch images carry an ordered `build-N` tag
and a short commit SHA alongside `latest`. Build numbers order published images;
they are not release versions. [Release versioning](docs/release-versioning.md)
defines each tag and the SemVer contract.

## Documentation

- [Documentation index](docs/wiki/index.md) — user and operator guides
- [Development guide](DEVELOPMENT.md) — source setup, builds, tests, migrations
- [Settings API](docs/settings-api.md) and [Downloads API](docs/downloads-api.md) — client contracts

## Community and contributions

Questions and discussion: [Discord](https://discord.gg/siloserver).
Bugs, install problems, and performance issues: the
[GitHub issue forms](https://github.com/Silo-Server/silo-server/issues/new/choose),
which ask for reproduction steps and raw logs.

Native clients are developed in
[`silo-apple`](https://github.com/Silo-Server/silo-apple) and
[`silo-android`](https://github.com/Silo-Server/silo-android). Client-visible
API, authentication, playback, or metadata changes should consider both.

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Features,
API changes, migrations, and behavior changes should start as an issue.

## Supporting Silo

Silo is developed in spare time and funded out of pocket, and will stay free and
open source. [GitHub Sponsors](https://github.com/sponsors/quick104) covers AI
development tooling (Claude, Codex), push-notification relay infrastructure, and
future project costs. Bug reports, code, and documentation help just as much.

## License and trademarks

Silo's source code is licensed under the
**GNU Affero General Public License v3.0 or later** (`AGPL-3.0-or-later`). See
[LICENSE](LICENSE).

The **Silo name, logo, and wordmark are trademarks of Silo Media L.L.C.** and
are not covered by the AGPL. Forks and redistributions may use the code but must
not use the Silo brand as their identity and must remove or replace the brand
assets. Publishing a Silo-branded app to an app store requires written
permission. See [TRADEMARK.md](TRADEMARK.md) for permitted referential use,
including phrases such as "compatible with Silo."
