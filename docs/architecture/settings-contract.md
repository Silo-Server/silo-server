# Cross-platform settings contract

Silo has one machine-readable contract for every production, user-facing
setting, owned by this repository in `contracts/settings/v1/`. Before the
contract existed, the server, the web client, and the native apps each kept
their own partial registries of keys, defaults, ranges, and enum members, and
they drifted: mismatched key names, disagreeing defaults, device values that
nothing read. The contract exists so that a setting's key, value type,
constraints, storage scopes, resolution order, default, and UX copy have
exactly one definition, and a client PR can never invent a production setting
independently.

The HTTP endpoints that serve and store values are documented in
[docs/settings-api.md](../settings-api.md). This document covers the contract
itself and its invariants.

## Canonical artifact

- `contracts/settings/v1/manifest.schema.json` validates the contract format.
- `contracts/settings/v1/manifest.json` holds the definitions and is embedded
  by the server; `internal/settingscontract` loads and validates it.
- Object-valued settings reference a named JSON Schema under
  `contracts/settings/v1/schemas/`.
- The canonical bytes of the manifest are its RFC 8785 (JCS) canonicalization.
  The manifest endpoint's `ETag` is the SHA-256 of those bytes, and
  generated-code reproducibility is defined over the same bytes.

Clients vendor a pinned copy of the manifest and generate bindings from it
(key constants, typed values, scope enums, defaults). Handwritten raw remote
key literals are forbidden outside migration tests; client CI fails when a
production key is not generated or a local default disagrees with generated
metadata.

## Versioning and compatibility

The manifest carries two numbers:

- `api_version` identifies the settings protocol. It changes only for a change
  no revision rule can express.
- `revision` is a monotonically increasing integer bumped by every manifest PR.

Within one `api_version`, revisions are monotone-compatible in both
directions: a client pinned to an older revision remains valid, and a client
pinned to a newer revision hides definitions, scopes, enum members, and bounds
introduced after the connected server's advertised revision. That property
rests on classifying every change:

| Change | Allowed within `api_version` |
|---|---|
| Add a key, widen `allowed_scopes`, add an enum member, widen a numeric range | Yes — revision bump; the new sub-element carries its own `introduced_in` |
| Change a default | Yes — revision bump plus explicit release notes |
| Deprecate a key | Yes — `deprecated: true`; the definition stays published |
| Narrow scopes, tighten a range, remove an enum member | No — new key plus a migration of previously valid stored values |
| Change value type, persistence class, or meaning | No — new key |

Widening and narrowing must not share one rule: an older client that does not
know a new scope still resolves correctly, but a client that stored a value at
a removed scope has nowhere to put it. `introduced_in` is a manifest revision
(not an `api_version`) and is carried per sub-element, which is what lets a
newer client avoid offering a choice an older server would reject. Published
definitions are never unpublished.

A new setting after the initial cutover is one server PR plus independent
client PRs on their own schedules — the contract PR merges first, then each
client updates its pinned manifest and regenerates bindings. No lockstep
releases.

## Ownership classes

Every definition declares one persistence class:

- `remote` — stored and authorized by the server; roams by scope.
- `client_local` — production client-side behavior with shared, reviewed
  semantics; defined in the contract but never sent to the API.
- Private `local.<client>.<domain>.<name>` keys are the one escape hatch: a
  client-only diagnostics or implementation knob needs no contract PR, but it
  must never be shown as a production setting, sent to any Silo API, or
  expected to roam. Promoting one to a production feature means adding it to
  the contract first.

## Resolution

There is no universal hard-coded precedence. Each definition declares its own
`allowed_scopes` and `resolution_order`, and the server is the only canonical
resolver: clients may cache effective values but must not reimplement a
different precedence. `unset` is an operation, not a value, and is distinct
from `false`, `0`, `""`, and JSON `null`.

Most keys resolve their most specific scope first. A key can instead resolve
`profile` ahead of `profile_device` to model an "apply to all devices" switch:
a profile value is the choice for every device, and clearing it lets each
device follow its own value again. The switch's state is whether the effective
source is `profile`, so clients still read one server-resolved value.
`ui.title_art` works this way; its manifest notes define the writes.

A definition can declare `admin_default`, letting the server administrator
replace `default_value` for the whole server. The replacement is a server
setting named `defaults.<key>`, validated against the definition's value
schema when it is written and again when it is read. It is the value a viewer
inherits, so it resolves after every allowed scope and is still reported with
source `default`: clients that only know the contract default keep treating it
as inherited, and an explicit value at any scope, including one equal to the
default, still wins. A stored administrator default the schema no longer
accepts, or one that cannot be read, falls back to `default_value` rather than
failing the read. `ui.title_art`, `ui.next_up_mode` and
`ui.next_up_new_releases_first` declare it.

Only resolvers built with `WithAdminDefaults` read administrator defaults. Each
node builds one `config.AdminDefaults` in `cmd/silo` and passes it to every
resolver that needs one: the settings-values handler, the viewer access and
policy resolvers (whose scope carries the Next Up settings to home sections),
the sections fetchers, progress sync, and jellycompat's Next Up ordering and
scope resolvers. Before another server path resolves a key that declares
`admin_default`, give its resolver the same instance, or it will answer with
`default_value` while the settings API answers with the administrator's value.

`config.AdminDefaults` is a per-node snapshot of every `defaults.<key>`, read in
one query, because the viewer scope resolves on nearly every authenticated
request. A fresh snapshot is read without a lock. `cmd/silo` runs its refresh
loop, which reloads it every half TTL whether or not anything reads it; at most
one refresh runs at a time, and a caller that still finds it expired keeps it
while one refreshes in the background. A failed refresh keeps the last good snapshot and retries
after a short delay. An admin write reloads the writing node's snapshot before
the client event is published, and a read that began before the write can no
longer publish over it. Every other node reloads on the admin-channel
`settings_changed` event, which travels separately from the client event, so a
client of another node can re-read before that node has reloaded; while a node
can read its settings, a 10-second TTL bounds how long it serves the old
default, and the web client re-reads once more after it.

## Preferences versus restrictions

`internal/policy` is a second resolver over some of the same subject matter,
and it stays authoritative. Settings answer "what does this user want?";
policy answers "what is this user allowed to have?". A definition that policy
can constrain declares `constrained_by`, and the effective-values endpoint
applies the constraint — clients act on the permitted value, with
`requested_value` reported only when a constraint changed the outcome.

Mutations are not rejected for exceeding a restriction: restrictions change,
and a stored preference the current policy forbids should take effect when the
cap is lifted rather than be destroyed by it. Validation rejects values
invalid against the definition; policy constrains at resolution time.

Settings values must never contain secrets. A secret-like preference needs a
dedicated encrypted/credential API, not a settings schema type.
