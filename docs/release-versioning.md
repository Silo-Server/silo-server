# Release versioning

Silo uses GitHub Releases as the public record of shipped versions and their
changes. Releases start at `v0.24.0` and continue with `0.x` releases, then
`v1.0.0-rc.1`, `v1.0.0-rc.2`, and so on until `v1.0.0`.

## Version format

Release tags follow [Semantic Versioning](https://semver.org/) with a leading
`v`. The Release workflow accepts two forms:

- Release: `vMAJOR.MINOR.PATCH`, for example `v0.24.0` or `v1.0.0`
- Release candidate: `vMAJOR.MINOR.PATCH-rc.N`, for example `v1.0.0-rc.1`. It is
  published as a GitHub prerelease. `rc.10` sorts after `rc.9`.

For releases below `v1.0.0`, a minor release may include breaking changes. Every
release note must call out upgrade, configuration, and compatibility impact when
applicable.

## Backups and rollback

Administrators take a backup before every release upgrade and verify that it can
be restored. This applies to every upgrade, not only to major versions.

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
| `latest` | each release | The newest release: `0.x` releases, the `v1.0.0` release candidates, then `v1.0.0` and later releases. Until the first release it follows every `main` build. |
| `X.Y.Z`, `X.Y.Z-rc.N` | never | One release, for example `0.24.0` or `1.0.0-rc.1`. |
| `X.Y` | each release in that line | The newest `X.Y.Z` release, for example `0.24`. Release candidates never move it. |
| `X` | each release in that major version, from 1 on | The newest `X.Y.Z` release, for example `1`. There is no `0` tag. |

A release image is the image CI built and tested for that commit, plus one file,
`/usr/share/silo/version`, from which the server reads its version. Release
candidates after `v1.0.0`, such as `v1.1.0-rc.1`, get only their own tag, and a
fix to an older line, such as `0.24.1` after `0.25.0`, never moves `latest`.

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

1. Wait until the **Docker Image** workflow has published the `main` commit you
   want to release. The release reuses that image.
2. Open **Actions > Release > Run workflow** from the `main` branch and enter the
   version, for example `0.24.0` or `1.0.0-rc.1`. A version with `-rc.N` is a
   prerelease.
3. Edit the draft release. Add upgrade, configuration, and compatibility notes.
   The first release has no earlier tag, so its notes are written by hand.
4. Publish the draft. **Release published** then tags the commit's image with the
   version and the tags above, including `latest`.

Release refuses an invalid version, a version that already exists or is older
than the newest release, a run from a branch other than `main`, and a commit
whose image has not been published. Generated notes start at the previous
release for a release candidate, and at the previous non-candidate release
otherwise, so `v1.0.0` covers everything since `v0.24.0`. If tagging the image
fails, run **Release published** again with the release's tag.

GitHub's manual-workflow permission is the release access boundary: only users
with repository write access can dispatch it. If Silo later requires a second
approval, maintainers should configure a protected release environment with
required reviewers and attach the release job to it before use.
