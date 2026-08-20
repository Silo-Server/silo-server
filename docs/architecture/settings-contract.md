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
