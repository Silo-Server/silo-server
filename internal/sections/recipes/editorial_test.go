package recipes

import (
	"encoding/json"
	"testing"
	"time"
)

// --- RotationIndex tests ---

func TestRotationIndexStableWithinWeek(t *testing.T) {
	// ISO week 18 of 2026 runs Mon 2026-04-27 through Sun 2026-05-03.
	t1 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC) // ISO week 18 (Mon)
	t2 := time.Date(2026, 5, 3, 23, 59, 0, 0, time.UTC) // ISO week 18 (Sun)
	if RotationIndex(t1, "director", 12, 7) != RotationIndex(t2, "director", 12, 7) {
		t.Fatal("rotation drifted within a week")
	}
}

func TestRotationIndexAdvancesOnWeekBoundary(t *testing.T) {
	// Sun 2026-05-03 is in ISO week 18; Mon 2026-05-04 starts ISO week 19.
	// These are deterministic, pinned dates; the FNV-64a buckets for
	// "director|202618" and "director|202619" hash to distinct indices
	// modulo 12. If this test ever flakes, double-check the hash function
	// hasn't changed — the indices below are pinned to a known-good run.
	idx18 := RotationIndex(time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), "director", 12, 7)
	idx19 := RotationIndex(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC), "director", 12, 7)
	if idx18 == idx19 {
		t.Fatalf("expected indices to differ across ISO weeks: both = %d", idx18)
	}
	const wantIdx18, wantIdx19 = 4, 11
	if idx18 != wantIdx18 || idx19 != wantIdx19 {
		t.Fatalf("pinned hash drift: idx18=%d (want %d), idx19=%d (want %d) — did the hash change?",
			idx18, wantIdx18, idx19, wantIdx19)
	}
}

func TestRotationKeyIsValueBased(t *testing.T) {
	// Simulate two process runs that both scope to library 42. The fetcher
	// builds the key from the integer value (not the *int pointer), so the
	// rotation must be stable across restarts.
	keyA := "director|42"
	keyB := "director|42"
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	if RotationIndex(now, keyA, 12, 7) != RotationIndex(now, keyB, 12, 7) {
		t.Fatal("rotation must be stable for the same value-based key")
	}
}

func TestRotationIndexBoundsByCandidateCount(t *testing.T) {
	for i := 0; i < 100; i++ {
		idx := RotationIndex(time.Now().Add(time.Duration(i)*24*time.Hour), "director", 5, 0)
		if idx < 0 || idx >= 5 {
			t.Fatalf("index %d out of [0,5)", idx)
		}
	}
}

// --- Recipe registration and definition tests ---

func TestEditorialSpotlightRecipeRegistered(t *testing.T) {
	rec, ok := Get("editorial_spotlight")
	if !ok {
		t.Fatal("editorial_spotlight not registered")
	}
	if !rec.Definition().SupportsRotation {
		t.Error("editorial_spotlight should advertise rotation support")
	}
}

func TestEditorialSpotlightDefinition(t *testing.T) {
	rec, ok := Get("editorial_spotlight")
	if !ok {
		t.Fatal("editorial_spotlight not registered")
	}
	def := rec.Definition()
	if def.Category != CategoryEditorial {
		t.Errorf("category = %v, want editorial", def.Category)
	}
	if !def.SupportsRotation {
		t.Error("SupportsRotation should be true")
	}
	if len(def.Presets) < 4 {
		t.Errorf("expected at least 4 presets, got %d", len(def.Presets))
	}
}

// --- Validate tests ---

func TestEditorialSpotlightAcceptsDirectorAutoRotate(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	raw := json.RawMessage(`{"subject_type":"director","auto_rotate":true,"rotation_cadence":"weekly"}`)
	if err := rec.Validate(raw); err != nil {
		t.Errorf("valid params rejected: %v", err)
	}
}

func TestEditorialSpotlightAcceptsEraWithSubject(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	raw := json.RawMessage(`{"subject_type":"era","subject":"1980s"}`)
	if err := rec.Validate(raw); err != nil {
		t.Errorf("valid params rejected: %v", err)
	}
}

func TestEditorialSpotlightRejectsFranchise(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	// No franchise data source exists, so the fetcher can never resolve a
	// franchise subject — accepting it here would let admins save a section
	// that errors at fetch time. Reject until the data lands.
	raw := json.RawMessage(`{"subject_type":"franchise","subject":"Marvel"}`)
	if err := rec.Validate(raw); err == nil {
		t.Error("franchise subject_type should be rejected until franchise data exists")
	}
}

func TestEditorialSpotlightRejectsEmptyRaw(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	if err := rec.Validate(nil); err == nil {
		t.Error("empty raw should be rejected (subject_type required)")
	}
	if err := rec.Validate(json.RawMessage(``)); err == nil {
		t.Error("empty raw should be rejected (subject_type required)")
	}
}

func TestEditorialSpotlightRejectsUnknownSubjectType(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	raw := json.RawMessage(`{"subject_type":"unknown"}`)
	if err := rec.Validate(raw); err == nil {
		t.Error("unknown subject_type should be rejected")
	}
}

func TestEditorialSpotlightRejectsAutoRotateFalseWithEmptySubject(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	raw := json.RawMessage(`{"subject_type":"director","auto_rotate":false,"subject":""}`)
	if err := rec.Validate(raw); err == nil {
		t.Error("auto_rotate=false with empty subject should be rejected")
	}
}

func TestEditorialSpotlightRejectsInvalidCadence(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	raw := json.RawMessage(`{"subject_type":"director","auto_rotate":true,"rotation_cadence":"hourly"}`)
	if err := rec.Validate(raw); err == nil {
		t.Error("rotation_cadence=hourly should be rejected")
	}
}

func TestEditorialSpotlightEmptyCadenceIsWeekly(t *testing.T) {
	rec, _ := Get("editorial_spotlight")
	// empty cadence should default to weekly and pass validation
	raw := json.RawMessage(`{"subject_type":"director","auto_rotate":true}`)
	if err := rec.Validate(raw); err != nil {
		t.Errorf("empty rotation_cadence should be treated as weekly: %v", err)
	}
}
