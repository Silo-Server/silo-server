package middleware

// Domain instrumentation lives with its producers: userdb, scanner, playback,
// workmetrics and streamtelemetry. The former declarations here had no writers.
// In particular, replication/restore and reconciliation gauges must not claim
// healthy zeroes for operations the server does not implement or measure.
