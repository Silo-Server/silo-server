# Plugin trust model

An installed plugin is trusted code with the same access as the Silo process
that runs it. Silo does not sandbox plugins. An admin who installs a plugin
grants it that access; the defense against a plugin you don't trust is not to
install it.

## What a plugin can reach

`pluginhost.Host.Start` (`internal/pluginhost/host.go`) launches a plugin as a
native child process through go-plugin. The API server, or the
[network access](network-access.md) proxy for provider plugins, starts it with:

- **The same OS user.** The shipped image runs as root, so plugins do too.
  There is no separate user, namespace, seccomp profile, or filesystem
  confinement.
- **An allowlisted environment.** `pluginhost.ProcessEnv`
  (`internal/pluginhost/process_env.go`) passes only `PATH`, `HOME`, `TMPDIR`,
  `TZ`, the locale, the CA certificate paths, and the proxy variables, and the
  host sets go-plugin's `SkipHostEnv` so go-plugin doesn't append the rest.
  This keeps `DATABASE_URL`, `SECRET_KEY`, `REDIS_URL` and the like out of
  anything a plugin dumps or reports about its own environment. It is not a
  boundary: a process running as the same user can still read the server's
  `/proc/<pid>/environ`.
- **The same network.** A plugin can open connections to Postgres, Redis, and
  anything else the server can reach, without going through the host.
- **The same filesystem.** Anything the server's user can read, including
  configuration, mounted secrets, the media libraries, and the plugin cache
  (`SILO_PLUGIN_CACHE_DIR`), which holds every installed plugin's files.

Installation runs code too. `loadManifestFromBinary`
(`internal/plugins/installer.go`) executes a package's binary with the
`manifest` argument, and the same environment, to read its manifest before
the admin configures or enables it. Installing a package is the point of
trust, not enabling it: whatever passes the install checks runs. For a
first-party plugin ID, those checks include its
[release signature](#release-signatures) once enforcement is on, and a binary
that fails it is refused before it runs. For anything else the admin's choice
of catalog or file is the check.

Whoever can write the plugin cache can replace a plugin binary together with
the manifest checksum it is checked against. The default root,
`<tmp>/silo-plugins`, sits in the shared temp directory, so on Unix
`plugins.EnsurePrivateCacheRoot` creates it with mode 0700 and the server
refuses to start when it already exists as a symlink, belongs to another user,
or is writable by its group or other users. A root set with
`SILO_PLUGIN_CACHE_DIR` is not checked; the operator keeps it writable only by
Silo's user.

Chunked plugin uploads spool under `<tmp>/silo-uploads`, and chunked
diagnostics uploads under `<tmp>/silo-diagnostics-uploads`, both through
`internal/uploads`. The upload manager applies the same root checks before it
creates a session or sweeps orphaned sessions at startup, and refuses the
operation when they fail. Each chunk write and each completed upload reopens
the session file through the verified root and must find the file `Create`
made, so a swapped session directory or file cannot redirect uploaded bytes.
A bare plugin binary is installed from that checked handle; a zip archive is
still read by path, which only Silo's user can change while the root's parent
directory is sticky or private, as the shared temp directory is.

## Release signatures

A plugin release publishes `plugin-<os>-<arch>` binaries, `checksums.txt`, and
`plugin.sigstore.json`: one Sigstore bundle from
`actions/attest-build-provenance` holding an SLSA provenance attestation whose
subjects are every binary of the release, signed with a public-good Fulcio
certificate and logged in Rekor. A catalog entry names it with package-level
`bundle_url`, resolved against the catalog URL like `checksums_url`.
`internal/plugins/pluginsig` verifies it; `internal/plugins/signature.go`
decides the policy and what to record.

### What a verified signature proves

The downloaded binary's SHA-256 is a subject of an attestation that GitHub
Actions signed for a workflow run. The certificate names the workflow (its SAN),
the source repository and ref the run built, and the runner environment. The
bundle must carry at least one signed certificate timestamp, one transparency
log entry and one observer timestamp, and the certificate must have been valid
at that timestamp. After verification the installer reads the binary's own
manifest, and its plugin ID and version must equal the catalog's, which ties
the running code to the tag the policy named.

It does not prove the code is safe or reviewed. For first-party plugins, review
rests on the release workflow, which refuses a tag whose commit is not on
`main`, and on tag rulesets that stop a tag from being moved. The server checks
neither. Nor does a signature prove that a release is the newest one; see
accepted risks below.

### Policy by plugin ID

The plugin ID decides the policy, not the catalog that lists it:

- An ID in `officialPluginRepositories` (`internal/plugins/catalog_channels.go`)
  gets the **required** policy, whichever catalog lists it. The issuer is
  `https://token.actions.githubusercontent.com`. The SAN must equal
  `https://github.com/Silo-Server/.github/.github/workflows/plugin-release.yml@refs/heads/main`
  exactly, so a workflow pinned to a commit or run from another branch fails.
  The Source Repository URI must be the table's repository for that ID, the
  Source Repository Ref `refs/tags/v<catalog version>`, the runner environment
  `github-hosted`, and the predicate SLSA provenance v1. A request with an
  empty plugin ID or version gets this policy too, since it can't be ruled out
  as first-party.
- Any other ID gets the **advisory** policy, and only when the entry has
  `bundle_url`. It applies the same issuer, timestamp and digest checks, with
  the catalog entry's `repo_url` as Source Repository URI, ref
  `refs/tags/v<version>`, and any workflow. The outcome is recorded and never
  refuses an install.

sigstore-go skips an extension check whose expected value is empty and does
not anchor SAN regexes, so `pluginsig` refuses a policy without a repository or
ref, and repeats the comparisons on the verified certificate.

The table is the only binding between a first-party plugin ID and its
repository; nothing is pinned at first install. `Silo-Server/silo-plugins`
keeps a second copy, and its `update-catalog` workflow refuses a dispatch whose
repository doesn't match the plugin ID, so one compromised plugin repository
can't overwrite another plugin's catalog entry. Adding a first-party plugin
changes both copies and needs a server release.

### Where the check runs

Every catalog install, manual or automatic, resolves through
`CatalogService.installTargetFromPackage` and then `Installer.downloadBinary`:

1. `installTargetFromPackage` refuses a first-party entry listed as a legacy
   `archive_url` package or without `bundle_url`.
2. `downloadBinary` downloads the binary, checks its SHA-256, fetches the
   bundle (at most 256 KiB), and verifies it. Only then does
   `loadManifestFromBinary` run the binary, followed by the catalog identity
   check. A first-party binary that fails verification never runs.

These refusals apply while `SignatureCheckerOptions.Enforce` is on. It is one
switch, set where `cmd/silo` builds the `SignatureChecker` that the catalog
service and the installer share. With it off, the same checks run and are
recorded, and nothing is refused. Refusals are `PackageError`s with
admin-facing text; the cause goes to the server log.

Uploads (binary or zip) and direct `archive_url` installs carry no signature
and are recorded `unsigned`. They are the admin's own decision, including
replacing a first-party installation. A restore downloads nothing.

### What is recorded

`plugin_installations.signature_state` is `verified`, `unsigned`, `invalid`, or
`unknown`. `unknown` covers rows from before the check and checks that could
not run (trust root or bundle unreachable) while enforcement is off. A verified
row also stores `signature_source_repo` and `signature_verified_at`. Replacing
an installation records the new package's result, so an upload over a verified
install reads `unsigned`. The bundle is not stored. Verification happens once,
on the node that runs the install. Plugin loads, and the other nodes that
extract the stored archive, keep checking only the archive's SHA-256.

### Trust root

`pluginsig.TUFTrustRoot` loads Sigstore's public-good trusted root through
sigstore-go's TUF client. TUF starts from the cached `root.json` when it is
at least as new as the one embedded in sigstore-go, and from the embedded one
otherwise. go-tuf writes the root it starts from over the cached one, so
starting from the embedded root, which has expired, would discard every
verified rotation and need the mirror on every load. A cached root still goes
through TUF's checks: go-tuf requires it to be self-signed, stored it only
after verifying it against the root before it, refuses it once expired, and
walks newer versions from it on refresh. Its cache is `<plugin cache root>/@sigstore-tuf`, created 0700 and
checked like the cache root. No plugin directory can take that name, because
`sanitizeFilesystemSegment` never leaves an `@`. Fetches go through the netguard
transport with public addresses only, and stop when the verification's
context ends. Nothing is fetched until the first
verification, so a server without network access still starts, and each node
keeps its own cache. A cache older than a day is refreshed. When that refresh
fails, unexpired cached metadata is used, and for the next ten minutes
verifications use it without contacting the mirror. A stalled mirror would
otherwise cost each plugin of an automatic update a 30-second fetch timeout. A
canceled verification doesn't count as a failed refresh. With the mirror
unreachable and the metadata expired or missing, verification fails closed,
inside those ten minutes too. No bundled
`trusted_root.json` stands in.

### Accepted risks

- **Catalog freeze.** The catalog index is not signed. Whoever controls it can
  stop announcing new releases, so servers keep an old one.
- **Older signed release on fresh installs.** Automatic updates never
  downgrade, but a fresh install, including a default plugin's automatic
  install, takes whichever signed version the catalog lists.

## What RuntimeHost scoping is for

`RuntimeHost.v1` scopes what the host hands a plugin: catalog reads return
public-safe rows, and instance state is encrypted and keyed to one
installation. These rules keep a correctly written plugin from receiving data
it has no use for, and they keep the plugin API stable. They are not a
security boundary against the plugin itself, which can reach the same data
directly.

Keep validating what plugins send the host. Plugins relay data from remote
services and can have bugs, and the host must not trust that data any more
than it would trust the remote service.

## Consequences

- Security reports that start from "a malicious plugin is installed" describe
  this model, not a vulnerability. Reports remain in scope when someone who is
  not an admin can install or run plugin code, or can use an honest plugin to
  reach data or actions their own account isn't allowed, for example through
  the routes or content a plugin serves.
- Settings a plugin needs belong in its config schema. The host passes no
  other server environment variables.
- Real isolation would mean a separate user and network namespace per plugin,
  filesystem confinement, and a different way to run local tools. That is a
  redesign of the plugin runtime and is not planned.
