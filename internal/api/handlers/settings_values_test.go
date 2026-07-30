package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
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
