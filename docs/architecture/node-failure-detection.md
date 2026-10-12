# Transcode node failure detection

The health sweep (`internal/nodepool/health.go`) checks every node every 30
seconds. A node that dies between sweeps still looks healthy until the next one,
and every API server keeps choosing it. This note covers how an API server notices
a dead transcode node sooner than that, and what bounds the time a call to it can
take.

## Connect bound

Playback's calls to a transcode node use the shared node transport in
`internal/transcodeproxy/client.go`: transcode start, capability reads, and the
manifest and segment relays, on both the native and the Jellyfin-compatible
paths. The stop request in `internal/playback` has its own client. Both bound
opening a TCP connection, name resolution included, at
`nodepool.NodeDialTimeout` (3 seconds).

Other node calls, such as trickplay and chapter-thumbnail extraction, media
sampling, download preparation, and the admin node actions, still build their
own clients with Go's 30-second default and do not report failures.

Only the connect is bounded. A node may take a long time to answer a start or
a manifest request it has to rebuild, and a segment streams for as long as the
viewer downloads it, so neither has a response timeout. The request context of each
call bounds it instead.

## Marking a node unreachable

A call that cannot open a connection reports the node through
`nodepool.ReportConnectFailure`. A connect failure is a dial error: refused, timed
out, or no route. These do not count:

- an error after the connection opened;
- an HTTP error status;
- a caller that gave up;
- local socket or descriptor exhaustion (`EMFILE`, `ENFILE`, `EADDRNOTAVAIL`,
  `ENOBUFS`);
- a name that fails to resolve, since the lookup runs against this server's
  resolver and a resolver problem would otherwise mark every node.

The report marks the node unhealthy in this process's pool, so the planner skips it
for new sessions and replans at once. The database row and the admin node list
keep the last sweep's result until the next sweep.

A mark must not keep a healthy node out for a whole sweep interval. The health
checker rechecks a marked node right away, then up to twice more five seconds
apart. The first successful recheck or sweep puts it back, so a node that
answers loses milliseconds of selection to one transient error. A node that
fails every recheck stays out until a sweep finds it answering.

A start that failed to connect, a name lookup included, sent nothing to the node,
so the start paths skip the stop request they otherwise send after a failed start.
The same goes for a Jellyfin-compatible start whose first manifest fetch cannot
connect. That request would only wait on the dead host before the next candidate
is tried.

## Other API servers

The API server that saw the failure publishes `node_unreachable` on `silo:admin`
with the node URL. Each API server that receives it marks the node in its own pool
and rechecks it from there. It does not publish the event again. A node that one
API server cannot reach but another can is marked on the second only until its
own recheck answers.

The bus delivers an API server's own announcement back to it. An API server
ignores a report about a node it marked itself during the recheck window
(15 seconds), because its own recheck is newer evidence.

## Known gaps

- Reloading the node pool (`node_pool_changed`) replaces the pool with the
  database rows, which still carry the last sweep's result. A marked node comes
  back until a call fails on it again.
- A sweep that checked a node just before it died can publish it healthy after a
  failed call marked it. The next failed call marks it again.
- A node that is alive but too loaded to accept a connection within the bound is
  treated as dead until it answers a recheck.
