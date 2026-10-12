# Transcode node failure detection

The health sweep (`internal/nodepool/health.go`) checks every node every 30
seconds. A node that dies between sweeps still looks healthy until the next one,
and every API server keeps choosing it. This note covers how an API server notices
a dead transcode node sooner than that, and what bounds the time a call to it can
take.

## Connect bound

Every API call to a transcode node uses the shared node transport in
`internal/transcodeproxy/client.go`: transcode start, capability reads, and the
manifest and segment relays. It bounds opening a TCP connection at
`NodeDialTimeout` (3 seconds). The stop request in `internal/playback` has its own
client with the same bound, because that package cannot import `transcodeproxy`.

Only the connect is bounded. A node may take a long time to answer a start or
a manifest request it has to rebuild, and a segment streams for as long as the
viewer downloads it, so neither has a response timeout. The request context of each
call bounds it instead.

## Marking a node unreachable

A call that cannot open a connection reports the node through
`nodepool.ReportConnectFailure`. A connect failure is a dial error: refused, timed
out, no route, or a name that does not resolve. These do not count:

- an error after the connection opened;
- an HTTP error status;
- a caller that gave up;
- local socket or descriptor exhaustion (`EMFILE`, `ENFILE`, `EADDRNOTAVAIL`,
  `ENOBUFS`).

The report marks the node unhealthy in this process's pool, so the planner skips it
for new sessions and replans at once. The database row and the admin node list
keep the last sweep's result until the next sweep.

A mark must not keep a healthy node out for a whole sweep interval. The health
checker rechecks a marked node right away, then up to twice more five seconds
apart. The first successful recheck or sweep puts it back, so a node that
answers loses milliseconds of selection to one transient error. A node that
fails every recheck stays out until a sweep finds it answering.

A start that failed to connect sent nothing to the node, so the start paths skip
the stop request they otherwise send after a failed start. That request would only
wait on the dead host before the next candidate is tried.

## Other API servers

The API server that saw the failure publishes `node_unreachable` on `silo:admin`
with the node URL. Each API server that receives it marks the node in its own pool
and rechecks it from there. It does not publish the event again. A node that one
API server cannot reach but another can is marked on the second only until its
own recheck answers.
