package settingsresolve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// This is the Go runner for the cross-platform conformance fixture in
// contracts/settings/v1/conformance.json — the spec's named drift gate. The
// same hand-authored cases run against the TypeScript resolver
// (web/src/lib/settingsConformance.test.ts) and, in later phases, against the
// Kotlin and Swift resolvers in the client repos. Four independently written
// resolvers agreeing on these cases is the whole point, so this runner takes
// the fixture at face value: it decodes strictly (an unknown fixture field is
// itself drift), resolves through the real resolver against the real embedded
// manifest, and compares every declared expectation.

type conformanceFixture struct {
	FixtureVersion   int               `json:"fixture_version"`
	ManifestRevision int               `json:"manifest_revision"`
	Description      string            `json:"description"`
	Cases            []conformanceCase `json:"cases"`
}

type conformanceCase struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Keys        []string            `json:"keys"`
	Context     *conformanceContext `json:"context"`
	Stored      []conformanceRow    `json:"stored"`
	// Constraints are policy inputs by name, as the policy layer would supply
	// them. Keys here are data, not schema, so they are not field-checked.
	Constraints map[string]json.RawMessage `json:"constraints"`
	// ConstraintBindings attach a constraint to a copy of a real definition,
	// so constraint kinds no shipped definition carries stay testable.
	ConstraintBindings []conformanceBinding  `json:"constraint_bindings"`
	Expected           []conformanceExpected `json:"expected"`
}

type conformanceContext struct {
	ProfileID    string   `json:"profile_id"`
	ClientFamily string   `json:"client_family"`
	DeviceID     string   `json:"device_id"`
	LibraryIDs   []int    `json:"library_ids"`
	SeriesIDs    []string `json:"series_ids"`
}

type conformanceRow struct {
	Key          string          `json:"key"`
	Scope        string          `json:"scope"`
	ProfileID    string          `json:"profile_id"`
	ClientFamily string          `json:"client_family"`
	DeviceID     string          `json:"device_id"`
	LibraryID    int             `json:"library_id"`
	SeriesID     string          `json:"series_id"`
	Value        json.RawMessage `json:"value"`
}

type conformanceBinding struct {
	Key         string `json:"key"`
	PolicyInput string `json:"policy_input"`
	Constraint  string `json:"constraint"`
}

type conformanceExpected struct {
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	Source string          `json:"source"`
	// Constrained defaults to false. When true, StoredValue and ConstraintKind
	// must be present; StoredValue may be JSON null for an authored null.
	Constrained    bool            `json:"constrained"`
	StoredValue    json.RawMessage `json:"stored_value"`
	ConstraintKind string          `json:"constraint_kind"`
}

func loadConformanceFixture(t *testing.T) conformanceFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "settings", "v1", "conformance.json"))
	if err != nil {
		t.Fatalf("reading conformance fixture: %v", err)
	}

	// DisallowUnknownFields is the fixture's own drift gate: a field this
	// runner does not know is either a typo or a semantic another platform
	// would silently skip, and both must fail loudly.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fixture conformanceFixture
	if err := dec.Decode(&fixture); err != nil {
		t.Fatalf("decoding conformance fixture: %v", err)
	}
	if dec.More() {
		t.Fatal("conformance fixture has trailing content after the fixture object")
	}
	return fixture
}

func TestConformanceFixture(t *testing.T) {
	fixture := loadConformanceFixture(t)
	manifest := mustContract(t)

	if fixture.FixtureVersion != 1 {
		t.Fatalf("fixture_version = %d, this runner understands 1", fixture.FixtureVersion)
	}
	// Expectations are derived from one specific manifest. A revision bump
	// changes definitions, so the fixture author must re-confirm every case.
	if fixture.ManifestRevision != manifest.Revision {
		t.Fatalf("fixture targets manifest revision %d but the embedded manifest is revision %d; re-derive the fixture expectations",
			fixture.ManifestRevision, manifest.Revision)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("conformance fixture declares no cases")
	}

	seen := make(map[string]bool, len(fixture.Cases))
	for _, tc := range fixture.Cases {
		if tc.Name == "" {
			t.Fatal("conformance case with no name")
		}
		if seen[tc.Name] {
			t.Fatalf("duplicate conformance case name %q", tc.Name)
		}
		seen[tc.Name] = true
		t.Run(tc.Name, func(t *testing.T) {
			runConformanceCase(t, manifest, tc)
		})
	}
}

func runConformanceCase(t *testing.T, manifest *settingscontract.Manifest, tc conformanceCase) {
	t.Helper()
	if len(tc.Keys) == 0 {
		t.Fatal("case declares no keys")
	}
	if len(tc.Expected) == 0 {
		t.Fatal("case declares no expectations")
	}

	rc := Context{}
	if tc.Context != nil {
		rc = Context{
			ProfileID:    tc.Context.ProfileID,
			ClientFamily: settingscontract.ClientFamily(tc.Context.ClientFamily),
			DeviceID:     tc.Context.DeviceID,
			LibraryIDs:   tc.Context.LibraryIDs,
			SeriesIDs:    tc.Context.SeriesIDs,
		}
	}

	rows := make([]userstore.SettingValue, 0, len(tc.Stored))
	for i, stored := range tc.Stored {
		if stored.Value == nil {
			t.Fatalf("stored[%d] has no value; an authored null must be spelled null", i)
		}
		rows = append(rows, userstore.SettingValue{
			SettingIdentity: userstore.SettingIdentity{
				Key:          stored.Key,
				Scope:        settingscontract.Scope(stored.Scope),
				ProfileID:    stored.ProfileID,
				ClientFamily: settingscontract.ClientFamily(stored.ClientFamily),
				DeviceID:     stored.DeviceID,
				LibraryID:    stored.LibraryID,
				SeriesID:     stored.SeriesID,
			},
			Value: stored.Value,
		})
	}

	bindings, err := conformanceBindings(manifest, tc.ConstraintBindings)
	if err != nil {
		t.Fatal(err)
	}
	constraints := Constraints(tc.Constraints)

	resolver := New(manifest)
	store := &fakeStore{rows: rows}
	var effs []Effective
	if len(bindings) == 0 {
		// The production path: Resolve applies whatever constraints the
		// manifest itself binds.
		effs, err = resolver.Resolve(context.Background(), store, rc, tc.Keys, constraints)
	} else {
		// Bound cases resolve unconstrained, then apply the constraint against
		// a copy of the real definition carrying the injected binding —
		// mirroring what Resolve does internally without mutating the shared
		// loaded manifest.
		effs, err = resolver.Resolve(context.Background(), store, rc, tc.Keys, nil)
		if err == nil {
			for i, eff := range effs {
				def, ok := manifest.Lookup(eff.Key)
				if !ok {
					t.Fatalf("resolved key %q is not in the manifest", eff.Key)
				}
				bound := *def
				if binding, has := bindings[eff.Key]; has {
					bound.ConstrainedBy = binding
				}
				effs[i] = applyConstraint(&bound, eff, constraints)
			}
		}
	}
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(effs) != len(tc.Expected) {
		t.Fatalf("resolved %d settings, fixture expects %d", len(effs), len(tc.Expected))
	}
	byKey := make(map[string]Effective, len(effs))
	for _, eff := range effs {
		byKey[eff.Key] = eff
	}

	for _, exp := range tc.Expected {
		eff, ok := byKey[exp.Key]
		if !ok {
			t.Errorf("%s: no resolved value", exp.Key)
			continue
		}
		if exp.Value == nil {
			t.Errorf("%s: expectation has no value; an expected null must be spelled null", exp.Key)
			continue
		}
		if !jsonEquivalent(eff.Value, exp.Value) {
			t.Errorf("%s: value = %s, want %s", exp.Key, eff.Value, exp.Value)
		}
		if string(eff.Source) != exp.Source {
			t.Errorf("%s: source = %q, want %q", exp.Key, eff.Source, exp.Source)
		}
		if eff.Constrained != exp.Constrained {
			t.Errorf("%s: constrained = %v, want %v", exp.Key, eff.Constrained, exp.Constrained)
		}
		if string(eff.ConstraintKind) != exp.ConstraintKind {
			t.Errorf("%s: constraint_kind = %q, want %q", exp.Key, eff.ConstraintKind, exp.ConstraintKind)
		}
		if exp.Constrained {
			if exp.StoredValue == nil {
				t.Errorf("%s: a constrained expectation must declare stored_value", exp.Key)
			} else if !jsonEquivalent(eff.StoredValue, exp.StoredValue) {
				t.Errorf("%s: stored_value = %s, want %s", exp.Key, eff.StoredValue, exp.StoredValue)
			}
			if exp.ConstraintKind == "" {
				t.Errorf("%s: a constrained expectation must declare constraint_kind", exp.Key)
			}
		} else {
			if exp.StoredValue != nil {
				t.Errorf("%s: an unconstrained expectation must not declare stored_value", exp.Key)
			}
			if eff.StoredValue != nil {
				t.Errorf("%s: stored_value = %s reported without a constraint", exp.Key, eff.StoredValue)
			}
		}
	}
}

// conformanceBindings validates the case's injected bindings against the
// manifest and the known constraint kinds.
func conformanceBindings(
	manifest *settingscontract.Manifest,
	declared []conformanceBinding,
) (map[string]*settingscontract.Constraint, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	kinds := map[settingscontract.ConstraintKind]bool{
		settingscontract.ConstraintCeiling:   true,
		settingscontract.ConstraintFloor:     true,
		settingscontract.ConstraintAllowlist: true,
		settingscontract.ConstraintLocked:    true,
	}
	out := make(map[string]*settingscontract.Constraint, len(declared))
	for _, binding := range declared {
		if _, ok := manifest.Lookup(binding.Key); !ok {
			return nil, fmt.Errorf("constraint binding names unknown key %q", binding.Key)
		}
		kind := settingscontract.ConstraintKind(binding.Constraint)
		if !kinds[kind] {
			return nil, fmt.Errorf("constraint binding on %q names unknown kind %q",
				binding.Key, binding.Constraint)
		}
		if binding.PolicyInput == "" {
			return nil, fmt.Errorf("constraint binding on %q has no policy_input", binding.Key)
		}
		if _, dup := out[binding.Key]; dup {
			return nil, fmt.Errorf("duplicate constraint binding for %q", binding.Key)
		}
		out[binding.Key] = &settingscontract.Constraint{
			PolicyInput: binding.PolicyInput,
			Constraint:  kind,
		}
	}
	return out, nil
}

// jsonEquivalent compares two raw JSON values structurally, so formatting and
// object key order never matter.
func jsonEquivalent(a, b json.RawMessage) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
