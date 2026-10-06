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

Installs that share one Redis server on different database numbers then keep
both their keys and their events apart.

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
