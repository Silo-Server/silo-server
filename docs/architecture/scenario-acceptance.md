# Executable scenario pairing

Scenario catalogs retain the observed v1 exchange. A non-null `v2_expectation`
records a separate operation ID, method, concrete request, expected status,
headers and body assertions. Its optional principal overrides the v1 principal.
The catalog schema requires these fields; the loader checks the operation,
method and path against the committed OpenAPI document. No URL prefix substitution
or inherited v1 response expectation supplies the v2 contract.

`kind` identifies semantic equivalence or an intentional difference. An intentional
difference requires a summary. `recorded_in` points to the existing repository
contract or test reference supporting the recorded behavior; it is not evidence
that a new external review or approval occurred.

The executor runs both exchanges through the real router and reports stable IDs
such as `profiles_list.shape/v1` and `profiles_list.shape/v2`. Paired transports
start from reseeded fixture state. V1 follow-up steps remain attached to the v1
exchange; the explicit v2 exchange asserts its own response. The current pilot
covers reads, not mutation-effect equivalence.

The required pilot consists of ten cases for `GET /api/v1/profiles/` paired with
`listProfiles` at `GET /api/v2/profiles`. They cover list meaning, shape, ordering,
account isolation, API-key access, missing profile selection, foreign-profile
refusal and missing credentials. Successful v2 responses use `items`, explicit
empty arrays and strings; refusals use Problem Details. Assertions omit volatile
timestamp values and request identifiers. Problem type assertions check the
semantic path; canonical origin checks remain in the v2 contract fixtures.

Run `make test-scenario-profile-pairing` with `SILO_SCENARIO_DATABASE_URL` pointing
to a dedicated empty PostgreSQL database. The executor migrates and truncates
its synthetic fixture tables. Never use a shared integration database. The required
target fails if the database is missing, a fixed pilot case or pairing disappears,
an exchange skips, or any expected status/header/body assertion fails.
`SILO_SCENARIO_REPORT` optionally names the JSON result file.

Ordinary offline unit runs retain optional database execution. Their skipped
cases are not acceptance evidence. The catalog coverage gate still checks all
existing scenarios, but the other unpaired rows remain outside this bounded
acceptance pilot.

Offline baseline policy: `TestScenarioCatalogs` without a database must report
zero failures. The offline router is built without a user store, policy system,
viewer-access or acting-admin gate, so v2 operations whose declared class
(`x-silo-class`) is anything other than `public` or `authenticated` fail closed
with 503 `dependency_unavailable` before authentication. The executor therefore
treats those v2 exchanges, and any v2 follow-up step on such an operation, as
database-gated and skips them offline, exactly as it already skips v1 rows the
offline wiring does not register. Their 401 oracles are proven only on the live
router by the required targeted packets.

## Device-list checkpoint

`make test-scenario-device-pairing` requires the same guarded synthetic PostgreSQL
fixture database. It runs the fixed 13 `devices_list.*` scenarios as 26 independent
v1/v2 exchanges. A missing or duplicate case, cleared pairing, skipped exchange or
failed assertion fails acceptance. `SILO_SCENARIO_REPORT` optionally writes the
per-transport results; use separate report files for the profile and device targets.

The device pairs exercise profile and household visibility, current-device marking,
recency order, empty collections, row fields and authorization/validation failures
through the production router and PostgreSQL provider. V2's `items`/`page` envelope,
canonical UTC timestamps and Problem responses are intentional wire differences.
Uppercase `scope` is a v2 enum validation error (422), while v1 normalizes it before
authorization; a missing profile header is v2 validation 422 versus v1 bad request
400. Neither transport's expectations are inferred from the other's response.

This checkpoint covers the existing single-page fixture, not signed continuation,
timestamp ties, device reset/forget effects, SQLite execution or performance.
Those need separate executable evidence. The existing profile-list target remains
required; passing these two slices does not complete tier-1 migration acceptance.

## Explicit v2 follow-ups

A `v2_expectation` may declare `then` steps with their own operation ID, method,
request and assertions. These steps run against the same state as that transport's
initial exchange. The executor never inherits the legacy `then` list. A failed v2
exchange stops its dependent steps. Existing legacy follow-up behavior is unchanged.

A step's optional `from_previous` array copies a nonempty string from exactly one
header or JSON pointer in the immediately preceding response into exactly one
request destination. Header destinations are restricted to `If-Match` and
`If-None-Match`; query destinations must be declared string parameters of the
step's named OpenAPI operation. For example:

```json
"from_previous": [{"header": "ETag", "request_header": "If-Match"}]
```

Use `{"pointer":"/page/next_cursor","query":"cursor"}` for a continuation.
Captures are applied after fixture substitution and are never interpreted as
another template. They cannot replace the URL path, origin, body, credentials or
profile headers. Explicit fixture principals remain the sole authority selection
mechanism. Static and captured values cannot share a destination. Missing,
ambiguous, empty, non-string or oversized captures fail before the next request.
A previously consumed captured cursor also fails before another dependent request.

Each v2 sequence permits at most 16 physical requests, including declared repeats,
with at most eight captures per follow-up and 16 KiB per captured string. A repeat
retries the same constructed values; it does not consume another cursor. Captures
exist only within that transport's sequence and only the last response feeds the
next step. This is bounded fixture infrastructure, not automatic traversal or a
client retry implementation. Numeric/body/path captures and mutation acceptance
catalog expansion are outside this checkpoint.

## Device mutation effects

`make test-scenario-device-mutations` requires six existing reset/forget cases,
producing 12 transport results. Each v2 sequence explicitly reads the household
list afterward to verify the target effect and preserve sibling devices and
settings. Reset retains the target device with zero changed settings; forget
removes it. Repeated forget returns 404, repeated reset remains 204, and denied
or unknown-target mutations leave the fixture unchanged. These operations do not
support conditional requests, so the cases do not invent ETag preconditions.

Only this required target overlays one canonical setting on each fixture device,
after every reseed and independently for each transport. Teardown reseeds without
the overlay and checks that no profile-device settings remain. The ordinary list
fixture and original v1 records remain unchanged. Missing pairs, missing explicit
read-after vectors, skipped results and failed assertions fail the required gate.
The six pairs issue 22 physical requests including repeats and v2 follow-ups;
result counts describe transports rather than individual HTTP requests.

This checkpoint requires the guarded PostgreSQL scenario database. It does not
claim SQLite coverage, cursor traversal or conditional device mutation support.

## Profile mutation effects and PIN checks

`make test-scenario-profile-mutations` requires eight existing cases: name and
partial preference updates, rejected self-service access changes, deletion and
repeated deletion, protected primary-profile deletion, and correct/incorrect PIN
checks. These produce 16 transport results and 26 physical requests, including
repeated deletion and eight explicit v2 household reads. The reads verify target
changes, sibling preservation and the absence of PINs, hashes and credentials in
profile lists. PIN checks assert credential presence only on success; this slice
does not consume that credential or claim an unlock/expiry lifecycle test.

The v2 update uses PATCH; v1 uses PUT. V2 returns Problems and canonical profile
fields, and PIN checks include `expires_at` (null in this fixture configuration).
Neither update nor deletion supports conditional requests. No ETag preconditions
or dynamic resource-path captures are assumed.

The required target assigns distinct creation timestamps to its fixed synthetic
profiles after each independent transport reseed. This makes positional effect
assertions deterministic without assuming ordering among equal timestamps.
Teardown restores the ordinary fixture. Default fixtures, production behavior
and all original v1 records remain unchanged. Missing pairings/read-after steps,
skipped results and failed assertions fail this gate. SQLite execution, profile
creation, avatars and additional profile authorization cases remain separate work.

## Profile-section reads

`make test-scenario-section-reads` requires the 24 existing read scenarios for
profile overrides, resolved section settings, and the custom-section flag. Each
transport starts from a separate fixture reseed through the real router and
PostgreSQL provider. The gate requires all 48 transport results without skips;
`SILO_SCENARIO_REPORT` writes their individual assertions and outcomes.

The pairs preserve the original v1 exchanges. V2 collections use `items` with
explicit empty arrays and omit `page` for these bounded, unpaginated results. Authorization failures use Problem Details;
missing profile selection and invalid scope/library parameters use validation
422. The server's custom-section flag retains its boolean meaning, including
explicit false and true settings. These cases cover empty home/library pages,
response shape, account isolation, unavailable profiles and missing credentials.

This slice does not establish populated section ordering, override mutation
effects, catalog-media behavior, SQLite parity or full migration acceptance.
Use a dedicated empty database as described above; shared integration databases
are unsuitable because the executor migrates and reseeds synthetic fixture state.

## Profile-section reset effects

`make test-scenario-section-resets` requires the eight existing reset scenarios,
including repeated reset, library scope and authorization refusals. The target
installs a test-only overlay of home and library overrides for two profiles after
every transport reseed. Ordinary read fixtures retain their empty state.

Each v2 reset has four explicit follow-up reads. They verify that the selected
profile/page was cleared on success, other scopes and sibling profiles retain
their rows, and refused resets preserve all four sets. Repeated reset remains
204. The gate requires 16 transport results and issues 50 physical requests,
including repeats and follow-ups. Teardown restores the ordinary fixture and
checks that no section-override settings remain.

V1 requests and assertions remain frozen; their status checks run against the
same populated overlay, while the new effect reads belong explicitly to v2.
This target does not establish concurrent-write isolation, replacement semantics,
SQLite behavior or populated resolved-section ordering.

## Profile-section replacement effects

`make test-scenario-section-replacements` requires the 13 existing replacement
scenarios. Its isolated overlay gives a member's two profiles and a separate
admin account distinct home overrides before each transport. Three explicit v2
reads verify saved fields or empty-set replacement, recipe permission/configuration
refusals, malformed input and profile/account isolation. A successful custom
section retains its recipe/configuration; denied writes retain the original rows.

The gate requires 26 transport results and 66 physical requests, including the
original v1 round-trip and 39 explicit v2 follow-ups. Teardown restores the
ordinary fixture and checks that no section-override settings remain. The v1
oracle stays unchanged. Together with the read and reset targets, this covers
all 45 original profile-section scenarios; it does not establish concurrent
replacement safety, SQLite parity, catalog-media acceptance or full migration
completion.

## New catalog-media read regressions

`make test-scenario-new-catalog-reads` runs 14 **new** v2 scenarios through the
production API router, PostgreSQL catalog/auth/profile providers and scanner file
repository. These cases are separate from the frozen 598-scenario oracle and do
not increase its paired count. The required target fails without its dedicated
database, on a skipped/incomplete run, or on any assertion or cleanup failure.

The fixture contains two libraries, two permitted movies rated PG and R, and a
movie in the denied library. One permitted content item has two allowed file
versions and a third version in the denied library. Each case removes only the
media IDs created by this target, runs the existing scratch-database guard and
household reseed, then inserts fresh media. The guard's refusal of pre-existing
media remains unchanged. Teardown removes the media and verifies the ordinary
scratch guard again; it does not adopt or erase unrelated media.

The cases cover:

- Visible catalog identity/order, exact totals, child-rating filtering, and a
  two-page signed traversal that terminates without duplicates.
- Cursor rejection under another profile (`invalid_cursor`, HTTP 400).
- Content-item detail and the exact permitted file-version set, with string file
  IDs and file paths absent for a viewer without path visibility.
- A foreign `file_id` presentation hint retaining the requested content identity
  and permitted version set; a numeric file ID cannot substitute for a content ID.
- Denied item/version reads, a foreign profile, missing profile selection,
  anonymous requests and unknown items, with explicit Problem assertions.

The target issues 16 physical GET requests and writes a separate report through
`SILO_SCENARIO_REPORT` identifying its new-scenario count, request count and results.
The report complements the test exit status: setup and teardown must also pass.
These are metadata reads with synthetic database records; they do not establish
media-byte delivery, disk-file availability, search-provider behavior, SQLite
parity, concurrent catalog changes, all sorts/filters or performance acceptance.

## New admin catalog source-browse regressions

`make test-scenario-new-catalog-sources` runs 13 **new** v2 scenarios through the
production API router, PostgreSQL account/profile providers and filesystem browse
service. It requires the same exclusive scratch database described above and
reseeds the household before each scenario. These cases are separate from the
frozen 598-scenario oracle and do not increase its paired count.

Each scenario creates a temporary directory containing three directories, a
symlink to one directory, an ordinary file and a broken symlink. The two-page
traversal checks exact ordered directory names, the valid symlink's own path,
parent/path metadata and terminal pagination. Further cases check case-insensitive
prefix filtering, an empty result array, and signed cursor rejection after a
path, prefix, declared-profile or operation change.

Real authorization cases deny an ordinary account's primary profile, an admin
account's secondary profile, a foreign profile and an anonymous caller. Missing
directories and invalid page limits must return the specified Problem status and
code. The required target asserts 13 unique results and 18 physical GET requests;
`SILO_SCENARIO_REPORT` writes their separate new-scenario evidence. Setup, teardown
and the existing scratch guard must pass alongside the report. Temporary files
are removed by the test framework, and the database returns to the ordinary
synthetic household fixture.

This scope establishes local directory browsing and cursor/authorization behavior.
It does not exercise remote object storage, catalog archive import/export,
concurrent filesystem changes, permission-restricted operating-system accounts,
or full migration acceptance.

## New notification inbox regressions

`make test-scenario-new-notification-inbox` runs eight **new** v2 scenarios with
20 HTTP exchanges and five direct PostgreSQL effect reads. It uses the production
API router, notification system and delivery repository, real account/profile
providers, and the migrated inbox timestamp trigger. These cases remain outside
the frozen 598-scenario oracle and do not increase its paired count.

The required target refuses a missing database or pre-existing deliveries/inbox
clocks. Each case cleans only its exact delivery IDs and synthetic profile clock
rows, then runs the ordinary guarded household reseed. Synthetic operational
notifications belong to two profiles on the same account. Notification workers
are not started, and no outbound channels or real delivery destinations are
configured. Teardown restores the ordinary fixture and scratch guard.

The cases check:

- A signed list window excludes arrivals inserted after its first page.
- Read-all applies its captured cutoff, including older rows beyond the displayed
  page, while retaining unread later arrivals and the other profile's delivery.
- Repeated single-item read returns a bodyless 204 without changing `read_at`.
- Foreign delivery reads/writes and cross-profile list/read-all cursors fail
  without altering persisted read state.
- An empty sync checkpoint discovers later arrivals in bounded ordered pages,
  then returns an empty terminal continuation.
- Anonymous and missing-profile requests return their specified Problems.

`SILO_SCENARIO_REPORT` records the unique new cases, HTTP count, effect-read count
and failures. Setup/teardown and test exit must pass alongside that report. This
scope does not repeat the independent reversed-commit ordering tests or establish
websocket/push delivery, retention, concurrent writer behavior, native UI behavior,
or full notification migration acceptance.

### NEW literary administration regression scenarios

`make test-scenario-new-literary-admin` requires the dedicated scratch PostgreSQL
DSN and runs six **new** v2 scenarios, outside the frozen 598-scenario oracle.
The real router uses the production literary service and repository. Each case
reseeds synthetic accounts and two editions sharing a synthetic provider ID;
there are no media files, scans, provider calls or enrollment.

Twelve HTTP exchanges and eight persisted-state reads check existing-work reuse,
work/item-scoped unlink, confirmation and ignore attribution to the acting
account, reverse candidate exclusion after ignore, household-primary and
admin-secondary refusal, and validation/missing-item failures without changes.
State checks include exact edition format, work identity, manual confirmation,
and decision ownership. The fixture refuses pre-existing literary rows before
household reseeding can cascade to decisions, and removes only its exact items
and work between cases and on exit.

`SILO_SCENARIO_REPORT` records unique NEW cases, request/effect counts and failures;
setup, teardown and process exit must pass too. This scope does not establish
concurrent administration, atomic linking plus decision recording, provider
matching quality, client UI behavior or full literary migration acceptance.

### NEW diagnostic download failure scenarios

`make test-scenario-new-diagnostic-download` requires the dedicated scratch
PostgreSQL DSN. Five **new** v2 cases exercise the real router, diagnostic service
and report repository with object storage unconfigured. Seven HTTP requests and
ten full-row snapshot reads verify receiving/failed reports return 409, a ready
report returns 503, an unknown UUID returns 404, and report ownership does not
bypass acting-administrator authorization. The report's ordinary profile, an
administrator's secondary profile and anonymous callers are refused.

Requests include Range, but these failures must remain Problems without download
or redirect headers, stored bucket/key, or manifest content. Persisted report rows
must remain unchanged. A preflight guard refuses existing reports before account
reseeding can cascade through their foreign keys. Each case owns and removes only
one synthetic report UUID. The required test and its cleanup must pass alongside
`SILO_SCENARIO_REPORT`; results remain separate from the frozen 598-scenario oracle.
This scope does not test successful archive streaming, object-store behavior,
capture/upload, retention, browser downloads or complete diagnostics acceptance.

### NEW person curation regression scenarios

`make test-scenario-new-person-curation` requires the dedicated scratch PostgreSQL
DSN and exercises six **new** v2 scenarios through the real router and person
repository. Seven PATCH requests and twelve persisted-row reads check explicit
null preserving values, empty strings clearing dates/text/provider IDs, leap-day
round trips, rejected-date preflight preserving the whole row, acting-admin
refusals and exact missing identity. Successful updates may change `updated_at`;
rejected updates must preserve it along with every other stored field.

Each case reseeds the synthetic household and one synthetic person. Existing
people cause refusal before the fixture takes ownership; cleanup deletes only
its exact ID. Results and counts in `SILO_SCENARIO_REPORT` remain outside the
frozen 598-scenario oracle, and setup/teardown/test exit must pass too. No provider
refresh, concurrent full-row update, client UI or whole curation migration claim
follows from this scope.

### NEW diagnostic history and deletion scenarios

`make test-scenario-new-diagnostic-history` runs five **new** scenarios with the
real router, diagnostic service and PostgreSQL repository. Twelve HTTP requests
and ten full-table snapshot reads check deterministic traversal of equal
microsecond timestamps, cursor limit/filter binding, manifest detail preservation,
exact metadata deletion with repeated 204/absent 404 receipts, and refusal of
ordinary-profile or administrator-secondary deletes. List summaries omit manifests;
all responses omit stored object locations. Remaining reports must stay unchanged.

The fixture reuses the pre-reseed report occupancy guard and owns only three
synthetic UUIDs. Each case gets a fresh synthetic household/report set. Required
DSN, setup/teardown and process exit must pass alongside `SILO_SCENARIO_REPORT`.
These cases are outside the frozen 598-scenario oracle and separate from diagnostic
download-failure acceptance. Object storage is unconfigured: successful metadata
deletion does not establish blob cleanup, durable reconciliation, concurrent-list
snapshot behavior, capture/upload, or native/browser acceptance.

### NEW catalog item curation scenarios

`make test-scenario-new-item-curation` runs six **new** scenarios through the real
router and catalog repository. Seven PATCH requests and twelve full-table
PostgreSQL snapshots check explicit-null preservation, empty text/array/timezone
clearing, exact-item title/year/runtime/genre updates, invalid-timezone preflight,
acting-admin refusal and missing identity. Returned detail must agree with the
mutation; both unrelated items remain unchanged. Successful edits may touch only
the target timestamp, including all-null edits. Title normalization is checked.

The fixture reuses the guarded synthetic catalog, cleans its exact IDs before
each household reseed and refuses existing media before setup. Required DSN,
setup/teardown and process exit must pass alongside `SILO_SCENARIO_REPORT`. These
cases remain outside the frozen 598-scenario oracle. No metadata refresh/provider,
concurrent-update guarantee, season/episode edit or client UI claim follows.

### NEW translation-job history and cancellation scenarios

`make test-scenario-new-translation-jobs` runs six **new** scenarios through the
real router, translation service and PostgreSQL repository. Seven HTTP requests
and twelve full-table job snapshots check the newest-50 list bound and order,
content isolation, empty-array response, refusal of a job under another item's
URL, pending cancellation, completed-job preservation and acting-admin refusal.
Every listed job has the expected string identity; internal request attribution
and deduplication keys stay outside the response. Only the intended pending job
may change status, error message, update timestamp and heartbeat.

The fixture refuses existing translation jobs before router startup recovery or
household reseeding. It seeds completed history and one fresh pending row, then
cleans exact job and catalog IDs before each new case. It never enqueues work or
calls an AI provider. Required DSN, setup/teardown and process exit must pass with
`SILO_SCENARIO_REPORT`. These cases are outside the frozen 598-scenario oracle;
they do not establish cross-node runner interruption, cancellation/publication
races, durable replay, translated output or native/browser acceptance.

### NEW season and episode curation scenarios

`make test-scenario-new-child-curation` runs four **new** scenarios through the
real router and catalog repositories. Six PATCH requests and eight PostgreSQL
snapshots covering all item, season and episode rows check correct child identity,
parent/sibling preservation, episode runtime/leap-day edits, null preservation and
acting-admin refusal. The returned child type, content ID, title and series ID
must agree with the target. Only a successfully edited child's update timestamp
is exempt from the full-row comparison, including null-only edits.

The fixture reuses the catalog occupancy guard. Both child tables require parent
media-item foreign keys, so existing children cannot evade that guard. Exact child
and catalog IDs are removed before each household reseed. Required DSN, cleanup
and process exit must pass alongside `SILO_SCENARIO_REPORT`. These cases remain
outside the frozen 598-scenario oracle and are distinct from movie-only curation
acceptance. No metadata refresh/provider, hierarchy renumbering, concurrent-update
or native/browser behavior is claimed.

### NEW viewer-library discovery scenarios

`make test-scenario-new-viewer-libraries` runs five **new** scenarios through the
real router and library repository. Nine GET requests and ten full-table folder
snapshots check capability availability, enabled-only ordering, account/profile
allowlists, empty access, changed access between requests and unauthenticated
refusal. IDs must be strings. Disabled libraries and internal paths/poster keys
must not leak; reads must leave every stored folder unchanged.

The fixture owns three synthetic folders, reuses the pre-reseed occupancy guard
and deletes only its exact IDs before each new household. Required DSN, cleanup
and process exit must pass alongside `SILO_SCENARIO_REPORT`. These scenarios are
outside the frozen 598-scenario oracle. No object-store presigning, access-policy
mutation endpoint, native UI or concurrent policy-change guarantee is claimed.

### NEW webhook-destination read scenarios

`make test-scenario-new-webhook-destinations` runs four **new** scenarios through
the real router, notification service and PostgreSQL repository. Ten GET requests
and eight full-table snapshots check equal-timestamp ID traversal, disabled-row
visibility, profile/limit-bound cursors, household isolation, an empty administrator
profile and authentication refusal. Public responses include the host and nullable
status timestamps while omitting stored URL/signing fields. All rows stay unchanged.

The fixture refuses existing webhook destinations before setup, cleans only four
synthetic IDs and reseeds the household per case. The notification system is wired
without starting dispatch workers. Stored secret fields contain synthetic opaque
sentinels; this scope tests omission, not encryption or delivery. Required DSN,
cleanup and process exit must pass alongside `SILO_SCENARIO_REPORT`. These cases
remain outside the frozen 598-scenario oracle. No notification sends, provider,
creation/update/delete, concurrent snapshot or native/browser claim follows.

### NEW administrator notification-channel read scenarios

`make test-scenario-new-server-channels` runs four **new** scenarios through the
real router, notification service and PostgreSQL repository. Nine GET requests
and eight full-table snapshots check tied creation-time traversal, disabled rows,
page-size cursor binding, acting-administrator enforcement and the empty collection.
Failure counters/statuses, notification selections and UTC timestamps retain their
stored meaning. Responses omit ciphertext, signing data and internal delivery
watermarks; reads leave the complete stored rows unchanged.

The fixture refuses existing channels before setup and deletes only its three
synthetic IDs before each household reseed. No notification workers start. Opaque
secret sentinels test omission, not encryption or delivery. Required DSN, cleanup
and process exit must pass alongside `SILO_SCENARIO_REPORT`. These cases remain
outside the frozen 598-scenario oracle. No sends, mutation, concurrent snapshot,
native or browser behavior is claimed.

### NEW administrator device-read scenarios

`make test-scenario-new-admin-device-reads` runs five **new** scenarios through
real router, account fanout and PostgreSQL stores. Eleven GET requests and ten
snapshots of all registration, legacy and canonical override rows verify merged
logical override counts, profile metadata, the same device ID under separate
accounts, cursor traversal/binding, missing identities and administrator enforcement.
Canonical-only overrides affect counts while detail retains only the legacy
compatibility settings array. Existing canonical-store metadata has whole-second
precision, represented as UTC milliseconds at the v2 boundary. Reads preserve every
stored row.

Before setup, the fixture refuses existing override rows and registrations outside
its known household seed. Each case reseeds that household and adds one synthetic
registration plus four override rows. Required DSN, cleanup and process exit must
pass alongside `SILO_SCENARIO_REPORT`. These cases remain outside the frozen
598-scenario oracle. No device enrollment endpoint, preference mutation endpoint,
playback command, concurrent snapshot, native or browser behavior is exercised.

### Frozen API-key deletion pairs

`make test-scenario-api-key-deletions` requires seven original `keys_delete.*`
cases: `ok`, `gone`, `other_user`, `bad_id`, `demo`, `shape` and `no_token`.
The fixed selector refuses missing pairings and unsupported sequences. Original
v1 requests and expectations remain unchanged. V2 explicitly records deletion,
repeated/missing refusal, Problem Details and malformed-ID validation 422.

The focused runner uses the original request, principal, settings and expectations
through the real exchange helper. It reseeds before and after every transport,
including originals marked `fresh_state`, and compares complete PostgreSQL API-key
rows before teardown. Fourteen transport results cover sixteen HTTP requests and
28 full-table snapshots. Successful and repeated deletion remove only the exact
member key; refused requests preserve all keys. No asynchronous API-key-auth usage
metadata is exempted because that separate scenario is outside this cohort.

The pre-setup guard refuses non-fixture keys before migrations/reseed. Required
DSN, fixed result inventory, assertions, cleanup and process exit must all pass;
`SILO_SCENARIO_REPORT` records each transport. These are pairs from the frozen
598-scenario oracle, not NEW scenarios. No creation, SQLite, concurrent revocation,
in-flight credential drain or native/browser behavior is claimed.

### Frozen API-key list pairs

`make test-scenario-api-key-lists` requires four original cases:
`keys_list.ok`, `keys_list.sorted`, `keys_list.empty` and `keys_list.no_token`.
Original v1 expectations remain unchanged; v2 records collection envelopes,
string IDs, metadata without reusable secrets, and Problem Details explicitly.
The real router/provider runs eight transport requests with reseeding before
and after each transport and 16 complete API-key-table snapshots. Every row must
remain byte-identical. Required DSN and the pre-setup occupancy guard prevent
silent skips or destructive execution against non-fixture keys. The API-key-auth
usage timestamp case remains outside this cohort; no asynchronous metadata
exception, pagination traversal or concurrent snapshot behavior is claimed.
These are four pairs from the frozen 598, separate from NEW scenarios.

The original `keys_list.meaning` and `keys_list.shape` remain unpaired: their
frozen v1 oracle requires reusable secrets and the old exact field set, while
the current bridge returns metadata with `key_prefix` and `revision`. Their
expectations remain unchanged pending resolution by the contract owner.

### Frozen API-key scope discovery pairs

`make test-scenario-api-key-scopes` requires six original cases:
`scopes.ok`, `scopes.meaning`, `scopes.shape`, `scopes.sorted`,
`scopes.no_token` and `scopes.error_shape`. The original oracle remains unchanged.
V2 explicitly adds the availability flag and Problem Details while retaining the
two scope names, descriptions and fixed order. The real router/provider runs
twelve transport requests with reseeding before and after each transport; all
24 full API-key-table snapshots must prove unchanged rows without exemptions.
Required DSN, the pre-setup occupancy guard and the fixed selector fail closed.
API-key-auth usage metadata remains outside this cohort. These six frozen pairs
are separate from NEW scenarios; no provisioning or scope-enforcement mutation
is exercised.

### Frozen API-key creation refusal pairs

`make test-scenario-api-key-create-refusals` requires five original cases:
`keys_create.bad_scope`, `keys_create.missing_label`, `keys_create.malformed`,
`keys_create.demo` and `keys_create.no_token`. Original requests, settings and
expectations remain unchanged. V2 explicitly records validation 422, malformed
JSON 400 and demo/authentication 403/401 Problem Details. Ten real-router
transport requests use per-transport reseeding and 20 full API-key-table
snapshots to prove all three fixture rows remain unchanged. Required DSN,
pre-setup occupancy and fixed-selector gates fail closed. No successful
credential creation, API-key-auth metadata update or external call is exercised.
These five frozen pairs remain separate from NEW acceptance.

### Frozen successful API-key creation pairs

`make test-scenario-api-key-creations` requires `keys_create.ok`,
`keys_create.meaning`, `keys_create.scoped` and `keys_create.shape`.
Original v1 expectations and requests remain unchanged. V2 records string IDs,
UTC-millisecond timestamps and creation-only secret disclosure. Eight real
transport requests reseed independently and compare 16 full-table snapshots:
exactly one new member-owned row must match the returned ID, credential, label,
normalized scopes, standard tier and timestamp, with no usage timestamp; all
three prior rows remain byte-identical. Credentials stay out of effect-assertion
messages. Required DSN, pre-setup occupancy and fixed-selector gates fail closed.
These four frozen pairs are separate from NEW acceptance; no real-user
credential, authentication usage or provider operation is exercised.

### Frozen account password capability pairs

`make test-scenario-account-capability` requires seven original
`account_capability.*` cases: `ok`, `meaning`, `no_profile`,
`secondary_profile`, `no_token`, `bad_token` and `error_shape`.
The fixed selector accepts only the supported database requirement. Original
requests, principals and expectations remain unchanged. V2 records the same
authorization decision through `allowed` with capability revision/state,
password length policy and Problem Details. Fourteen real transport requests
reseed independently; 28 complete snapshots cover users, profiles and API keys
(84 table observations), with every row unchanged. Required DSN and pre-setup
scratch/API-key occupancy gates fail closed. No password change, session
revocation, credential usage or external provider operation is exercised.
These seven frozen pairs remain separate from NEW acceptance.

### Frozen sign-in provider discovery pairs

`make test-scenario-auth-providers` requires `providers.ok`,
`providers.local_default`, `providers.shape`, `providers.sorted` and
`providers.oauth_hidden`. Original requests, database requirements and
expectations remain unchanged. V2 adds a bounded items envelope while preserving
metadata, default-first ordering and OAuth filtering. Ten real transport
requests reseed independently; 20 combined snapshots cover complete users,
profiles and API-key tables (60 table observations), with every row unchanged.
Required DSN, pre-setup scratch/API-key occupancy and fixed-selector gates fail
closed. No login, plugin installation or external provider call is exercised.
These five frozen pairs remain separate from NEW acceptance.

### Frozen account authentication refusal pairs

`make test-scenario-account-me-refusals` requires exactly `me.no_token` and
`me.bad_token`, original GET `/api/v1/auth/me` registration 0. Both retain their
public principal and original request, including `Bearer not-a-jwt`. Their
explicit v2 pair calls `getCurrentUser` at GET `/api/v2/account/me` and requires
401 Problem Details: `authentication_required` or `invalid_token`, respectively.
No successful account read, login or provider operation is part of this cohort.

Prerequisites are the accepted current-user contract, the paired executor and
its synthetic household/auth wiring, PostgreSQL with the migration-required
`vector` extension available, and an exclusively reserved **new** scratch
database supplied through `SILO_SCENARIO_DATABASE_URL`. Missing DSN fails before
construction; scratch-data and API-key guards run before `New`, migrations or
reseeding. Never point this destructive fixture executor at an existing database.
The exact selector refuses missing, duplicate or unpaired IDs, altered authority,
request or operation, and unsupported effects or follow-up exchanges.

Each transport reseeds before and after its one HTTP exchange. Complete ordered
`users`, `user_profiles` and `api_keys` snapshots must remain byte-identical,
without timestamp, password, usage or revision exemptions. The required report
contains four unique passing transport results and the runner requires four HTTP
exchanges, eight combined snapshots and 24 table observations. Table mismatches
never print row contents. This adds two original pair declarations, not NEW cases
or download/ebook scenario coverage. Acceptance requires separate retained live
and guard evidence plus independent review; declarations alone are not a pass.

### Frozen public signup-status pairs

`make test-scenario-signup-status` requires `signup_status.ok`,
`signup_status.enabled`, `signup_status.disabled` and `signup_status.shape`.
Original public requests, database requirements, settings and expectations remain
unchanged. V2 preserves the enabled boolean, including false for the original
non-true setting value. Eight real transport requests reseed independently;
16 combined snapshots cover complete users, profiles, API-key and server-settings
tables (64 table observations), with every row unchanged during each read.
Snapshots follow the original setting override, which is restored afterward.
Required DSN, pre-setup scratch/API-key occupancy and fixed-selector gates fail
closed. No signup, account creation, unavailable-database case or external
provider call is exercised. These four frozen pairs remain separate from NEW
acceptance.

### Frozen public setup-status pairs

`make test-scenario-setup-status` requires `setup_status.ok`,
`setup_status.meaning` and `setup_status.shape`. Original public requests,
database requirements and expectations remain unchanged. V2 moves the probe
to `/api/v2/system/setup` and preserves the boolean from the shared account
count. Six transport requests reseed independently; 12 combined snapshots cover
complete users, profiles, API-key and settings tables (48 table observations),
with every row unchanged. Required DSN, pre-setup scratch/API-key occupancy
and fixed-selector gates fail closed. No setup POST, account creation,
unavailable-database case or enrollment is exercised. These three frozen pairs
remain separate from NEW acceptance.

### Frozen administrator build metadata pairs

`make test-scenario-build-info` requires exactly `build.ok`, `build.meaning`,
`build.shape` and `build.no_token`. Original requests and assertions remain
unchanged. The runner compares build metadata from the same binary across both
transports: only absent timestamp omission and UTC-millisecond formatting differ.
Unauthorized v2 responses use Problem Details. Eight transport requests use
independent reseeding and sixteen combined snapshots of the full users, profiles
and API-key tables (48 table observations), without field exemptions. Required
DSN, pre-setup occupancy and fixed-selector checks fail closed. No hardware,
provider, media-catalog or previously completed scenario is exercised.

`make test-scenario-build-authority` separately requires only
`build.admin_secondary_profile`, `build.non_admin` and `build.error_shape`.
It reuses the same guarded runner with six transport requests and twelve combined
full-table snapshots (36 table observations). Original 403 assertions and
principals remain unchanged: an admin account acting through its secondary
profile differs from a non-admin account acting through its primary profile.
V2 asserts permission_denied Problem Details. This target does not rerun or count
the four build-info pairs.

`make test-scenario-resource-refusals` selects only
`resources.admin_secondary_profile`, `resources.non_admin` and
`resources.no_token`. The guarded read runner preserves original principals and
403/403/401 assertions, with v2 Problem Details. Six requests use independent
transport reseeds and twelve full account/profile/API-key snapshots (36 table
observations). These authorization refusals do not sample hardware. Successful
resource cases, their requirements and all previously paired cases are untouched.

### Frozen login-session list pairs

`make test-scenario-login-sessions` requires `sessions.ok`, `sessions.meaning`,
`sessions.shape`, `sessions.sorted`, `sessions.no_token` and
`sessions.error_shape`. Original retained-session assertions remain unchanged.
V2 intentionally lists live sessions only in a bounded collection and uses
Problem Details for refusals. The seeded member's active session is the sole
v2 result; its revoked row and other accounts' rows remain excluded. Twelve
transport requests reseed independently; 24 combined snapshots cover complete
users, profiles, API-key, settings and login-session tables (120 observations),
with every row unchanged. Required DSN, pre-setup scratch/API-key occupancy
and fixed-selector gates fail closed. No session revocation, credential usage,
provider operation or enrollment is exercised. These six frozen pairs remain
separate from NEW acceptance.

### Frozen device-login capability pairs

`make test-scenario-device-capability` requires `capability.meaning` and
`capability.shape`. Original handoff/protocol assertions remain unchanged; v2
adds revision and configured state. Four transport requests reseed independently;
eight combined snapshots cover complete users, profiles, API-key, settings,
login-session and device-login-request tables (48 observations), with all rows
unchanged. Required DSN, pre-setup scratch/API-key occupancy and fixed-selector
gates fail closed. `capability.ok` is paired separately under the outage runner
below. No pairing start, approval, poll, token collection or enrollment is
exercised. These two frozen pairs remain separate from NEW acceptance.

### Frozen retained probes (v1 only)

`make test-scenario-probes` requires `health.ok`, `health.identity`, `health.shape`,
`ready.ok`, `ready.db_down`, `ready.shape_on_failure` and `ready.meaning`. Their
rows are ratified as retained unversioned probes (owner decision 2026-09-07), so
there is no v2 transport and the selector refuses a recorded `v2_expectation`;
each original runs once against the retained v1 route with its exact oracle. The
four `database_unavailable` cases run on the executor's offline router (pool at
`127.0.0.1:1`, proven unreachable before each exchange); the three others run on
the live router over the owned database (`ready.ok` requires `database`). Seven
requests, fourteen four-table snapshots of the live database unchanged. No v2
operation, redirect or root `/health` route is asserted or added.

### Frozen database-unavailable pairs

`make test-scenario-outage` requires `setup_status.db_down`,
`signup_status.db_down` and `capability.ok`, the three frozen public reads whose
original oracle is recorded with `requires: database_unavailable`. Originals are
unchanged. The outage is the executor's existing offline router, whose pool
targets `127.0.0.1:1`; nothing is stopped, paused or killed, so the state is
identical on every run and touches no shared resource. Before each of the six
transport requests the runner proves the outage (the offline pool's ping fails
with a network error) and snapshots the owned live database's users, profiles,
API-key, settings, login-session and device-login-request tables; the twelve
snapshots (72 observations) are unchanged, and each transport reseeds
independently. V2 answers the two 500 probes as `internal_error` Problem Details
(`application/problem+json`, `Cache-Control: no-store`, fixed detail, urn
instance) instead of the legacy `error`/`message` object, recorded as
intentional differences; the capability document stays reachable without a
database and adds revision `1`, state `available` (device-login wiring, not
database reachability) and `Cache-Control: private, no-cache`. `ready.db_down`
stays out: its row is a redesign proposal awaiting human disposition. No
Postgres stop or pause, cluster outage, recovery after reconnect or real
deployment is exercised.

### Frozen pending device-login lookup pairs

`make test-scenario-device-lookup` requires `device_lookup.by_token`,
`device_lookup.by_code`, `device_lookup.meaning` and `device_lookup.shape`.
Original public requests, database requirements and assertions remain unchanged.
V2 preserves code normalization, masked address and device metadata, with an
explicit temporary boolean and empty user_code on token lookup. Eight
transport requests reseed independently; 16 combined snapshots cover complete
users, profiles, API-key, settings, login-session and device-request tables
(96 observations), with every row unchanged. Required DSN, pre-setup scratch/API-key
occupancy and fixed-selector gates fail closed. No start, approval, poll, token
collection or enrollment is exercised. These four frozen pairs remain separate
from NEW acceptance.

### Frozen expired and missing device-login lookups

`make test-scenario-device-lookup-errors` requires `device_lookup.expired`,
`device_lookup.not_found` and `device_lookup.no_params`. Original public/database
requests and assertions remain unchanged. V2 preserves expired status with 200,
uses a 404 Problem for unknown tokens, and rejects missing parameters with a
422 validation Problem instead of legacy 404. Six transport requests reseed
independently; 12 combined snapshots cover complete users, profiles, API-key,
settings, login-session and device-request tables (72 observations), with every
row unchanged, including stored expiry status. Required DSN, pre-setup scratch/
API-key occupancy and fixed-selector gates fail closed. No pairing mutation,
token collection, enrollment or outage substitution is exercised. These three
frozen pairs remain separate from NEW acceptance.

### Frozen setup refusal pairs

`make test-scenario-setup-refusals` requires `setup.already_complete`,
`setup.missing_fields` and `setup.malformed_json`. Original public requests,
database requirements and assertions remain unchanged. Against the populated
fixture, v2 returns completed-setup 409, missing-fields 422 and malformed-JSON
400 Problems. Six transport requests reseed independently; 12 combined snapshots
cover complete users, profiles, API-key, settings, login-session and device-request
tables (72 observations), with every row unchanged. Required DSN, pre-setup
scratch/API-key occupancy and fixed-selector gates fail closed. No successful
setup, enrollment or outage substitution is exercised. These three frozen pairs
remain separate from NEW acceptance.

### Frozen login-session revocation refusal pairs

`make test-scenario-session-delete-refusals` requires `session_delete.other_user`,
`session_delete.unknown` and `session_delete.no_token`. Original requests,
principals and assertions remain unchanged. V2 preserves foreign/unknown-session
404 and missing-bearer 401 while using Problem Details. Six transport requests
reseed independently; 12 combined snapshots cover complete users, profiles,
API-key, settings, login-session and device-request tables (72 observations),
with every row unchanged, including foreign sessions. Required DSN, pre-setup
scratch/API-key occupancy and fixed-selector gates fail closed. No successful
revocation, enrollment or outage substitution is exercised. These three frozen
pairs remain separate from NEW acceptance.

### Frozen device-start refusal pairs

`make test-scenario-device-start-refusals` requires `device_start.bad_purpose`
and `device_start.malformed`. Original public/database requests and assertions
remain unchanged. V2 rejects the purpose/temporary mismatch with 422 validation
and malformed JSON with a 400 Problem. Four transport requests reseed independently;
eight combined snapshots cover complete users, profiles, API-key, settings,
login-session and device-request tables (48 observations), with every row
unchanged and no new pairing request. Required DSN, pre-setup scratch/API-key
occupancy and fixed-selector gates fail closed. No successful pairing, enrollment
or outage substitution is exercised. These two frozen pairs remain separate
from NEW acceptance.

### Frozen refresh refusal pairs

`make test-scenario-refresh-refusals` requires `refresh.access_token_rejected`,
`refresh.revoked`, `refresh.garbage` and `refresh.missing`. Original public
requests, database requirements and assertions remain unchanged. Fixture-generated
synthetic tokens exercise v2 invalid_token and session_expired refusals; missing
input returns 422 validation. Problems contain no token pair. Eight transport
requests reseed independently; 16 combined snapshots cover complete users, profiles,
API-key, settings, login-session and device-request tables (96 observations),
with every row unchanged. Required DSN, pre-setup scratch/API-key occupancy and
fixed-selector gates fail closed. No successful token rotation, real authentication,
enrollment or outage substitution is exercised. These four frozen pairs remain
separate from NEW acceptance.

### Frozen administrator invitation-list refusals

`make test-scenario-admin-invitation-refusals` requires exactly
`adm_inv_list.admin_secondary_profile`, `adm_inv_list.non_admin` and
`adm_inv_list.no_token`. Original requests and assertions remain unchanged;
v2 retains 403 for the administrator secondary profile and non-admin principal,
and 401 without authentication, using Problem Details. Each transport reseeds
independently and compares every row of users, profiles, API keys, settings,
login sessions, device-login requests, invitations and invite codes before/after.
Six HTTP requests require 12 combined snapshots (96 full-table observations).
Required DSN and scratch/API-key occupancy guards run before construction.
No successful list, invitation creation, send, redemption or account action is
exercised. These three original pairs remain separate from NEW acceptance.

### Frozen administrator invitation revocation refusals

`make test-scenario-admin-invitation-revoke-refusals` requires exactly
`adm_inv_revoke.admin_secondary_profile`, `adm_inv_revoke.non_admin` and
`adm_inv_revoke.no_token`. Original DELETE requests, principals and assertions
remain unchanged; v2 preserves 403/403/401 with Problem Details. The target
invitation and every row in the eight tables listed above remain byte-identical.
Six independently reseeded transports require 12 snapshots and 96 full-table
observations. Required DSN and scratch/API-key guards run before construction.
This exercises authorization refusals, not successful revocation, invitation
sending, redemption or account creation. These original pairs are separate from
NEW acceptance.

### Frozen password-input refusal pairs

`make test-scenario-password-refusals` requires `password.wrong_current`,
`password.weak`, `password.too_long`, `password.missing_fields` and
`password.malformed_json`. Original primary-profile requests, database requirements
and assertions remain unchanged. V2 maps invalid inputs to field-validation
Problems and malformed JSON to 400. Ten transport requests reseed independently;
20 combined snapshots cover complete users, profiles, API-key, settings,
login-session and device-request tables (120 observations), with every row
unchanged, including password hashes and sessions. Required DSN, pre-setup
scratch/API-key occupancy and fixed-selector gates fail closed. Synthetic
credentials only; no successful password change, real authentication, enrollment
or outage substitution. These five frozen pairs remain separate from NEW acceptance.

### Frozen password authority refusal pairs

`make test-scenario-password-authority` requires `password.no_profile`,
`password.secondary_profile`, `password.no_token` and `password.bad_token`.
Original principals, headers, requests, requirements and assertions remain
unchanged. V2 uses permission_denied for insufficient profile authority and
authentication_required/invalid_token for bearer refusals. Eight transport
requests reseed independently; 16 combined snapshots cover complete users,
profiles, API-key, settings, login-session and device-request tables
(96 observations), with every row unchanged, including password hashes.
Required DSN, pre-setup scratch/API-key occupancy and fixed-selector gates
fail closed. No successful password change, real authentication, enrollment
or outage substitution. These four frozen pairs remain separate from NEW acceptance.

`make test-scenario-hardware-refusals` selects only
`hwaccel.admin_secondary_profile`, `hwaccel.non_admin` and `hwaccel.no_token`.
Original requests, principals, 403/403/401 assertions and requirements remain
unchanged. These requests refuse before hardware inventory dispatch or probing.
The guarded runner performs six HTTP exchanges and twelve complete account,
profile and API-key snapshots (36 table observations), reseeding independently
per transport. Successful hardware cases and their requirements are untouched;
previous build/resource cohorts are not rerun.

`make test-scenario-hardware-inventory` pairs the remaining four hardware cases:
`hwaccel.ok`, `hwaccel.meaning`, `hwaccel.shape` and `hwaccel.error_shape`.
The executor wires no transcode pool, so both transports run the same local
probe of the host's ffmpeg (`playback.DetectHWAccelWithFFmpeg`, auto). The
original oracles assert only presence and types, never probe values, so they
are host independent; the runner additionally proves every v1 field appears in
v2 with an equal value and that v2 adds none. The one intentional difference is
that v2 serializes `render_devices` and `render_device_details` as explicit
arrays where v1 emits null on a host without render devices; `hwaccel.shape`
records that. `hwaccel.error_shape` refuses the non-admin profile with a
Problem before any probe. Eight HTTP exchanges and sixteen eight-table
snapshots; every table stays byte-identical. The packet does not prove any
particular backend, device inventory, node fan-out or probe timeout.

### Frozen unauthenticated logout pairs

`make test-scenario-logout-refusals` requires `logout.no_token` and
`logout.error_shape`. Original public requests and assertions remain unchanged.
V2 returns a 401 authentication_required Problem. Four transport requests reseed
independently; eight combined snapshots cover complete users, profiles, API-key,
settings, login-session and device-request tables (48 observations), with every
row unchanged. Required DSN, pre-setup scratch/API-key occupancy and fixed-selector
gates fail closed. The API-key logout case is separate and is not covered by
these public-principal tests. No successful logout, session revocation, enrollment
or outage substitution is exercised. These two frozen pairs remain separate
from NEW acceptance.

`make test-scenario-invite-code-refusals` selects only
`codes_list.admin_secondary_profile`, `codes_list.non_admin` and
`codes_list.no_token`. Original requests, principals and 403/403/401 assertions
remain unchanged. Six HTTP refusals use independent transport reseeds and twelve
full eight-table snapshots (96 observations): accounts, profiles, API keys,
settings, login sessions, device requests, invitations and invite codes.
No successful list, invitation send, code creation or redemption runs. Required
DSN, pre-constructor occupancy and exact selector checks fail closed.

`make test-scenario-invite-code-create-refusals` selects only
`codes_create.admin_secondary_profile`, `codes_create.non_admin` and
`codes_create.no_token`. Original bodies, principals and 403/403/401 assertions
remain unchanged. Six HTTP refusals use independent transport reseeds and twelve
full eight-table snapshots (96 observations), with no field exemptions. No code
creation, successful mutation or other cohort is executed; the existing required
DSN, pre-constructor occupancy and fixed-selector guards remain mandatory.

### Frozen device-denial refusal pairs

`make test-scenario-device-deny-refusals` requires `deny.expired`,
`deny.not_found` and `deny.no_token`. Original requests, principals and
assertions remain unchanged. V2 preserves expiry 410, unknown 404 and absent
bearer 401 with domain Problem Details. Six transport requests reseed independently;
12 combined snapshots cover complete users, profiles, API-key, settings,
login-session and device-request tables (72 observations), with every row
unchanged, including stored pairing status. Required DSN, pre-setup scratch/
API-key occupancy and fixed-selector gates fail closed. No successful denial,
enrollment or outage substitution is exercised. These three frozen pairs remain
separate from NEW acceptance.

### Frozen device-approval refusal pairs

`make test-scenario-device-approve-refusals` requires `approve.expired`,
`approve.purpose_mismatch`, `approve.not_found` and `approve.no_token`. Original
requests, principals and assertions remain unchanged. V2 preserves the refusal
statuses with domain Problem Details. Eight transport requests reseed independently;
16 combined snapshots cover complete users, profiles, API-key, settings,
login-session and device-request tables (96 observations), with every row
unchanged, including approval identity and timestamps. Required DSN, pre-setup
scratch/API-key occupancy and fixed-selector gates fail closed. No successful
approval, enrollment or outage substitution is exercised. These four frozen
pairs remain separate from NEW acceptance.

### Frozen handoff approval refusal pairs

`make test-scenario-handoff-refusals` requires `handoff.no_profile`,
`handoff.locked_unverified`, `handoff.purpose_mismatch`,
`handoff.other_account_profile` and `handoff.no_token`. Original requests,
principals and assertions remain unchanged. V2 returns 422 for the missing
profile header and preserves the other refusal statuses with explicit
profile/PIN/account/purpose Problem types. Ten transport requests reseed
independently; 20 combined snapshots cover complete users, profiles, API-key,
settings, login-session and device-request tables (120 observations), with every
row unchanged, including approval and profile state. Required DSN, pre-setup
scratch/API-key occupancy and fixed-selector gates fail closed. No successful
handoff, enrollment or outage substitution is exercised. These five frozen
pairs remain separate from NEW acceptance.

`make test-scenario-invite-code-delete-refusals` selects only
`codes_delete.admin_secondary_profile`, `codes_delete.non_admin` and
`codes_delete.no_token`. Original symbolic IDs, public literal ID, principals and
403/403/401 assertions remain unchanged. Six HTTP refusals use independent
transport reseeds and twelve full eight-table snapshots (96 observations), with
no field exemptions. No successful deletion or other cohort runs. Required DSN,
pre-constructor occupancy and exact selector guards remain mandatory.

`make test-scenario-invite-code-update-refusals` selects only
`codes_update.admin_secondary_profile`, `codes_update.non_admin` and
`codes_update.no_token`. Original IDs, label bodies, principals and 403/403/401
assertions remain unchanged. Six HTTP refusals use independent transport reseeds
and twelve full eight-table snapshots (96 observations), with no exemptions.
No successful update or other cohort runs. Required DSN, pre-constructor occupancy
and exact selector guards remain mandatory.

### Frozen device-approval state refusal pairs

`make test-scenario-approval-state-refusals` requires `approve.conflict`,
`approve.denied` and `approve.disabled_user`. Original principals, requests
and assertions remain unchanged. V2 uses conflict and permission_denied Problems.
Six transport requests reseed independently; 12 combined snapshots cover complete
users, profiles, API-key, settings, login-session and device-request tables
(72 observations), with every row unchanged, including existing approver identity
and denial timestamps. Required DSN, pre-setup scratch/API-key occupancy and
fixed-selector gates fail closed. No successful approval, enrollment or outage
substitution is exercised. These three frozen pairs remain separate from NEW acceptance.

`make test-scenario-invite-code-topup-refusals` selects only
`codes_topup.admin_secondary_profile`, `codes_topup.non_admin` and
`codes_topup.no_token`. Original IDs, additional-use bodies, principals and
403/403/401 assertions remain unchanged. Six HTTP refusals use independent
transport reseeds and twelve full eight-table snapshots (96 observations), with
no exemptions. No successful top-up or other cohort runs. Required DSN,
pre-constructor occupancy and exact selector guards remain mandatory.

### Frozen impersonation-end refusal pairs

`make test-scenario-impersonation-refusals` requires `imp_end.not_impersonating`
and `imp_end.no_token`. Original requests, principals and assertions remain
unchanged. V2 uses 409 conflict for an ordinary session and 401 authentication
required for an absent bearer. Four transport requests reseed independently;
eight combined snapshots cover complete users, profiles, API-key, settings,
login-session and device-request tables (48 observations), with every row
unchanged. Required DSN, pre-setup scratch/API-key occupancy and fixed-selector
gates fail closed. No impersonation creation, session revocation, enrollment or
outage substitution is exercised. These two frozen pairs remain separate from
NEW acceptance.

`make test-scenario-invite-code-create-input` selects only
`codes_create.zero_uses` and `codes_create.malformed`. Original administrator
requests and v1 400 assertions remain unchanged. V2 returns 422 validation for
the zero-use request, which also omits its required client-selected code; this
is not isolated proof of the maximum-use check. Malformed JSON remains 400.
Four HTTP refusals use independent transport reseeds and eight full eight-table
snapshots (64 observations), without exemptions or successful creation.
Required DSN, pre-constructor occupancy and exact selector guards remain mandatory.

`make test-scenario-invite-code-update-input` selects only
`codes_update.bad_id` and `codes_update.malformed`. Original administrator
requests and v1 400 assertions remain unchanged. V2 returns 422 for invalid
path input and 400 for malformed JSON. Four HTTP refusals use independent
transport reseeds and eight full eight-table snapshots (64 observations), with
no exemptions or successful update. Required DSN, pre-constructor occupancy
and exact selector guards remain mandatory.

`make test-scenario-invite-code-topup-input` selects only
`codes_topup.zero` and `codes_topup.bad_id`. Original administrator requests and
v1 400 assertions remain unchanged; v2 returns 422 validation Problems. Four
HTTP refusals use independent transport reseeds and eight full eight-table
snapshots (64 observations), with no exemptions or successful top-up. Required
DSN, pre-constructor occupancy and exact selector guards remain mandatory.

### Frozen login input refusal pairs

`make test-scenario-login-input` requires `login.unknown_provider`,
`login.missing_fields` and `login.malformed_json`, preserving every original
request, principal and assertion. V2 reports unknown provider credentials as
401 invalid_token, empty required fields as 422 validation_failed, and malformed
JSON as 400 malformed_request. Six transport requests reseed independently;
twelve combined snapshots compare complete users, profiles, API-key, settings,
login-session and device-request tables (72 observations). All rows must remain
unchanged, and responses must omit token pairs. Required DSN, pre-setup scratch
and API-key occupancy guards and fixed-selector checks fail closed. This scope
does not exercise valid credentials, provider calls, enrollment or outages.
These three original frozen pairs remain separate from NEW acceptance.

`make test-scenario-invite-code-delete-input` selects only
`codes_delete.bad_id`. The original administrator request and v1 400 assertions
remain unchanged; v2 returns a 422 validation Problem. Two HTTP refusals use
independent transport reseeds and four full eight-table snapshots (32 observations),
with no exemptions or successful deletion. Required DSN, pre-constructor
occupancy and exact selector guards remain mandatory.

`make test-scenario-admin-invitation-resend-refusals` selects only
`adm_inv_resend.admin_secondary_profile`, `adm_inv_resend.non_admin`, and
`adm_inv_resend.no_token`. Original literal IDs, principals, requests and
403/403/401 assertions remain unchanged. Six HTTP refusals use independent
transport reseeds and twelve full snapshots of users, user_profiles, api_keys,
server_settings, auth_sessions, device_login_requests, invitations and invite_codes
(96 table observations), with no exemptions. Required DSN and pre-constructor
occupancy guards run before setup. No successful resend or other cohort runs.

### Frozen local login credential refusals

`make test-scenario-login-credentials` selects `login.wrong_password`,
`login.unknown_user` and `login.disabled`. Original requests, public principals,
database requirements and assertions remain unchanged. V2 uses 401 invalid_token
for invalid credentials and 403 permission_denied for the disabled account.
Six real-router requests reseed independently; twelve combined full snapshots
compare users, profiles, API keys, settings, login sessions and device requests
(72 table observations), with no exemptions. Token pairs must be absent.
Required DSN, pre-constructor occupancy and fixed-selector guards fail closed.
No successful login, external provider, enrollment or outage is exercised.
These three original frozen pairs remain separate from NEW acceptance.

`make test-scenario-invite-code-list-error` selects only `codes_list.error_shape`.
The original non-admin request and v1 403 envelope assertions remain unchanged;
v2 returns its typed permission-denied Problem envelope. Two HTTP refusals use
independent transport reseeds and four full eight-table snapshots (32 observations),
without exemptions or successful list requests. Required DSN, pre-constructor
occupancy and exact selector guards remain mandatory.

`make test-scenario-invite-code-list-reads` selects only `codes_list.ok`,
`codes_list.meaning`, `codes_list.shape` and `codes_list.sorted`. Original v1
requests and assertions remain unchanged. V2 projects the collection envelope,
string IDs and descending-ID order with the exact seeded label sequence. All
three records fit one terminating page; this is not multi-page traversal proof.
Eight HTTP reads use independent transport reseeds and sixteen full eight-table
snapshots (128 observations), without exemptions. Required DSN, pre-constructor
occupancy and exact selector guards remain mandatory.

### Frozen device poll refusals

`make test-scenario-device-poll-refusals` selects `device_poll.unknown` and
`device_poll.missing`. Original requests, public principals, requirements and
404/400 assertions remain unchanged. V2 uses 404 not_found and 422
validation_failed Problems without tokens. Four real-router requests reseed
independently; eight combined full snapshots compare users, profiles, API keys,
settings, login sessions and device requests (48 table observations), with no
exemptions. Required DSN, pre-constructor occupancy and fixed-selector guards
fail closed. No successful polling, token redemption, enrollment or outage is
exercised. These two original frozen pairs remain separate from NEW acceptance.

`make test-scenario-admin-invitation-create-refusals` selects only
`adm_inv_create.admin_secondary_profile`, `adm_inv_create.non_admin`, and
`adm_inv_create.no_token`. Original bodies, principals, requests and 403/403/401
assertions remain unchanged. Six HTTP refusals use independent transport reseeds
and twelve full snapshots of users, user_profiles, api_keys, server_settings,
auth_sessions, device_login_requests, invitations and invite_codes (96 table
observations), without exemptions. Required DSN and pre-constructor occupancy
guards run before setup. No successful creation, send or other cohort runs.

### Frozen device poll state reads

`make test-scenario-device-poll-states` selects `device_poll.pending`,
`device_poll.denied` and `device_poll.expired`. Original requests, public
principals, database requirements and 200 assertions remain unchanged. V2
reports the same state and three-second polling interval with empty profile
fields, temporary=false and no tokens or session expiry. Six real-router
requests reseed independently; twelve combined full snapshots compare users,
profiles, API keys, settings, login sessions and device requests (72 table
observations), with no exemptions. Required DSN, pre-constructor occupancy and
fixed-selector guards fail closed. No approved/consumed flow, token issuance,
enrollment or outage is exercised. These three original frozen pairs remain
separate from NEW acceptance.

`make test-scenario-invite-code-missing` selects only `codes_update.not_found`
and `codes_topup.not_found`. Original administrator requests and v1 404
assertions remain unchanged; v2 returns typed not-found Problems. Four HTTP
refusals use independent transport reseeds and eight full eight-table snapshots
(64 observations), with no exemptions or successful mutation. Required DSN,
pre-constructor occupancy and exact selector guards remain mandatory.

`make test-scenario-admin-invitation-input-refusals` selects only
`adm_inv_create.invalid_email` and `adm_inv_create.malformed`. Original administrator
requests, structured/raw bodies and v1 400 assertions remain unchanged. V2 returns
422 for the invalid email and 400 for malformed JSON. Four HTTP refusals use
independent transport reseeds and eight full snapshots of users, user_profiles,
api_keys, server_settings, auth_sessions, device_login_requests, invitations and
invite_codes (64 table observations), without exemptions. Required DSN and
pre-constructor occupancy guards run before setup. No successful creation or send.

### Frozen signup refusals

`make test-scenario-signup-refusals` selects `signup.disabled_setting` and
`signup.missing_fields`, preserving original requests, principals, requirements,
settings and assertions. V2 reports 403 permission_denied and 422 validation_failed
without token pairs. The original missing-code request also has a one-character
password; its 422 is not isolated missing-code validation proof. Four real-router
requests reseed independently. Eight full snapshots cover users, profiles, API
keys, settings, login sessions, device requests, invitations and invite codes
(64 table observations), after applying original settings, with no exemptions.
Required DSN, pre-constructor occupancy and fixed-selector guards remain enforced.
No signup success, enrollment, external provider or outage is exercised. These
two original frozen pairs remain separate from NEW acceptance.

`make test-scenario-admin-invitation-resend-targets` selects only
`adm_inv_resend.bad_id` and `adm_inv_resend.not_found`. Original administrator
requests, target IDs and v1 400/404 assertions remain unchanged. V2 returns 422
for the invalid path and 404 for the missing record. Four HTTP refusals use
independent transport reseeds and eight full snapshots of users, user_profiles,
api_keys, server_settings, auth_sessions, device_login_requests, invitations and
invite_codes (64 table observations), without exemptions. Required DSN and
pre-constructor occupancy guards run before setup. No successful resend occurs.

`make test-scenario-invite-code-duplicate` selects only `codes_create.duplicate_code`.
The original administrator request and v1 500 oracle remain unchanged; v2 maps
the conflicting configuration to 409. Two HTTP exchanges use independent reseeds
and four full eight-table snapshots (32 observations), without row exemptions.
Both failed insert attempts allocate one invite-code sequence value; four sequence
observations explicitly require that effect. No successful creation or retry
acceptance is claimed. Required DSN, pre-constructor occupancy and exact selector
guards remain mandatory.

### Frozen signup invite-code refusals

`make test-scenario-signup-codes` selects `signup.bad_code`,
`signup.exhausted_code` and `signup.disabled_code`, preserving original requests,
principals, requirements and 400 assertions. V2 returns 422 validation_failed
with the exact invite-code field and refusal detail, without credentials.
Six requests reseed independently; twelve full snapshots cover users, profiles,
API keys, settings, login sessions, device requests, invitations and invite codes
(96 table observations), with no exemptions. Code redemption refuses before
account insertion; no code use is consumed. Required DSN, pre-constructor
occupancy and fixed-selector guards remain enforced. No successful signup,
enrollment, external provider or outage is exercised. These three original
frozen pairs remain separate from NEW acceptance.

`make test-scenario-admin-invitation-role-refusals` selects only
`adm_inv_create.bad_role` and `adm_inv_create.admin_grouped`. Original administrator
requests, bodies and v1 403/422 assertions remain unchanged. V2 returns 422 validation
Problems. The original grouped request sends a numeric access_group_id, rejected
by the string schema: this is not isolated proof of the admin-group domain rule.
Four HTTP refusals use independent reseeds and eight full snapshots of users,
user_profiles, api_keys, server_settings, auth_sessions, device_login_requests,
invitations and invite_codes (64 table observations), without exemptions. Required
DSN and pre-constructor occupancy guards run before setup. No creation or send succeeds.

`make test-scenario-invite-code-empty-update` selects only `codes_update.partial`.
The original administrator request, empty object and 204 empty-body oracle remain
unchanged. Both transports check the seeded code exists without changing fields
or timestamps. Two HTTP exchanges use independent transport reseeds and four
full eight-table snapshots (32 observations), without exemptions. This does not
cover updates that supply fields. Required DSN, pre-constructor occupancy and
exact selector guards remain mandatory.

`make test-scenario-invite-code-field-updates` selects only `codes_update.ok`,
`codes_update.disable` and `codes_update.shape`. Original requests, fresh-state
declarations and 204 empty-body oracles remain unchanged. Six exchanges use
independent reseeds and twelve full eight-table snapshots (96 observations).
Only the seeded target's requested label or enabled field and its `updated_at`
may change. The timestamp must fall between database clocks around the request;
every other code column and every other table row must match exactly. No broad
retry or concurrent-writer claim. Required DSN, pre-constructor occupancy and
exact selector guards remain mandatory.

### Frozen impersonated account read

`make test-scenario-me-impersonation` selects only `me.impersonation`, retaining
the original fresh-state requirement, request and identity assertions. Both
transports use the fixture session issued by `auth.Service.StartImpersonation`;
v2 returns the administrator account ID as a string. Two reads independently
reseed before and after execution. Four full snapshots compare users, profiles,
API keys, settings, login sessions, device requests, invitations and invite codes
(32 table observations), with no exemptions. Required DSN, pre-constructor
occupancy and fixed-selector guards remain enforced. Fixture setup creates the
impersonation session before observation; this scope proves account-read
semantics, not a paired impersonation-creation operation. No external provider
or outage is exercised. This original pair remains separate from NEW acceptance.

`make test-scenario-invite-code-topups` selects only `codes_topup.ok`,
`codes_topup.meaning` and `codes_topup.shape`. Original requests, fresh-state
flags and response oracles remain unchanged. V2 verifies updated counts, string
IDs and full row shape. Six exchanges use independent reseeds and twelve full
eight-table snapshots (96 observations). Only the target's `max_uses` increment
and database-clock-bounded `updated_at` may change; every other column/row must
match. No retry or concurrent top-up claim. Required DSN, pre-constructor
occupancy and exact selector guards remain mandatory.

`make test-scenario-admin-invitation-revoke-targets` selects only
`adm_inv_revoke.bad_id` and `adm_inv_revoke.not_found`. Original administrator
DELETE requests and 400/404 oracles remain unchanged; v2 returns 422 validation
and 404 not-found Problems. Four requests use independent transport reseeds
and eight full eight-table snapshots (64 observations), with no row or timestamp
exemptions. Required DSN, pre-constructor occupancy and exact selector guards
remain mandatory. This does not cover successful revocation or delivery.

### Frozen ordinary account reads

`make test-scenario-account-reads` selects `me.ok` and `me.meaning`, retaining
original requests, authenticated principals, requirements and assertions. V2
returns the same current account and effective download policy with a string
account ID and no impersonation object. Four reads reseed independently before
and after execution. Eight full snapshots compare users, profiles, API keys,
settings, login sessions, device requests, invitations and invite codes
(64 table observations), with no exemptions. Required DSN, pre-constructor
occupancy and fixed-selector guards remain enforced. No API-key usage,
impersonation, enrollment, external provider or outage is exercised. These two
original frozen pairs remain separate from NEW acceptance.

### Frozen successful logout

`make test-scenario-logout-success` selects `logout.ok` and `logout.shape`,
retaining original requests, authenticated principals, fresh-state requirements
and empty 204 assertions. Four requests independently reseed before and after
execution. Eight full snapshots cover users, profiles, API keys, settings, login
sessions, device requests, invitations and invite codes (64 table observations).
Only the caller's login session may change: revoked_at must move from null to a
timestamp within database clock readings around the request. Every other column
and row remains unchanged. Required DSN, pre-constructor occupancy and
fixed-selector guards remain enforced. This scope does not cover repeated
logout, API-key usage, enrollment, external providers or outages. These two
original frozen pairs remain separate from NEW acceptance.

`make test-scenario-invite-code-deletions` selects only `codes_delete.ok`,
`codes_delete.gone` and `codes_delete.shape`. Original targets, fresh-state flags,
204 empty-body and final 404 oracles remain unchanged, including `repeat: 2`.
Six transport results cover eight HTTP requests and twelve full eight-table
snapshots (96 observations), with independent transport reseeds. Exactly the
selected usable or disabled code must disappear; every surviving column and
other table row must match. Required DSN, pre-constructor occupancy and exact
selector guards remain mandatory. No concurrent deletion or durable replay claim.

`make test-scenario-admin-invitation-email-conflicts` selects only
`adm_inv_create.email_taken` and `adm_inv_resend.accepted`. Original requests
and 409 email-taken oracles remain unchanged; v2 returns typed 409 conflict
Problems. The accepted resend fixture already has an account at its address:
this proves email collision, not a general accepted-state refusal. Four requests
use independent reseeds and eight full eight-table snapshots (64 observations),
with no row or timestamp exemptions. Required DSN and pre-constructor guards
remain mandatory. No token creation, invitation write or delivery is reached.

### Frozen login and session lifecycle batch

`make test-scenario-auth-lifecycle` runs ten original cases: login.ok,
login.user_meaning, login.email_alias, login.grouped_download_policy,
login.admin_permissions, login.unknown_fields_ignored, refresh.ok,
refresh.rotation, refresh.shape and logout.session_gone. Original requests,
principals, requirements, repeats and assertions stay fixed. V2 adds no-store
to credentials and string account IDs, rejects the original unknown login field,
and reports a revoked session as session_expired.

Twenty transport results execute 22 HTTP requests with independent reseeding
before and after each transport. Forty full snapshots compare users, profiles,
API keys, settings, login sessions, device requests, invitations and invite codes
(320 table observations). Login must add exactly one session bound to both
cryptographically validated returned tokens, the expected account/role and
request device/IP. Its creation time is database-clock bounded and expiry is
application-clock bounded at the configured refresh lifetime. Refresh retains
that existing session identity and changes only its expiry, bounded in the same
way. Both returned token kinds and configured lifetimes are checked. Repeated
logout must revoke only the caller session within database request-time bounds
and refuse its bearer on the second request. Unknown-field v2 refusal changes
no rows. Every other field and row remains identical. These are exact admitted
effects, not blanket timestamp exemptions.

Required DSN, pre-constructor scratch/API-key guards and the fixed batch selector
fail closed. No rate-limit, outage, provider, enrollment, profile mutation,
concurrent refresh or durable replay coverage is claimed. Refresh credential
strings need not differ when minted within the same second; session identity,
signatures, token kinds and expiry extension are the verified semantics.
These ten original pairs remain separate from NEW acceptance.

`make test-scenario-household-delete-pin` selects the fourteen remaining frozen
profile deletion/PIN cases declared by `RequiredHouseholdDeletePINScenarios`.
It includes successful child-profile deletion and PIN verification plus authority,
missing-target and malformed-input refusals. All original requests/oracles remain
unchanged. Twenty-eight exchanges use independent transport reseeds and fifty-six
full nineteen-table snapshots (1064 observations), including profile deletion
dependencies. Only the successful child profile may disappear; all other stored
rows remain unchanged. The PIN success asserts a token and nullable v2 expiry,
not a token-use or native adoption flow. Related child rows are absent in this
fixture, so populated cascade deletion is not claimed. Required DSN and pre-New
scratch/API-key guards remain mandatory; prior negative guard evidence is reused.

`make test-scenario-admin-invitation-lifecycle` selects eighteen original
administrator invitation cases: six list reads/refusals, five creates, three
resends and four revokes. Original oracles and settings are preserved. V2 lists
use items/page, IDs are strings, resend creation returns 201, and delivery is
explicitly not_configured. The scoped-key list case is excluded because its
asynchronous usage write requires a separate observation boundary.

The guarded synthetic database has no email settings before dispatch, exercising
the real unconfigured-SMTP path without external sends. Each transport reseeds
independently; 36 results issue 38 requests with 74 full eight-table snapshots
(592 table observations), including intermediate repeated-revoke equality.
Invitation sequence changes are checked separately. Only expected new rows and
target revocation timestamps may change; every other column and table is equal.
Claim URLs match inserted token hashes and fresh 32-byte tokens. Created/updated
times are bounded by database request clocks; expiry uses application-clock bounds plus the exact seven-day TTL,
with the lower bound truncated to PostgreSQL microsecond storage precision. No
real email delivery, concurrent resend/accept race or durable retry claim.

### Household profile updates

`make test-scenario-household-update` selects ten original profile-update cases:
self-service credits and manager recap updates, other-profile refusal, missing
profile, conflicting/blank name, invalid quality, foreign declared/path profiles,
and missing bearer. The original v1 requests and assertions remain unchanged;
v2 uses PATCH, canonical profile responses and Problems, including 422 for invalid
fields. Existing profile-update pairs are excluded.

The required runner checks its selected exchanges and scratch database before
constructing the router, then reseeds before and after each transport. Twenty
HTTP exchanges produce forty full snapshots across twenty-two tables, including
profile relationships and canonical settings mutation/migration tables. Successful
updates change only the target flag and application-clock-bounded timestamp, plus
one profile-scoped canonical setting with the exact identity, value, revision,
defaults, sequence allocation and database-clock-bounded timestamps. All other
rows and columns stay unchanged; refusals allocate no setting identity. Credential
fields remain absent from v2 responses. The proof covers fresh-setting insertion,
not existing-setting revision updates, concurrent writes or retry safety.

### Frozen password and session transitions

`make test-scenario-password-sessions` runs password.ok, password.meaning,
password.shape, session_delete.ok, session_delete.meaning and session_delete.shape.
This six-case batch preserves the original multi-step password sequence and
repeated deletion request. Each transport reseeds independently; each password
follow-up observes the state established by its preceding step. Twelve transport
results execute eighteen HTTP requests with thirty-two full eight-table snapshots
(256 observations). The eight tables are users, profiles, API keys, settings,
login sessions, device requests, invitations and invite codes.

Password replacement may change only the target account hash, a database-clock-
bounded updated_at, and the trigger-maintained admin_revision by exactly one. Bcrypt checks prove the old/new credential transition; the
original follow-ups refuse the old password and return a valid new-password
login whose exact session/token effects use the accepted lifecycle checker.
Session deletion changes only target revoked_at within database request bounds,
including timestamp refresh on an already-revoked row. Repeated current-session
deletion must refuse its now-invalid bearer. All other fields/rows remain equal,
including all sessions during the password update itself. No blanket timestamp
or hash exemptions are used. V2 preserves empty204 responses and expresses the
old-password/revoked-session refusals as invalid_token/session_expired Problems.

Required DSN, pre-constructor scratch/API-key guards and fixed-selector checks
remain enforced. The original six-case scope is kept together because it spans
credential changes, sequential authentication and session mutation. This does
not claim concurrent password changes, API-key usage, profile/PIN mutation,
external providers, enrollment, outages or durable replay. The six original
pairs remain separate from NEW acceptance.

`make test-scenario-invitation-token-lifecycle` selects the fourteen original
non-`.r1` invitation lookup/acceptance cases. Original requests, requirements,
oracles and single-use repeat are preserved. V2 reports acceptance capability,
typed errors, and committed acceptance with a separate signed-in token envelope.

Synthetic account/default-profile/session provisioning and invitation consumption
are checked as invitation effects only. Twenty-eight results issue thirty HTTP
requests with fifty-eight eleven-table snapshots (638 observations); the
intermediate single-use snapshot proves the second accept changes nothing.
All preexisting rows/columns remain exact except the consumed invitation fields.
New rows retain fixture-equivalent defaults with new account revisions exactly1,
requested identity, verified bcrypt
hash and bounded timestamps. Issued access/refresh signatures, identities and
lifetimes match the new account/session. Account sequence effects are explicit.
Database timestamps use database bounds; profile and session/token expiry use
application bounds at their storage precision. No account login, refresh, profile
CRUD or rate-limit case is added; no real enrollment or network delivery occurs.
Required scratch guards, original pertransport reseeding, full effects and exact
cleanup remain mandatory; prior negative guard evidence may be reused.

### Household profile creation

`make test-scenario-household-create` selects all sixteen original profile-create
cases, including default/PIN/preset creation, administrator secondary-profile
creation, household authorization, validation and sequential profile-limit refusal.
Original requests and assertions remain fixed. V2 uses canonical fields, string
library IDs, no-store and validation Problems; the original unknown-library v1
500 remains a database rejection, paired with explicit v2 422 validation.

The guarded runner reseeds before and after each transport. Thirty-two results
issue thirty-four requests and capture sixty-six full snapshots across twenty-three
tables (1,518 observations). The limit case observes first creation and verifies
the second request leaves its complete intermediate snapshot unchanged.
Successful creation adds exactly one profile and five canonical settings with
exact defaults, identity, revision and sequence allocation. PIN creation checks
the bcrypt hash and exact account access-policy/admin revision increments. All
other rows and columns remain unchanged; timestamps use their writer's clock.
This covers synthetic household creation, not real enrollment, concurrent limits,
uncertain retries, existing settings inheritance or post-commit PIN failure.

### Device approval, denial and credential consumption

`make test-scenario-device-decisions` runs thirteen original approval, denial and
polling scenarios through the real router and device-login service. It preserves
all original requests, callers, repeats and follow-up steps. Each transport is
reseeded before and after its case. The packet observes 34 HTTP exchanges and
68 complete snapshots across the same eight account/credential tables (544 table
observations), including each exchange of repeated approval and polling.

Approval checks the exact account binding and decision timestamps; repeated
approval preserves every row. Denial clears the approved account/profile and
approval timestamp, including an approved request, while repeated denial is a
no-op. The original administrator denial caller remains unchanged. Polling must
issue credentials once, insert exactly one session, and consume exactly its
request. The next poll returns consumed without credentials or stored changes.
Signed access/refresh tokens bind the member account, role and new session with
exact token kinds and lifetimes. Temporary polling additionally validates the
signed profile proof against the current account policy revision and the actual
unlocked primary profile. Its session expiry is capped at 24 hours; wire expiry
must equal the stored expiry at v1 second or v2 millisecond precision.

V2 nests credentials under `tokens`, uses string account IDs, supplies explicit
empty ordinary-profile fields and returns `Cache-Control: no-store`. The original
v1 absent-cache-header expectation remains intact. Full snapshots permit only
explicitly checked decision/consumption columns and the validated session insert.
Required DSN and pre-constructor scratch/API-key guards remain enforced. This
packet does not exercise handoff approval, PIN changes, concurrent consumption,
uncertain-response replay, external providers or real enrollment. Polling remains
non-retryable; these thirteen original pairs are separate from NEW acceptance.

### Onboarding flow and state reads

`make test-scenario-onboarding-reads` selects the ten original flow cases and seven
original state cases. Progress writes are excluded. Original v1 requests, profile
principals, child-to-parent follow-up and assertions stay fixed. V2 translates the
original `TV` surface to its declared lowercase `tv`, retains the filtered flow
meaning, and uses validation Problems, no-store and an ETag on state responses.

The required guarded runner reseeds before and after each transport and snapshots
all twenty-five tables before and after every HTTP request, including the child's
parent-profile follow-up. Thirty-four results issue thirty-six requests with
seventy-two snapshots (1,800 table observations). Every row and column stays
unchanged, including the empty onboarding table; reading fresh state must not
persist a progress row. Canonical/legacy settings and request gate settings are
included. This proves the original fresh-state reads and filtering, not progress
writes, conditional requests, concurrent progress changes or actual user onboarding.

### Device forget and settings-clear authority

`make test-scenario-device-removal` pairs the remaining seven device-forget and
six device-clear originals. The unchanged requests cover the active profile,
explicit sibling profile, missing named profile, other-profile and other-account
probes, absent profile header and missing bearer. V2 retains empty 204 success
and uses Problems for refusals, including 422 for the required profile header;
the original v1 400 expectation remains intact.

Before each transport the guarded fixture populates canonical and legacy device
settings for eight registry identities. Overlapping device IDs on sibling and
other accounts ensure deletion cannot silently cross an account/profile boundary.
Three profile-level canonical values prove device clearing preserves inherited
settings. Forgetting removes exactly the target registry row and its one value
in each settings generation. Clearing removes exactly those two settings rows
and preserves the registry. All refusals preserve every stored row.

The thirteen pairs produce 26 HTTP exchanges and 52 full snapshots across
fourteen tables (728 table observations): users, profiles, API keys, server
settings, login sessions, device-login requests, invitations, invite codes,
device registry, legacy device settings, canonical setting values, mutation
receipts, migration rejects and legacy user settings. Every other field and row
must remain identical, including all stored login sessions. Each transport is
reseeded before and after execution. Required DSN and pre-constructor scratch
and API-key guards remain mandatory.

These operations remain non-retryable. The source preflight checks the v2
account advisory lock and transaction, but this packet does not claim concurrent
writes, bridge atomicity, runtime-device logout, realtime delivery or retry
recovery. It changes no household profiles or PINs and performs no real enrollment.
The thirteen original pairs remain separate from NEW acceptance.

### Onboarding progress writes

`make test-scenario-onboarding-progress` selects the nine original progress cases.
The original POST bodies, empty success responses and completion follow-ups remain
unchanged. Successful v2 sequences read the current state ETag before guarded PUT;
omitted legacy tour IDs become the explicit current tour ID. PUT returns the
state and ETag. Refusals retain their intent through typed Problems.

Eighteen paired results execute twenty-eight requests with fifty-six full snapshots
across twenty-five tables (1,400 observations). Every prerequisite read and original
follow-up has independent before/after snapshots. Successful writes affect only the
calling profile's current-tour row, with exact fields and revision increments;
completion survives the later plain progress write byte-for-byte. Timestamps are
bounded by the application request clock at the original writer's precision. All
other rows/columns remain unchanged. Required scratch guards and pertransport
reseeding apply. This proves the selected sequential writes, not concurrent writers,
uncertain retries, stale-precondition recovery or real-user onboarding.

### Avatar uploads and deletion

`make test-scenario-avatar` pairs eleven original avatar-upload scenarios and
eight avatar-delete scenarios. Original requests and oracles remain unchanged,
including `avatar_upload.typed_nil_panic` and `avatar_upload.meaning`: the actual
missing-store v1 configuration still produces an empty 500 for those valid image
requests. V2 wraps the panic in an internal-error Problem. This is historical
behavior coverage, not evidence that uploads succeed without storage.

V2 parses multipart before service-level target lookup, so an absent multipart
body can yield 415 before an unknown or foreign path reaches profile lookup.
Declared foreign-profile and bearer checks still run before the handler. Invalid
or missing avatar parts use 422 Problems. V2 avatar deletion returns empty 204
instead of the v1 profile response; each transport keeps its own explicit oracle.

Delete cases use a private local S3 protocol fixture through the real S3 client
and router. Uploaded references and objects for the member's primary/secondary
profiles and another account are populated, plus a prefix lookalike and an
unrelated preset profile. Successful uploaded-avatar deletion must clear exactly
the target reference, constrain its updated timestamp to the writer's second
precision, list exactly its prefix, and delete the original and display objects.
All unrelated object bytes and rows remain equal. The original no-avatar meaning
case preserves both the profile and orphan objects without contacting storage.
Refusals likewise preserve all state and make no object calls.

The nineteen pairs produce 38 HTTP exchanges and 76 full snapshots across the
same fourteen account/profile/settings tables as the device-removal packet
(1,064 table observations). Six uploaded-avatar deletion transports each make
one actual S3 list and two delete requests. Each transport starts and ends with
a reseed; object endpoints are isolated and closed with their test. Required DSN
and pre-constructor scratch/API-key guards remain mandatory.

The packet does not prove successful avatar upload, disabled-storage recovery,
concurrent replacement, durable retries, failed object cleanup, realtime delivery
or atomic database/object changes. Both mutations remain non-retryable. The
nineteen original pairs remain separate from NEW acceptance.

### Invitation-code creation successes

`make test-scenario-invite-code-creation` selects the four remaining original
creation-success cases: status, generated code, explicit code and timestamp shape.
V1 requests that omit the code remain unchanged and require server-generated
8-character codes. The accepted v2 contract instead requires a caller-chosen code;
the paired harness generates that identity once before dispatch and requires the
response and inserted row to retain it. Explicit `FIXTURE7` remains verbatim.
This is an intentional contract difference, not v2 server-generation coverage.

Eight results execute eight requests with sixteen complete eight-table snapshots
(128 observations). Each transport reseeds before and after its exchange. Creation
must add exactly one code and consume exactly one sequence identity. All prior rows
and unrelated tables remain unchanged. The new row has exact creator, label,
maximum, zero uses, enabled default and database-clock-bounded equal timestamps;
the response must match its committed identity and configuration. This covers
fresh issuance, not conflict resolution, retries, redemption, native code generation
or real enrollment. Previously accepted refusal and top-up cases are excluded.

### Login rate-limited registration

`make test-scenario-login-r1` executes the thirteen original `login.*.r1`
scenarios. The selector requires registration index 1, and every exchange uses
the real limited router on both transports. These are distinct executions of
the frozen registrations, separate from the completed non-r1 login scenarios.
Existing credential-effect assertions are reused, but no earlier test result
supplies acceptance credit.

Each transport reseeds its database and invokes the existing `resetRateLimits`
mechanism. Reload clears both per-key and global memory counters. The runner
checks the enabled original login configuration: burst 10, 20 requests per minute.
It neither changes thresholds nor substitutes a fake clock. The rate-limit case
retains its exact eleven-request sequence, observes ten credential refusals and
then 429, and requires the entire sequence to finish before the first three-second
token replenishment interval. Retry-After is bounded; the v1 body/header delay and
reset timestamp must agree with the limiter's clock. V2 preserves Retry-After
and intentionally omits legacy X-RateLimit headers and the retry_after body field.

Successful login requests must add exactly one valid login session with signed
account/role/session-bound access and refresh tokens and the original lifetimes.
All refusal requests, including every burst request, preserve complete database
state. Unknown fields retain v1 admission and v2 validation refusal. The 26 paired
results cover 46 HTTP exchanges and 92 complete snapshots across the same fourteen
tables as the device-removal packet (1,288 table observations). Every transport
reseeds before and after execution; required DSN and scratch/API-key guards remain.

The accepted integration's reload-serialization mutex is outside this packet's
serial reload stimulus; no concurrent reload, Redis limiter, distributed clock,
refill-after-wait, durable login replay or outage behavior is claimed. Login
remains non-retryable. These thirteen original pairs are separate from NEW
acceptance and do not rerun the closed non-r1 scenarios.

### Signup registrations

`make test-scenario-signup-family` requires the guarded scratch database and executes
all fourteen remaining signup originals independently: four registration-zero cases
and ten registration-one cases. Registration one uses the real rate-limited router;
its seven-request burst retains the default six-request budget and real clock.
Every request, including each admitted and refused burst request, has full before/after
snapshots across 26 account, profile, credential, code, settings and related tables.

Successful signup must add exactly one account, primary profile and login session,
consume exactly one code use, and preserve every existing row and unrelated table.
The checks verify the stored password hash, default account policy, profile ownership,
code rollback on duplicate refusal, signed access/refresh identity and bounded lifetimes.
Both original registrations execute afresh; no earlier refusal result is copied.
V2 explicitly projects string account IDs, no-store credentials and problem errors;
v1 requests, status, response oracles and requirements remain unchanged.
Each transport reseeds before and after execution. The evidence proves these selected
sequential flows, not concurrent redemption, uncertain retry recovery or real enrollment.

### Invitation token rate-limited registration

`make test-scenario-invitation-r1` executes all sixteen original lookup and
acceptance scenarios at registration index 1. Each transport uses the real
limited router, reseeds before and after execution, and resets counters through
the existing limiter reload mechanism. Closed non-r1 scenarios supply no execution
credit. Only explicit `v2_expectation` declarations translate the v2 contract;
the original requests and assertions remain unchanged.

Each rate-limit case observes ten unknown-token refusals followed by the original
429 within the first three-second refill interval, with the unchanged burst of
10 and rate of 20 per minute. Retry-After and the original v1 delay/reset fields
are checked against the actual clock. V2 retains Retry-After and omits the legacy
rate-limit headers and body field. Lookup preserves expired, accepted, unknown,
and trailing-slash behavior through its explicit v2 canonical path mapping.

Successful acceptance verifies exactly one committed account, default profile,
and login session, the password hash and signed token authority/lifetimes, and
only the intended invitation's acceptance fields. Single-use observes the first
201 and its effects separately from the second 404. Every request checks the
account sequence and complete before/after snapshots of sixteen tables, including
invitations and invite codes. Untouched rows remain byte-identical; refusals and
reads preserve every table. The 32 paired results cover 74 HTTP requests,
148 snapshots and 2,368 table observations. SMTP configuration must be absent;
no invitation delivery or external enrollment occurs.

Required DSN and pre-constructor scratch/API-key guards remain mandatory. The
packet uses a unique disposable database and verifies cleanup. It does not prove
concurrent acceptance, atomicity across acceptance and subsequent session creation,
postcommit session-failure recovery, durable replay, distributed rate limiting,
concurrent reload, refill-after-wait, or outage behavior. Acceptance remains
non-retryable. These sixteen original pairs remain separate from NEW acceptance.

### Frozen device burst sequence bounds

The default v2 sequence budget remains 16 exchanges, including all repeated
primary and follow-up requests. `ValidateV2Sequence` and `ValidatePairing` retain
that context-free limit. Selectors for the two larger frozen bursts must instead
call `ValidateScenarioPairing(row, originalScenario)` before translating the
original request.

Only `device_lookup.rate_limited.r1` (21 requests) and
`device_poll.rate_limited.r1` (31 requests) qualify. The validator compares the
entire original scenario and its ledger row key against the frozen originals in
`sequence_originals.json`, copied unchanged from the original catalog checkpoint.
It requires the corresponding v2 operation and method, the exact original request
with only its API version translated, the original repeat count, no principal
override, no follow-up steps, and the expected 429. An ID alone grants no larger
budget. Changed originals, shortened or enlarged bursts, added request material,
and split sequences are rejected.

Catalog loading applies the same validator as manual executor input. `Env.Run`
validates before either transport can reseed or send; the v2 transport validates
again while the original request is still available, then translates it. Existing
operation, follow-up binding, body-source and total-exchange checks remain in
force. This accommodation supplies no execution credit or rate-limit behavior
proof: each owning acceptance packet must run every original request and verify
its own effects and cleanup.

### Device poll rate-limited registration

`make test-scenario-device-poll-r1` executes the nine original registration-one
`device_poll.*.r1` cases as eighteen independent v1/v2 results on the real
rate-limited router. `device_poll.rate_limited.r1` sends all 31 original requests per
transport through `ValidateScenarioPairing`; the first 30 must reach the original
unknown-code refusal and request 31 the real per-second refusal, measured against the
actual 500 ms refill. Every request has one-statement before/after snapshots of 26
tables. A collecting poll must add exactly one login session and move exactly one
fixture request from approved to consumed, bound to that session; the two-poll
`consumed.r1` sequence verifies the first collection and the credential-free second
poll separately. Remote approval must carry the member's unlocked primary profile, a
profile token bound to account, session, profile and policy revision, and a stored
session expiry capped at 24 hours; the wire instant equals that expiry truncated to
seconds on v1 and milliseconds on v2. V2 projects nested tokens, string account IDs,
always-present profile fields, no-store and problem errors; v1 oracles, requirements
and requests are unchanged. The runner is documented in
`internal/scenariocatalog/executor/testdata/device_poll_r1.md`. This evidence does not
cover concurrent polls, retry after a lost collecting response, locked-profile
approval, the decision endpoints or real enrollment.

### Host resources, remote-playback handoff and impersonation end

`make test-scenario-resources-handoff-impersonation` pairs four original
`GET /admin/system/resources` reads, four successful
`POST /auth/device/approve-handoff` approvals and three
`POST /auth/impersonation/end` scenarios. Original requests, principals and
oracles are unchanged; only explicit `v2_expectation` declarations are added.

The executor wires no resource sampler, so both transports answer the frozen
unsampled host read. V2 serializes `gpu` as an explicit empty array while
`system` and `sampled_at` stay absent; the public read refuses with a 401
Problem. Reads preserve every table.

Handoff approvals move the pending remote-playback request to approved for
exactly the calling member profile (primary, or the PIN-locked profile with its
token), bounding `approved_at`/`updated_at` to the request clock. The meaning
follow-up poll must create exactly one temporary session bound to that profile
with a verified profile token; v2 nests account credentials under `tokens`.
Impersonation end revokes only the impersonated session inside the database
request window with an empty 204; the repeated bearer is refused with v1
`unauthorized` and v2 `session_expired`.

The eleven pairs produce 26 HTTP exchanges and 52 eight-table snapshots.
Each transport starts and ends with a reseed; required DSN and pre-constructor
scratch/API-key guards remain mandatory. The four hw-accel successes stay
unpaired on this branch, which carries no v2 hardware operation; nothing was
substituted. The packet does not prove sampled resource output, concurrent
approvals, or any hardware probe.

## Plugin launch checkpoint

`make test-scenario-plugin-launch` pairs the eight frozen plugin launch cases:
`plugin_launch.ok`, `secure_flag`, `meaning`, `shape`, `profile_validated`,
`locked_profile`, `api_key` and `no_token`. Every case asserts the launch
response itself, so no plugin process is served. The v2 operation issues the
same five-minute `HttpOnly` `SameSite=Lax` plugin access credential as v1 with
one intentional difference, the cookie path: `/api/v2/plugin-content` instead of
`/api/v1`, never `/`. The runner proves the two cookies carry the same signed
plugin access claims (user, session, role, profile) and differ only in path,
that `Secure` follows the shared HTTPS seam, and that no refusal sets a cookie.
The four v2 refusals are typed Problems: an unknown profile is `not_found`, a
locked profile without its token is `profile_verification_required`, a missing
bearer is `authentication_required`, and an API key, which carries no login
session, is `permission_denied` where v1 answered `unauthorized`. Sixteen HTTP
exchanges and 32 eight-table snapshots; every table stays byte-identical.

What the packet does not prove: that the reissued cookie authenticates a
request to a served auth-provider or HTTP-routes plugin under
`/api/v2/plugin-content`. No such first-party plugin build exists and the
executor serves no plugin; that compatibility proof is a follow-up with a real
plugin. The bundled web client keeps launching through v1 until plugin hrefs
move off `/api/v1/plugins`, so the v1-path cookie is not expired by the v2
launch and dies within its five-minute maximum.
