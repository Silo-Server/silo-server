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

A request can also go out on a pooled connection that was opened before the node
died, where the connect bound never applies. On Linux the same dialer sets
`TCP_USER_TIMEOUT` to `nodepool.NodeUnacknowledgedTimeout` (10 seconds): data sent
to the node that goes unacknowledged that long fails the connection with
`ETIMEDOUT`. Other platforms have no such bound.

Neither bound is a response timeout. A node may take a long time to answer a start
or a manifest request it has to rebuild, and a segment streams for as long as the
viewer downloads it; a live node acknowledges the request at once, and the API
sends nothing that needs acknowledging while it receives. The request context of
each call bounds the wait for an answer.

## Marking a node unreachable

A call that cannot reach a node reports it through
`nodepool.ReportConnectFailure`. That covers a dial error (refused, timed out, or no
route) and an open connection that failed with `ETIMEDOUT` because the node stopped
acknowledging. These do not count:

- an error after the connection opened;
- an HTTP error status;
- a caller that gave up;
- local socket or descriptor exhaustion (`EMFILE`, `ENFILE`, `EADDRNOTAVAIL`,
  `ENOBUFS`);
- a name that fails to resolve, since the lookup runs against this server's
  resolver and a resolver problem would otherwise mark every node.

The report marks the node unhealthy in this process's pool, so the planner skips it
for new sessions and replans at once. The database row and the admin node list
keep the last sweep's result until the next sweep. The node configuration
reconciliation rebuilds the pool from those rows every few seconds, so the pool
keeps its own set of marked URLs and applies it to every rebuild; a successful
health result for the node, or the node leaving the pool, clears its entry.

A mark must not keep a healthy node out for a whole sweep interval. The health
checker rechecks a marked node right away, then up to twice more five seconds
apart. The first successful recheck or sweep puts it back, so a node that
answers loses milliseconds of selection to one transient error. A recheck holds
the node's check lock, like the sweep and the check a drain announcement starts,
so it cannot overwrite a newer result. A successful recheck is published and
stored like a sweep result, drain state included: the reconciliation cannot
restore an older failed sweep over it, and a node that comes back draining takes
no new work. A node that fails every recheck stays out until a sweep finds it
answering.

After a failed start, or a Jellyfin-compatible start whose first manifest fetch
fails, the start paths send the node a stop request in case it started the job.
When the node could not be reached, that includes a failed name lookup, the stop
runs in the background instead, so the next candidate is tried at once and a node
that answers again by then still drops the job.

Name lookups are the one difference between the two rules: a failed lookup counts
as unreachable for the stop, but never marks the node.

## Other API servers

The API server that saw the failure publishes `node_unreachable` on `silo:admin`
with the node URL. Each API server that receives it marks the node in its own pool
and rechecks it from there. It does not publish the event again. A node that one
API server cannot reach but another can is marked on the second only until its
own recheck answers.

The event payload names the announcing process. The bus delivers an API server's
own announcement back to it, and the server ignores it: it already marked the node,
and its recheck is the newer evidence. A peer's report is always applied.

## Known gaps

- A sweep that checked a node just before it died can publish it healthy after a
  failed call marked it. The next failed call marks it again.
- A node that is alive but too loaded to accept a connection within the bound is
  treated as dead until it answers a recheck.
