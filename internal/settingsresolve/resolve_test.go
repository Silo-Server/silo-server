package settingsresolve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeStore records the query it was asked and replays fixed rows, so a test
// can assert both the answer and that only one read produced it.
type fakeStore struct {
	rows    []userstore.SettingValue
	queries []userstore.SettingResolutionQuery
	err     error
}

func (f *fakeStore) ListSettingValuesForResolution(
	_ context.Context, query userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	f.queries = append(f.queries, query)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func row(key string, scope settingscontract.Scope, value string, id userstore.SettingIdentity) userstore.SettingValue {
	id.Key = key
	id.Scope = scope
	return userstore.SettingValue{SettingIdentity: id, Value: json.RawMessage(value)}
}

func mustContract(t *testing.T) *settingscontract.Manifest {
	t.Helper()
	manifest, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	return manifest
}

func resolveOne(t *testing.T, store Store, rc Context, key string, constraints Constraints) Effective {
	t.Helper()
	got, err := New(mustContract(t)).Resolve(context.Background(), store, rc, []string{key}, constraints)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Resolve returned %d results, want 1", len(got))
	}
	return got[0]
}

// TestResolutionOrderIsHonored is the whole point of the package: the most
// specific scope holding a value wins, and the declared order decides what
// "specific" means rather than each caller's own opinion.
func TestResolutionOrderIsHonored(t *testing.T) {
	const key = "playback.subtitle_language"

	profile := row(key, settingscontract.ScopeProfile, `"en"`,
		userstore.SettingIdentity{ProfileID: "p1"})
	device := row(key, settingscontract.ScopeProfileDevice, `"de"`,
		userstore.SettingIdentity{ProfileID: "p1", DeviceID: "d1"})
	library := row(key, settingscontract.ScopeProfileLibrary, `"fr"`,
		userstore.SettingIdentity{ProfileID: "p1", LibraryID: 7})
	series := row(key, settingscontract.ScopeProfileSeries, `"ja"`,
		userstore.SettingIdentity{ProfileID: "p1", SeriesID: "s1"})

	rc := Context{ProfileID: "p1", DeviceID: "d1", LibraryIDs: []int{7}, SeriesIDs: []string{"s1"}}

	for name, tc := range map[string]struct {
		rows       []userstore.SettingValue
		wantValue  string
		wantSource settingscontract.Scope
	}{
		"series beats everything": {
			[]userstore.SettingValue{profile, device, library, series}, `"ja"`,
			settingscontract.ScopeProfileSeries,
		},
		"library beats device and profile": {
			[]userstore.SettingValue{profile, device, library}, `"fr"`,
			settingscontract.ScopeProfileLibrary,
		},
		"device beats profile": {
			[]userstore.SettingValue{profile, device}, `"de"`,
			settingscontract.ScopeProfileDevice,
		},
		"profile alone": {
			[]userstore.SettingValue{profile}, `"en"`, settingscontract.ScopeProfile,
		},
		"nothing stored falls to the default": {
			nil, `null`, settingscontract.ScopeDefault,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := resolveOne(t, &fakeStore{rows: tc.rows}, rc, key, nil)
			if string(got.Value) != tc.wantValue {
				t.Errorf("value = %s, want %s", got.Value, tc.wantValue)
			}
			if got.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

func TestEffectiveReportsCurrentManifestRevision(t *testing.T) {
	manifest := mustContract(t)
	got, err := New(manifest).Resolve(context.Background(), &fakeStore{},
		Context{ProfileID: "p1"}, []string{"playback.subtitle_language"}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].DefinitionRevision != manifest.Revision {
		t.Fatalf("definition_revision = %d, want current manifest revision %d",
			got[0].DefinitionRevision, manifest.Revision)
	}
}

// TestAbsentIdentityDropsItsScope covers the anonymous caller. jellycompat
// seeds DisplayPreferences without a device, and a device override leaking into
// that seed would hand one device's settings to every Jellyfin client.
func TestAbsentIdentityDropsItsScope(t *testing.T) {
	const key = "playback.subtitle_language"
	rows := []userstore.SettingValue{
		row(key, settingscontract.ScopeProfile, `"en"`,
			userstore.SettingIdentity{ProfileID: "p1"}),
		row(key, settingscontract.ScopeProfileDevice, `"de"`,
			userstore.SettingIdentity{ProfileID: "p1", DeviceID: "d1"}),
	}

	got := resolveOne(t, &fakeStore{rows: rows}, Context{ProfileID: "p1"}, key, nil)
	if got.Source != settingscontract.ScopeProfile {
		t.Fatalf("source = %q, want profile: a device row resolved for a caller with no device",
			got.Source)
	}
	if string(got.Value) != `"en"` {
		t.Errorf("value = %s, want \"en\"", got.Value)
	}
}

// TestUnrelatedIdentitiesAreIgnored guards the cross-identity leak: rows for
// another profile, device, library or series must not resolve just because the
// batched read returned them.
func TestUnrelatedIdentitiesAreIgnored(t *testing.T) {
	const key = "playback.subtitle_language"
	rows := []userstore.SettingValue{
		row(key, settingscontract.ScopeProfile, `"xx"`,
			userstore.SettingIdentity{ProfileID: "other"}),
		row(key, settingscontract.ScopeProfileDevice, `"yy"`,
			userstore.SettingIdentity{ProfileID: "p1", DeviceID: "other-device"}),
		row(key, settingscontract.ScopeProfileSeries, `"zz"`,
			userstore.SettingIdentity{ProfileID: "p1", SeriesID: "other-series"}),
	}
	rc := Context{ProfileID: "p1", DeviceID: "d1", SeriesIDs: []string{"s1"}}

	got := resolveOne(t, &fakeStore{rows: rows}, rc, key, nil)
	if got.Source != settingscontract.ScopeDefault {
		t.Fatalf("source = %q with value %s, want default: a foreign row resolved",
			got.Source, got.Value)
	}
}

// TestResolveIssuesOneRead pins the batching. The design rejects one lookup per
// scope per key, and a season view resolving many items is exactly where that
// would show up.
func TestResolveIssuesOneRead(t *testing.T) {
	store := &fakeStore{}
	keys := []string{
		"playback.subtitle_language", "playback.audio_language",
		"playback.subtitle_mode", "playback.show_forced_subtitles",
	}
	rc := Context{
		ProfileID:  "p1",
		DeviceID:   "d1",
		LibraryIDs: []int{1, 2, 3},
		SeriesIDs:  []string{"s1", "s2", "s3"},
	}

	if _, err := New(mustContract(t)).Resolve(context.Background(), store, rc, keys, nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(store.queries) != 1 {
		t.Fatalf("issued %d reads for %d keys, want 1", len(store.queries), len(keys))
	}
	if len(store.queries[0].Keys) != len(keys) {
		t.Errorf("query carried %d keys, want %d", len(store.queries[0].Keys), len(keys))
	}
}

func TestResolveContextsIssuesOneReadAndRanksEachContext(t *testing.T) {
	const key = "playback.subtitle_language"
	store := &fakeStore{rows: []userstore.SettingValue{
		row(key, settingscontract.ScopeProfile, `"en"`,
			userstore.SettingIdentity{ProfileID: "p1"}),
		row(key, settingscontract.ScopeProfileSeries, `"ja"`,
			userstore.SettingIdentity{ProfileID: "p1", SeriesID: "s1"}),
		row(key, settingscontract.ScopeProfileSeries, `"de"`,
			userstore.SettingIdentity{ProfileID: "p1", SeriesID: "s2"}),
	}}
	contexts := []Context{
		{ProfileID: "p1", DeviceID: "d1", LibraryIDs: []int{7}, SeriesIDs: []string{"s1"}},
		{ProfileID: "p1", DeviceID: "d1", LibraryIDs: []int{7}, SeriesIDs: []string{"s2"}},
	}

	got, err := New(mustContract(t)).ResolveContexts(
		context.Background(), store, contexts, []string{key}, nil,
	)
	if err != nil {
		t.Fatalf("ResolveContexts: %v", err)
	}
	if len(store.queries) != 1 {
		t.Fatalf("issued %d candidate reads, want 1", len(store.queries))
	}
	query := store.queries[0].Normalized()
	if len(query.SeriesIDs) != 2 || query.SeriesIDs[0] != "s1" || query.SeriesIDs[1] != "s2" {
		t.Fatalf("query series ids = %#v, want both contexts", query.SeriesIDs)
	}
	if len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 1 {
		t.Fatalf("resolved shape = %#v", got)
	}
	if string(got[0][0].Value) != `"ja"` || string(got[1][0].Value) != `"de"` {
		t.Errorf("per-context values = %s, %s; want ja, de", got[0][0].Value, got[1][0].Value)
	}
}

// TestUnknownAndLocalKeysAreOmitted keeps a newer client's request from
// failing wholesale. A key this server does not have, or one the contract says
// never leaves the device, simply has no server answer.
func TestUnknownAndLocalKeysAreOmitted(t *testing.T) {
	got, err := New(mustContract(t)).Resolve(context.Background(), &fakeStore{},
		Context{ProfileID: "p1"},
		[]string{"playback.subtitle_mode", "not.a.real.key", "downloads.wifi_only"}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d results, want only the one remote key", len(got))
	}
	if got[0].Key != "playback.subtitle_mode" {
		t.Errorf("resolved %q", got[0].Key)
	}
}

// TestCeilingNarrowsWithoutDestroying is the preferences-versus-restrictions
// rule: a capped preference is reported capped and kept intact, so it takes
// effect the day the cap lifts.
func TestCeilingNarrowsWithoutDestroying(t *testing.T) {
	const key = "playback.preferred_quality"
	rows := []userstore.SettingValue{
		row(key, settingscontract.ScopeProfile, `"2160p"`,
			userstore.SettingIdentity{ProfileID: "p1"}),
	}
	constraints := Constraints{"max_playback_quality": json.RawMessage(`"1080p"`)}

	got := resolveOne(t, &fakeStore{rows: rows}, Context{ProfileID: "p1"}, key, constraints)
	if string(got.Value) != `"1080p"` {
		t.Errorf("effective = %s, want \"1080p\"", got.Value)
	}
	if string(got.StoredValue) != `"2160p"` {
		t.Errorf("stored = %s, want \"2160p\" preserved", got.StoredValue)
	}
	if !got.Constrained || got.ConstraintKind != settingscontract.ConstraintCeiling {
		t.Errorf("constrained=%v kind=%q, want true/ceiling", got.Constrained, got.ConstraintKind)
	}
	if string(got.RequestedValue) != `"2160p"` {
		t.Errorf("requested_value = %s, want authored 2160p", got.RequestedValue)
	}
	if got.ConstrainedBy == nil || got.ConstrainedBy.PolicyInput != "max_playback_quality" ||
		got.ConstrainedBy.Constraint != settingscontract.ConstraintCeiling {
		t.Errorf("constrained_by = %#v", got.ConstrainedBy)
	}
	wantPermitted := []string{`"auto"`, `"480p"`, `"720p"`, `"1080p"`}
	if len(got.PermittedValues) != len(wantPermitted) {
		t.Fatalf("permitted_values = %q, want %q", got.PermittedValues, wantPermitted)
	}
	for i := range wantPermitted {
		if string(got.PermittedValues[i]) != wantPermitted[i] {
			t.Errorf("permitted_values[%d] = %s, want %s", i, got.PermittedValues[i], wantPermitted[i])
		}
	}

	// Under the cap, nothing is touched and no constraint is reported.
	rows[0].Value = json.RawMessage(`"720p"`)
	got = resolveOne(t, &fakeStore{rows: rows}, Context{ProfileID: "p1"}, key, constraints)
	if string(got.Value) != `"720p"` || got.Constrained {
		t.Errorf("value = %s constrained = %v, want \"720p\"/false", got.Value, got.Constrained)
	}
	if got.StoredValue != nil {
		t.Errorf("stored_value = %s, want absent when nothing was narrowed", got.StoredValue)
	}
	if got.RequestedValue != nil {
		t.Errorf("requested_value = %s, want absent when nothing was narrowed", got.RequestedValue)
	}
	if got.ConstrainedBy == nil || len(got.PermittedValues) == 0 {
		t.Error("active ceiling metadata was omitted for an already-permitted value")
	}
}

// TestCeilingCapsAnUncappedNullable covers the case CompareValues cannot rank.
// null on max_bitrate_kbps means "no cap of my own", which is unbounded above —
// precisely what a ceiling exists to bring down. Ranking it as equal would let
// the one value that most needs capping slip past.
func TestCeilingCapsAnUncappedNullable(t *testing.T) {
	manifest := mustContract(t)
	def, ok := manifest.Lookup("playback.max_bitrate_kbps")
	if !ok {
		t.Fatal("playback.max_bitrate_kbps is not registered")
	}
	// The manifest does not bind this key to a policy input today; the rule
	// still has to hold for whichever numeric key does.
	bound := *def
	bound.ConstrainedBy = &settingscontract.Constraint{
		PolicyInput: "max_bitrate_kbps",
		Constraint:  settingscontract.ConstraintCeiling,
	}

	capped := applyConstraint(&bound, Effective{
		Key:    bound.Key,
		Value:  json.RawMessage(`null`),
		Source: settingscontract.ScopeDefault,
	}, Constraints{"max_bitrate_kbps": json.RawMessage(`8000`)})

	if string(capped.Value) != `8000` {
		t.Errorf("uncapped bitrate resolved to %s, want the policy cap 8000", capped.Value)
	}
	if !capped.Constrained {
		t.Error("capping an uncapped value was not reported as constrained")
	}

	// A floor is the mirror: unbounded already satisfies it.
	bound.ConstrainedBy.Constraint = settingscontract.ConstraintFloor
	floored := applyConstraint(&bound, Effective{
		Key:   bound.Key,
		Value: json.RawMessage(`null`),
	}, Constraints{"max_bitrate_kbps": json.RawMessage(`8000`)})
	if floored.Constrained {
		t.Errorf("floor narrowed an already-unbounded value to %s", floored.Value)
	}
}

// TestAllowlistFallsBackInsideTheAllowedSet guards the one thing a constraint
// must never do: return a value the policy forbids. The definition's own
// default is not a safe fallback, because it may itself be outside the list.
func TestAllowlistFallsBackInsideTheAllowedSet(t *testing.T) {
	manifest := mustContract(t)
	def, ok := manifest.Lookup("catalog.metadata_language")
	if !ok {
		t.Fatal("catalog.metadata_language is not registered")
	}
	bound := *def
	bound.ConstrainedBy = &settingscontract.Constraint{
		PolicyInput: "allowed_metadata_languages",
		Constraint:  settingscontract.ConstraintAllowlist,
	}

	got := applyConstraint(&bound, Effective{
		Key:    bound.Key,
		Value:  json.RawMessage(`"ja"`),
		Source: settingscontract.ScopeProfile,
	}, Constraints{"allowed_metadata_languages": json.RawMessage(`["en","fr"]`)})

	if string(got.Value) != `"en"` {
		t.Errorf("value = %s, want the first allowed member", got.Value)
	}
	if string(got.StoredValue) != `"ja"` {
		t.Errorf("stored = %s, want the authored value kept", got.StoredValue)
	}

	// A permitted value passes through untouched.
	allowed := applyConstraint(&bound, Effective{
		Key:   bound.Key,
		Value: json.RawMessage(`"fr"`),
	}, Constraints{"allowed_metadata_languages": json.RawMessage(`["en","fr"]`)})
	if allowed.Constrained {
		t.Error("a permitted value was reported as constrained")
	}
}

// TestNoConstraintInputLeavesValueAlone covers the viewer a policy says nothing
// about, which is most of them.
func TestNoConstraintInputLeavesValueAlone(t *testing.T) {
	const key = "playback.preferred_quality"
	rows := []userstore.SettingValue{
		row(key, settingscontract.ScopeProfile, `"2160p"`,
			userstore.SettingIdentity{ProfileID: "p1"}),
	}
	for name, constraints := range map[string]Constraints{
		"no constraints at all": nil,
		"unrelated input":       {"something_else": json.RawMessage(`"1080p"`)},
		"empty input":           {"max_playback_quality": json.RawMessage(``)},
	} {
		t.Run(name, func(t *testing.T) {
			got := resolveOne(t, &fakeStore{rows: rows}, Context{ProfileID: "p1"}, key, constraints)
			if got.Constrained || string(got.Value) != `"2160p"` {
				t.Errorf("value = %s constrained = %v, want the stored value untouched",
					got.Value, got.Constrained)
			}
		})
	}
}

// TestIdentityLocatesTheResolvedRow so a client can offer "reset this device's
// override" against the scope that actually holds the value.
func TestIdentityLocatesTheResolvedRow(t *testing.T) {
	const key = "playback.subtitle_language"
	rows := []userstore.SettingValue{
		row(key, settingscontract.ScopeProfileDevice, `"de"`,
			userstore.SettingIdentity{ProfileID: "p1", DeviceID: "d1"}),
	}
	got := resolveOne(t, &fakeStore{rows: rows},
		Context{ProfileID: "p1", DeviceID: "d1"}, key, nil)

	if got.Identity == nil {
		t.Fatal("no identity reported for a stored value")
	}
	if got.Identity.Scope != settingscontract.ScopeProfileDevice || got.Identity.DeviceID != "d1" {
		t.Errorf("identity = %+v, want the profile_device row", *got.Identity)
	}

	// A default came from no row, so there is nothing to reset.
	def := resolveOne(t, &fakeStore{}, Context{ProfileID: "p1", DeviceID: "d1"}, key, nil)
	if def.Identity != nil {
		t.Errorf("identity = %+v for a default, want none", *def.Identity)
	}
}

func TestStoreErrorsPropagate(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := New(mustContract(t)).Resolve(context.Background(),
		&fakeStore{err: sentinel}, Context{ProfileID: "p1"},
		[]string{"playback.subtitle_mode"}, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap the store error", err)
	}
}

// The production store must satisfy the narrow read interface declared here.
// The package takes an interface rather than the concrete store so tests can
// fake it, which is exactly the seam that lets an incompatible signature go
// unnoticed until a caller wires the two together.
var _ Store = (userstore.UserStore)(nil)

// TestResolveProfilesRanksPerProfileInOneRead is the household shape: several
// profiles resolved from one candidate set, each getting exactly what a
// single-profile Resolve would have returned, and no profile inheriting
// another's value. GET /profiles is the caller that makes this matter — a read
// per profile would turn one list request into n round trips.
func TestResolveProfilesRanksPerProfileInOneRead(t *testing.T) {
	const key = "playback.subtitle_language"
	store := &fakeStore{rows: []userstore.SettingValue{
		row(key, settingscontract.ScopeAccount, `"en"`, userstore.SettingIdentity{}),
		row(key, settingscontract.ScopeProfile, `"fr"`,
			userstore.SettingIdentity{ProfileID: "p1"}),
		row(key, settingscontract.ScopeProfile, `"ja"`,
			userstore.SettingIdentity{ProfileID: "p2"}),
	}}

	got, err := New(mustContract(t)).ResolveProfiles(context.Background(), store,
		[]string{"p1", "p2", "p3"}, []string{key}, nil)
	if err != nil {
		t.Fatalf("ResolveProfiles: %v", err)
	}
	if len(store.queries) != 1 {
		t.Fatalf("issued %d reads for 3 profiles, want 1", len(store.queries))
	}
	if len(store.queries[0].ProfileIDs) != 3 {
		t.Errorf("query carried %d profile ids, want 3", len(store.queries[0].ProfileIDs))
	}

	// p3 has no row of its own. The account-scope row is not in this key's
	// resolution order, so p3 falls to the contract default rather than
	// inheriting a neighbor's language.
	for profileID, want := range map[string]string{
		"p1": `"fr"`,
		"p2": `"ja"`,
		"p3": `null`,
	} {
		effective, ok := got[profileID]
		if !ok {
			t.Errorf("no result for %s", profileID)
			continue
		}
		if len(effective) != 1 {
			t.Errorf("%s returned %d results, want 1", profileID, len(effective))
			continue
		}
		if string(effective[0].Value) != want {
			t.Errorf("%s resolved to %s, want %s", profileID, effective[0].Value, want)
		}
	}
}

// TestResolveProfilesWithoutProfilesReadsNothing: an account with no profiles
// is not an error and must not issue a read that would return every
// account-scope row for no one to use.
func TestResolveProfilesWithoutProfilesReadsNothing(t *testing.T) {
	store := &fakeStore{}
	got, err := New(mustContract(t)).ResolveProfiles(context.Background(), store,
		nil, []string{"playback.subtitle_mode"}, nil)
	if err != nil {
		t.Fatalf("ResolveProfiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("returned %d results for no profiles, want none", len(got))
	}
	if len(store.queries) != 0 {
		t.Errorf("issued %d reads for no profiles, want none", len(store.queries))
	}
}
