package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
				verified, err := access.VerifyProfileForRequest(&profile, access.ResolveInput{ProfileID: profile.ID}, 1, nil)
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

func TestHandleCreateProfilePINByteLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		pin  string
		want int
	}{
		{"ascii over limit", strings.Repeat("1", 73), http.StatusBadRequest},
		{"unicode over limit", strings.Repeat("é", 37), http.StatusBadRequest},
		{"ascii at limit", strings.Repeat("1", 72), http.StatusCreated},
		{"unicode at limit", strings.Repeat("é", 36), http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newProfileTestStore(t)
			handler := NewProfileHandler(testUserStoreProvider{store: store})
			body, err := json.Marshal(ProfileCreateRequest{Name: "New profile", PIN: tc.pin, Language: "de"})
			if err != nil {
				t.Fatal(err)
			}
			req := newAuthorizedProfileRequestWithRole(http.MethodPost, "/profiles", string(body), "user", "profile-1")
			rr := httptest.NewRecorder()
			handler.HandleCreateProfile(rr, req)
			if rr.Code != tc.want {
				t.Errorf("POST status = %d, want %d", rr.Code, tc.want)
			}
			profiles, err := store.ListProfiles(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == http.StatusBadRequest {
				if len(profiles) != 1 {
					t.Errorf("invalid PIN persisted a profile: count=%d, want 1", len(profiles))
				}
				return
			}
			if len(profiles) != 2 {
				t.Fatalf("profiles = %d, want 2", len(profiles))
			}
			for _, profile := range profiles {
				if profile.ID != "profile-1" {
					if err := bcrypt.CompareHashAndPassword([]byte(profile.PINHash), []byte(tc.pin)); err != nil {
						t.Errorf("stored PIN: %v", err)
					}
				}
			}
		})
	}
}
