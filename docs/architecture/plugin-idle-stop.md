# Stopping idle plugin processes

A plugin that is not resident starts on its first RPC. The host stops it again
once nothing has used it for 30 minutes, and the next RPC starts it as the first
one did. A server whose libraries never call a plugin
(an audiobook metadata provider on a Movies and Series server) does not keep
that process in memory.

The period is fixed (`pluginhost.IdleStopAfter`). Thirty minutes covers a burst
of calls and is no shorter than the response caches plugins keep, typically half
an hour, so a quiet spell does not throw a live cache away. Proxy nodes run only resident
plugins, so nothing there is stopped for being idle.

## What counts as use

`internal/pluginhost/idle.go` keeps, per process, the time of the last use and
the number of calls in flight:

- A lookup through `Host.Client`, which every RPC to a lazily started plugin
  makes through `Service.ensureClient`.
- A host call to the plugin on the `silo.plugin.v1` services, from the moment
  it is sent until it returns. Scheduled task runs, event deliveries, plugin
  HTTP routes, scan source polls and sign-in exchanges are all such calls. The
  idle period counts from the end of the last call.
- A plugin call back to the host (`RuntimeHost`), for the same span. A callback
  with no host call in flight is the only sign of work a plugin does on its
  own.

The health probe, go-plugin's broker, stdio and controller traffic, and status
reads (`Host.Running`, which backs the admin `runtime.state`) are not use.

## What is never stopped

- Resident plugins (`network_access_provider.v1`). The supervisor owns them and
  their RPCs do not start a process. `Service.start` asks for an idle stop only
  when the manifest is not resident; the host never decides this by itself.
- Config test runs (negative installation IDs), which their caller stops.
- A process that opened a `silo.plugin.v1` stream. Every capability RPC is
  unary today. A streaming one has to count as in flight until it closes
  before its plugin can be stopped.

## Invariants

- The idle check and the removal from the host's instance map happen under the
  lock `Host.Client` takes to record a use. A caller either finds the process
  and keeps it alive for another idle period, or finds none and starts one.
- The watcher holds the installation's start slot until the old process has
  exited, so a restart never overlaps the process it replaces. A `Start`
  already in flight wins: it cancels the watcher. `Stop` and `Shutdown` also
  wait for that exit.
- The first call after an idle stop waits for a cold start before it is sent:
  the binary is verified again, the process launches, and the host reads its
  manifest and configures it. A start that fails is recorded as any failed
  lazy start is.
- An idle stop is a deliberate stop. The client is not marked unhealthy, the
  exit handler is not called, no lazy start failure is recorded, and the
  installation reports `stopped`, which the admin UI shows as a healthy plugin
  that is not running. The process is asked to shut down and is only killed if
  it has not exited after two seconds.
- State a plugin keeps in memory does not survive an idle stop. Host-held
  state (configuration, instance state, OAuth and device-code flow state, scan
  markers, sync cursors) is sent again with each call. A plugin must not rely
  on a response cache or a background timer outliving its last call by more
  than the idle period.

A caller that resolves a capability client and waits longer than the idle
period before calling it gets a connection error, not a restart. No caller
holds a client across operations; keep it that way.
