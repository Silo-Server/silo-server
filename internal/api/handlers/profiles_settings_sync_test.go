package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// These tests pin the seam the settings cutover opened: every server-side
// reader of the profile preferences resolves them from user_setting_values,
// while clients still write them through the profile create and update
// operations. A profile write that does not land in the canonical store never
// takes effect: the stale backfilled row (or the contract default) wins
// forever.

// createAsPrimary creates a profile acting as the primary profile-1.
func createAsPrimary(handler *ProfileHandler, req ProfileCreateRequest) (ProfileView, error) {
	return handler.CreateProfile(profileServiceContext("profile-1"), ProfileCreateCommand{
		UserID: 1, ActiveProfileID: "profile-1", Request: req, VerifyProfile: noProfileVerification,
	})
}

func storedProfileSetting(t *testing.T, store userstore.UserStore, key, profileID string) *userstore.SettingValue {
	t.Helper()
	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       key,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: profileID,
	})
	if err != nil {
		t.Fatalf("reading canonical %s: %v", key, err)
	}
	return value
}

// failingSettingsWriteStore fails every canonical setting write, simulating a
// store whose user_setting_values table is unavailable while profile CRUD
// still works.
type failingSettingsWriteStore struct {
	userstore.UserStore
}

type failingPreferenceSettingsWriter struct {
	userstore.PreferenceSettingsWriter
}

func (s failingSettingsWriteStore) UpsertSettingValue(
	context.Context, userstore.SettingIdentity, json.RawMessage,
) (*userstore.SettingValue, error) {
	return nil, errors.New("settings storage unavailable")
}

func (s failingSettingsWriteStore) WithPreferenceSettingsTransaction(
	ctx context.Context,
	fn func(userstore.PreferenceSettingsWriter) error,
) error {
	transactioner, ok := s.UserStore.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		return errors.New("wrapped store does not support preference settings transactions")
	}
	return transactioner.WithPreferenceSettingsTransaction(ctx, func(tx userstore.PreferenceSettingsWriter) error {
		return fn(failingPreferenceSettingsWriter{PreferenceSettingsWriter: tx})
	})
}

func (w failingPreferenceSettingsWriter) UpsertSettingValue(
	context.Context, userstore.SettingIdentity, json.RawMessage,
) (*userstore.SettingValue, error) {
	return nil, errors.New("settings storage unavailable")
}

// --- Read side ---
//
// The mirror of the tests above: the view's preference fields are served from
// the canonical rows, so a write that never touched a legacy column is still
// visible to every profile reader on every platform.

func updateOwnProfile(handler *ProfileHandler, profileID string, req ProfileUpdateRequest) (ProfileView, error) {
	return handler.UpdateProfile(profileServiceContext(profileID), ProfileUpdateCommand{
		UserID: 1, ProfileID: profileID, ActiveProfileID: profileID, Request: req,
		VerifyProfile: noProfileVerification,
	})
}

func TestProfileWritesRollBackWhenSettingsSyncFails(t *testing.T) {
	base := newProfileTestStore(t)
	failing := NewProfileHandler(testUserStoreProvider{store: failingSettingsWriteStore{UserStore: base}})

	_, err := createAsPrimary(failing, ProfileCreateRequest{Name: "Kids", Language: "de"})
	requireAPIStatus(t, err, http.StatusInternalServerError)
	profiles, err := base.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("listing profiles: %v", err)
	}
	for _, p := range profiles {
		if p.Name == "Kids" {
			t.Fatalf("profile %q survived a failed settings sync", p.Name)
		}
	}
	// The rollback lets the retry succeed once the store recovers.
	if _, err := createAsPrimary(NewProfileHandler(testUserStoreProvider{store: base}), ProfileCreateRequest{Name: "Kids", Language: "de"}); err != nil {
		t.Fatalf("retry create: %v", err)
	}

	before, err := base.GetProfile(context.Background(), "profile-1")
	if err != nil || before == nil {
		t.Fatalf("reading profile before update: profile=%+v err=%v", before, err)
	}
	_, err = updateOwnProfile(failing, "profile-1", ProfileUpdateRequest{Language: new("de")})
	requireAPIStatus(t, err, http.StatusInternalServerError)
	after, err := base.GetProfile(context.Background(), "profile-1")
	if err != nil || after == nil {
		t.Fatalf("reading profile after rollback: profile=%+v err=%v", after, err)
	}
	if after.Language != before.Language {
		t.Fatalf("legacy language after rollback = %q, want %q", after.Language, before.Language)
	}
	if value := storedProfileSetting(t, base, settingskeys.PlaybackAudioLanguage, "profile-1"); value != nil {
		t.Fatalf("canonical language survived rollback: %+v", value)
	}
}
