package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newValuesTestHandler(t *testing.T) (*SettingValuesHandler, userstore.UserStore) {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(),
		userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	return NewSettingValuesHandler(testUserStoreProvider{store: store}, contract), store
}

// valuesRequest builds a request carrying the session identity the handlers
// read: user, profile and device all come from context or headers rather than
// the query string, so one profile cannot address another's settings.
func valuesRequest(method, target string, body []byte) *http.Request {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	}
	req.Header.Set(deviceIDHeader, "device-1")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1})
	return req.WithContext(apimw.SetProfileID(ctx, "profile-1"))
}

// routeValues wires the chi URL params the handlers read from the path.
func routeValues(t *testing.T, h *SettingValuesHandler, method, key, query string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	target := "/settings/values/" + key
	if query != "" {
		target += "?" + query
	}
	req := valuesRequest(method, target, body)

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("key", key)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		h.HandleGetValue(rec, req)
	case http.MethodPut:
		h.HandleSetValue(rec, req)
	case http.MethodDelete:
		h.HandleDeleteValue(rec, req)
	}
	return rec
}

func TestSettingValuesRoundTrip(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	// Nothing stored yet.
	if rec := routeValues(t, handler, http.MethodGet,
		"playback.subtitle_language", "scope=profile", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET before write = %d, want 404", rec.Code)
	}

	rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile", []byte(`{"value":"ja"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	var stored settingValueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decoding PUT response: %v", err)
	}
	if string(stored.Value) != `"ja"` || stored.Scope != "profile" {
		t.Errorf("stored %s at %s, want \"ja\" at profile", stored.Value, stored.Scope)
	}

	rec = routeValues(t, handler, http.MethodGet, "playback.subtitle_language", "scope=profile", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after write = %d", rec.Code)
	}

	if rec := routeValues(t, handler, http.MethodDelete,
		"playback.subtitle_language", "scope=profile", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if rec := routeValues(t, handler, http.MethodGet,
		"playback.subtitle_language", "scope=profile", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", rec.Code)
	}
}

func TestGetSettingValuesReportsSetAndUnsetAtOneScope(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key: "playback.subtitle_mode", Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
	}, json.RawMessage(`"always"`)); err != nil {
		t.Fatalf("seeding explicit value: %v", err)
	}

	req := valuesRequest(http.MethodGet,
		"/settings/values?keys=playback.subtitle_mode,playback.subtitle_language&scope=profile", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetValues(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET collection = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Values   []map[string]any `json:"values"`
		Revision int              `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding collection: %v", err)
	}
	if len(body.Values) != 2 {
		t.Fatalf("values = %d, want 2: %s", len(body.Values), rec.Body.String())
	}
	if body.Values[0]["key"] != "playback.subtitle_mode" || body.Values[0]["is_set"] != true ||
		body.Values[0]["value"] != "always" {
		t.Errorf("stored entry = %#v", body.Values[0])
	}
	if body.Values[1]["key"] != "playback.subtitle_language" || body.Values[1]["is_set"] != false {
		t.Errorf("unset entry = %#v", body.Values[1])
	}
	if _, present := body.Values[1]["value"]; present {
		t.Errorf("unset entry contains value: %#v", body.Values[1])
	}
	contract, _ := settingscontract.Load()
	if body.Revision != contract.Revision {
		t.Errorf("contract revision = %d, want %d", body.Revision, contract.Revision)
	}
}

type settingValuesLibraryLookup struct {
	existing map[int]bool
	err      error
}

func (l settingValuesLibraryLookup) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if l.err != nil {
		return nil, l.err
	}
	if !l.existing[id] {
		return nil, catalog.ErrFolderNotFound
	}
	return &models.MediaFolder{ID: id}, nil
}

func TestSettingValuesRejectNonexistentLibraryContext(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	handler.SetLibraryLookup(settingValuesLibraryLookup{existing: map[int]bool{7: true}})

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		var body []byte
		if method == http.MethodPut {
			body = []byte(`{"value":"de"}`)
		}
		rec := routeValues(t, handler, method, "playback.subtitle_language",
			"scope=profile_library&library_id=99", body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s nonexistent library = %d, want 404: %s", method, rec.Code, rec.Body.String())
		}
	}
	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key: "playback.subtitle_language", Scope: settingscontract.ScopeProfileLibrary,
		ProfileID: "profile-1", LibraryID: 99,
	})
	if err != nil || value != nil {
		t.Fatalf("nonexistent library left value (%+v, %v)", value, err)
	}

	if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile_library&library_id=7", []byte(`{"value":"de"}`)); rec.Code != http.StatusOK {
		t.Errorf("existing library write = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestUnknownKeysAreRefused is the extension bag closing. The legacy endpoint
// stored any unregistered key as an unvalidated string, which is how six ui.*
// settings and five orphan keys reached production untyped.
func TestUnknownKeysAreRefused(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	rec := routeValues(t, handler, http.MethodPut, "totally.invented.key",
		"scope=profile", []byte(`{"value":"x"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT of an unknown key = %d, want 404: %s", rec.Code, rec.Body.String())
	}

	// A contract-known local setting is not server storage either.
	rec = routeValues(t, handler, http.MethodPut, "downloads.wifi_only",
		"scope=profile", []byte(`{"value":true}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT of a client_local key = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestInvalidValuesAreRefusedByType covers what the string-only endpoint could
// not check at all.
func TestInvalidValuesAreRefusedByType(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	for name, tc := range map[string]struct{ key, body string }{
		"enum member":      {"playback.subtitle_mode", `{"value":"sideways"}`},
		"integer range":    {"playback.next_up_prompt_seconds", `{"value":9999}`},
		"wrong type":       {"playback.auto_skip_intro", `{"value":"yes"}`},
		"quoted number":    {"playback.next_up_prompt_seconds", `{"value":"30"}`},
		"bad language":     {"playback.subtitle_language", `{"value":"!!!"}`},
		"object schema":    {"playback.subtitle_appearance", `{"value":{"fontSize":"enormous"}}`},
		"null when not ok": {"playback.subtitle_mode", `{"value":null}`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := routeValues(t, handler, http.MethodPut, tc.key, "scope=profile", []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("PUT %s = %d, want 400: %s", tc.body, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestScopeMustBeAllowedByTheContract. A definition declares where it may be
// written; the identity being well-formed is a separate question.
func TestScopeMustBeAllowedByTheContract(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	// ui.custom_css is profile-only, so a device override is refused.
	rec := routeValues(t, handler, http.MethodPut, "ui.custom_css",
		"scope=profile_device", []byte(`{"value":"body{}"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("device write to a profile-only setting = %d, want 400: %s",
			rec.Code, rec.Body.String())
	}

	// A missing scope is a request error rather than a silent default: writing
	// to the wrong scope is exactly the mistake this API exists to prevent.
	rec = routeValues(t, handler, http.MethodPut, "playback.subtitle_mode", "",
		[]byte(`{"value":"always"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("write with no scope = %d, want 400", rec.Code)
	}
}

// TestLibraryAndSeriesScopesNeedTheirIdentity.
func TestLibraryAndSeriesScopesNeedTheirIdentity(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile_library", []byte(`{"value":"de"}`)); rec.Code != http.StatusBadRequest {
		t.Errorf("library scope without library_id = %d, want 400", rec.Code)
	}
	if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile_series", []byte(`{"value":"de"}`)); rec.Code != http.StatusBadRequest {
		t.Errorf("series scope without series_id = %d, want 400", rec.Code)
	}

	if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile_library&library_id=7", []byte(`{"value":"de"}`)); rec.Code != http.StatusOK {
		t.Errorf("library write = %d, want 200", rec.Code)
	}
	if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
		"scope=profile_series&series_id=s1", []byte(`{"value":"ja"}`)); rec.Code != http.StatusOK {
		t.Errorf("series write = %d, want 200", rec.Code)
	}
}

// TestEffectiveResolvesThroughTheLadder proves the route is wired to the real
// resolver rather than reading one scope.
func TestEffectiveResolvesThroughTheLadder(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	write := func(query, value string) {
		t.Helper()
		if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
			query, []byte(`{"value":`+value+`}`)); rec.Code != http.StatusOK {
			t.Fatalf("seeding %s = %d: %s", query, rec.Code, rec.Body.String())
		}
	}
	write("scope=profile", `"en"`)
	write("scope=profile_device", `"de"`)
	write("scope=profile_series&series_id=s1", `"ja"`)

	effective := func(query string) effectiveSettingValueResponse {
		t.Helper()
		req := valuesRequest(http.MethodGet, "/settings/values/effective?"+query, nil)
		rec := httptest.NewRecorder()
		handler.HandleGetEffective(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("effective %s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Settings []effectiveSettingValueResponse `json:"settings"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if len(body.Settings) != 1 {
			t.Fatalf("got %d settings, want 1", len(body.Settings))
		}
		return body.Settings[0]
	}

	// Without a series context the device override is the most specific match.
	got := effective("keys=playback.subtitle_language")
	if string(got.Value) != `"de"` || got.Source != "profile_device" {
		t.Errorf("no-series resolution = %s from %s, want \"de\" from profile_device",
			got.Value, got.Source)
	}

	// Naming the series promotes its override.
	got = effective("keys=playback.subtitle_language&series_ids=s1")
	if string(got.Value) != `"ja"` || got.Source != "profile_series" {
		t.Errorf("series resolution = %s from %s, want \"ja\" from profile_series",
			got.Value, got.Source)
	}
	if got.SeriesID != "s1" {
		t.Errorf("resolved identity series = %q, want s1 so a client can reset it", got.SeriesID)
	}
}

func TestPostEffectiveResolvesContentContexts(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	for seriesID, value := range map[string]string{"s1": `"ja"`, "s2": `"de"`} {
		if rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_language",
			"scope=profile_series&series_id="+seriesID,
			[]byte(`{"value":`+value+`}`)); rec.Code != http.StatusOK {
			t.Fatalf("seeding %s = %d: %s", seriesID, rec.Code, rec.Body.String())
		}
	}

	body := []byte(`{
		"keys":["playback.subtitle_language"],
		"contexts":[
			{"context_id":"first","library_id":"7","series_id":"s1"},
			{"context_id":"second","library_id":7,"series_id":"s2"}
		]
	}`)
	req := valuesRequest(http.MethodPost, "/settings/values/effective", body)
	rec := httptest.NewRecorder()
	handler.HandlePostEffective(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST effective = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Contexts []struct {
			ContextID string                          `json:"context_id"`
			Settings  []effectiveSettingValueResponse `json:"settings"`
		} `json:"contexts"`
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(response.Contexts) != 2 {
		t.Fatalf("contexts = %d, want 2", len(response.Contexts))
	}
	if response.Contexts[0].ContextID != "first" ||
		string(response.Contexts[0].Settings[0].Value) != `"ja"` ||
		response.Contexts[1].ContextID != "second" ||
		string(response.Contexts[1].Settings[0].Value) != `"de"` {
		t.Errorf("context response = %#v", response.Contexts)
	}
}

func TestPostEffectiveRejectsInvalidContexts(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	for name, body := range map[string]string{
		"empty":           `{"keys":["ui.custom_css"],"contexts":[]}`,
		"duplicate id":    `{"keys":["ui.custom_css"],"contexts":[{"context_id":"x","series_id":"s1"},{"context_id":"x","series_id":"s2"}]}`,
		"missing content": `{"keys":["ui.custom_css"],"contexts":[{"context_id":"x"}]}`,
		"invalid library": `{"keys":["ui.custom_css"],"contexts":[{"context_id":"x","library_id":"nope"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := valuesRequest(http.MethodPost, "/settings/values/effective", []byte(body))
			rec := httptest.NewRecorder()
			handler.HandlePostEffective(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestEffectiveRejectsUnknownKeys. Omitting an unknown key silently lets a
// client fill the gap with its own vendored default and present a value this
// server would refuse to store.
func TestEffectiveRejectsUnknownKeys(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	req := valuesRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode,totally.invented.key", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetEffective(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown key = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "totally.invented.key") {
		t.Errorf("the error does not name the offending key: %s", rec.Body.String())
	}
}

// TestEffectiveRequiresDeviceIdentityForDeviceAwareKeys: resolving a
// device-capable key without a device identity would silently skip stored
// device overrides and pass the profile fallback off as effective.
func TestEffectiveRequiresDeviceIdentityForDeviceAwareKeys(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	effective := func(query string) *httptest.ResponseRecorder {
		req := valuesRequest(http.MethodGet, "/settings/values/effective?"+query, nil)
		req.Header.Del(deviceIDHeader)
		rec := httptest.NewRecorder()
		handler.HandleGetEffective(rec, req)
		return rec
	}

	// playback.subtitle_language allows profile_device, so it needs the header.
	if rec := effective("keys=playback.subtitle_language"); rec.Code != http.StatusBadRequest {
		t.Errorf("device-aware key without a device id = %d, want 400: %s",
			rec.Code, rec.Body.String())
	}
	// The no-keys form resolves every remote definition, which includes
	// device-aware ones.
	if rec := effective(""); rec.Code != http.StatusBadRequest {
		t.Errorf("all-keys request without a device id = %d, want 400", rec.Code)
	}
	// ui.custom_css is profile-only: no device identity needed.
	if rec := effective("keys=ui.custom_css"); rec.Code != http.StatusOK {
		t.Errorf("profile-only key without a device id = %d, want 200: %s",
			rec.Code, rec.Body.String())
	}
}

// TestEffectiveWithNoKeysReturnsEveryRemoteSetting, which is what a settings
// screen opening for the first time asks for.
func TestEffectiveWithNoKeysReturnsEveryRemoteSetting(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	req := valuesRequest(http.MethodGet, "/settings/values/effective", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetEffective(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("effective = %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Settings []effectiveSettingValueResponse `json:"settings"`
		Revision int                             `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	contract, _ := settingscontract.Load()
	remote := 0
	for i := range contract.Definitions {
		if contract.Definitions[i].IsRemote() {
			remote++
		}
	}
	if len(body.Settings) != remote {
		t.Errorf("returned %d settings, want every remote definition (%d)",
			len(body.Settings), remote)
	}
	if body.Revision != contract.Revision {
		t.Errorf("revision = %d, want %d", body.Revision, contract.Revision)
	}
	// Everything unset resolves to its contract default.
	for _, setting := range body.Settings {
		if setting.Source != string(settingscontract.ScopeDefault) {
			t.Errorf("%s resolved from %s with nothing stored", setting.Key, setting.Source)
		}
	}
}

// TestEffectiveAppliesViewerQualityCap wires the preferences-versus-restrictions
// seam end to end: the access scope's MaxPlaybackQuality becomes the resolver's
// ceiling, the effective value is the cap, and the authored preference survives
// untouched so it takes effect the day the cap lifts.
func TestEffectiveAppliesViewerQualityCap(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	if rec := routeValues(t, handler, http.MethodPut, "playback.preferred_quality",
		"scope=profile", []byte(`{"value":"2160p"}`)); rec.Code != http.StatusOK {
		t.Fatalf("seeding preference = %d: %s", rec.Code, rec.Body.String())
	}

	effective := func(maxQuality string) effectiveSettingValueResponse {
		t.Helper()
		req := valuesRequest(http.MethodGet,
			"/settings/values/effective?keys=playback.preferred_quality", nil)
		req = req.WithContext(access.SetScope(req.Context(), access.Scope{
			UserID:             1,
			ProfileID:          "profile-1",
			MaxPlaybackQuality: maxQuality,
		}))
		rec := httptest.NewRecorder()
		handler.HandleGetEffective(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("effective = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Settings []effectiveSettingValueResponse `json:"settings"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if len(body.Settings) != 1 {
			t.Fatalf("got %d settings, want 1", len(body.Settings))
		}
		return body.Settings[0]
	}

	// Capped at 1080p: the cap is the answer, the choice is reported alongside.
	got := effective("1080p")
	if string(got.Value) != `"1080p"` {
		t.Errorf("capped effective = %s, want \"1080p\"", got.Value)
	}
	if !got.Constrained || got.ConstraintKind != string(settingscontract.ConstraintCeiling) {
		t.Errorf("constrained=%v kind=%q, want true/ceiling", got.Constrained, got.ConstraintKind)
	}
	if string(got.StoredValue) != `"2160p"` {
		t.Errorf("stored_value = %s, want the authored \"2160p\" reported", got.StoredValue)
	}
	if string(got.RequestedValue) != `"2160p"` {
		t.Errorf("requested_value = %s, want authored 2160p", got.RequestedValue)
	}
	if got.ConstrainedBy == nil || got.ConstrainedBy.PolicyInput != policyInputMaxPlaybackQuality ||
		got.ConstrainedBy.Constraint != settingscontract.ConstraintCeiling {
		t.Errorf("constrained_by = %#v", got.ConstrainedBy)
	}
	if len(got.PermittedValues) == 0 || string(got.PermittedValues[len(got.PermittedValues)-1]) != `"1080p"` {
		t.Errorf("permitted_values = %q, want choices through 1080p", got.PermittedValues)
	}

	// The stored row itself was not rewritten by resolution.
	stored, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       "playback.preferred_quality",
		Scope:     settingscontract.ScopeProfile,
		ProfileID: "profile-1",
	})
	if err != nil || stored == nil {
		t.Fatalf("reading stored value: %v", err)
	}
	if string(stored.Value) != `"2160p"` {
		t.Errorf("stored row = %s, want \"2160p\" untouched", stored.Value)
	}

	// An uncapped viewer ("" means the policy sets no cap) gets the preference
	// as authored, with no constraint reported.
	got = effective("")
	if string(got.Value) != `"2160p"` || got.Constrained {
		t.Errorf("uncapped effective = %s constrained=%v, want \"2160p\"/false",
			got.Value, got.Constrained)
	}
	if got.StoredValue != nil {
		t.Errorf("stored_value = %s, want absent when nothing was narrowed", got.StoredValue)
	}
}

// TestMutationIDMakesWritesIdempotent covers the retry a mobile client performs
// when a response is lost.
func TestMutationIDMakesWritesIdempotent(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	send := func(mutationID, value string) *httptest.ResponseRecorder {
		req := valuesRequest(http.MethodPut,
			"/settings/values/playback.subtitle_mode?scope=profile",
			[]byte(`{"value":`+value+`}`))
		req.Header.Set(mutationIDHeader, mutationID)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("key", "playback.subtitle_mode")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rec := httptest.NewRecorder()
		handler.HandleSetValue(rec, req)
		return rec
	}

	if rec := send("mut-1", `"always"`); rec.Code != http.StatusOK {
		t.Fatalf("first write = %d: %s", rec.Code, rec.Body.String())
	}

	// The same id and body replays the receipt rather than writing again.
	replay := send("mut-1", `"always"`)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay = %d: %s", replay.Code, replay.Body.String())
	}
	if replay.Header().Get("X-Silo-Idempotent-Replay") != "true" {
		t.Error("a repeated mutation id was not reported as a replay")
	}

	// The same id with different content is a conflict, not a silent overwrite.
	conflict := send("mut-1", `"off"`)
	if conflict.Code != http.StatusConflict {
		t.Errorf("reused id with new content = %d, want 409", conflict.Code)
	}

	// The stored value is still the first write's.
	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       "playback.subtitle_mode",
		Scope:     settingscontract.ScopeProfile,
		ProfileID: "profile-1",
	})
	if err != nil || value == nil {
		t.Fatalf("reading stored value: %v", err)
	}
	if string(value.Value) != `"always"` {
		t.Errorf("stored value = %s, want the first write preserved", value.Value)
	}
}

// TestMutationReceiptReplaysTheStoredResponse: the receipt is the response the
// original write returned, so a replay carries the real revision and
// updated_at rather than a reconstruction of the request.
func TestMutationReceiptReplaysTheStoredResponse(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	send := func() *httptest.ResponseRecorder {
		req := valuesRequest(http.MethodPut,
			"/settings/values/playback.subtitle_mode?scope=profile",
			[]byte(`{"value":"always"}`))
		req.Header.Set(mutationIDHeader, "mut-replay")
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("key", "playback.subtitle_mode")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rec := httptest.NewRecorder()
		handler.HandleSetValue(rec, req)
		return rec
	}

	first := send()
	if first.Code != http.StatusOK {
		t.Fatalf("first write = %d: %s", first.Code, first.Body.String())
	}
	replay := send()
	if replay.Code != http.StatusOK {
		t.Fatalf("replay = %d: %s", replay.Code, replay.Body.String())
	}
	if strings.TrimSpace(first.Body.String()) != strings.TrimSpace(replay.Body.String()) {
		t.Errorf("replay body diverged from the original response:\n first: %s\nreplay: %s",
			first.Body.String(), replay.Body.String())
	}
	var original settingValueResponse
	if err := json.Unmarshal(first.Body.Bytes(), &original); err != nil {
		t.Fatalf("decoding original response: %v", err)
	}
	if original.Revision == 0 || original.UpdatedAt == "" {
		t.Errorf("original response revision=%d updated_at=%q — the replayed "+
			"receipt must carry the stored row, not the request",
			original.Revision, original.UpdatedAt)
	}
}

// failingUpsertStore simulates the store failing the write itself — the
// PostgreSQL profile FK rejecting a row, a dropped connection — while every
// other operation, the receipt lookup and insert included, works.
type failingUpsertStore struct {
	userstore.UserStore
}

func (failingUpsertStore) UpsertSettingValue(
	context.Context, userstore.SettingIdentity, json.RawMessage,
) (*userstore.SettingValue, error) {
	return nil, errors.New("simulated write failure")
}

// TestFailedWritesLeaveNoReceipt: a receipt for a write that never landed
// would turn the client's retry of a 500 into a silent success replay.
func TestFailedWritesLeaveNoReceipt(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	handler.storeProvider = testUserStoreProvider{store: failingUpsertStore{UserStore: store}}

	send := func() *httptest.ResponseRecorder {
		req := valuesRequest(http.MethodPut,
			"/settings/values/playback.subtitle_mode?scope=profile",
			[]byte(`{"value":"always"}`))
		req.Header.Set(mutationIDHeader, "mut-fail")
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("key", "playback.subtitle_mode")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rec := httptest.NewRecorder()
		handler.HandleSetValue(rec, req)
		return rec
	}

	if rec := send(); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed write = %d, want 500", rec.Code)
	}
	prior, err := store.GetSettingMutation(context.Background(), "mut-fail")
	if err != nil {
		t.Fatalf("reading mutation receipt: %v", err)
	}
	if prior != nil {
		t.Error("a failed write left an idempotency receipt; its retry would replay a success")
	}
	// And the retry actually retries: with the store healthy again it stores
	// the value rather than replaying a phantom result.
	handler.storeProvider = testUserStoreProvider{store: store}
	retry := send()
	if retry.Code != http.StatusOK {
		t.Fatalf("retry after failure = %d: %s", retry.Code, retry.Body.String())
	}
	if retry.Header().Get("X-Silo-Idempotent-Replay") == "true" {
		t.Error("the retry was served as a replay of the failed attempt")
	}
}

// TestMutationBodyMustBeOneDocument: trailing content after the envelope means
// different parsers could disagree about which mutation was requested.
func TestMutationBodyMustBeOneDocument(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	rec := routeValues(t, handler, http.MethodPut, "playback.subtitle_mode",
		"scope=profile", []byte(`{"value":"always"}{"value":"off"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("concatenated envelopes = %d, want 400", rec.Code)
	}
	rec = routeValues(t, handler, http.MethodPut, "playback.subtitle_mode",
		"scope=profile", []byte(`{"value":"always"} trailing`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("trailing garbage = %d, want 400", rec.Code)
	}
	// Trailing whitespace is not content.
	rec = routeValues(t, handler, http.MethodPut, "playback.subtitle_mode",
		"scope=profile", []byte(`{"value":"always"}`+"\n"))
	if rec.Code != http.StatusOK {
		t.Errorf("trailing newline = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// TestContractIsServedWithAnETag. Clients vendor a pinned copy and generate
// bindings from it, so the common request asks "still the same contract?".
func TestContractIsServedWithAnETag(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	rec := httptest.NewRecorder()
	handler.HandleGetContract(rec, httptest.NewRequest(http.MethodGet, "/settings/contract", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET contract = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the contract response")
	}

	var manifest map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("contract body is not JSON: %v", err)
	}
	// Maintainer notes are stripped from the public projection.
	if definitions, ok := manifest["definitions"].([]any); ok && len(definitions) > 0 {
		if first, ok := definitions[0].(map[string]any); ok {
			if _, leaked := first["notes"]; leaked {
				t.Error("maintainer notes leaked into the public contract")
			}
		}
	}

	conditional := httptest.NewRequest(http.MethodGet, "/settings/contract", nil)
	conditional.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	handler.HandleGetContract(rec, conditional)
	if rec.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", rec.Code)
	}
}

func TestCapabilitiesReportTheContractRevision(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	rec := httptest.NewRecorder()
	handler.HandleGetCapabilities(rec,
		httptest.NewRequest(http.MethodGet, "/settings/contract/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities = %d", rec.Code)
	}

	var body struct {
		APIVersion int      `json:"api_version"`
		Revision   int      `json:"revision"`
		Scopes     []string `json:"scopes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	contract, _ := settingscontract.Load()
	if body.APIVersion != contract.APIVersion || body.Revision != contract.Revision {
		t.Errorf("reported %d/%d, want %d/%d",
			body.APIVersion, body.Revision, contract.APIVersion, contract.Revision)
	}
	if len(body.Scopes) != 5 {
		t.Errorf("reported %d scopes, want 5", len(body.Scopes))
	}
}

// storedDeviceIDFor reports the device a profile_device row was written for, or
// "" when the key has no device-scoped row at all. The device-widening tests
// assert on stored rows rather than status codes: before the query parameter is
// honored a named device is silently ignored and the write lands on the header
// device, which is a 200 either way.
func storedDeviceIDFor(t *testing.T, store userstore.UserStore, key string) string {
	t.Helper()
	values, err := store.ListAllSettingValues(context.Background())
	if err != nil {
		t.Fatalf("listing stored values: %v", err)
	}
	for _, value := range values {
		if value.Key == key && value.Scope == settingscontract.ScopeProfileDevice {
			return value.DeviceID
		}
	}
	return ""
}

func TestSetValue_RejectsDeviceNotOwnedByCaller(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatal("store does not implement DeviceRegistry")
	}
	if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: "profile-1", DeviceID: "device-1", DeviceName: "Laptop",
	}); err != nil {
		t.Fatalf("registering caller device: %v", err)
	}

	rec := routeValues(t, handler, http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&device_id=device-someone-else", []byte(`{"value":false}`))

	// 404 rather than 403: a 403 would confirm the device id exists.
	if rec.Code != http.StatusNotFound {
		t.Errorf("PUT naming an unknown device = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "" {
		t.Fatalf("wrote a row for device %q; want no write", got)
	}
}

func TestSetValue_WritesNamedDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	registry := store.(userstore.DeviceRegistry)
	for _, id := range []string{"device-1", "device-b"} {
		if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
			ProfileID: "profile-1", DeviceID: id,
		}); err != nil {
			t.Fatalf("registering %s: %v", id, err)
		}
	}

	// The request's own header is device-1; the query names device-b.
	rec := routeValues(t, handler, http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&device_id=device-b", []byte(`{"value":false}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "device-b" {
		t.Errorf("stored on device %q, want device-b", got)
	}
}

func TestGetValue_ReadsNamedDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	registry := store.(userstore.DeviceRegistry)
	if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: "profile-1", DeviceID: "device-b",
	}); err != nil {
		t.Fatalf("registering device-b: %v", err)
	}
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       "player.hdr_enabled",
		Scope:     settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1", DeviceID: "device-b",
	}, json.RawMessage(`false`)); err != nil {
		t.Fatalf("seeding device-b value: %v", err)
	}

	rec := routeValues(t, handler, http.MethodGet, "player.hdr_enabled",
		"scope=profile_device&device_id=device-b", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET named device = %d: %s", rec.Code, rec.Body.String())
	}
	var got settingValueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.DeviceID != "device-b" || string(got.Value) != "false" {
		t.Errorf("read %s on %q, want false on device-b", got.Value, got.DeviceID)
	}

	// The caller's own device has no row, so it must still 404.
	if rec := routeValues(t, handler, http.MethodGet, "player.hdr_enabled",
		"scope=profile_device", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET own device = %d, want 404", rec.Code)
	}
}

// The regression guard for every existing client: with no device_id in the
// query the header device is used, exactly as before this parameter existed.
func TestSetValue_FallsBackToHeaderDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	rec := routeValues(t, handler, http.MethodPut, "player.hdr_enabled",
		"scope=profile_device", []byte(`{"value":false}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "device-1" {
		t.Errorf("stored on device %q, want the header device device-1", got)
	}
}

func TestDeleteValue_RejectsDeviceNotOwnedByCaller(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	rec := routeValues(t, handler, http.MethodDelete, "player.hdr_enabled",
		"scope=profile_device&device_id=device-someone-else", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE naming an unknown device = %d, want 404", rec.Code)
	}
}

// --- Household widening: a primary profile addressing a sibling profile ---

// newHouseholdValuesHandler builds a handler with two profiles on one account:
// "profile-1" is the household parent, "profile-2" is another member.
func newHouseholdValuesHandler(t *testing.T, pin string) (*SettingValuesHandler, userstore.UserStore) {
	t.Helper()
	handler, store := newValuesTestHandler(t)

	// profile-1 is already the household parent: is_primary is assigned to the
	// first profile an account creates and is not settable afterwards.
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Robin"}); err != nil {
		t.Fatalf("create sibling: %v", err)
	}
	primary, err := store.GetProfile(ctx, "profile-1")
	if err != nil || primary == nil || !primary.IsPrimary {
		t.Fatalf("profile-1 is not the primary profile (%+v, %v)", primary, err)
	}
	if pin != "" {
		if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{
			PIN: &pin,
		}); err != nil {
			t.Fatalf("set pin: %v", err)
		}
	}

	registry := store.(userstore.DeviceRegistry)
	if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{
		ProfileID: "profile-2", DeviceID: "robin-ipad", DeviceName: "Robin's iPad",
	}); err != nil {
		t.Fatalf("registering sibling device: %v", err)
	}

	handler.UserRepo = stubUserRepo{user: &models.User{ID: 1}}
	handler.ProfileTokens = access.NewProfileTokenService("test-secret-value-at-least-32-chars", 0)
	return handler, store
}

// routeValuesAs is routeValues with an explicit acting profile, so a test can
// call as a non-primary member of the same household.
func routeValuesAs(
	t *testing.T, h *SettingValuesHandler, actingProfileID, method, key, query string, body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	target := "/settings/values/" + key
	if query != "" {
		target += "?" + query
	}
	req := valuesRequest(method, target, body)
	req = req.WithContext(apimw.SetProfileID(req.Context(), actingProfileID))

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("key", key)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		h.HandleGetValue(rec, req)
	case http.MethodPut:
		h.HandleSetValue(rec, req)
	case http.MethodDelete:
		h.HandleDeleteValue(rec, req)
	}
	return rec
}

func TestSetValue_NonPrimaryCannotNameSiblingProfile(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "")

	rec := routeValuesAs(t, handler, "profile-2", http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&profile_id=profile-1&device_id=device-1", []byte(`{"value":false}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-primary naming a sibling = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "" {
		t.Errorf("wrote a row on device %q; want no write", got)
	}
}

func TestSetValue_PrimaryWithUnverifiedPINCannotNameSibling(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "1234")

	rec := routeValuesAs(t, handler, "profile-1", http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&profile_id=profile-2&device_id=robin-ipad", []byte(`{"value":false}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("primary with unverified PIN = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "pin") {
		t.Errorf("error does not mention the PIN: %s", rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "" {
		t.Errorf("wrote a row on device %q; want no write", got)
	}
}

// A profile id that is not on this account resolves out of the caller's own
// store, so it is simply absent — 404, and the caller learns nothing about
// whether it exists elsewhere.
func TestSetValue_ProfileFromAnotherAccountIsNotFound(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "")

	rec := routeValuesAs(t, handler, "profile-1", http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&profile_id=someone-elses-profile&device_id=device-1",
		[]byte(`{"value":false}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign profile = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "" {
		t.Errorf("wrote a row on device %q; want no write", got)
	}
}

func TestSetValue_PrimaryWritesSiblingProfileDeviceSetting(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "")

	rec := routeValuesAs(t, handler, "profile-1", http.MethodPut, "playback.subtitle_mode",
		"scope=profile_device&profile_id=profile-2&device_id=robin-ipad", []byte(`{"value":"always"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("primary writing a sibling = %d: %s", rec.Code, rec.Body.String())
	}

	values, err := store.ListAllSettingValues(context.Background())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	var found bool
	for _, value := range values {
		if value.Key != "playback.subtitle_mode" {
			continue
		}
		found = true
		if value.ProfileID != "profile-2" || value.DeviceID != "robin-ipad" {
			t.Errorf("stored on (%s, %s), want (profile-2, robin-ipad)",
				value.ProfileID, value.DeviceID)
		}
	}
	if !found {
		t.Error("no row stored for the sibling profile")
	}
}

// A device belonging to a different profile than the one being addressed is
// still rejected: the household widening changes who you may act for, not
// which devices belong to whom.
func TestSetValue_PrimaryCannotMixSiblingProfileWithForeignDevice(t *testing.T) {
	handler, _ := newHouseholdValuesHandler(t, "")

	rec := routeValuesAs(t, handler, "profile-1", http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&profile_id=profile-2&device_id=not-robins-device",
		[]byte(`{"value":false}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("sibling profile with a foreign device = %d, want 404: %s",
			rec.Code, rec.Body.String())
	}
}

// Naming your own profile explicitly is not a household action and must work
// for anyone — it is what a client does when it sends the identity it read back.
func TestSetValue_NamingOwnProfileIsAllowedForAnyone(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "")

	rec := routeValuesAs(t, handler, "profile-2", http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&profile_id=profile-2&device_id=robin-ipad", []byte(`{"value":false}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("naming own profile = %d: %s", rec.Code, rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "robin-ipad" {
		t.Errorf("stored on device %q, want robin-ipad", got)
	}
}

// Registration means "this device is in use by this profile". A write aimed at
// some *other* device, or made on another profile's behalf, is not that — and
// registering the actor's browser under the target profile would invent a
// device nobody is holding.
func TestSetValue_DoesNotRegisterWhenActingOnAnotherDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	registry := store.(userstore.DeviceRegistry)
	if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: "profile-1", DeviceID: "apple-tv", DeviceName: "Apple TV",
	}); err != nil {
		t.Fatalf("registering apple-tv: %v", err)
	}

	// Header device is device-1; the write targets apple-tv.
	if rec := routeValues(t, handler, http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&device_id=apple-tv", []byte(`{"value":false}`)); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	exists, err := registry.DeviceExists(context.Background(), "profile-1", "device-1")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if exists {
		t.Error("registered the acting device while writing to a different device")
	}
}

func TestSetValue_DoesNotRegisterActorsDeviceUnderAnotherProfile(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "")

	if rec := routeValuesAs(t, handler, "profile-1", http.MethodPut, "player.hdr_enabled",
		"scope=profile_device&profile_id=profile-2&device_id=robin-ipad",
		[]byte(`{"value":false}`)); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-2", "device-1")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if exists {
		t.Error("registered the parent's browser under the child's profile")
	}
}

// captureAuditLogs swaps the default slog handler for the duration of a test.
// The returned function parses whatever has been emitted so far and keeps only
// the settings-audit records.
func captureAuditLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return func() []map[string]any {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if record["msg"] == settingsAuditMsg {
				records = append(records, record)
			}
		}
		return records
	}
}

func TestSetValue_AuditsCrossProfileWrite(t *testing.T) {
	handler, _ := newHouseholdValuesHandler(t, "")
	audited := captureAuditLogs(t)

	if rec := routeValuesAs(t, handler, "profile-1", http.MethodPut, "playback.subtitle_mode",
		"scope=profile_device&profile_id=profile-2&device_id=robin-ipad",
		[]byte(`{"value":"always"}`)); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	records := audited()
	if len(records) != 1 {
		t.Fatalf("emitted %d audit records, want 1: %+v", len(records), records)
	}
	record := records[0]
	if record["actor_profile_id"] != "profile-1" || record["target_profile_id"] != "profile-2" {
		t.Errorf("actor/target = %v/%v, want profile-1/profile-2",
			record["actor_profile_id"], record["target_profile_id"])
	}
	if record["setting_key"] != "playback.subtitle_mode" || record["device_id"] != "robin-ipad" {
		t.Errorf("key/device = %v/%v", record["setting_key"], record["device_id"])
	}
	// Identity only: the value must never reach an operator's log.
	for key, value := range record {
		if text, ok := value.(string); ok && text == "always" {
			t.Errorf("audit record leaked the value under %q", key)
		}
	}
}

// Ordinary self-service writes stay out of the trail. A record of everything
// answers nothing, and the question this exists for is "who changed it for me".
func TestSetValue_DoesNotAuditOwnWrite(t *testing.T) {
	handler, _ := newHouseholdValuesHandler(t, "")
	audited := captureAuditLogs(t)

	if rec := routeValuesAs(t, handler, "profile-1", http.MethodPut, "playback.subtitle_mode",
		"scope=profile", []byte(`{"value":"always"}`)); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	if records := audited(); len(records) != 0 {
		t.Errorf("audited an ordinary self-service write: %+v", records)
	}
}

func TestGetEffective_ResolvesNamedDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	registry := store.(userstore.DeviceRegistry)
	if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: "profile-1", DeviceID: "apple-tv",
	}); err != nil {
		t.Fatalf("registering apple-tv: %v", err)
	}
	// The Apple TV overrides subtitle mode; this browser does not.
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       "playback.subtitle_mode",
		Scope:     settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1", DeviceID: "apple-tv",
	}, json.RawMessage(`"always"`)); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	read := func(query string) map[string]any {
		t.Helper()
		req := valuesRequest(http.MethodGet, "/settings/values/effective?"+query, nil)
		rec := httptest.NewRecorder()
		handler.HandleGetEffective(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET effective?%s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Settings []map[string]any `json:"settings"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		if len(body.Settings) != 1 {
			t.Fatalf("resolved %d settings, want 1", len(body.Settings))
		}
		return body.Settings[0]
	}

	named := read("keys=playback.subtitle_mode&device_id=apple-tv")
	if named["value"] != "always" || named["source"] != "profile_device" {
		t.Errorf("named device resolved %v from %v, want always from profile_device",
			named["value"], named["source"])
	}

	// Without the parameter this browser still resolves its own answer.
	own := read("keys=playback.subtitle_mode")
	if own["source"] == "profile_device" {
		t.Errorf("this browser resolved a device override it does not have: %v", own)
	}
}

func TestGetEffective_RejectsDeviceNotOwnedByCaller(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	req := valuesRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode&device_id=not-mine", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetEffective(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("effective read of a foreign device = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestGetEffective_NonPrimaryCannotResolveSiblingProfile(t *testing.T) {
	handler, _ := newHouseholdValuesHandler(t, "")

	req := valuesRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode&profile_id=profile-1", nil)
	req = req.WithContext(apimw.SetProfileID(req.Context(), "profile-2"))
	rec := httptest.NewRecorder()
	handler.HandleGetEffective(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-primary effective read of a sibling = %d, want 403: %s",
			rec.Code, rec.Body.String())
	}
}

// A server admin may act for any profile, including from a non-primary active
// profile: apimw.IsAdmin short-circuits the household check by design. Pinned
// because it is easy to mistake for the non-primary refusal above — the
// difference is the account's role, not the profile's.
func TestSetValue_ServerAdminMayNameAnyProfile(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "")

	target := "/settings/values/player.hdr_enabled" +
		"?scope=profile_device&profile_id=profile-2&device_id=robin-ipad"
	req := valuesRequest(http.MethodPut, target, []byte(`{"value":false}`))
	// Acting as the *non-primary* profile, but on an admin account.
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"})
	req = req.WithContext(apimw.SetProfileID(ctx, "profile-2"))

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("key", "player.hdr_enabled")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	handler.HandleSetValue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin naming a profile = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "robin-ipad" {
		t.Errorf("stored on device %q, want robin-ipad", got)
	}
}

func TestMergeLanguageSuggestionsKeepsFloorObservedAndCurrent(t *testing.T) {
	got := mergeLanguageSuggestions(
		[]string{"en", "fr", "pt"},
		[]string{"eng", "deu", "not a tag", "fr"},
		json.RawMessage(`"pt-BR"`),
	)
	want := []string{"en", "fr", "pt", "deu", "pt-BR"}
	if !slices.Equal(got, want) {
		t.Errorf("mergeLanguageSuggestions() = %v, want %v", got, want)
	}
}

func TestMergeLanguageSuggestionsUsesExactCurrentAlias(t *testing.T) {
	got := mergeLanguageSuggestions(
		[]string{"en", "fr"},
		[]string{"eng", "fra"},
		json.RawMessage(`"eng"`),
	)
	want := []string{"eng", "fr"}
	if !slices.Equal(got, want) {
		t.Errorf("mergeLanguageSuggestions() = %v, want %v", got, want)
	}
}

type recordingLanguageSuggestionSource struct {
	filters  catalog.BrowseFilters
	original []string
	calls    int
}

func (s *recordingLanguageSuggestionSource) ListOriginalLanguages(
	_ context.Context, filters catalog.BrowseFilters,
) ([]string, error) {
	s.filters = filters
	s.calls++
	return s.original, nil
}

func TestObservedLanguageSuggestionsIncludesAccessibleOriginalLanguages(t *testing.T) {
	source := &recordingLanguageSuggestionSource{original: []string{"is", "no"}}
	handler, _ := newValuesTestHandler(t)
	handler.SetLanguageSuggestionSource(source)

	req := valuesRequest(http.MethodGet, "/settings/values/effective", nil)
	req = req.WithContext(access.SetScope(req.Context(), access.Scope{
		AllowedLibraryIDs:  []int{4, 9},
		DisabledLibraryIDs: []int{12},
		MaxContentRating:   "PG-13",
	}))
	observed := handler.observedLanguageSuggestions(req, []settingsresolve.Effective{{
		Key: settingskeys.CatalogMetadataLanguage,
	}})

	if !slices.Equal(observed[settingskeys.CatalogMetadataLanguage], []string{"is", "no"}) {
		t.Fatalf("metadata suggestions = %v", observed[settingskeys.CatalogMetadataLanguage])
	}
	if !slices.Equal(source.filters.LibraryIDs, []int{4, 9}) ||
		!slices.Equal(source.filters.DisabledLibraryIDs, []int{12}) ||
		source.filters.MaxContentRating != "PG-13" {
		t.Fatalf("catalog filters = %#v", source.filters)
	}
}

// TestObservedLanguageSuggestionsSkipsPlaybackKeys pins the design decision
// that only catalog.metadata_language gets deployment-observed suggestions.
// The audio/subtitle track listings walk every media file — tens of seconds
// on large catalogs — so those pickers ship the contract floor and clients
// offer free entry for anything beyond it.
func TestObservedLanguageSuggestionsSkipsPlaybackKeys(t *testing.T) {
	source := &recordingLanguageSuggestionSource{original: []string{"is"}}
	handler, _ := newValuesTestHandler(t)
	handler.SetLanguageSuggestionSource(source)

	req := valuesRequest(http.MethodGet, "/settings/values/effective", nil)
	observed := handler.observedLanguageSuggestions(req, []settingsresolve.Effective{
		{Key: settingskeys.PlaybackAudioLanguage},
		{Key: settingskeys.PlaybackSubtitleLanguage},
	})

	if len(observed) != 0 {
		t.Fatalf("observed suggestions for playback keys = %v, want none", observed)
	}
	if source.calls != 0 {
		t.Fatalf("catalog scans = %d, want 0 for playback-only requests", source.calls)
	}
}
