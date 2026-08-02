# User-Facing Device Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> Commands assume the repository root is the cwd.

**Goal:** Let a person see and change the device settings for every device they watch on, from whichever device they are holding — and let the household parent do the same for everyone on the account.

**Architecture:** No new storage and no schema change. `user_devices` and `user_setting_values` are already keyed `(user_id, profile_id, device_id, …)` and both list queries are already account-wide, so the work is authorization plus routes plus UI. Two identity widenings on the existing canonical settings API, each behind a guard: a caller may name a `device_id` other than the request's own (checked against `user_devices`), and a household parent may name a `profile_id` other than their own (checked by the existing `canManageHouseholdProfiles`). One new self-service device registry endpoint, deliberately profile-filtered by default. One new settings page reusing `SettingsGroup`/`SettingRow`.

**Tech Stack:** Go, `net/http` handler tests, `internal/userstore/storetest` conformance suite, React 19 + react-router v7 + TanStack Query, Vitest + Testing Library, shadcn/ui primitives.

**Design source:** `docs/superpowers/specs/2026-07-10-cross-platform-user-settings-contract-design.md`. Mockups were reviewed out-of-band; the shipping shape is the "B2 + B3" pair — a searchable device list with an editable detail pane, plus a household scope switch for the primary profile.

## Global Constraints

- **Additive only.** No existing response field is renamed, retyped, or removed, and no status code is repurposed. New behavior arrives as new endpoints or new optional query parameters. See `CLAUDE.md` "v1 API rules".
- **The header stays the default.** When `device_id` / `profile_id` are absent from the query, every existing route must behave exactly as it does today. Existing clients must not need a change.
- **No new hand-written setting metadata.** Labels, descriptions, controls, options and bounds come from `contracts/settings/v1/manifest.json` via the generated `web/src/lib/settingsContract.ts`. A per-key table beside the generated one is exactly the drift the contract exists to remove.
- **Never render raw setting keys** in user-facing UI.
- **Scope wording is mandated** by the design spec: "this device, for your profile only". Do not invent alternatives such as "global", "default", or a bare "this device".
- **Restrictions are not preferences.** Policy caps are explained with the permitted value and the reason; they are never rendered as a disabled control with no explanation, and this screen never authors a restriction.
- Prove each authorization regression RED before writing production code.
- Do not edit this plan file while implementing it.

---

## Phase 1 — Server: identity widening and the device registry

### Task 1: Reject a device the caller does not own

`completeIdentity` validates an identity's *shape* but never that a `profile_device` identity names a device belonging to the caller. That is safe today only because `DeviceID` is taken from the request's own header. Task 2 removes that guarantee, so the check lands first.

**Files:**
- Modify: `internal/api/handlers/settings_values.go`
- Modify: `internal/userstore/store.go`
- Modify: `internal/userstore/pgstore/settings.go`
- Modify: `internal/userdb/settings.go` (the per-user SQLite backend)
- Test: `internal/api/handlers/settings_values_test.go`
- Test: `internal/userstore/storetest/settingvalues.go`

**Interfaces:**
- Produces: `DeviceRegistry.DeviceExists(ctx, profileID, deviceID string) (bool, error)` — a targeted existence check rather than a full `ListDevices` scan on every write.
- Produces: a device-ownership guard invoked from `completeIdentity` for `ScopeProfileDevice`.

- [ ] **Step 1: Write the failing ownership test**

Add `TestSetValue_RejectsDeviceNotOwnedByCaller` to `settings_values_test.go`: register device `dev-a` for the caller, then `PUT /settings/values/player.hdr_enabled?scope=profile_device&device_id=dev-someone-else`. Expect `404` with error code `not_found` — not `403`, which would confirm the device id exists.

Because Task 2 has not landed, the query parameter is ignored and the write silently succeeds against the header device. Assert on the *stored* row, so the test fails for the right reason:

```go
if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "" {
    t.Fatalf("wrote a row for device %q; want no write", got)
}
```

- [ ] **Step 2: Run the test and verify RED**

```bash
go test ./internal/api/handlers/ -run TestSetValue_RejectsDeviceNotOwnedByCaller -v
```

- [ ] **Step 3: Add `DeviceExists` to the store interface and both backends**

Postgres: `SELECT EXISTS(SELECT 1 FROM user_devices WHERE user_id = $1 AND profile_id = $2 AND device_id = $3)`. SQLite: the same without `user_id`, matching the existing `ListDevices` asymmetry (one DB per user). Add the case to the shared conformance suite in `internal/userstore/storetest/settingvalues.go` so both backends are held to it.

- [ ] **Step 4: Enforce it in `completeIdentity`**

For `ScopeProfileDevice`, when the device id did **not** come from the request header, verify it exists for `(profileID, deviceID)`. A device that is unknown returns 404 `not_found`. Registering-on-write stays the behavior for the caller's own header device, so a brand-new device can still store its first value.

- [ ] **Step 5: Run and verify GREEN**

```bash
go test ./internal/api/handlers/ ./internal/userstore/... -run 'Device|SettingValue' -v
```

---

### Task 2: Accept an explicit `device_id` on the self-service settings routes

**Files:**
- Modify: `internal/api/handlers/settings_values.go`
- Test: `internal/api/handlers/settings_values_test.go`

**Interfaces:**
- Consumes: optional `device_id` query parameter on `GET|PUT|DELETE /settings/values/{key}` and `GET /settings/values` when `scope=profile_device`.
- Produces: `identityForSessionKey` resolving `DeviceID` from the query when present, else from `X-Silo-Device-Id` exactly as today.

- [ ] **Step 1: Write the failing tests**

Three cases in `settings_values_test.go`:
- `TestSetValue_WritesNamedDevice` — register `dev-b`, `PUT …?scope=profile_device&device_id=dev-b` from a request whose header is `dev-a`; assert the stored row is on `dev-b` and `dev-a` has none.
- `TestGetValues_ReadsNamedDevice` — same shape for the read path.
- `TestSetValue_FallsBackToHeaderDevice` — no `device_id` in the query writes the header device. This is the regression guard for every existing client.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api/handlers/ -run 'NamedDevice|FallsBackToHeaderDevice' -v
```

- [ ] **Step 3: Implement**

In `identityForSessionKey`, prefer a non-empty `device_id` from the query and fall through to `deviceMetadataFromRequest(r).DeviceID`. Update the comment at the profile assignment so it still describes reality: the profile remains session-derived here; only the device may be named. Keep the existing 400 when neither source yields a device id.

Do **not** call `registerWritingDevice` for a named device — registration is a statement that *this* device is in use, and a remote write is not that.

- [ ] **Step 4: Run and verify GREEN, then run the whole handler package**

```bash
go test ./internal/api/handlers/ -v
```

---

### Task 3: `GET /api/v1/devices` — list your own devices

**Files:**
- Create: `internal/api/handlers/devices.go`
- Create: `internal/api/handlers/devices_test.go`
- Modify: `internal/api/router.go`

**Interfaces:**
- Produces: `GET /api/v1/devices` → `{"devices":[{device_id, device_name, device_platform, last_seen_at, profile_id, profile_name, is_current_device, changed_count}]}`.
- Consumes: `DeviceRegistry.ListDevices`, `ListAllSettingValues`, `X-Silo-Device-Id`.

**The trap this task exists to avoid:** `ListDevices` is account-wide in *both* backends — `WHERE user_id = $1` in `internal/userstore/pgstore/settings.go:115`, and no `WHERE` clause at all in `internal/userdb/settings.go:112` because there is one DB per user. `profile_id` is a selected column, never a predicate. A naive passthrough would show every household member's devices to everyone. The handler must filter.

- [ ] **Step 1: Write the failing tests**

- `TestListDevices_FiltersToCallingProfile` — seed devices for profile A and profile B, call as A, assert only A's are returned. **This is the security test; write it first.**
- `TestListDevices_CountsChangedSettings` — `changed_count` equals the number of `profile_device` rows for that `(profile, device)`.
- `TestListDevices_MarksCurrentDevice` — the device matching `X-Silo-Device-Id` has `is_current_device: true`.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api/handlers/ -run TestListDevices -v
```

- [ ] **Step 3: Implement the handler**

Filter `ListDevices` output to `apimw.GetProfileID(ctx)`. Derive `changed_count` from `ListAllSettingValues` filtered to `scope == profile_device` and the same profile — one store round trip, not one per device. `profile_name` comes from the existing `listProfileNamesByID` helper pattern in `internal/api/handlers/admin.go`.

- [ ] **Step 4: Register the route**

In `internal/api/router.go`, inside the authenticated group, `r.With(apimw.RequireProfile).Get("/devices", devicesHandler.HandleListDevices)`. Place it away from the `/devices/push/apple` line so the two device namespaces stay visibly distinct.

- [ ] **Step 5: Run and verify GREEN**

```bash
go test ./internal/api/handlers/ -run TestListDevices -v
```

---

### Task 4: Forget a device, and bulk-clear one device

**Files:**
- Modify: `internal/api/handlers/devices.go`
- Modify: `internal/api/router.go`
- Modify: `internal/userstore/store.go` (if a targeted delete is missing)
- Test: `internal/api/handlers/devices_test.go`

**Interfaces:**
- Produces: `DELETE /api/v1/devices/{device_id}` — forget: clears settings **and** the registry row.
- Produces: `DELETE /api/v1/devices/{device_id}/settings` — clear overrides, keep the device.

The design spec requires Forget device (`…-design.md:416`) and lists it as outstanding (`:1202`). `DeleteAllDeviceSettings` exists and clears both storage generations but is reachable only through profile deletion today. Bulk clear also fixes the admin screen's 30-sequential-DELETE loop in `web/src/hooks/queries/admin/users.ts:451`.

- [ ] **Step 1: Write failing tests**

- `TestForgetDevice_RemovesSettingsAndRegistryRow`
- `TestForgetDevice_RejectsOtherProfilesDevice` → 404
- `TestClearDeviceSettings_KeepsRegistryRow`
- `TestForgetDevice_IsIdempotent` — a second call returns 204, not 500

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api/handlers/ -run 'ForgetDevice|ClearDeviceSettings' -v
```

- [ ] **Step 3: Implement both routes**

Reuse `DeleteSettingValuesForDevice` (`internal/userstore/store.go:216`) and `DeleteAllDeviceSettings`. Publish `user_settings.changed` once per cleared key so other devices invalidate — or once for the device if a batch event shape is added; do not skip the event.

- [ ] **Step 4: Run and verify GREEN**

```bash
go test ./internal/api/handlers/ ./internal/userstore/... -v
```

---

## Phase 2 — Server: the household tier

### Task 5: Extract the household-parent guard

`canManageHouseholdProfiles` (`internal/api/handlers/profiles.go:146`) already encodes exactly the right rule — server admin, or an `is_primary` active profile, and when that profile has a PIN a verified `X-Profile-Token` so sending only `X-Profile-Id` cannot bypass the profile lock. It is a method on `ProfileHandler`, so `SettingValuesHandler` cannot call it.

**Files:**
- Create: `internal/api/handlers/household.go`
- Modify: `internal/api/handlers/profiles.go`
- Modify: `internal/api/handlers/settings_values.go`
- Modify: `internal/api/router.go`
- Test: `internal/api/handlers/household_test.go`

**Interfaces:**
- Produces: `canManageHousehold(r *http.Request, store userstore.UserStore, tokens ProfileTokenValidator) (bool, error)` — a package-level function.
- `ProfileHandler.canManageHouseholdProfiles` becomes a thin wrapper so its four existing call sites and their behavior are untouched.
- `SettingValuesHandler` gains a `ProfileTokens` field, wired in `internal/api/router.go` next to `profileHandler.ProfileTokens = profileTokenService` (`router.go:812`).

- [ ] **Step 1: Characterization tests before moving anything**

In `household_test.go`, cover: admin → true; primary without PIN → true; primary with PIN and no token → `access.ErrProfileUnverified`; primary with PIN and valid token → true; non-primary → false; no active profile → false. Run them against the *existing* method first so the extraction is provably behavior-preserving.

- [ ] **Step 2: Extract, then re-run**

Move the body to the package-level function; leave the method delegating. Run the full profiles suite — those four call sites are the regression surface:

```bash
go test ./internal/api/handlers/ -run 'Profile|Household' -v
```

- [ ] **Step 3: Wire `ProfileTokens` into `SettingValuesHandler`**

Nil `ProfileTokens` must mean "no household widening", never "allow" — assert that in a test.

---

### Task 6: Accept an explicit `profile_id` for the household parent

**Files:**
- Modify: `internal/api/handlers/settings_values.go`
- Modify: `internal/api/handlers/devices.go`
- Test: `internal/api/handlers/settings_values_test.go`
- Test: `internal/api/handlers/devices_test.go`

**Interfaces:**
- Consumes: optional `profile_id` query parameter on the settings-value routes and on `GET /api/v1/devices`.
- Produces: identity resolution that permits a non-own `profile_id` only when `canManageHousehold` passes.

- [ ] **Step 1: Write the three refusal tests first**

These are the security surface. All three must be RED before any production code:
- `TestSetValue_NonPrimaryCannotNameSiblingProfile` → 403
- `TestSetValue_PrimaryWithUnverifiedPINCannotNameSibling` → 403, code `forbidden`, message naming PIN verification
- `TestSetValue_ProfileFromAnotherAccountIsNotFound` → 404

Then the positive cases: `TestSetValue_PrimaryWritesSiblingProfileDeviceSetting`, `TestListDevices_PrimarySeesHouseholdWhenRequested`.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api/handlers/ -run 'NameSibling|AnotherAccount|PrimaryWrites|PrimarySees' -v
```

- [ ] **Step 3: Implement**

In `identityForSessionKey`: when `profile_id` is present and differs from the session profile, require `canManageHousehold`; on failure return 403 without disclosing whether the profile exists. Then resolve the profile through the caller's **own** store, exactly as `internal/access/resolver.go:73-86` does — a profile from another account is simply absent, which yields the 404 and preserves the cross-account boundary for free.

For `GET /api/v1/devices`, add `?scope=household` (default: own profile only). Do not make household the default; the plain screen must stay private by construction.

- [ ] **Step 4: Run and verify GREEN, then the full package**

```bash
go test ./internal/api/handlers/ -v
```

**Record in the PR:** this widening does not create a new capability. The primary profile can already rewrite a sibling's canonical settings rows through `PUT /profiles/{id}` (`internal/api/handlers/profiles_settings_sync.go:218-231`), and `GET /profiles` (`profiles.go:263`) already returns every sibling's resolved preferences to any profile with no gate. All profiles share one login session, so `X-Profile-Id` is self-asserted for PIN-less profiles — stated in-repo at `internal/api/middleware/auth.go:180-184`. This task replaces an unlabelled path with a guarded, audited one.

---

### Task 7: Audit cross-profile and device settings mutations

`internal/activitylog` is only an HTTP request-log middleware mounted globally before auth (`internal/api/router.go:236-239`); no handler writes to it. Its entries carry method, path pattern, status, user and session but **no profile id and no body**, so a settings write is indistinguishable from any other `PUT`. Tolerable while every write is your own; not once one profile can change another's. The settings spec already requires admin clear/reset to be audited.

**Files:**
- Modify: `internal/activitylog/` (new entry type or a settings-audit sink)
- Modify: `internal/api/handlers/settings_values.go`
- Modify: `internal/api/handlers/settings_values_admin.go`
- Modify: `internal/api/handlers/devices.go`
- Test: `internal/api/handlers/settings_values_test.go`

- [ ] **Step 1: Write failing tests**

`TestSetValue_AuditsCrossProfileWrite` asserts an entry recording actor profile, target profile, device, key, and action. `TestSetValue_DoesNotAuditOwnWrite` keeps ordinary self-service writes out of the audit trail — otherwise volume makes it useless.

- [ ] **Step 2: Run RED, implement, verify GREEN**

Record the *identity* of what changed, never the value: `user_settings.changed` deliberately carries no value because admins receive other accounts' events (`internal/api/handlers/user_settings_events.go:8-13`). The same reasoning applies to a stored audit row. Also audit Forget device and bulk clear.

---

## Phase 3 — Web: the device settings screen

### Task 8: Query hooks for devices and cross-identity settings

**Files:**
- Create: `web/src/hooks/queries/devices.ts`
- Modify: `web/src/hooks/queries/settingValues.ts`
- Modify: `web/src/hooks/queries/keys.ts`
- Modify: `web/src/api/types.ts`
- Test: `web/src/hooks/queries/devices.test.ts`

**Interfaces:**
- Produces: `useMyDevices({ household? })`, `useForgetDevice()`, `useClearDeviceSettings()`.
- Modifies: `SettingIdentity` gains optional `deviceId` and `profileId`; `identityQuery` (`settingValues.ts:60`) serializes them.

- [ ] **Step 1: Extend `SettingIdentity` and `identityQuery`**

Both fields optional, so every existing caller compiles and behaves identically.

- [ ] **Step 2: Fix the cache key — this is a real bug if skipped**

`effectiveSettingsQueryKey` (`settingValues.ts:76`) namespaces by `activeProfileId()`. Reading another profile's or another device's values through the same key would collide with the current device's cache and serve one device's settings as another's. Add `deviceId` and `profileId` to the key, and add a test that two devices' reads occupy distinct entries.

- [ ] **Step 3: Write the device hooks and tests**

Follow the existing `api()` + TanStack Query conventions. Invalidate `[...settingsKeys.all, "values"]` on every mutation, as `useSetSettingValue` does.

```bash
cd web && pnpm vitest run src/hooks/queries/devices.test.ts
```

---

### Task 9: The device list pane

**Files:**
- Create: `web/src/pages/settings/DeviceSettings.tsx`
- Create: `web/src/components/settings/DeviceList.tsx`
- Create: `web/src/components/settings/deviceDisplay.ts`
- Modify: `web/src/pages/SettingsLayout.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/lib/documentTitle.ts`
- Test: `web/src/components/settings/DeviceList.test.tsx`

**Interfaces:**
- Produces: a master-detail layout — searchable list on the left, selected device on the right, stacking to a single column below the `md` breakpoint.
- Produces: `deviceDisplay.ts` — platform icon/label classification and relative-time formatting. Adapt the existing helpers in `web/src/components/admin/deviceOverrides.tsx:40-146` rather than duplicating them; move them here and have the admin page import from the shared module.

- [ ] **Step 1: Add the route and nav entry**

One `NavSection` item under "Account" in `SettingsLayout.tsx` (`NAV_SECTIONS`, from line 52) with `settings: settingIndex(...)` so the entries reach the settings search index, and one `<Route path="devices" …>` in `App.tsx` beside the other settings routes.

- [ ] **Step 2: Build the list with tests**

Rows are fixed height and carry name, last-used, and a changed-count pill; a device with nothing changed shows a dash rather than "0". Group by recency — Using now / This week / Earlier. Search filters by name and platform. Tests: grouping boundaries, the current device is marked, count pill renders a dash at zero, and search matches on platform as well as name.

```bash
cd web && pnpm vitest run src/components/settings/DeviceList.test.tsx
```

---

### Task 10: The device detail pane

**Files:**
- Create: `web/src/components/settings/DeviceSettingGroups.tsx`
- Create: `web/src/lib/deviceSettingGroups.ts`
- Test: `web/src/lib/deviceSettingGroups.test.ts`
- Test: `web/src/components/settings/DeviceSettingGroups.test.tsx`

**Interfaces:**
- Produces: `groupDeviceSettings(keys)` → Picture / Sound / Subtitles / Episodes, derived from each definition's `category` plus a small key→group map for the cases `category` does not separate (`player.*` splits across Picture and Sound).
- Consumes: `ALL_DEVICE_SETTING_KEYS` (`web/src/lib/settingsDisplay.ts:126`) and `RegistrySettingControl` (`web/src/components/settings/RegistrySettingControl.tsx`).

- [ ] **Step 1: Group mapping, with a completeness test**

`TestEveryDeviceKeyIsGrouped` — every key in `ALL_DEVICE_SETTING_KEYS` lands in exactly one group. This is what stops a newly added manifest key from silently vanishing from the UI.

- [ ] **Step 2: Render rows through the shared primitives**

Use `SettingRow` and `SettingsGroup`. No raw keys. "Changed here" badge when a `profile_device` row exists; "Use my setting" clears at `profile_device` — a DELETE, never a copy of the profile value into the device row. Sliders and steppers must round-trip as numbers; do not reuse the admin screen's string round-trip (`web/src/hooks/queries/admin/users.ts:106-145`).

- [ ] **Step 3: Policy-capped rows**

When the effective response carries `constrained_by`, render `permitted_values` only and state the limit and who set it. Never a disabled control with no reason. Test both a capped select and a `locked` constraint.

- [ ] **Step 4: Run**

```bash
cd web && pnpm vitest run src/components/settings/ src/lib/deviceSettingGroups.test.ts
```

---

### Task 11: Remote-device editing and its copy

**Files:**
- Modify: `web/src/pages/settings/DeviceSettings.tsx`
- Modify: `web/src/components/settings/DeviceSettingGroups.tsx`
- Test: `web/src/pages/settings/DeviceSettings.test.tsx`

- [ ] **Step 1: Write to the selected device**

Every mutation passes `deviceId` explicitly rather than relying on the header, so selecting a device and editing it writes that device. Test that editing a non-current device sends its id.

- [ ] **Step 2: Scope and sync copy**

Show the mandated scope sentence once per device — "this device, for your profile only" — not per row. For a non-current device, state that it picks the change up next time it is on. `useSettingValuesRealtime` (`settingValues.ts:245`) already invalidates on `user_settings.changed`, so no polling.

- [ ] **Step 3: Forget and bulk clear, with confirmation**

Both are destructive and both name their target: "Clear all N changes on this device", "Forget this device".

---

## Phase 4 — Web: the household view

### Task 12: Household scope switch and person grouping

**Files:**
- Modify: `web/src/pages/settings/DeviceSettings.tsx`
- Modify: `web/src/components/settings/DeviceList.tsx`
- Test: `web/src/pages/settings/DeviceSettings.household.test.tsx`

**Interfaces:**
- Consumes: `useIsActingAdmin`, `useCurrentProfile`, and `profile.is_primary` — the same rule as `RequirePrimaryOrAdmin` (`web/src/App.tsx:209`) and `isActingAdmin` (`web/src/lib/permissions.ts:19`). Do not write a fourth definition of this predicate.
- Consumes: `GET /profiles` for names, avatars, `is_child`.

- [ ] **Step 1: The switch appears only for the household parent**

Test that a non-primary, non-admin profile never sees it, and that the page still works fully for them in "just mine" mode. The switch is additive; nothing is taken away from anyone.

- [ ] **Step 2: Group devices by person**

Person header with avatar, name, a "You" or "Kid" tag, and a device count; devices nested beneath. When two profiles have registered the same physical TV, say so — "Same TV as yours — separate settings per person" — because that is the single most confusable thing on this screen.

- [ ] **Step 3: Acting-on-behalf copy**

A persistent banner while a sibling's device is selected: "You're changing Robin's settings, not your own." Reset actions name the person: "Use Robin's setting". Test that the banner is absent for one's own devices.

---

### Task 13: Household boundaries in the UI

**Files:**
- Modify: `web/src/components/settings/DeviceSettingGroups.tsx`
- Modify: `web/src/pages/settings/DeviceSettings.tsx`
- Test: `web/src/pages/settings/DeviceSettings.household.test.tsx`

- [ ] **Step 1: Household limits read as limits, and link out**

A value capped by parental controls shows a lock pill naming who set it and links to the profiles screen. This screen never authors a restriction — settings answer "what does this user want", policy answers "what are they allowed to have" (design spec, "Preferences versus restrictions").

- [ ] **Step 2: State the privacy boundary**

A short block: this page shows how Silo is set up per device, not what anyone watched. Viewing history stays private per profile. Test that it renders in household mode.

- [ ] **Step 3: Full check**

```bash
cd web && pnpm run lint && pnpm run format:check && pnpm vitest run
```

---

## Phase 5 — Verification

### Task 14: Cross-cutting checks

- [ ] **Step 1: Full suite**

```bash
make lint
make test
cd web && pnpm run lint && pnpm run format:check
make verify-local-paths
make verify-settings-bindings-all
```

Four Go failures (auth, catalog, jellycompat, notifications) pre-exist on some local Postgres provisioning and are not caused by this work — verify against the branch base before chasing one. `make lint` runs `golangci-lint` over the whole tree while CI runs `--new-from-merge-base`, so expect pre-existing findings that CI will not fail on; do not add to them.

- [ ] **Step 2: Browser verification**

Use the `web-ui-testing` skill against a real backend. Capture, for the PR: the device list at ten or more devices, a device detail pane, a remote-device edit, the household switch, and a policy-capped row. UI changes need screenshots (`CLAUDE.md`, "Pull requests").

- [ ] **Step 3: Manual authorization pass**

With `curl` against a dev server, confirm each refusal returns the intended status and leaks nothing:
- non-primary naming a sibling profile → 403
- primary with an unverified PIN → 403
- a profile id from another account → 404
- a device id belonging to another profile → 404

- [ ] **Step 4: Cross-repo follow-up**

The two identity widenings are additive server capabilities that Apple and Android may adopt later; nothing in those clients breaks without a change. Note in the PR whether follow-up issues are wanted, per `CLAUDE.md` "Multi-repo".

---

## Out of scope, and why

- **Renaming a device.** `device_name` is client-reported and re-registration overwrites it, so a user-set name needs a separate column and a precedence rule. Worth doing; not part of this plan.
- **"Copy my settings from another device".** Appealing, but it writes up to 30 keys in one request, which is the argument for the batch mutations endpoint the design spec names (`POST /api/v1/settings/mutations`) and which does not exist. Build that first.
- **Transferring the primary designation.** `is_primary` is assigned implicitly to the first profile created (`internal/userstore/pgstore/profiles.go:60-73`) and cannot be moved. The household view makes that visible, so it likely needs to exist — as its own issue.
- **Watch history in the household view.** A different privacy question that a settings screen should not quietly answer.
- **Admin device screen rework.** Once users self-serve, `/admin/devices` can go on being a fleet console. Grouping its rows by what they affect and hiding raw keys behind a disclosure is worth doing separately.

## Adjacent gaps found while planning

Same shape as this work, but not blockers — each deserves its own issue:

- `PUT`/`DELETE /profiles/{id}/avatar` (`internal/api/handlers/profile_avatars.go:202`, `:290`) apply no household guard and no self-check: any profile can change or delete any sibling's avatar, including the primary's.
- `GET /profiles` (`internal/api/handlers/profiles.go:263`) returns every sibling's resolved preferences, `has_pin`, `is_child`, content rating and library restrictions to any profile on the account.
- `POST /profiles/{id}/verify-pin` (`internal/api/handlers/profiles.go:701`) is open to any authenticated user of the account for any profile id, with no rate limiting in the handler.
