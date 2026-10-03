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

Database 0 keeps the bare name. Builds from before the scoping use the bare
name on every database number.

## Rules for operators

- Installs that share one Redis server each need their own database number.
  Two installs on the same number share keys and events.
- All nodes of one install use the same database number.
- An install on a non-zero database number upgrades all its nodes together
  when it moves from a build without scoping to one with it. While the builds
  are mixed, events do not cross between old and new nodes. Events with no
  other delivery path are then missed: a node that does not receive
  `user_sessions_revoked` keeps serving its cached Jellyfin-compatible
  sessions for that account, and one that does not receive `node_pool_changed`
  keeps its old node list until it restarts or a node change is made through
  it.
- A build without scoping still shares events with an install on database 0,
  whatever its own database number.
- A Redis ACL user needs channel access to the scoped names. `&silo:*` covers
  them. A list of the five bare names does not, and a node that is refused a
  subscription exits at startup.
