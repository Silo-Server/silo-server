# Release versioning

Silo uses GitHub Releases as the public record of shipped versions and their
changes. The releases before 1.0 are betas, `v1.0.0-beta.1`, `v1.0.0-beta.2`,
and so on, then release candidates from `v1.0.0-rc.1`, until `v1.0.0`. The
clients name their tester builds the same way. `0.x` releases such as `v0.24.0`
are also accepted.

## Version format

Release tags follow [Semantic Versioning](https://semver.org/) with a leading
`v`. The Release workflow accepts three forms:

- Release: `vMAJOR.MINOR.PATCH`, for example `v0.24.0` or `v1.0.0`
- Beta: `vMAJOR.MINOR.PATCH-beta.N`, for example `v1.0.0-beta.1`
- Release candidate: `vMAJOR.MINOR.PATCH-rc.N`, for example `v1.0.0-rc.1`

Betas and release candidates are published as GitHub prereleases. Within one
`MAJOR.MINOR.PATCH`, every beta sorts before every release candidate, and both
sort before the release. `beta.10` sorts after `beta.9`.

For releases below `v1.0.0`, a minor release may include breaking changes. Every
release note must call out upgrade, configuration, and compatibility impact when
applicable.

## Backups and rollback

Administrators take a backup before every release upgrade and verify that it can
be restored. This applies to every upgrade, not only to major versions.

Silo also backs up the database itself before it applies migrations at startup,
unless backups are turned off (see [before upgrades](architecture/backups.md#before-upgrades)).
That copy holds the database only, keeps the two newest, and sits at the backup
destination, which by default is this server's own storage. It does not replace
the administrator's verified backup, which should also include the files,
configuration, and `SECRET_KEY`, and be kept off the server.

Silo does not guarantee downgrade or in-place rollback compatibility between
releases. A newer release may write or migrate data that an older release cannot
read, and there is no supported in-place database downgrade. Recovery from a
failed upgrade means stopping the new version, restoring the verified
pre-upgrade backup and its matching configuration, and redeploying the previous
release. That restore discards any change made after the backup was taken.

Release notes are authoritative for version-specific compatibility. They state
the supported upgrade path, required intermediate releases, and any database or
component combination a particular release certifies. Treat a combination the
release notes do not certify as unsupported.

## Source of truth

The Git tag and matching GitHub Release are the authoritative product version.
Other version-like values in the repository describe dependencies, protocols,
or compatibility targets and are not Silo release numbers. The build details in
the admin interface and `server_version` show the release version for a
release image (for example `0.24.0`), and the commit SHA for every other build.

GitHub automatically supplies source archives for each release.

## Container tags

There is one published image, `ghcr.io/silo-server/silo-server`. It bundles
PostgreSQL, Redis, and Meilisearch; see [Omnibus image](architecture/omnibus-image.md).

| Tag | Moves on | Meaning |
| --- | --- | --- |
| `dev` | every successful `main` build | The newest build. Follow it to get every change as it merges. |
| `build-N` | never | One `main` build. A larger number is newer; gaps from unsuccessful runs are expected. |
| Full commit SHA | never | The exact source of one `main` build. |
| `latest` | each release | The newest release: `0.x` releases, the `v1.0.0` betas and release candidates, then `v1.0.0` and later releases. Until the first release it follows every `main` build. |
| `X.Y.Z`, `X.Y.Z-beta.N`, `X.Y.Z-rc.N` | never | One release, for example `0.24.0`, `1.0.0-beta.1` or `1.0.0-rc.1`. |
| `X.Y` | each release in that line | The newest `X.Y.Z` release, for example `0.24`. Betas and release candidates never move it. |
| `X` | each release in that major version, from 1 on | The newest `X.Y.Z` release, for example `1`. There is no `0` tag. |

A release image is the image CI built and tested for that commit, plus one file,
`/usr/share/silo/version`, from which the server reads its version. The file
stays in the image, so a macOS transcode node binary copied out of a release
image reports its revision, not the version. Betas and release candidates other
than `v1.0.0`'s, such as `v0.25.0-rc.1` or `v1.1.0-beta.1`, get only their own tag.

Build numbers are not release versions and do not carry compatibility or
support guarantees. Pin a release tag, `build-N`, commit-SHA tag, or image
digest when a deployment must not move with `latest` or `dev`.

## Release notes

GitHub generates the initial notes from merged pull requests and contributors.
The categories in [`.github/release.yml`](../.github/release.yml) group features,
fixes, documentation, dependency updates, and uncategorized changes. Maintainers
should review the generated text and add any important upgrade guidance before
announcing a release.

## Publishing a release

1. Wait until CI has passed on the `main` commit you want to release and the
   **Docker Image** workflow has published it. The release reuses that image.
2. Open **Actions > Release > Run workflow** from the `main` branch and enter the
   version, for example `1.0.0-beta.1` or `1.0.0-rc.1`. A version with `-beta.N`
   or `-rc.N` is a prerelease.
3. Edit the draft release. Add upgrade, configuration, and compatibility notes.
   The first release has no earlier tag, so its notes are written by hand.
4. Publish the draft. **Release published** then tags the commit's image with the
   version and the tags above, including `latest`. `latest` never moves to an
   older commit: if a `main` build after the release commit already holds it
   (possible only before the first release), it stays there until the next
   release.

Release refuses an invalid version, a version that already exists or is older
than the newest release, a run from a branch other than `main`, a run while
another draft release exists, a commit with no new commits since the previous
release, and a commit that CI hasn't passed or whose image hasn't been
published. Generated notes start at the previous
release for a beta or release candidate, and at the previous stable release
otherwise, so `v1.0.0` covers everything since the last `0.x` release, or is
written by hand when there is none. If
tagging the image fails, run **Release published** again with the release's tag.
A re-run never rebuilds a version that is already published: it points the
tags at the existing version image.

GitHub's manual-workflow permission is the release access boundary: only users
with repository write access can dispatch it. If Silo later requires a second
approval, maintainers should configure a protected release environment with
required reviewers and attach the release job to it before use.
