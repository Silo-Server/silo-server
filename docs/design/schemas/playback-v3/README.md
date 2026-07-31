The canonical playback protocol v3 wire contract lives in `v3/`.
Clients vendor the schema files and fixtures from this directory; each schema is self-contained and refers only to its own `$defs`, so a client can vendor one body at a time.
The fixtures under `v3/fixtures/valid/` are copies of the server-generated golden bodies in `internal/playback/testdata/protocol_v3/`; treat them as opaque expected output rather than hand-edited examples, and regenerate them from the server rather than editing them in place.
Every bound in these schemas mirrors a server validator, so a body this directory rejects is a body the server rejects.
Identity fields the server computes — `plan_id`, `plan_attempt_key` — are opaque to clients: store them, echo them back on replan and route events, and never parse or recompute them.
Within v3, changes are additive only: new optional fields, and new enum values after coordinated client support.
Breaking changes require a new versioned contract directory and explicit server support.
