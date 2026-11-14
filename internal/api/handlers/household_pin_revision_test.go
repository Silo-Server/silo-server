package handlers

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
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
