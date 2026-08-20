# Release versioning

Silo uses GitHub Releases as the public record of shipped versions and their
changes. There is no historical Silo release series yet: the repository has no
release tags. Newly published default-branch container images are identified by
an ordered `build-N`, `latest`, and a short commit SHA.

## Version format

Release tags follow [Semantic Versioning](https://semver.org/) with a leading
`v`:

- Stable release: `vMAJOR.MINOR.PATCH`, for example `v1.2.0`
- Prerelease: `vMAJOR.MINOR.PATCH-SUFFIX`, for example `v0.1.0-alpha.1`
- Build metadata: append `+IDENTIFIER`, for example `v1.2.0+build.7` or
  `v0.1.0-alpha.1+build.7`

For releases below `v1.0.0`, a minor release may include breaking changes. Every
release note must call out upgrade, configuration, and compatibility impact when
applicable.

## Source of truth

The Git tag and matching GitHub Release are the authoritative product version.
Other version-like values in the repository describe dependencies, protocols,
or compatibility targets and are not Silo release numbers. The build details in
the admin interface show both values for numbered container builds and retain
the commit SHA alone for older or local builds.

GitHub automatically supplies source archives for each release. Container
publishing remains independent. Successful default-branch builds publish three
tags that identify the same multi-platform image:

| Tag | Meaning |
| --- | --- |
| `build-N` | Ordered build identifier. A larger number is a newer published build; gaps from unsuccessful or non-publishing workflow runs are expected. |
| `latest` | Mutable pointer updated by successful default-branch publications. |
| Short commit SHA | Exact source identity for the build. |

Build numbers are not release versions and do not carry compatibility or
support guarantees. Pin a `build-N`, commit-SHA tag, or image digest when a
deployment must not move with `latest`. Versioned container tags are not
introduced by the GitHub Release process.

## Release notes

GitHub generates the initial notes from merged pull requests and contributors.
The categories in [`.github/release.yml`](../.github/release.yml) group features,
fixes, documentation, dependency updates, and uncategorized changes. Maintainers
should review the generated text and add any important upgrade guidance before
announcing a release.

## Publishing a release

1. Confirm the intended commit is on `main` and required checks are green.
2. Open **Actions > Release > Run workflow** from the `main` branch.
3. Enter a new SemVer tag and select whether it is a prerelease.
4. Review the generated draft and add any required upgrade notes.
5. Publish and announce the release only after its tag, target commit, and notes
   have been verified.

The workflow rejects non-SemVer tags, mismatched prerelease settings, and runs
from branches other than `main`. It also refuses to reuse a release tag or create
a release when there are no commits since the previous one.

GitHub's manual-workflow permission is the release access boundary: only users
with repository write access can dispatch it. If Silo later requires a second
approval, maintainers should configure a protected release environment with
required reviewers and attach the release job to it before use.

This process does not choose or create Silo's first version. The first tag must
be agreed by the maintainers before the workflow is run.
