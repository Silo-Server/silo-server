package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func userViewNames(t *testing.T, h *ItemsHandler, session *Session) []string {
	t.Helper()
	req := httptest.NewRequest("GET", "/UserViews", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, session))
	rec := httptest.NewRecorder()
	h.HandleViews(rec, req)
	if rec.Code != 200 {
		t.Fatalf("UserViews: %d %s", rec.Code, rec.Body.String())
	}
	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		names = append(names, item.Name)
	}
	return names
}

func newViewOrderTestHandler(store userstore.UserStore) *ItemsHandler {
	collections := &fakeCollectionSource{collections: []*models.LibraryCollection{{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible", ItemCount: 1}}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{
		{ID: 1, Name: "Movies", Type: "movies"},
		{ID: 2, Name: "Shows", Type: "tvshows"},
		{ID: 3, Name: "Kids", Type: "movies"},
	}, nil)
	h.storeProvider = compatTestUserStoreProvider{store: store}
	return h
}

// Jellyfin lists the views named in OrderedViews first, in that order, and the
// rest after them in the default order. Clients set it by posting the user
// configuration; it is per profile.
func TestUserViewsFollowPostedOrderedViews(t *testing.T) {
	store := newJellycompatUserStore(t)
	views := newViewOrderTestHandler(store)
	auth := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil).WithUserStore(compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	other := &Session{StreamAppUserID: 1, ProfileID: "profile-2"}

	defaultOrder := []string{"Collections", "Movies", "Shows", "Kids"}
	if got := userViewNames(t, views, session); !slices.Equal(got, defaultOrder) {
		t.Fatalf("default order = %v, want %v", got, defaultOrder)
	}

	kids := views.codec.EncodeIntID(EncodedIDLibrary, 3)
	shows := views.codec.EncodeIntID(EncodedIDLibrary, 2)
	// Jellyfin stores view IDs without dashes; unknown IDs are ignored.
	ordered := []string{strings.ToUpper(kids), "00000000000000000000000000000001", strings.ReplaceAll(shows, "-", ""), collectionsViewID}
	body, _ := json.Marshal(map[string][]string{"OrderedViews": ordered})
	rec := httptest.NewRecorder()
	auth.HandleUpdateConfiguration(rec, viewerRequest("POST", "/Users/Configuration", string(body), "", "", session))
	if rec.Code != 204 {
		t.Fatalf("update returned %d %s", rec.Code, rec.Body.String())
	}

	if got, want := userViewNames(t, views, session), []string{"Kids", "Shows", "Collections", "Movies"}; !slices.Equal(got, want) {
		t.Fatalf("ordered views = %v, want %v", got, want)
	}
	if got := userViewNames(t, views, other); !slices.Equal(got, defaultOrder) {
		t.Fatalf("other profile order = %v, want %v", got, defaultOrder)
	}
	dto, err := auth.resolvedUserDTO(t.Context(), session)
	if err != nil || !slices.Equal(dto.Configuration.OrderedViews, ordered) {
		t.Fatalf("OrderedViews read back = %v (%v), want %v", dto.Configuration.OrderedViews, err, ordered)
	}

	// Clearing OrderedViews restores the default order.
	rec = httptest.NewRecorder()
	auth.HandleUpdateConfiguration(rec, viewerRequest("POST", "/Users/Configuration", `{"OrderedViews":[]}`, "", "", session))
	if rec.Code != 204 {
		t.Fatalf("clear returned %d %s", rec.Code, rec.Body.String())
	}
	if got := userViewNames(t, views, session); !slices.Equal(got, defaultOrder) {
		t.Fatalf("cleared order = %v, want %v", got, defaultOrder)
	}
}

// Libraries follow the profile's native library order. OrderedViews then
// moves the views it names to the front; the rest keep the native order.
func TestUserViewsFallBackToNativeLibraryOrder(t *testing.T) {
	store := newJellycompatUserStore(t)
	views := newViewOrderTestHandler(store)
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	tx, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		t.Fatal("test store does not support settings transactions")
	}
	if err := tx.WithPreferenceSettingsTransaction(t.Context(), func(writer userstore.PreferenceSettingsWriter) error {
		_, err := writer.UpsertSettingValue(t.Context(), userstore.SettingIdentity{Key: settingskeys.UiLibraryOrder, Scope: settingscontract.ScopeProfile, ProfileID: session.ProfileID}, json.RawMessage(`[3,1]`))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if got, want := userViewNames(t, views, session), []string{"Collections", "Kids", "Movies", "Shows"}; !slices.Equal(got, want) {
		t.Fatalf("native order = %v, want %v", got, want)
	}

	shows := views.codec.EncodeIntID(EncodedIDLibrary, 2)
	if err := store.SetSetting(t.Context(), configurationKey(session.ProfileID), `{"OrderedViews":["`+shows+`"]}`); err != nil {
		t.Fatal(err)
	}
	if got, want := userViewNames(t, views, session), []string{"Shows", "Collections", "Kids", "Movies"}; !slices.Equal(got, want) {
		t.Fatalf("OrderedViews over native order = %v, want %v", got, want)
	}
}
