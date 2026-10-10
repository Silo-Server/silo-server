# Redis event bus

The nodes of one install tell each other about changes over Redis pub/sub:
settings, plugin and node-pool changes, revoked sessions, finished scans,
playback notifications, live log rows and websocket events.
`cache.RedisEventBus` in `internal/cache/redis.go` is the only code that
publishes or subscribes. Without a Redis URL the bus is a no-op and events stay
in the process.

## Channels and database numbers

The bus has five channels: `silo:catalog`, `silo:admin`, `silo:playback`,
`silo:logs` and `silo:events`. Code, logs and other documents use these names.

Redis scopes keys by database number but not pub/sub. A published message
reaches every subscriber of the channel on that server, whichever database
either connection selected. The bus therefore adds the database number of its
own connection to the name it gives Redis:

| `REDIS_URL` | Redis channel for `silo:admin` |
| --- | --- |
| `redis://host:6379` or `redis://host:6379/0` | `silo:admin` |
| `redis://host:6379/3` | `silo:admin@db3` |
| `redis://sentinel-1:26379/3?master_name=mymaster` | `silo:admin@db3` |

The number is the one the bus's connection selects. A `redis.db` setting with a
value replaces the number in a saved `redis.url`, so a saved
`redis://host:6379/3` with `redis.db` 5 gives `silo:admin@db5`. `REDIS_URL`
supplies the whole connection, and a process started with it does not apply
`redis.db`.

Installs that share one Redis server on different database numbers then keep
both their keys and their events apart.

## Subscriptions behind Sentinel

A URL with a `master_name` parameter names a Sentinel deployment, and the bus
connects to the master Sentinel names. go-redis sends commands to a new master
on the next connection it opens. It moves a subscription only when that
subscription's connection breaks, and a master that freezes or drops off the
network leaves the connection open. The bus would then publish to the new
master and keep listening to the old one.

For a Sentinel URL the bus reads each subscription itself, in
`listenToMaster`. Every receive has a 10 second deadline, and a ping every 3
seconds makes a healthy master answer within it. When the deadline passes,
go-redis drops the connection, and the bus redials until it has a connection
to the master Sentinel names, on which go-redis subscribes again. Redis
pub/sub does not replay, so events published while a subscription is between
masters do not reach that node.

A single-server URL keeps go-redis's `pubsub.Channel()`. There is no other
server to move to.

## Invariants

- Publish and subscribe through `RedisEventBus` only. A `PUBLISH` or
  `SUBSCRIBE` sent on a go-redis client directly uses the bare name and reaches
  every install on the server.
- Database 0 keeps the bare name. Builds from before the scoping use the bare
  name on every database number, so nodes of a database 0 install on old and
  new builds still exchange events. Scoping database 0 would end that.
- Changing the name a database number maps to splits an install whose nodes
  run mixed builds: events stop crossing between old and new nodes for as long
  as the builds differ. Two events have no other delivery path. A node that
  misses `user_sessions_revoked` keeps serving its cached Jellyfin-compatible
  sessions for that account, and one that misses `node_pool_changed` keeps its
  old node list until it restarts or a node change is made through it. The
  scoping itself had this effect on installs on a non-zero database number.
- Keep every Redis channel name under `silo:`. A Redis ACL user can be granted
  channels by pattern (`&silo:*`), and a node that is refused a subscription
  exits at startup.
- A Sentinel subscription has to notice a silent master by itself. Read
  through `pubsub.Channel()`, it stays on a master that froze for as long as
  the connection stays open.
- The redial of a Sentinel subscription runs outside the receive deadline and
  under a context that `Close` ends. Inside the deadline, a Sentinel lookup
  slower than the deadline is taken for a silent server and never completes.
  Without the context, `Close` waits for the redial of each subscription in
  turn. With it, `Close` waits for the attempt in flight, which go-redis does
  not interrupt.
- A Sentinel URL keeps its read timeout. A redial has no deadline of its own,
  so the read timeout is what ends a handshake with a frozen master. Without
  one the redial never returns and holds the subscription's lock, which
  `Close` needs.
- `Subscribe` refuses a subscription once `Close` has begun. `Close` takes the
  list of subscriptions once, and one added later would never be closed.
