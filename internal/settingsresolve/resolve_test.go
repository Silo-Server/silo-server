package settingsresolve

import (
	"context"
	"encoding/json"
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

// The production store must satisfy the narrow read interface declared here.
// The package takes an interface rather than the concrete store so tests can
// fake it, which is exactly the seam that lets an incompatible signature go
// unnoticed until a caller wires the two together.
var _ Store = (userstore.UserStore)(nil)
