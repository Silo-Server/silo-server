package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// revisionUsers is the account repository for the scenario: one member whose
// access_policy_revision the test advances. The Postgres profile store bumps
// it on every PIN, child-flag, rating, advisory, library and quality edit;
// the in-memory SQLite store used here does not, so the test does it for the
// store, after each such edit.
type revisionUsers struct{ user *models.User }

func (u *revisionUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if id != u.user.ID {
		return nil, nil
	}
	clone := *u.user
	return &clone, nil
}

type singleStoreProvider struct{ store userstore.UserStore }

func (p singleStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p singleStoreProvider) Close() error { return nil }

// A PIN-locked household parent verifies its PIN, edits a child's parental
// controls and keeps working with the same profile token: editing another
// profile, even one that advances the account's access_policy_revision, must
// not sign the parent out. Changing the parent's own PIN must.
func TestProfileTokenSurvivesManagingAnotherProfile(t *testing.T) {
	ctx := context.Background()
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	users := &revisionUsers{user: &models.User{ID: 1, Role: "user", Enabled: true, MaxProfiles: 5, AccessPolicyRevision: 1}}
	store := userdb.NewSQLiteUserStore(db.DB)
	for _, p := range []userstore.Profile{{ID: "p-parent", Name: "Parent"}, {ID: "p-kid", Name: "Kid"}} {
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatalf("CreateProfile(%s): %v", p.ID, err)
		}
	}
	parentPIN, kidPIN := "1234", "9999"
	if err := store.UpdateProfile(ctx, "p-parent", userstore.UpdateProfileInput{PIN: &parentPIN}); err != nil {
		t.Fatal(err)
	}

	stores := singleStoreProvider{store: store}
	tokens := access.NewProfileTokenService("profile-token-revision-test-secret", 0)
	profileHandler := handlers.NewProfileHandler(stores)
	profileHandler.UserRepo = users
	profileHandler.ProfileTokens = tokens
	deps := pilotDeps(nil, nil)
	deps.Profiles = profileHandler
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(access.NewResolver(users, stores, tokens))
	h := newTestHandler(t, deps)

	verify := func(pin string) string {
		t.Helper()
		rec := do(t, h, http.MethodPost, "/api/v2/profiles/p-parent/verify-pin", `{"pin":"`+pin+`"}`, bearer(memberToken))
		if rec.Code != http.StatusOK {
			t.Fatalf("verify-pin: %d %s", rec.Code, rec.Body.String())
		}
		var out ProfileVerification
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !out.Valid || out.ProfileToken == "" {
			t.Fatalf("verify-pin = %+v", out)
		}
		return out.ProfileToken
	}
	asParent := func(token string) map[string]string {
		return with(with(bearer(memberToken), "X-Profile-Id", "p-parent"), "X-Profile-Token", token)
	}
	patchKid := func(token, body string) int {
		t.Helper()
		rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-kid", body, asParent(token))
		if rec.Code != http.StatusOK && rec.Code != http.StatusForbidden {
			t.Fatalf("PATCH kid %s: %d %s", body, rec.Code, rec.Body.String())
		}
		if rec.Code == http.StatusOK {
			// Every kid edit below touches an access-policy field.
			users.user.AccessPolicyRevision++
		}
		return rec.Code
	}

	token := verify(parentPIN)
	revisionBefore := users.user.AccessPolicyRevision

	// The reported scenario: save the kid's rating, then keep going.
	if code := patchKid(token, `{"max_content_rating":"PG"}`); code != http.StatusOK {
		t.Fatalf("first kid edit answered %d", code)
	}
	if code := patchKid(token, `{"max_playback_quality":"1080p","is_child":true}`); code != http.StatusOK {
		t.Fatalf("parent token died after editing the kid's rating: %d", code)
	}
	if code := patchKid(token, `{"pin":"`+kidPIN+`"}`); code != http.StatusOK {
		t.Fatalf("parent token died after editing the kid's limits: %d", code)
	}
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-parent", `{"name":"Mum"}`, asParent(token)); rec.Code != http.StatusOK {
		t.Fatalf("parent token died after setting the kid's PIN: %d %s", rec.Code, rec.Body.String())
	}
	if users.user.AccessPolicyRevision == revisionBefore {
		t.Fatal("scenario did not advance the account revision; it would not reproduce the defect")
	}

	// The parent's own PIN change ends every token minted for the old PIN.
	newPIN := "4321"
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-parent", `{"pin":"`+newPIN+`"}`, asParent(token)); rec.Code != http.StatusOK {
		t.Fatalf("parent PIN change: %d %s", rec.Code, rec.Body.String())
	}
	users.user.AccessPolicyRevision++
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-kid", `{"max_content_rating":"G"}`, asParent(token)), TypeProfileVerificationRequired)

	// A token for the new PIN works again.
	token = verify(newPIN)
	if code := patchKid(token, `{"max_content_rating":"G"}`); code != http.StatusOK {
		t.Fatalf("fresh token refused: %d", code)
	}

	// A token minted before this release carries no pin_revision and is
	// refused once, even with an account revision that still matches.
	legacy := mintLegacyProfileToken(t, "profile-token-revision-test-secret", 1, "s1", "p-parent", users.user.AccessPolicyRevision)
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-kid", `{"max_content_rating":"PG"}`, asParent(legacy)), TypeProfileVerificationRequired)
}

func mintLegacyProfileToken(t *testing.T, secret string, userID int, sessionID, profileID string, policyRevision int64) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": userID, "session_id": sessionID, "profile_id": profileID,
		"policy_revision": policyRevision, "iat": time.Now().Unix(),
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
