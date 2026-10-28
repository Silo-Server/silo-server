# TMDB client and request budget

Silo calls TMDB itself for request discovery, the request reconcile and
Discover list refresh tasks, watchlist title repair, and library and personal
collections (charts, franchises, discover queries, lists, and their scheduled
syncs). Metadata plugins call TMDB on their own and are outside this
document.

## One client per process

`cmd/silo` builds one `tmdb.Client` at startup and hands it to every feature
above, through `api.Dependencies.TMDB` for the router. Every feature in a
process therefore shares one set of response caches (chart and discover
pages, external IDs, certifications, details) and one limiter. Do not build
another `tmdb.Client` in production code; take the shared one. The router
builds its own only when `Dependencies.TMDB` is nil, which only tests do.

Each HTTP attempt waits on the limiter, retries after a 429 or 5xx included,
so a throttled burst cannot spend more than the budget. Concurrent callers of
the same chart page, discover page, or external ID lookup share one fetch
(`sharedFetch`). It keeps running while any caller still waits, so a page
request that gives up does not fail a collection sync that joined it, and it
is cancelled once every caller has given up, so an abandoned fetch does not
keep waiting for the budget.

## The budget

`tmdb.RequestsPerSecond` (40) is the budget for the whole deployment, not per
feature or per node. It is a constant, not a setting.
`ratelimit.NewOutboundLimiter` picks the backend:

- **Redis configured** (`REDIS_URL` or the saved Redis settings, whatever
  `ratelimit.backend` says): every node spends one budget through
  `RedisLimiter`, counted in the key `outbound:tmdb` in wall-clock second
  windows. A call the window refuses waits for the next second and tries
  again, until its context ends, so a busy moment loads more slowly instead of
  returning errors. On each node one waiter at a time checks Redis while the
  rest wait locally, so a backlog costs Redis at most one refused check per
  node a second. Nodes are not served in order: at each second every node's
  current waiter tries again and Redis decides who gets a slot. Window keys
  come from each node's clock, so nodes should keep their clocks in sync.
- **No Redis**: a per-process token bucket with a burst of the budget, the
  limiter each client used to build for itself.

While Redis is down, `RedisLimiter` counts on each node until Redis answers
again (see its comments), so the budget then holds per node rather than for
the whole deployment.
