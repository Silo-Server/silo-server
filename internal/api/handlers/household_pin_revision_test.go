package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Move the PIN after service authorization but before the transaction begins.
// This exercises the short check/write race independently of the upload window.
type pinChangeBeforeTransaction struct {
	userstore.UserStore
	before func()
}

func (s *pinChangeBeforeTransaction) WithPreferenceSettingsTransaction(ctx context.Context, fn func(userstore.PreferenceSettingsWriter) error) error {
	s.before()
	return s.UserStore.(userstore.PreferenceSettingsTransactioner).WithPreferenceSettingsTransaction(ctx, fn)
}

func TestHouseholdMutationRechecksPINInsideTransaction(t *testing.T) {
	for _, mutation := range []string{"create", "relax limits", "clear PIN"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: "user"})
			db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.DB.SetMaxOpenConns(1)
			store := userdb.NewSQLiteUserStore(db.DB)
			if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "Parent"}); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{PINHash: storedPIN(t, "1234")}); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateProfile(ctx, userstore.Profile{ID: "child", Name: "Child", MaxContentRating: "PG"}); err != nil {
				t.Fatal(err)
			}
			wrapped := &pinChangeBeforeTransaction{UserStore: store, before: func() {
				if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{PINHash: storedPIN(t, "4321")}); err != nil {
					t.Fatal(err)
				}
			}}
			h := NewProfileHandler(testUserStoreProvider{store: wrapped})
			if mutation == "create" {
				_, err = h.CreateProfile(ctx, ProfileCreateCommand{UserID: 1, ActiveProfileID: "profile-1", Request: ProfileCreateRequest{Name: "Extra"}, VerifyProfile: noProfileVerification})
			} else {
				id, req := "child", ProfileUpdateRequest{MaxContentRating: new("")}
				if mutation == "clear PIN" {
					id, req = "profile-1", ProfileUpdateRequest{PIN: new("")}
				}
				_, err = h.UpdateProfile(ctx, ProfileUpdateCommand{UserID: 1, ProfileID: id, ActiveProfileID: "profile-1", Request: req, VerifyProfile: noProfileVerification})
			}
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != http.StatusForbidden || apiErr.Code != codeProfileManagement {
				t.Fatalf("revoked authority: %v", err)
			}
			rows, err := store.ListProfiles(ctx)
			if err != nil || len(rows) != 2 {
				t.Fatalf("created profile after revocation: count=%d err=%v", len(rows), err)
			}
			child, err := store.GetProfile(ctx, "child")
			if err != nil || child.MaxContentRating != "PG" {
				t.Fatalf("changed limits after revocation: %v %v", child, err)
			}
			parent, err := store.GetProfile(ctx, "profile-1")
			if err != nil || parent == nil || bcrypt.CompareHashAndPassword([]byte(parent.PINHash), []byte("4321")) != nil {
				t.Fatalf("changed replacement PIN: profile=%v err=%v", parent, err)
			}
		})
	}
}

func TestHistoryImportRequestActorRechecksPINRevision(t *testing.T) {
	ctx := access.SetScope(t.Context(), access.Scope{ProfileID: "parent", ProfileVerified: true})
	r := httptest.NewRequest(http.MethodPost, "/history-imports/runs", nil).WithContext(ctx)
	actor := historyImportRequestActor(r)
	if err := actor.VerifyProfile(&userstore.Profile{ID: "parent"}); err != nil {
		t.Fatalf("legacy revision-zero proof: %v", err)
	}
	if err := actor.VerifyProfile(&userstore.Profile{ID: "parent", PINRevision: 1}); !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("changed PIN revision accepted: %v", err)
	}
}

// pinChangeOnRead changes the primary PIN when the handler starts reading the
// body, standing in for a PIN change made while a client holds its upload.
type pinChangeOnRead struct {
	io.Reader
	change func()
	once   sync.Once
}

func (b *pinChangeOnRead) Read(p []byte) (int, error) {
	b.once.Do(b.change)
	return b.Reader.Read(p)
}

// pinLockedHousehold is a migrated store, which carries the pin_revision
// trigger that InitSchema alone does not: a PIN-locked primary and a child.
func pinLockedHousehold(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := userdb.NewSQLiteUserStore(db.DB)
	for _, p := range []userstore.Profile{{ID: "profile-1", Name: "Parent"}, {ID: "profile-2", Name: "Child"}} {
		if err := store.CreateProfile(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateProfile(t.Context(), "profile-1", userstore.UpdateProfileInput{PINHash: storedPIN(t, "1234")}); err != nil {
		t.Fatal(err)
	}
	return store
}

// v1Write is one v1 household write whose body the request holds while the
// primary PIN may change.
type v1Write struct {
	store   userstore.UserStore
	method  string
	serve   http.HandlerFunc
	target  string
	body    string
	written func() bool
	// admitted, when set, is the status a request that passed the
	// household check ends with, for a handler with nothing to write to.
	admitted int
}

// v1HouseholdWrites are the v1 handlers that check household authority and
// then decode a body.
func v1HouseholdWrites(tokens *access.ProfileTokenService) map[string]func(t *testing.T) v1Write {
	return map[string]func(t *testing.T) v1Write{
		"API key": func(t *testing.T) v1Write {
			keys := &fakeAPIKeyStore{}
			store := pinLockedHousehold(t)
			h := NewAPIKeyHandler(keys)
			h.Stores = testUserStoreProvider{store: store}
			h.ProfileTokens = tokens
			return v1Write{
				store: store, method: http.MethodPost, serve: h.HandleCreateAPIKey,
				target: "/api/v1/api-keys", body: `{"label":"ci"}`,
				written: func() bool { return keys.created },
			}
		},
		"Discord delivery": func(t *testing.T) v1Write {
			store := pinLockedHousehold(t)
			h := NewDiscordNotificationsHandler(nil, "")
			h.Stores = testUserStoreProvider{store: store}
			h.ProfileTokens = tokens
			return v1Write{
				store: store, method: http.MethodPut, serve: h.HandleUpdatePreferences,
				target: "/api/v1/notifications/discord-preferences", body: `{"mode":"off"}`,
				admitted: http.StatusBadRequest, // No preference store: the mode is refused after admission.
			}
		},
		"another profile's setting": func(t *testing.T) v1Write {
			contract, err := settingscontract.Load()
			if err != nil {
				t.Fatal(err)
			}
			store := pinLockedHousehold(t)
			h := NewSettingValuesHandler(testUserStoreProvider{store: store}, contract)
			h.ProfileTokens = tokens
			return v1Write{
				store: store, method: http.MethodPut,
				serve: func(rec http.ResponseWriter, req *http.Request) {
					routeCtx := chi.NewRouteContext()
					routeCtx.URLParams.Add("key", "ui.next_up_mode")
					h.HandleSetValue(rec, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)))
				},
				target: "/settings/values/ui.next_up_mode?scope=profile&profile_id=profile-2",
				body:   `{"value":"separate"}`,
				written: func() bool {
					value, err := store.GetSettingValue(t.Context(), userstore.SettingIdentity{
						Key: "ui.next_up_mode", Scope: settingscontract.ScopeProfile, ProfileID: "profile-2",
					})
					if err != nil {
						t.Fatal(err)
					}
					return value != nil
				},
			}
		},
	}
}

// v1 handlers that decoded the body after the household check let a held
// upload keep PIN proof that a PIN change revoked.
func TestV1HouseholdWritesRecheckPINAfterBody(t *testing.T) {
	tokens := access.NewProfileTokenService("test-secret-value-at-least-32-chars", time.Minute)
	setups := v1HouseholdWrites(tokens)
	for name, setup := range setups {
		for _, revoke := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/revoked=%v", name, revoke), func(t *testing.T) {
				w := setup(t)
				primary, err := w.store.GetProfile(t.Context(), "profile-1")
				if err != nil {
					t.Fatal(err)
				}
				token, _, err := tokens.Mint(access.ProfileTokenClaims{UserID: 1, SessionID: "sess-1", ProfileID: "profile-1", PINRevision: primary.PINRevision})
				if err != nil {
					t.Fatal(err)
				}
				body := &pinChangeOnRead{Reader: strings.NewReader(w.body), change: func() {
					if revoke {
						if err := w.store.UpdateProfile(t.Context(), "profile-1", userstore.UpdateProfileInput{PINHash: storedPIN(t, "4321")}); err != nil {
							t.Error(err)
						}
					}
				}}
				req := httptest.NewRequest(w.method, w.target, body)
				req.Header.Set("X-Profile-Token", token)
				ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess, SessionID: "sess-1"})
				req = req.WithContext(apimw.SetProfileID(ctx, "profile-1"))
				rec := httptest.NewRecorder()
				w.serve(rec, req)
				if w.admitted != 0 {
					want := w.admitted
					if revoke {
						want = http.StatusForbidden
					}
					if rec.Code != want {
						t.Fatalf("revoked=%v: status=%d want=%d %s", revoke, rec.Code, want, rec.Body.String())
					}
					return
				}
				if revoke {
					if rec.Code != http.StatusForbidden || w.written() {
						t.Fatalf("revoked proof: status=%d written=%v %s", rec.Code, w.written(), rec.Body.String())
					}
					return
				}
				if rec.Code >= 300 || !w.written() {
					t.Fatalf("unchanged PIN: status=%d written=%v %s", rec.Code, w.written(), rec.Body.String())
				}
			})
		}
	}
}
