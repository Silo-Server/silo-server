# TCP Congestion Control

`internal/tcpcongestion` requests kernel BBR and configures TCP send buffers on
Silo's client listeners. `internal/netadmin` confines the required NET_ADMIN
capability to a helper process. Kernel or helper refusals produce logs and leave
the listener serving; an address bind error still fails the bind.

This note defines the implementation contract for changes to these packages,
their startup wiring in `cmd/silo/main.go`, and the shipped Compose file.

## Listener Contract

| Listener | Log label | Policy |
|---|---|---|
| Native API in integrated, API and frontend modes, including the listener passed to `internal/lantls` | `api` | Request BBR and evaluate send-buffer forcing |
| Jellyfin compatibility | `jellyfin` | Request BBR and evaluate send-buffer forcing |
| Audiobookshelf compatibility | `abs` | Request BBR and evaluate send-buffer forcing |
| Standalone proxy mode | `proxy` | Request BBR and evaluate send-buffer forcing |
| Transcode mode, metrics and debug listeners | None | Keep the existing socket configuration |

On Linux, `Listen` sets `TCP_CONGESTION` to `bbr`, then reads it back.
A running helper performs the set; Silo performs the read. Without the helper,
Silo attempts the set itself. A refusal still triggers a readback so the log can
show the kernel's choice. The comparison stops at the first NUL byte.

When that readback confirms BBR, the first successful `Accept` reads the accepted
socket's algorithm once. A mismatch or read error produces another congestion
line at Warn. A match produces no extra line. `Accept` returns the original
`*net.TCPConn`, preserving sendfile and `CloseWrite` behavior in `internal/lantls`.
Each listener samples only its first accepted connection, which may be loopback
from the image healthcheck or a resident plugin's reverse proxy, so a route-level
override for remote clients can go unlogged.

Linux listeners call `SetMultipathTCP(false)`, including when BBR is off and no
buffer force is due. Multipath TCP (MPTCP) clients fall back to TCP; ordinary TCP
clients keep the same protocol. The helper accepts only TCP stream sockets, so
it refuses an MPTCP listener. MPTCP also fails to pass `TCP_NOTSENT_LOWAT` to
accepted TCP connections. Plain TCP keeps both the helper and the memory guard
working on these listeners.

A TLS-terminating reverse proxy owns the client connection. Silo's socket policy
then affects only the hop between that proxy and Silo. A network access provider
terminates connections in its plugin and proxies to Silo, so this policy does not
change the provider's client hop either. Silo cannot detect those hops.
With rootless Docker or Podman port drivers (RootlessKit, slirp4netns) and Docker
Desktop's VM proxy, the client's TCP connection ends in the proxy, so the tuning
reaches only the hop from that proxy to Silo.

## Tuning Contract

Network tuning uses the `SILO_NET_` environment prefix. It is per node and read
once at process start, after migration-only exits and before serving modes split.
`internal/tuning.Parse` returns typed configuration and adjustments; the caller
shares that configuration with all listeners and logs each adjustment once.

`SILO_NET_TCP_CONGESTION` accepts `bbr` (default) and `off`.
Parsing trims surrounding whitespace and lowercases the value. An unset
or blank value selects `bbr` silently. Any other value is refused: the effective
value is `off`, and a warning records the original value with `reason=invalid`.
Like the flags in `internal/envutil`, an unrecognized value fails toward off,
because an operator who mistypes the off switch was reaching for off.

`SILO_NET_TCP_SEND_BUFFER` accepts a positive decimal byte size up to 1073741823
(default 33554432), or `off`. It uses the same whitespace and case normalization.
An unset or blank value selects the default silently; an invalid value,
including `0`, selects `off` and records an adjustment with `reason=invalid`. The upper bound is
Linux's `INT_MAX/2` limit before the kernel doubles a request.

The two knobs are independent. Congestion `off` skips its set and readback.
Send-buffer `off` skips `SO_SNDBUFFORCE`, retains `TCP_NOTSENT_LOWAT`, logs
`state=off reason=disabled`, and keeps kernel send-buffer autotuning. A configured
size reports `state=off reason=not_needed` when the existing send maximum is at
least twice that size. Raising the maximum (third value) of `net.ipv4.tcp_wmem`
to at least 25000000 also keeps autotuning and logs `not_needed` on the next
start. Removing NET_ADMIN before startup prevents forcing too. Removing the Compose
`cap_add: [NET_ADMIN]` line does this only when no other
source grants NET_ADMIN. Root without a container, `privileged: true`, or a pod
spec can also supply it. With no helper, BBR can still work if the host loads
`tcp_bbr` and allows `bbr` in
`net.ipv4.tcp_allowed_congestion_control`, or makes it the namespace default.

Non-Linux listeners bind normally, with congestion reason `unsupported_platform`
(or `disabled` when the knob is off), and send-buffer reason
`unsupported_platform` (or `disabled` when send-buffer forcing is off).
They do not configure Linux socket options.

## TCP Buffer Contract

Startup reads the third whitespace-separated integer in
`/proc/sys/net/ipv4/tcp_wmem` and `/proc/sys/net/ipv4/tcp_rmem` for Silo's network
namespace. The caller reads and logs these limits once in serving modes other
than transcode. Transcode mode reads neither limit and emits no limits line.

A send maximum below 25000000 bytes is capped. At 1 Gbit/s and 100 ms, the
bandwidth-delay product is 12.5 MB. The buffer budget needs about twice that
because only about half carries data. The threshold therefore uses 25 MB.
The receive maximum is reported for context; it does not select a state or action.
Increasing the server's receive limit does not increase download throughput.
The client's receive limit can still cap a download.

Every Linux client listener sets `TCP_NOTSENT_LOWAT` to 131072 bytes (128 KiB),
regardless of the force decision or `SILO_NET_TCP_SEND_BUFFER=off`; this needs
no capability. Accepted sockets inherit the mark, which bounds unsent data
queued per connection. In a five-run sweep with forced buffers and autotuning,
128 KiB is the smallest mark that preserves throughput across all tested links;
16 KiB starves BBR at 1 Gbit/s and 100 ms. Compared with no mark, 128 KiB saves
3-56 MB of send-buffer memory per socket at a cost of 16-44% more API-container
CPU per GB.

Forcing requires Linux, readable send limits below the threshold, and a running
helper, with `SILO_NET_TCP_SEND_BUFFER` enabled. After setting the low-water
mark, Silo asks the helper to set `SO_SNDBUFFORCE` to the configured size
(33554432 bytes, or 32 MiB, by default). Silo reads back
`SO_SNDBUF` itself. Linux doubles the request; success requires a readback of at
least twice the configured size (67108864 bytes, or 64 MiB, by default).
Accepted sockets inherit both options from the listener. No per-connection force
is needed.

A forced send buffer fixes its size and disables send-buffer autotuning. Silo
therefore leaves autotuning intact when the send maximum meets the threshold.
When the maximum is at least twice the configured size, Silo also leaves
autotuning intact, skips the force, and reports `not_needed`.

If setting the low-water mark fails, Silo reports `kernel_error` in the existing
send-buffer listener line, even when forcing is off or not needed, and keeps
serving without forcing the buffer. Once set, the low-water mark stays in place
even if the force or readback fails. A bare
`EPERM` refusal means `not_allowed`. Other force errors report `kernel_error`
with `granted=0`.

Without the grant, a capped limit produces a `no_net_admin` send-buffer line.
Its action describes raising the maximum to 67108864 bytes in Silo's network
namespace or granting NET_ADMIN. `net.ipv4.tcp_wmem: "4096 16384 67108864"`
meets the threshold without disabling autotuning. The shipped Compose files do
not set it: a container sysctl that the runtime refuses, including with host
networking, stops the container from starting. `tcp_wmem` is per network
namespace on Linux 4.15 and later.
The helper changes owned sockets without changing a sysctl.

## Capability Containment

The shipped `docker-compose.yml`, which also runs proxy and transcode nodes, and
`docker-compose.split.yml`, which runs the regular image as root, grant
`cap_add: [NET_ADMIN]` to `silo`. Proxy nodes serve clients directly and use the
same socket policy. Transcode nodes configure no client listener; startup drops
their capability and releases the helper after the standalone bind.

In a Linux cgo build, a C constructor runs before the Go runtime. It counts
entries in `/proc/self/task` and reads the effective capabilities. When NET_ADMIN
is held, the constructor starts a helper, then drops NET_ADMIN from Silo's
bounding, ambient, inheritable, permitted and effective sets. The drop still
runs if starting the helper fails.

Dropping the bounding bit needs CAP_SETPCAP. Whenever the constructor cleans up
a grant, it sets `PR_SET_NO_NEW_PRIVS` after dropping the other sets, then reads
containment back. This flag prevents children from
gaining privileges at `execve`: setuid and setgid bits and file capabilities
grant nothing, and Linux security module transitions are limited. In the omnibus
image, Silo, ffmpeg, ffprobe and plugins run as `silo`, without relying on those
grants. Their work does not require gaining NET_ADMIN.

The startup log reports `no_new_privs` and `threads`. A running helper with
`threads` other than 1 produces a warning because the constructor cannot prove
it drops NET_ADMIN before another thread starts. Readback failures or retained
capabilities make `children_inherit=true`. These cases keep startup running and
produce a warning; they do not establish containment.

If the initial capability read fails, the constructor records `inspect_failed`
and `children_inherit=true`. It starts no helper, attempts the bounding and
ambient drops that need no capability read, sets `no_new_privs`, and skips
rewriting unknown capability sets. Builds without the Linux cgo constructor report
`unsupported_platform` and have no helper.

When NET_ADMIN is absent from the effective set at constructor entry, the helper
is `none`. Any remaining inheritable, permitted, ambient or bounding grant still
triggers cleanup and containment readback. A launcher that sets the inheritable
bit but has its ambient raise refused leaves such a grant: Silo clears the
inheritable bit and enables `no_new_privs` even though no helper starts.
(`svc-silo` never starts Silo that way: `setpriv` exits on a refused raise and
the script falls back to `s6-setuidgid`.) `children_inherit` reports the
readback in this path too.
With no grant in any set, startup leaves `no_new_privs` unchanged. Linux's
default bounding set includes NET_ADMIN, so a non-root run outside a container,
for example under systemd, takes the cleanup path and runs with `no_new_privs`
even though nothing granted it NET_ADMIN.

The helper keeps only NET_ADMIN in its effective and permitted sets and enables
`no_new_privs`. Its closed protocol accepts two socket operations:
`TCP_CONGESTION=bbr` and `SO_SNDBUFFORCE` with a size in [1, 1073741823]. Each
request carries exactly one IPv4 or IPv6 TCP stream socket descriptor through
`SCM_RIGHTS`.
The request contains a four-byte command and a four-byte size in little-endian
order. Congestion requires a zero size; forcing requires a size within the
allowed range. No algorithm payload is accepted. Other commands, sizes, socket
types, descriptor counts, malformed lengths and truncated messages are refused
with `EINVAL`. The helper closes every received
descriptor before replying. Both ends of its socket pair are close-on-exec, so
programs Silo executes do not inherit that channel. Before creating it, the
constructor opens `/dev/null` on any closed fd 0, 1 or 2, as the Go runtime
would later, so the channel never takes a standard descriptor; if that fails,
the helper does not start.

The startup wiring closes the helper channel and reaps the helper after the
last client listener bind attempt: after compatibility binds in integrated, API
and frontend modes, and after the standalone bind in proxy and transcode modes.
Closed-channel end-of-file or a parent-death signal ends the helper on other
exit paths. `Release` is idempotent; its state becomes `released`
and later requests return `netadmin.ErrUnavailable`.

Whoever holds NET_ADMIN can reach the network namespace's interfaces, routes,
firewall rules and nf_tables kernel code. With `network_mode: host`, that
includes the host's network. Silo confines its own use to the helper, but a
container grant also stays with the container's root processes for the
container's life: in the omnibus image, the s6 supervisor, disabled-service
placeholders and `docker exec` shells. The restricted protocol
limits requests from Silo; NET_ADMIN itself remains broader than these two
socket operations. Removing NET_ADMIN from Silo's capability set before startup
removes the helper, regardless of how the capability is granted; removing the
container grant also removes it from the container's root processes.
Silo and its children lose NET_ADMIN on a successful drop; the state log reports
when that cannot be confirmed.

### Deployment Constraints

| Deployment | Capability and limit constraints |
|---|---|
| Rootful Docker or Podman | The grant enables the helper when NET_ADMIN reaches the effective set. Container policy can still refuse operations. |
| Omnibus image | `svc-silo` uses `setpriv` to pass a permitted NET_ADMIN grant as an ambient capability when it changes to `silo`. It probes `setpriv` first; without the grant, or when `setpriv` fails, it uses `s6-setuidgid`. The constructor sets `no_new_privs` because `silo` lacks CAP_SETPCAP and cannot drop the bounding bit. |
| Root without a container | Root can hold NET_ADMIN without an explicit grant. The helper then uses the host's network namespace; removing a Compose grant has no effect. |
| Privileged containers | `privileged: true` supplies NET_ADMIN without `cap_add`. Removing `cap_add` alone leaves that capability available. |
| Rootless Docker or Podman | NET_ADMIN in the container's user namespace can bypass the BBR allowed list for a loaded module. It cannot load host kernel modules or force send buffers; forcing needs NET_ADMIN in the initial user namespace. With RootlessKit or slirp4netns port drivers, the client's TCP connection ends in the proxy, so the tuning reaches only the hop from that proxy to Silo. |
| Kubernetes | `securityContext.capabilities.add: ["NET_ADMIN"]` can grant the capability. Pod Security Baseline and Restricted refuse it. A pod without CAP_SETPCAP relies on the constructor's `no_new_privs` fallback when the bounding bit remains. |
| Kubernetes TCP limits | `net.ipv4.tcp_wmem` is a safe pod sysctl from Kubernetes 1.32 on Linux 4.15 or later; older clusters must allow it. The same namespace and threshold rules apply. |
| systemd | `AmbientCapabilities=CAP_NET_ADMIN` can supply the capability, subject to the unit's other restrictions. A host that loads and allows BBR provides the congestion alternative without the grant. |

## Log Contract

The emitting code owns message text, attributes and actions.
Congestion and send-buffer lines follow each successful listener bind.
Attribute byte counts are integers. An empty reason denotes success.

### `tcp congestion control: listener state`

Attributes: `component`, `listener`, `requested`, `granted`, `state`, `reason`, `cap_net_admin`, `action`, `errno`, `error`.

The component is `tcpcongestion`; listener labels are `api`, `jellyfin`, `abs`
and `proxy`. `requested` is `bbr` or `off`. `granted` is the readback
algorithm, or empty if unread. States `on` and `off` log at Info;
`fallback` logs at Warn. `errno` appears only for `kernel_error`, as a symbolic
name or `unknown`. `error` accompanies an unknown errno with the error text.

`cap_net_admin` is true for helper calls. Without the helper, Silo reads its
effective capabilities once, when it first attempts a congestion set, and
reports that value whether the set succeeds or fails.

| State | Reason | Condition |
|---|---|---|
| `off` | `disabled` | Knob is off |
| `off` | `unsupported_platform` | BBR enabled on a non-Linux platform |
| `fallback` | `module_missing` | Set returns `ENOENT` with NET_ADMIN |
| `fallback` | `module_not_loaded` | Set returns `ENOENT` without NET_ADMIN |
| `fallback` | `not_allowed` | Set returns `EPERM` without NET_ADMIN |
| `fallback` | `kernel_error` | Any other set error, or a readback error |
| `fallback` | `overridden` | Readback differs from `bbr` |
| `on` | Empty | Set and readback confirm `bbr` |

The `congestionAction` function in
[`internal/tcpcongestion/decision.go`](../../internal/tcpcongestion/decision.go)
selects the remedy for each congestion reason.

### `tuning knob: value adjusted`

Attributes: `component`, `env`, `requested`, `effective`, `reason`.

The component is `tuning`. Each invalid value adjustment logs at Warn.
`env` names the knob and `requested` preserves the original text. `effective`
is `off` for both knobs; `reason` is `invalid`. This line has no `action`.

### `tcp buffer limits: kernel state`

Attributes: `component`, `tcp_wmem_max`, `tcp_rmem_max`, `threshold`, `state`, `reason`.

The component is `tcpcongestion`. Unread maxima are zero; `threshold` is
25000000. A failed receive-limit read leaves `tcp_rmem_max=0` without changing
the send-limit state. This line logs at Info in every state and has no action.
The send-buffer lines carry the remedies.

| State | Reason | Condition |
|---|---|---|
| `ok` | Empty | Send maximum meets the threshold |
| `capped` | `below_threshold` | Send maximum is below the threshold |
| `unknown` | `unreadable` | Send maximum cannot be read or parsed |
| `off` | `unsupported_platform` | Non-Linux platform |

### `tcp send buffer: listener state`

Attributes: `component`, `listener`, `requested`, `granted`, `notsent_lowat`, `state`, `reason`, `action`, `errno`, `error`.

The component is `tcpcongestion`. `requested` is the configured byte size when a
force is attempted, otherwise zero. `granted` is the `SO_SNDBUF` readback,
otherwise zero. `notsent_lowat` is 131072 after a successful Linux set,
independent of forcing, otherwise zero. The errno and error rules match the
congestion line.
State `on` logs at Info; `capped` logs at Warn. State `off` logs at Info
except for `limits_unknown`, which logs at Warn.

| State | Reason | Condition, in Precedence Order |
|---|---|---|
| `capped` | `kernel_error` | Linux low-water mark set fails |
| `off` | `disabled` | Send-buffer knob is off |
| `off` | `unsupported_platform` | Non-Linux platform |
| `off` | `limits_unknown` | Send limits are unknown |
| `off` | `not_needed` | Send limit meets the threshold |
| `off` | `limits_unknown` | Limits have any other state than `ok` or `capped` |
| `off` | `not_needed` | Send maximum is at least twice the configured size |
| `capped` | `no_net_admin` | Helper is `none` |
| `capped` | `helper_unavailable` | Helper is any state other than `running` |
| `capped` | `not_allowed` | Helper refuses the force with bare `EPERM` |
| `capped` | `kernel_error` | Force or readback fails, including a lost reply |
| `capped` | `granted_below_request` | Readback is below twice the requested size |
| `on` | Empty | Readback is at least twice the requested size |

The `sendBufferAction` function in
[`internal/tcpcongestion/buffers.go`](../../internal/tcpcongestion/buffers.go)
selects each send-buffer remedy. It names the namespace limit to raise when a
force is unavailable and the path to inspect when the limits are unreadable.

### `net admin: capability state`

Attributes: `component`, `held_at_start`, `helper`, `children_inherit`, `no_new_privs`, `threads`, `action`, `errno`.

The component is `netadmin`. Startup logs this line once in every mode, before
release. `held_at_start` is false also when inspection fails; `inspect_failed`
distinguishes that case from proven absence. Startup helper values are
`running`, `none`, `start_failed`, `inspect_failed` and
`unsupported_platform`. `released` is a later state and is not a startup value.

`threads` is the constructor entry count, or zero if unread or no constructor
runs. `errno`, when present, names the first failed constructor step.
`no_new_privs` reports the kernel readback on every constructor path,
including when NET_ADMIN is absent. Builds without the constructor report
false.

The level is Warn when `children_inherit` is true, the helper is `start_failed`
or `inspect_failed`, or a running helper reports `threads` other than 1.
Otherwise it is Info. The `stateAction` function in
[`internal/netadmin/state.go`](../../internal/netadmin/state.go) selects the
remedy for failed inspection, unproven containment or helper startup failure.
