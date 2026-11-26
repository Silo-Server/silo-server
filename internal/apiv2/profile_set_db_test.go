package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestProfileSetNameConflictOnTheWireDB: a new item named like a profile
// outside the set is refused, through the real service, as clients receive
// it in both forms: validation_failed with one entry at
// body.set.items[n].name and the code name_conflict. Clients branch on that
// code. It catches the code renamed, the entry pointed at another item or
// member, and the two forms reporting it differently.
func TestProfileSetNameConflictOnTheWireDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := "setclash-" + uuid.NewString()[:8]
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions)
		VALUES ($1, $2, 'x', 'user', '{}') RETURNING id`, name, name+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	primary := uuid.NewString()
	for _, p := range []userstore.Profile{{ID: primary, Name: "Parent", IsPrimary: true}, {ID: uuid.NewString(), Name: "Alex"}} {
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	item := func(syncID, name string) map[string]any {
		return map[string]any{"sync_id": syncID, "name": name, "picture": "", "pin": map[string]any{"keep": true},
			"is_child": false, "max_content_rating": "", "max_advisory_age": nil, "require_advisory_age": false,
			"requests_blocked": false, "settings": map[string]any{}}
	}
	// Item 2 is the one named like the profile outside the set.
	set, _ := json.Marshal(map[string]any{"version": 1, "items": []any{
		item(uuid.NewString(), "Parent"), item(uuid.NewString(), "Sam"), item(uuid.NewString(), "alex"),
	}})
	sessionBody, _ := json.Marshal(map[string]json.RawMessage{"set": set})

	service := handlers.NewProfileHandler(provider)
	service.ProfileSetAvailable = true
	sessionID := uuid.NewString()
	deps := appKeyDeps(&fakeAppKeys{enabled: true})
	deps.AppKeys = &profileSetKeys{fakeAppKeys: &fakeAppKeys{enabled: true}, signed: &auth.ProfileSetAuthority{UserID: userID, Set: set}}
	deps.ProfileSet = service
	deps.Auth = apimw.NewAuthMiddleware(
		fakeTokens{map[string]*auth.Claims{"tok-set": {UserID: userID, Role: "user", SessionID: sessionID, TokenType: auth.TokenTypeAccess}}},
		fakeSessions{map[string]string{sessionID: "user"}}, fakeAPIKeys{},
		fakeUsers{map[int]*models.User{userID: {ID: userID, Role: "user", Enabled: true}}})
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(onlyProfileResolver{id: primary})
	h := NewHandler(deps)
	for form, rec := range map[string]func() *problemAnswer{
		"session": func() *problemAnswer {
			return readProblemAnswer(t, do(t, h, http.MethodPut, Prefix+"/profiles/set", string(sessionBody),
				map[string]string{"Authorization": "Bearer tok-set", "X-Profile-Id": primary}))
		},
		"signed": func() *problemAnswer {
			return readProblemAnswer(t, do(t, h, http.MethodPut, Prefix+"/profiles/set", testEnvelope, nil))
		},
	} {
		got := rec()
		if got.status != http.StatusUnprocessableEntity || got.problem.Type != TypeValidationFailed.URI() || len(got.problem.Errors) != 1 ||
			got.problem.Errors[0].Location != "body.set.items[2].name" || got.problem.Errors[0].Code != "name_conflict" {
			t.Fatalf("%s form answered %d %+v", form, got.status, got.problem)
		}
	}
	if profiles, err := store.ListProfiles(ctx); err != nil || len(profiles) != 2 {
		t.Fatalf("a refused set changed the household: %+v, %v", profiles, err)
	}
}

// onlyProfileResolver verifies one profile, as the production resolver does
// for an unlocked profile of the account.
type onlyProfileResolver struct{ id string }

func (r onlyProfileResolver) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	if in.ProfileID == "" || in.ProfileID == r.id {
		return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true}, nil
	}
	return access.Scope{}, access.ErrProfileNotFound
}

type problemAnswer struct {
	status  int
	problem Problem
}

func readProblemAnswer(t *testing.T, rec interface {
	Result() *http.Response
},
) *problemAnswer {
	t.Helper()
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	out := &problemAnswer{status: res.StatusCode}
	_ = json.NewDecoder(res.Body).Decode(&out.problem)
	return out
}
