# silo-plugin-sdk

Public Go SDK for building Silo plugins. **Not a runtime plugin** — this is a library that plugin authors depend on via `go.mod`.

`silo-plugin-sdk` is the source of truth for the plugin authoring contract. First-party consumers (Silo host, `silo-plugin-tmdb`, `silo-plugin-metadb`, every other plugin in this repo) pin tagged semver releases. Local multi-repo workspaces may use `go.work` or a temporary `replace`, but CI and release builds resolve the SDK from a published module tag.

## Packages

- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1` — generated protobuf code.
- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability` — stable capability type constants for manifests and peer discovery.
- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/config` — config-schema helpers.
- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/convert` — type conversions.
- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest` — manifest loading/rendering.
- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime` — `manifest` subcommand + `Runtime` server scaffolding.
- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtimedefault` — default `Runtime` implementation with `BindHostBroker` already wired; embed it to skip boilerplate.
- `github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtimehost` — typed client for the host's `RuntimeHost` service, including event publishing, host info, catalog browsing, installed-plugin discovery, scoped streams, plugin-to-plugin HTTP calls, and plugin-owned config writes.

## Capability families

The SDK ships protobuf contracts for every capability the host understands:

- `metadata_provider.v1`
- `marker_provider.v1`
- `media_analyzer.v1`
- `scheduled_task.v1`
- `event_consumer.v1`
- `auth_provider.v1`
- `http_routes.v1`
- `request_router.v1`
- `scan_source.v1`
- `audiobook_backend.v1`
- `ebook_backend.v1`

Plugins implement one or more, advertise them in `manifest.json`, and serve them over gRPC.

## Author workflow

A typical plugin:

1. Defines a `manifest.json` using the protobuf-derived schema.
2. Exposes a `Runtime` gRPC server plus one or more capability servers.
3. Supports the `manifest` subcommand via `pkg/pluginsdk/runtime` so the host can introspect manifests without launching the plugin.
4. Is installed either from a catalog or by uploading a trusted binary to a Silo server.

For a minimal self-describing plugin, see [`examples/hello-scheduled-task`](examples/hello-scheduled-task). For a plugin that calls back into the host via `RuntimeHost` (publishing events, listing libraries), see [`examples/hello-runtime-host`](examples/hello-runtime-host).

## Calling back into the host

Plugins talk to the host through the `RuntimeHost` service, accessed via `pkg/pluginsdk/runtimehost.Client`. The host invokes `Runtime.BindHostBroker` on startup so plugins can dial back over the shared broker; `runtimedefault` handles that step for you. Available RPCs:

- `PublishEvent` / `PublishEventTo` / `PublishEventToInstallation` — fire events into the host's bus, broadcast, addressed to a stable `plugin_id`, or addressed to one specific installation.
- `GetHostInfo` — read host URL metadata for callback URLs and external-facing plugin links.
- `ListLibraries` — enumerate libraries the operator has configured.
- `CheckMediaPresence` — ask whether a given external id is already in the catalog.
- `ListInstalledPlugins` — discover sibling plugins (e.g. routers a request plugin can target).
- `ListLibraryMedia` / `GetCatalogStats` — read public-safe catalog rows and aggregate counts.
- `ResolveCatalogImageURLs` — resolve stored poster/backdrop image paths into host-generated browser URLs.
- `MintScopedStream` — create short-lived, narrowly scoped stream grants for guest/public workflows.
- `CallPluginHTTP` — invoke another installed plugin's `http_routes.v1` handler through the host control plane.
- `SetGlobalConfigEntry` — persist plugin-owned config that admins didn't set via the manifest form.

For plugin-to-plugin JSON calls, prefer the helper layer:

```go
plugins, err := host.ListInstalledPluginsByCapability(ctx, capability.RequestRouter)
if err != nil || len(plugins) == 0 {
    return err
}

var out struct {
    Accepted bool `json:"accepted"`
}
err = host.CallPluginJSON(ctx, runtimehost.CallPluginJSONRequest{
    InstallationID: int(plugins[0].GetInstallationId()),
    Path:           "/api/request",
    Request:        map[string]any{"title": "The Matrix"},
    Response:       &out,
})
```

The `auth_provider.v1` capability also exposes OAuth-flow RPCs (`InitAuthorize`, `ExchangeCode`, `RefreshSession`) for plugins that wrap external identity providers.

## Scan sources

The `scan_source.v1` capability is for Autoscan providers. The host owns the
poll timer, marker persistence, path rewrites, validation, dedupe, and scan
enqueueing. The plugin only polls its upstream provider and returns changed
absolute paths in that provider's source namespace. The host applies autoscan
source rewrite rules before enqueueing scans.

The host resolves the configured upstream connection and passes it to
`PollChanges` for each poll. Plugins should treat request values such as API
keys as transient secrets and avoid logging them without redaction.

## Self-describing binaries

Direct binary upload works best when the plugin embeds a manifest template and computes its own executable checksum at runtime before returning `Runtime.GetManifest`. That keeps the plugin installable without requiring a checked-out Silo repository or a sibling `manifest.json` file at upload time. The example plugin shows the pattern.

## Compatibility

Compatibility and versioning expectations are documented in [`docs/compatibility.md`](docs/compatibility.md).

## Releases

SDK releases are cut from semver tags such as `v0.1.0` and published through GitHub Actions.

- Additive public API changes belong in a new minor release.
- Compatible fixes and documentation updates belong in a patch release.
- Breaking public API, protobuf, or manifest contract changes require a new major version.

Before downstream repos stop using local workspace overrides, the required SDK commit must be pushed and tagged here first.

## Build & test

```bash
make proto       # regenerate protobuf code (uses locally vendored buf under ./bin/)
go test ./...
```

## License

Apache-2.0. See [LICENSE](LICENSE).
