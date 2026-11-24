# Silo server docs

**Running Silo?** Guides for installing, configuring, and troubleshooting a
server are in the [user manual](https://siloserver.org/docs). Its source is
[Silo-Server/siloserver.org](https://github.com/Silo-Server/siloserver.org);
send documentation fixes there.

This folder is for people and coding agents changing the server.

## API

Start with [the native API contract](architecture/api-contract.md) (versioning,
errors, paging, capabilities) and [the interactive OpenAPI viewer](api-docs.md)
every server serves. The `*-api.md` files describe each area for client and
integration developers.

Client apps:

- [auth-api.md](auth-api.md): sign-in, sessions, passwords, profile PINs, and device sign-in.
- [catalog-api.md](catalog-api.md): browse, search, item detail, and sorting.
- [collections-api.md](collections-api.md): personal collections.
- [images-api.md](images-api.md): artwork sizes and image URLs.
- [playback-api.md](playback-api.md): playback negotiation and streams.
- [progress-api.md](progress-api.md): progress bootstrap snapshots.
- [downloads-api.md](downloads-api.md): downloads and offline sync.
- [subtitles-api.md](subtitles-api.md): subtitle search, upload, sync, and AI.
- [markers-api.md](markers-api.md): intro, credits, and other markers.
- [ebooks-api.md](ebooks-api.md): ebook reader progress.
- [settings-api.md](settings-api.md): user preferences from the settings contract.
- [notifications-api.md](notifications-api.md): the notification inbox.
- [notifications-push-api.md](notifications-push-api.md): APNs, FCM, and web push.
- [realtime-api.md](realtime-api.md): event subscriptions and the socket handshake.
- [invitations-api.md](invitations-api.md): invitations and signup.
- [diagnostics-upload-api.md](diagnostics-upload-api.md): client diagnostics upload.
- [operational-api.md](operational-api.md): connect info and image capabilities.

Administration:

- [admin-api.md](admin-api.md): `/api/v2/admin` operations not covered below.
- [admin-users-api.md](admin-users-api.md): accounts and access groups.
- [admin-settings-api.md](admin-settings-api.md): server settings.
- [admin-catalog-api.md](admin-catalog-api.md): catalog administration.
- [admin-recommendations-api.md](admin-recommendations-api.md): recommendation administration.
- [admin-plugin-catalog-api.md](admin-plugin-catalog-api.md): plugin catalog settings.
- [admin-tasks-api.md](admin-tasks-api.md): scheduled tasks and jobs.
- [admin-diagnostics-api.md](admin-diagnostics-api.md): diagnostic report downloads.
- [libraries-api.md](libraries-api.md): library diagnostics and scoped discovery.
- [backups-api.md](backups-api.md): backups and profile export and import; full
  restore is `silo backup restore` ([architecture/backups.md](architecture/backups.md)).
- [api-keys-api.md](api-keys-api.md): API keys for scripts and integrations.

Integrations:

- [scan-api.md](scan-api.md): on-demand scans for Sonarr, Radarr, and Autoscan.
- [autoscan-delivery-api.md](autoscan-delivery-api.md): the autoscan webhook receiver.
- [network-access-api.md](network-access-api.md): overlay network providers.
- [direct-connections-api.md](direct-connections-api.md): direct connection status.

Jellyfin compatibility:

- [jellycompat-api.md](jellycompat-api.md): the supported Jellyfin protocol subset.

Operations and nodes:

- [transcode-node-api.md](transcode-node-api.md): the API server to transcode node contract.

Client protocol schemas:

- [design/schemas/playback-v3/](design/schemas/playback-v3/README.md): playback protocol v3.
- [design/schemas/client-diagnostics/](design/schemas/client-diagnostics/README.md): client diagnostics reports.

## Other docs

- [`architecture/`](architecture/) records how each subsystem works, its
  invariants, and the contracts it keeps.
  [v1-scope.md](architecture/v1-scope.md) records the 1.0 capability scope and
  release history. [i18n.md](architecture/i18n.md) covers interface translation
  across the clients.
- [`design/`](design/) holds design notes and schemas for client-facing
  protocols.
- [non-goals.md](non-goals.md), [ai-contributions.md](ai-contributions.md), and
  [release-versioning.md](release-versioning.md) are project policy.
- [update-to-1.0.md](update-to-1.0.md) is the draft procedure for updating an
  alpha server to 1.0. It moves to the manual when 1.0 ships.

[DEVELOPMENT.md](../DEVELOPMENT.md) covers building and testing, and
[CONTRIBUTING.md](../CONTRIBUTING.md) covers pull requests.
