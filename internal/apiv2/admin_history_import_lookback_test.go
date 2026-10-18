package apiv2

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// A run or bulk-run without a body stays a full import; lookback_hours turns
// into one cutoff measured back from when the request arrived.
func TestAdminHistoryRunLookbackBecomesPlayedSince(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	run := Prefix + "/admin/history-imports/mappings/2/run"
	bulk := Prefix + "/admin/history-imports/sources/1/bulk-run"

	before := time.Now()
	for _, req := range []struct{ path, body string }{
		{run, ""},
		{run, `{"lookback_hours":72}`},
		{bulk, ""},
		{bulk, `{"lookback_hours":24}`},
		{run, `{}`},
	} {
		rec := do(t, h, http.MethodPost, req.path, req.body, actingRequestAdmin)
		if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", req.path, req.body, rec.Code, rec.Body.String())
		}
	}
	after := time.Now()

	if len(f.runOpts) != 5 {
		t.Fatalf("calls = %d, want 5", len(f.runOpts))
	}
	for _, i := range []int{0, 2, 4} {
		if f.runOpts[i].PlayedSince != nil {
			t.Fatalf("call %d: PlayedSince = %v, want nil for a full import", i, *f.runOpts[i].PlayedSince)
		}
	}
	for i, hours := range map[int]int{1: 72, 3: 24} {
		got := f.runOpts[i].PlayedSince
		if got == nil {
			t.Fatalf("call %d: PlayedSince = nil, want %dh before the request", i, hours)
		}
		window := time.Duration(hours) * time.Hour
		if got.Before(before.Add(-window)) || got.After(after.Add(-window)) {
			t.Fatalf("call %d: PlayedSince = %v, want within [%v, %v]", i, got, before.Add(-window), after.Add(-window))
		}
	}
}

func TestAdminHistoryRunLookbackValidation(t *testing.T) {
	f := fixtureAdminHistoryImports()
	h := adminHistoryHandler(f)
	for _, body := range []string{`{"lookback_hours":0}`, `{"lookback_hours":-5}`, `{"lookback_hours":8761}`, `{"lookback_hours":"72"}`, `{"lookback_hours":1.5}`} {
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
