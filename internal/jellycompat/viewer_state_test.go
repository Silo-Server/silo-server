package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func viewerRequest(method, target, body, param, value string, session *Session) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	route := chi.NewRouteContext()
	route.URLParams.Add(param, value)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
	return r.WithContext(context.WithValue(ctx, compatSessionKey, session))
}

func TestUserDataUpdatePersistsPartialFieldsAndProfileIsolation(t *testing.T) {
	store := newJellycompatUserStore(t)
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "profile-2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	provider := notifications.WrapUserStoreProvider(compatTestUserStoreProvider{store: store}, &notifications.System{})
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	codec := NewResourceIDCodec()
	id := codec.EncodeStringID(EncodedIDItem, "movie-1")
	h := NewUserDataHandler(&stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Type: "movie", Runtime: 100}}, service, codec, &config.Config{})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1", PseudoUserID: uuid.New()}
	run := func(body string) itemUserDataDTO {
		t.Helper()
		rec := httptest.NewRecorder()
		h.HandleUpdateUserData(rec, viewerRequest("POST", "/", body, "itemId", id, session))
		if rec.Code != 200 {
			t.Fatalf("update %s: %d %s", body, rec.Code, rec.Body.String())
		}
		var dto itemUserDataDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		return dto
	}
	dto := run(`{"PlaybackPositionTicks":1230000000,"IsFavorite":true,"LastPlayedDate":"2025-01-02T03:04:05Z"}`)
	if dto.PlaybackPositionTicks != 1230000000 || !dto.IsFavorite || dto.LastPlayedDate != "2025-01-02T03:04:05Z" {
		t.Fatalf("lost values: %+v", dto)
	}
	dto = run(`{"IsFavorite":false}`)
	if dto.IsFavorite || dto.PlaybackPositionTicks != 1230000000 {
		t.Fatalf("partial update overwrote progress: %+v", dto)
	}
	dto = run(`{"Played":true,"UnplayedItemCount":0}`)
	if !dto.Played || dto.PlaybackPositionTicks != 0 {
		t.Fatalf("mark played retained resume position: %+v", dto)
	}
	dto = run(`{"Played":true,"PlaybackPositionTicks":70000000}`)
	if dto.PlaybackPositionTicks != 70000000 {
		t.Fatalf("explicit rewatch position lost: %+v", dto)
	}
	dto = run(`{"Played":false,"PlaybackPositionTicks":50000000,"LastPlayedDate":"2024-01-01T00:00:00Z"}`)
	if dto.Played || dto.PlaybackPositionTicks != 50000000 || dto.LastPlayedDate != "2024-01-01T00:00:00Z" {
		t.Fatalf("historical edit suppressed: %+v", dto)
	}
	for _, body := range []string{`{"Rating":5,"IsFavorite":true}`, `{"Likes":true,"IsFavorite":true}`} {
		rec := httptest.NewRecorder()
		h.HandleUpdateUserData(rec, viewerRequest("POST", "/", body, "itemId", id, session))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unsupported writable field: %d %s", rec.Code, rec.Body.String())
		}
	}
	dto = run(`{"UnplayedItemCount":10,"Key":"echo","ItemId":"echo"}`)
	if dto.IsFavorite || dto.PlaybackPositionTicks != 50000000 {
		t.Fatalf("echoed read-only fields changed state: %+v", dto)
	}
	other, err := store.GetProgress(t.Context(), "profile-2", "movie-1")
	if err != nil || other != nil {
		t.Fatalf("cross-profile state: %+v %v", other, err)
	}
	rec := httptest.NewRecorder()
	h.HandleUpdateUserData(rec, viewerRequest("POST", "/?userId="+uuid.NewString(), `{"IsFavorite":true}`, "itemId", id, session))
	if rec.Code != 404 {
		t.Fatalf("foreign user status=%d", rec.Code)
	}
}

func TestPlayedMutationsReturnCurrentDTOAndDate(t *testing.T) {
	store := newJellycompatUserStore(t)
	provider := notifications.WrapUserStoreProvider(compatTestUserStoreProvider{store: store}, &notifications.System{})
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	codec := NewResourceIDCodec()
	id := codec.EncodeStringID(EncodedIDItem, "movie-1")
	h := NewUserDataHandler(&stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Type: "movie"}}, service, codec, &config.Config{})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	rec := httptest.NewRecorder()
	h.HandleMarkPlayed(rec, viewerRequest("POST", "/?datePlayed=2025-02-03T04:05:06Z", "", "itemId", id, session))
	var dto itemUserDataDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != 200 || !dto.Played || dto.LastPlayedDate != "2025-02-03T04:05:06Z" {
		t.Fatalf("mark played %d %+v %s", rec.Code, dto, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleMarkUnplayed(rec, viewerRequest("DELETE", "/", "", "itemId", id, session))
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != 200 || dto.Played {
		t.Fatalf("mark unplayed %d %+v", rec.Code, dto)
	}
}

type failingViewerStore struct{ userstore.UserStore }

func (failingViewerStore) GetJellycompatDisplayPrefs(context.Context, string, string) (string, error) {
	return "", errors.New("read unavailable")
}
func (failingViewerStore) SetJellycompatDisplayPrefs(context.Context, string, string, string) error {
	return errors.New("write unavailable")
}

func TestDisplayPreferencesProfileIsolationAndFailures(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewDisplayPreferencesHandler(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	rec := httptest.NewRecorder()
	h.HandleUpdateDisplayPreferences(rec, viewerRequest("POST", "/?client=web", `{"ViewType":"List","IndexBy":"Genres"}`, "displayPreferencesId", "home", session))
	if rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleGetDisplayPreferences(rec, viewerRequest("GET", "/?client=web", "", "displayPreferencesId", "home", session))
	var dto displayPreferencesDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if dto.ViewType != "List" || dto.IndexBy != "Genres" {
		t.Fatalf("lost fields %+v", dto)
	}
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "profile-2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	session.ProfileID = "profile-2"
	rec = httptest.NewRecorder()
	h.HandleGetDisplayPreferences(rec, viewerRequest("GET", "/?client=web", "", "displayPreferencesId", "home", session))
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != 200 || dto.ViewType == "List" {
		t.Fatalf("profile leak %d %+v", rec.Code, dto)
	}
	h = NewDisplayPreferencesHandler(compatTestUserStoreProvider{store: failingViewerStore{store}})
	for _, method := range []string{"GET", "POST"} {
		rec = httptest.NewRecorder()
		req := viewerRequest(method, "/", `{}`, "displayPreferencesId", "home", session)
		if method == "GET" {
			h.HandleGetDisplayPreferences(rec, req)
		} else {
			h.HandleUpdateDisplayPreferences(rec, req)
		}
		if rec.Code != 500 {
			t.Fatalf("%s failure returned %d", method, rec.Code)
		}
	}
}

func TestConfigurationPersistsProfileSettingsAndPartialChanges(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1", PseudoUserID: uuid.New()}
	for _, body := range []string{`{"AudioLanguagePreference":"fra","SubtitleLanguagePreference":"en","SubtitleMode":"Always","EnableNextEpisodeAutoPlay":false,"OrderedViews":["movies"]}`, `{"HidePlayedInLatest":true}`} {
		rec := httptest.NewRecorder()
		h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", body, "", "", session))
		if rec.Code != 204 {
			t.Fatalf("update config %d %s", rec.Code, rec.Body.String())
		}
	}
	dto, err := h.resolvedUserDTO(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if dto.Configuration.AudioLanguagePreference != "fr" || dto.Configuration.SubtitleMode != "Always" || dto.Configuration.EnableNextEpisodeAutoPlay || !dto.Configuration.HidePlayedInLatest || len(dto.Configuration.OrderedViews) != 1 {
		t.Fatalf("lost configuration %+v", dto.Configuration)
	}
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "profile-2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	session.ProfileID = "profile-2"
	dto, err = h.resolvedUserDTO(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if dto.Configuration.SubtitleMode == "Always" || dto.Configuration.HidePlayedInLatest {
		t.Fatalf("configuration profile leak %+v", dto.Configuration)
	}
}

func TestCulturesIncludesLanguageCodes(t *testing.T) {
	rec := httptest.NewRecorder()
	(&AuthHandler{}).HandleCultures(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ThreeLetterISOLanguageName":"eng"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

type failingProgressStore struct{ userstore.UserStore }

func (failingProgressStore) SetJellycompatProgress(context.Context, string, string, float64, float64, bool, time.Time) error {
	return errors.New("progress write unavailable")
}

type deniedViewerContent struct{ ContentService }

func (deniedViewerContent) GetItemDetail(context.Context, *Session, string, *int) (*upstreamItemDetail, error) {
	return nil, &HTTPError{StatusCode: 404, Message: "Item not found"}
}

func TestUserDataFailureAndInaccessibleItemDoNotReportSuccess(t *testing.T) {
	store := newJellycompatUserStore(t)
	codec := NewResourceIDCodec()
	id := codec.EncodeStringID(EncodedIDItem, "movie-1")
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	service := &directUserDataService{storeProvider: compatTestUserStoreProvider{store: failingProgressStore{store}}}
	h := NewUserDataHandler(&stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Type: "movie"}}, service, codec, &config.Config{})
	rec := httptest.NewRecorder()
	h.HandleUpdateUserData(rec, viewerRequest("POST", "/", `{"PlaybackPositionTicks":10000000}`, "itemId", id, session))
	if rec.Code != 500 {
		t.Fatalf("failed update returned %d %s", rec.Code, rec.Body.String())
	}
	h.content = deniedViewerContent{}
	rec = httptest.NewRecorder()
	h.HandleAddFavorite(rec, viewerRequest("POST", "/", "", "itemId", id, session))
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	favorite, err := store.IsFavorite(t.Context(), session.ProfileID, "movie-1")
	if err != nil || favorite {
		t.Fatalf("inaccessible favorite mutated %v %v", favorite, err)
	}
}

type failingConfigurationStore struct{ userstore.UserStore }

func (failingConfigurationStore) WithPreferenceSettingsTransaction(context.Context, func(userstore.PreferenceSettingsWriter) error) error {
	return errors.New("transaction unavailable")
}

func TestConfigurationFailureAndForeignProfile(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: failingConfigurationStore{store}})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1", PseudoUserID: uuid.New()}
	rec := httptest.NewRecorder()
	h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", `{"SubtitleMode":"Always"}`, "", "", session))
	if rec.Code != 500 {
		t.Fatalf("failed config update returned %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", `{"SubtitleMode":"Always"}`, "userId", uuid.NewString(), session))
	if rec.Code != 404 {
		t.Fatalf("foreign profile status %d", rec.Code)
	}
}

func TestDisplayPreferencesCustomPrefsObject(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewDisplayPreferencesHandler(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	for _, body := range []string{`{}`, `{"CustomPrefs":null}`} {
		rec := httptest.NewRecorder()
		h.HandleUpdateDisplayPreferences(rec, viewerRequest("POST", "/?client=web", body, "displayPreferencesId", "home", session))
		if rec.Code != 204 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		raw, err := store.GetJellycompatDisplayPrefs(t.Context(), profilePreferencesID(session.ProfileID, "home"), "web")
		if err != nil || !strings.Contains(raw, `"CustomPrefs":{}`) {
			t.Fatalf("stored prefs %s: %v", raw, err)
		}
	}
	if err := store.SetJellycompatDisplayPrefs(t.Context(), profilePreferencesID(session.ProfileID, "home"), "web", `{"CustomPrefs":null}`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.HandleGetDisplayPreferences(rec, viewerRequest("GET", "/?client=web", "", "displayPreferencesId", "home", session))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"CustomPrefs":{}`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestConfigurationNullClearsPreferences(t *testing.T) {
	store := newJellycompatUserStore(t)
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	for _, body := range []string{`{"AudioLanguagePreference":"fr","SubtitleLanguagePreference":"en","CastReceiverId":"receiver"}`, `{"AudioLanguagePreference":null,"SubtitleLanguagePreference":null,"CastReceiverId":null}`} {
		rec := httptest.NewRecorder()
		h.HandleUpdateConfiguration(rec, viewerRequest("POST", "/", body, "", "", session))
		if rec.Code != 204 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	dto, err := h.resolvedUserDTO(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if dto.Configuration.AudioLanguagePreference != "" || dto.Configuration.SubtitleLanguagePreference != "" || dto.Configuration.CastReceiverID != "" {
		t.Fatalf("stale cleared preferences: %+v", dto.Configuration)
	}
	// A native settings reset must override a stale compatibility blob too.
	if err := store.SetSetting(t.Context(), configurationKey(session.ProfileID), `{"AudioLanguagePreference":"fr","SubtitleLanguagePreference":"en"}`); err != nil {
		t.Fatal(err)
	}
	dto, err = h.resolvedUserDTO(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if dto.Configuration.AudioLanguagePreference != "" || dto.Configuration.SubtitleLanguagePreference != "" {
		t.Fatalf("stale canonical preferences: %+v", dto.Configuration)
	}
}

// Expose only the existing atomic batch capability; a dated series mark must
// not require a second pass through the individual progress-edit capability.
type batchOnlyViewerStore struct {
	userstore.UserStore
	userstore.WatchedBatchWriter
}

func TestDatedPlayedBatchNeedsNoIndividualProgressRewrites(t *testing.T) {
	store := newJellycompatUserStore(t)
	batch, ok := store.(userstore.WatchedBatchWriter)
	if !ok {
		t.Fatal("store does not support atomic watched batches")
	}
	provider := notifications.WrapUserStoreProvider(compatTestUserStoreProvider{store: batchOnlyViewerStore{store, batch}}, &notifications.System{})
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	ids := []string{"episode-1", "episode-2"}
	if err := service.MarkPlayedBatchAt(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "profile-1"}, ids, date); err != nil {
		t.Fatal(err)
	}
	dates, err := jellycompatProgressDates(t.Context(), store, "profile-1", ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if dates[id] != date.Format(time.RFC3339) {
			t.Fatalf("date for %s: %s", id, dates[id])
		}
	}
	history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("batch history: %+v %v", history, err)
	}
}
