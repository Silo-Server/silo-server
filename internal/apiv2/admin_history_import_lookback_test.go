package apiv2

import (
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// A run or bulk-run without a lookback stays a full import, as it was before
// the body existed: no body, an empty or whitespace-only one under any
// Content-Type, {} and a null lookback all qualify. lookback_hours turns into
// one cutoff measured back from when the request arrived.
func TestAdminHistoryRunLookbackBecomesPlayedSince(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	run := Prefix + "/admin/history-imports/mappings/2/run"
	bulk := Prefix + "/admin/history-imports/sources/1/bulk-run"
	textPlain := maps.Clone(actingRequestAdmin)
	textPlain["Content-Type"] = "text/plain"

	before := time.Now()
	for _, req := range []struct {
		path, body string
		headers    map[string]string
		hours      int
	}{
		{run, "", actingRequestAdmin, 0},
		{run, `{"lookback_hours":72}`, actingRequestAdmin, 72},
		{bulk, "", textPlain, 0},
		{bulk, `{"lookback_hours":24}`, actingRequestAdmin, 24},
		{run, `{}`, actingRequestAdmin, 0},
		{run, " \n\t ", textPlain, 0},
		{bulk, "  ", actingRequestAdmin, 0},
		{run, `{"lookback_hours":null}`, actingRequestAdmin, 0},
	} {
		f.runOpts = nil
		rec := do(t, h, http.MethodPost, req.path, req.body, req.headers)
		if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
			t.Fatalf("%s %q: %d %s", req.path, req.body, rec.Code, rec.Body.String())
		}
		after := time.Now()
		if len(f.runOpts) != 1 {
			t.Fatalf("%s %q: service calls = %d, want 1", req.path, req.body, len(f.runOpts))
		}
		got := f.runOpts[0].PlayedSince
		if req.hours == 0 {
			if got != nil {
				t.Fatalf("%s %q: PlayedSince = %v, want nil for a full import", req.path, req.body, *got)
			}
			continue
		}
		window := time.Duration(req.hours) * time.Hour
		if got == nil || got.Before(before.Add(-window)) || got.After(after.Add(-window)) {
			t.Fatalf("%s %q: PlayedSince = %v, want %dh before the request", req.path, req.body, got, req.hours)
		}
	}
}

// A body that is not blank still has to be JSON.
func TestAdminHistoryRunLookbackBodyMustBeJSON(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	textPlain := maps.Clone(actingRequestAdmin)
	textPlain["Content-Type"] = "text/plain"
	rec := do(t, h, http.MethodPost, Prefix+"/admin/history-imports/mappings/2/run", `{"lookback_hours":72}`, textPlain)
	requireProblem(t, rec, TypeUnsupportedMediaType)
	if len(f.runOpts) != 0 {
		t.Fatalf("a text/plain body reached the service")
	}
}

// Run responses show the lookback cutoff; a full import leaves it out.
func TestAdminHistoryRunShowsPlayedSince(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	path := Prefix + "/admin/history-imports/runs/" + f.run.ID
	for _, since := range []*time.Time{nil, new(time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC))} {
		f.run.PlayedSince = since
		rec := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		got, present := body["played_since"]
		switch {
		case since == nil && present:
			t.Fatalf("full import: played_since = %v, want absent", got)
		case since != nil && got != "2026-10-14T04:00:00.000Z":
			t.Fatalf("lookback: played_since = %v, want 2026-10-14T04:00:00.000Z", got)
		}
	}
}

func TestAdminHistoryRunLookbackValidation(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	for _, body := range []string{`{"lookback_hours":0}`, `{"lookback_hours":-5}`, `{"lookback_hours":8761}`, `{"lookback_hours":"72"}`, `{"lookback_hours":1.5}`, `{"lookback_hours":72.0}`, `{"lookback_hours":1e2}`} {
		for _, path := range []string{Prefix + "/admin/history-imports/mappings/2/run", Prefix + "/admin/history-imports/sources/1/bulk-run"} {
			rec := do(t, h, http.MethodPost, path, body, actingRequestAdmin)
			requireProblem(t, rec, TypeValidationFailed)
		}
	}
	if len(f.runOpts) != 0 {
		t.Fatalf("invalid bodies reached the service %d times", len(f.runOpts))
	}
}

// A lookback on an Emby or Plex source is refused instead of silently
// importing everything.
func TestAdminHistoryRunLookbackUnsupportedSource(t *testing.T) {
	f := fixtureAdminHistoryImports()
	f.runErr = historyimport.ErrLookbackUnsupported
	h := adminHistoryHandler(f)
	for _, path := range []string{Prefix + "/admin/history-imports/mappings/2/run", Prefix + "/admin/history-imports/sources/1/bulk-run"} {
		rec := do(t, h, http.MethodPost, path, `{"lookback_hours":72}`, actingRequestAdmin)
		requireProblem(t, rec, TypeValidationFailed)
	}
}

func TestAdminHistoryCapabilitiesAdvertiseLookback(t *testing.T) {
	h := adminHistoryHandler(fixtureAdminHistoryImports())
	rec := do(t, h, http.MethodGet, Prefix+"/admin/history-imports/capabilities", "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var body struct {
		PlayedLookback   bool `json:"played_lookback"`
		MaxLookbackHours int  `json:"max_lookback_hours"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.PlayedLookback || body.MaxLookbackHours != 8760 {
		t.Fatalf("capabilities = %+v, want played_lookback with max 8760", body)
	}
}
