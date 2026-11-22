package catalog

import (
	"context"
	"database/sql"
	"encoding/json"

	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testDetailUserStoreProvider struct {
	store userstore.UserStore
}

func (p testDetailUserStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p testDetailUserStoreProvider) Close() error { return nil }

func newDetailTestStore(t *testing.T) userstore.UserStore {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	return store
}

// setProfileAudioLanguage stores the profile's preferred audio language as a
// canonical setting value.
//
// The profile column these tests used to write is a migration source, not a
// read path: playback resolves playback.audio_language through the settings
// contract, so seeding the column would leave the resolver seeing nothing.
func setProfileAudioLanguage(t *testing.T, store userstore.UserStore, language string) {
	t.Helper()
	setScopedAudioLanguage(t, store, settingscontract.ScopeProfile, "", 0, language)
}

func setScopedAudioLanguage(
	t *testing.T,
	store userstore.UserStore,
	scope settingscontract.Scope,
	seriesID string,
	libraryID int,
	language string,
) {
	t.Helper()
	setScopedAudioLanguageForDevice(t, store, scope, seriesID, libraryID, "", language)
}

func setScopedAudioLanguageForDevice(
	t *testing.T,
	store userstore.UserStore,
	scope settingscontract.Scope,
	seriesID string,
	libraryID int,
	deviceID string,
	language string,
) {
	t.Helper()
	encoded, err := json.Marshal(language)
	if err != nil {
		t.Fatalf("encoding language: %v", err)
	}
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       settingskeys.PlaybackAudioLanguage,
		Scope:     scope,
		ProfileID: "profile-1",
		SeriesID:  seriesID,
		LibraryID: libraryID,
		DeviceID:  deviceID,
	}, encoded); err != nil {
		t.Fatalf("seeding %s: %v", settingskeys.PlaybackAudioLanguage, err)
	}
}
