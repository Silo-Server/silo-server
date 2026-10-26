# API server shutdown

An API process that is told to stop fails readiness first and keeps serving
for a short while, so load balancers and Kubernetes route new requests to
another replica before its listeners close. Requests already in flight finish
under the existing graceful shutdown.

## Sequence

On SIGTERM or SIGINT, `cmd/silo/main.go`:

1. Marks the process as shutting down. `GET /api/v1/ready` answers `503`
   with `status` `shutting_down` (see the retained probe contract in
   [api-contract.md](api-contract.md)) at once: it reports the last dependency
   probe instead of waiting on a new one. `GET /api/v1/health`, the liveness
   probe, keeps answering `200`. The LAN discovery advertisement is
   withdrawn.
2. Keeps serving every request, and keeps its background work running, for
   `SILO_SHUTDOWN_DELAY` (a Go duration or whole seconds, default `5s`; `0`
   skips the wait). A second signal ends the wait early.
3. Cancels the application context and runs the existing shutdown: resident
   plugins stop, the HTTP servers stop accepting connections and wait up to
   30 seconds for in-flight requests, then playback cleanup, stale session
   cleanup and heartbeat removal run.

A restart request stops at once, as before, and ends the wait if it arrives
during it: an administrator's restart has already told players the server is
restarting, and the loss of storage admission sends one after it has stopped
storage writers. A listener that fails on its own also stops the process at
once, during the wait or not.

Only the native API listener's readiness changes. The Jellyfin-compatible and
Audiobookshelf-compatible listeners have no readiness probe; a load balancer
in front of them keeps routing to them until their listeners close.

## Deployment

- **Docker Compose** gives a container 10 seconds before SIGKILL. The example
  `silo` service sets `stop_grace_period: 2m`, enough for the delay and the
  shutdown after it. The development compose file and `make dev-backend` set
  `SILO_SHUTDOWN_DELAY=0`.
- **Kubernetes:** point the readiness probe at `GET /api/v1/ready` and the
  liveness probe at `GET /api/v1/health`. Kubernetes already removes a
  terminating pod from Service endpoints; the delay covers the time that
  takes to reach kube-proxy and ingress controllers. Set
  `terminationGracePeriodSeconds` to at least `SILO_SHUTDOWN_DELAY` plus 90
  seconds. A load balancer that only polls readiness needs a delay of at least
  its probe interval times its failure threshold.
- A single server behind no load balancer gains nothing from the delay; set
  `SILO_SHUTDOWN_DELAY=0` to stop at once.
