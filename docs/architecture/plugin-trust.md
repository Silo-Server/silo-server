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
trust, not enabling it.

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
