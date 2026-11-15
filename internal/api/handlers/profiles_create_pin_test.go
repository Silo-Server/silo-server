package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Pause after the real transaction commits, while the create request is still
// running. Other requests can already list and select the new profile here.
type profileCommitObserver struct {
	userstore.UserStore
	committed chan struct{}
	resume    chan struct{}
}

func (s profileCommitObserver) WithPreferenceSettingsTransaction(ctx context.Context, fn func(userstore.PreferenceSettingsWriter) error) error {
	err := s.UserStore.(userstore.PreferenceSettingsTransactioner).WithPreferenceSettingsTransaction(ctx, fn)
	if err != nil {
		return err
	}
	close(s.committed)
	select {
	case <-s.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// failingProfileUpdateStore fails every UpdateProfile, inside the create
// transaction and outside it.
type failingProfileUpdateStore struct {
	userstore.UserStore
}

type failingProfileUpdateWriter struct {
	userstore.PreferenceSettingsWriter
}

var errProfileUpdateFailed = errors.New("profile update failed")

func (s failingProfileUpdateStore) UpdateProfile(context.Context, string, userstore.UpdateProfileInput) error {
	return errProfileUpdateFailed
}

func (s failingProfileUpdateStore) WithPreferenceSettingsTransaction(ctx context.Context, fn func(userstore.PreferenceSettingsWriter) error) error {
	return s.UserStore.(userstore.PreferenceSettingsTransactioner).WithPreferenceSettingsTransaction(ctx, func(tx userstore.PreferenceSettingsWriter) error {
		return fn(failingProfileUpdateWriter{PreferenceSettingsWriter: tx})
	})
}

func (failingProfileUpdateWriter) UpdateProfile(context.Context, string, userstore.UpdateProfileInput) error {
	return errProfileUpdateFailed
}

// TestCreateProfileStoresForcedSubtitlesOffInOneCommit pins #2743: a profile
// created with show_forced_subtitles false stores it in the transaction that
// creates the profile. When that write fails, no profile is left behind, so a
// retry doesn't meet a name conflict.
func TestCreateProfileStoresForcedSubtitlesOffInOneCommit(t *testing.T) {
	ctx := t.Context()
	base := newProfileTestStore(t)
	off := false
	request := ProfileCreateRequest{Name: "Second", ShowForcedSubtitles: &off}

	_, err := createAsPrimary(NewProfileHandler(testUserStoreProvider{store: failingProfileUpdateStore{UserStore: base}}), request)
	requireAPIStatus(t, err, http.StatusInternalServerError)
	profiles, err := base.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.Name == "Second" {
			t.Fatalf("profile %q survived a failed create", p.Name)
		}
	}

	created, err := createAsPrimary(NewProfileHandler(testUserStoreProvider{store: base}), request)
	if err != nil {
		t.Fatalf("retry create: %v", err)
	}
	stored, err := base.GetProfile(ctx, created.ID)
	if err != nil || stored == nil {
		t.Fatalf("read created profile: %+v, %v", stored, err)
	}
	if stored.ShowForcedSubtitles {
		t.Fatal("stored show_forced_subtitles = true, want the requested false")
	}
	if value := storedProfileSetting(t, base, settingskeys.PlaybackShowForcedSubtitles, created.ID); value == nil || string(value.Value) != "false" {
		t.Fatalf("canonical show_forced_subtitles = %v, want false", value)
	}
}

func TestCreateProfilePINProtectedAtFirstCommit(t *testing.T) {
	for _, pin := range []string{"1234", ""} {
		t.Run("pin="+pin, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			base := newProfileTestStore(t)
			store := profileCommitObserver{UserStore: base, committed: make(chan struct{}), resume: make(chan struct{})}
			handler := NewProfileHandler(testUserStoreProvider{store: store})
			done := make(chan error, 1)
			go func() {
				_, err := handler.CreateProfile(ctx, ProfileCreateCommand{
					UserID: 1, ActiveProfileID: "profile-1",
					Request: ProfileCreateRequest{Name: "New profile", PIN: pin, Language: "de"},
				})
				done <- err
			}()
			defer func() {
				close(store.resume)
				if err := <-done; err != nil {
					t.Errorf("create profile: %v", err)
				}
			}()
			select {
			case <-store.committed:
			case <-ctx.Done():
				t.Fatal("create did not commit")
			}
			profiles, err := base.ListProfiles(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(profiles) != 2 {
				t.Fatalf("visible profiles = %d, want 2", len(profiles))
			}
			for _, profile := range profiles {
				if profile.ID == "profile-1" {
					continue
				}
				verified, _, err := access.VerifyProfileForRequest(&profile, access.ResolveInput{ProfileID: profile.ID}, 1, nil)
				if value := storedProfileSetting(t, base, settingskeys.PlaybackAudioLanguage, profile.ID); value == nil {
					t.Error("profile preferences missing at first commit")
				}
				if pin == "" {
					if !verified || err != nil || profile.PINHash != "" {
						t.Errorf("PIN-less profile: verified=%v err=%v", verified, err)
					}
					continue
				}
				if verified || !errors.Is(err, access.ErrProfileUnverified) {
					t.Errorf("first committed row allows selection without PIN: verified=%v err=%v", verified, err)
				}
				if err := bcrypt.CompareHashAndPassword([]byte(profile.PINHash), []byte(pin)); err != nil {
					t.Errorf("first committed row does not contain requested PIN: %v", err)
				}
			}
		})
	}
}
