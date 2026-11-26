package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
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

// TestClearOverrideNameConflictOnTheWireDB: clearing the owner's name for a
// profile from a set, when another profile now has the set's name, is
// refused through the real service exactly as the owner's own rename to that
// name is, and the override stays. It catches a clear that leaves two
// profiles with one name, and the two routes answering a clash differently.
func TestClearOverrideNameConflictOnTheWireDB(t *testing.T) {
	pool, userID := newSetAccountDB(t)
	provider := pgstore.NewPostgresProvider(pool)
	service := handlers.NewProfileHandler(provider)
	service.ProfileSetAvailable = true
	service.UserRepo = ownerRepo{2: {ID: 2, Role: models.RoleAdmin, IsOwner: true}, userID: {ID: userID, Role: "user"}}
	ctx := t.Context()
	syncID := uuid.NewString()
	set, _ := json.Marshal(map[string]any{"version": 1, "items": []any{setDBItem(uuid.NewString(), "Parent"), setDBItem(syncID, "Kid")}})
	held, err := service.ApplyProfileSet(ctx, handlers.ProfileSetCommand{UserID: userID, Set: set, Signed: true})
	if err != nil {
		t.Fatal(err)
	}
	kidID := held.Profiles[1].ID
	admin, req := profileedits.WithRequest(apimw.SetClaims(ctx, &auth.Claims{UserID: 2, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess}))
	req.SetActor(2, true, nil)
	if _, err := service.UpdateProfile(admin, handlers.ProfileUpdateCommand{UserID: userID, ProfileID: kidID,
		Request: handlers.ProfileUpdateRequest{Name: new("Kiddo")}, AccountAdmin: true}); err != nil {
		t.Fatal(err)
	}
	local, err := service.CreateProfile(admin, handlers.ProfileCreateCommand{UserID: userID, Request: handlers.ProfileCreateRequest{Name: "Guest"}, AccountAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = fixtureAdminAccounts()
	deps.Profiles = service
	deps.ProfileSetEdits = service
	h := NewHandler(deps)
	base := Prefix + "/admin/users/" + strconv.Itoa(userID) + "/profiles/"
	// The answer an administrator's own rename to a taken name gets.
	rename := readProblemAnswer(t, do(t, h, http.MethodPatch, base+local.ID, `{"name":"kiddo"}`, actingRequestAdmin))
	// Then the local profile takes the set's name, which is free now.
	if _, err := service.UpdateProfile(admin, handlers.ProfileUpdateCommand{UserID: userID, ProfileID: local.ID,
		Request: handlers.ProfileUpdateRequest{Name: new("Kid")}, AccountAdmin: true}); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Edits []listedOverride `json:"edits"`
	}
	if rec := do(t, h, http.MethodGet, Prefix+"/admin/users/"+strconv.Itoa(userID)+"/profile-set/edits", "", actingRequestAdmin); json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed.Edits) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	cleared := readProblemAnswer(t, do(t, h, http.MethodPatch, base+kidID+"/overrides/name", clearedBody, with(actingRequestAdmin, "If-Match", listed.Edits[0].ETag)))
	if rename.status != http.StatusConflict || cleared.status != rename.status || cleared.problem.Type != rename.problem.Type || cleared.problem.Detail != rename.problem.Detail {
		t.Fatalf("clear answered %d %+v; the owner's rename to a taken name answers %d %+v", cleared.status, cleared.problem, rename.status, rename.problem)
	}
	after, err := service.HeldProfileSet(ctx, userID)
	if err != nil || len(after.Overrides) != 1 || after.Overrides[0].Field != "name" || after.Items[1].Name != "Kiddo" {
		t.Fatalf("after a refused clear: %+v, %v", after, err)
	}
}

// newSetAccountDB is a user account on the test database, deleted with its
// profiles and edit rows when the test ends.
func newSetAccountDB(t *testing.T) (*pgxpool.Pool, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := "setclear-" + uuid.NewString()[:8]
	var userID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, permissions)
		VALUES ($1, $2, 'x', 'user', '{}') RETURNING id`, name, name+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(t.Context())
		_, _ = pool.Exec(bg, `DELETE FROM profile_set_edits WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	return pool, userID
}

func setDBItem(syncID, name string) map[string]any {
	return map[string]any{"sync_id": syncID, "name": name, "picture": "", "pin": map[string]any{"keep": true},
		"is_child": false, "max_content_rating": "", "max_advisory_age": nil, "require_advisory_age": false,
		"requests_blocked": false, "settings": map[string]any{}}
}

// ownerRepo answers the accounts the Owner rules load.
type ownerRepo map[int]*models.User

func (r ownerRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if u, ok := r[id]; ok {
		return u, nil
	}
	return nil, auth.ErrNotFound
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

// overrideClearRig is an account on the test database whose profiles came
// from a set, served by the real profile service behind the admin routes.
type overrideClearRig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	userID  int
	service *handlers.ProfileHandler
	h       http.Handler
	admin   context.Context
	main    map[string]any
	version int
	woke    bool
}

func newOverrideClearRig(t *testing.T) *overrideClearRig {
	pool, userID := newSetAccountDB(t)
	service := handlers.NewProfileHandler(pgstore.NewPostgresProvider(pool))
	service.ProfileSetAvailable = true
	service.UserRepo = ownerRepo{2: {ID: 2, Role: models.RoleAdmin, IsOwner: true}, userID: {ID: userID, Role: "user"}}
	r := &overrideClearRig{t: t, pool: pool, userID: userID, service: service, main: setDBItem(uuid.NewString(), "Parent"), woke: true}
	service.ProfileSetWaker = func(context.Context, int) bool { return r.woke }
	admin, req := profileedits.WithRequest(apimw.SetClaims(t.Context(), &auth.Claims{UserID: 2, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess}))
	req.SetActor(2, true, nil)
	r.admin = admin
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = fixtureAdminAccounts()
	deps.Profiles = service
	deps.ProfileSetEdits = service
	r.h = NewHandler(deps)
	return r
}

// apply sends the next version of the set: the main profile and kid.
func (r *overrideClearRig) apply(kid map[string]any) {
	r.t.Helper()
	r.version++
	set, _ := json.Marshal(map[string]any{"version": r.version, "items": []any{r.main, kid}})
	if _, err := r.service.ApplyProfileSet(r.t.Context(), handlers.ProfileSetCommand{UserID: r.userID, Set: set, Signed: true}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *overrideClearRig) kid(syncID string) *userstore.Profile {
	r.t.Helper()
	held, err := r.service.HeldProfileSet(r.t.Context(), r.userID)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, item := range held.Items {
		if item.SyncID == syncID {
			store, _ := pgstore.NewPostgresProvider(r.pool).ForUser(r.t.Context(), r.userID)
			p, err := store.GetProfile(r.t.Context(), item.ID)
			if err != nil {
				r.t.Fatal(err)
			}
			return p
		}
	}
	r.t.Fatalf("no profile with sync_id %s", syncID)
	return nil
}

// listedOverride is what a client reads of an override in the admin list.
type listedOverride struct {
	Kind         string `json:"kind"`
	Field        string `json:"field"`
	ETag         string `json:"etag"`
	ClearApplies bool   `json:"clear_applies"`
}

// listed is the override of field in the admin list, read through the
// route; nil when there is none.
func (r *overrideClearRig) listed(field string) *listedOverride {
	r.t.Helper()
	rec := do(r.t, r.h, http.MethodGet, Prefix+"/admin/users/"+strconv.Itoa(r.userID)+"/profile-set/edits", "", actingRequestAdmin)
	var out struct {
		Edits []listedOverride `json:"edits"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		r.t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	for i := range out.Edits {
		if out.Edits[i].Kind == "override" && out.Edits[i].Field == field {
			return &out.Edits[i]
		}
	}
	return nil
}

// clear sends the clear of field on profileID with body and If-Match
// (none when ifMatch is "").
func (r *overrideClearRig) clear(profileID, field, body, ifMatch string) *problemAnswer {
	r.t.Helper()
	headers := actingRequestAdmin
	if ifMatch != "" {
		headers = with(actingRequestAdmin, "If-Match", ifMatch)
	}
	path := Prefix + "/admin/users/" + strconv.Itoa(r.userID) + "/profiles/" + profileID + "/overrides/" + field
	return readProblemAnswer(r.t, do(r.t, r.h, http.MethodPatch, path, body, headers))
}

const clearedBody = `{"state":"cleared"}`

// TestClearOverridePreconditionDB: a clear does what the owner was shown,
// or nothing. The case it is for: the list says clearing keeps the owner's
// PIN; the person's app then sends the profile with no PIN; a clear with
// the etag the owner saw is refused and the PIN stays, and only a clear
// with the current etag removes it. It catches a clear applying a set the
// owner never saw, a clear without a precondition at all, and refusals
// answered in another order than the documented one.
func TestClearOverridePreconditionDB(t *testing.T) {
	t.Run("a PIN the owner was told stays is not removed by a stale clear", func(t *testing.T) {
		r := newOverrideClearRig(t)
		kid := setDBItem(uuid.NewString(), "Kid")
		kid["pin"] = map[string]any{"set": "1111"}
		r.apply(kid)
		p := r.kid(kid["sync_id"].(string))
		if _, err := r.service.UpdateProfile(r.admin, handlers.ProfileUpdateCommand{UserID: r.userID, ProfileID: p.ID,
			Request: handlers.ProfileUpdateRequest{PIN: new("2222")}, AccountAdmin: true}); err != nil {
			t.Fatal(err)
		}
		shown := r.listed("pin")
		if shown == nil || shown.ClearApplies || shown.ETag == "" {
			t.Fatalf("list before = %+v; want a PIN the clear keeps, with an etag", shown)
		}
		kid["pin"] = map[string]any{"clear": true}
		r.apply(kid)
		if got := r.clear(p.ID, "pin", clearedBody, shown.ETag); got.status != http.StatusPreconditionFailed {
			t.Fatalf("stale clear answered %d %+v", got.status, got.problem)
		}
		if r.kid(kid["sync_id"].(string)).PINHash == "" || r.listed("pin") == nil {
			t.Fatal("a refused clear removed the PIN or the override")
		}
		current := r.listed("pin")
		if !current.ClearApplies || current.ETag == shown.ETag {
			t.Fatalf("list after the newer set = %+v", current)
		}
		if got := r.clear(p.ID, "pin", clearedBody, current.ETag); got.status != http.StatusOK {
			t.Fatalf("current clear answered %d %+v", got.status, got.problem)
		}
		if r.kid(kid["sync_id"].(string)).PINHash != "" {
			t.Fatal("the clear the owner confirmed did not remove the PIN")
		}
	})
	t.Run("a value that changed since the owner saw it is not restored by a stale clear", func(t *testing.T) {
		r := newOverrideClearRig(t)
		kid := setDBItem(uuid.NewString(), "Kid")
		kid["max_content_rating"] = "PG"
		r.apply(kid)
		p := r.kid(kid["sync_id"].(string))
		if _, err := r.service.UpdateProfile(r.admin, handlers.ProfileUpdateCommand{UserID: r.userID, ProfileID: p.ID,
			Request: handlers.ProfileUpdateRequest{MaxContentRating: new("G")}, AccountAdmin: true}); err != nil {
			t.Fatal(err)
		}
		shown := r.listed("max_content_rating")
		kid["max_content_rating"] = "R"
		r.apply(kid)
		if got := r.clear(p.ID, "max_content_rating", clearedBody, shown.ETag); got.status != http.StatusPreconditionFailed {
			t.Fatalf("stale clear answered %d %+v", got.status, got.problem)
		}
		if got := r.kid(kid["sync_id"].(string)).MaxContentRating; got != "G" {
			t.Fatalf("rating after a refused clear = %q", got)
		}
		if got := r.clear(p.ID, "max_content_rating", clearedBody, r.listed("max_content_rating").ETag); got.status != http.StatusOK {
			t.Fatalf("current clear answered %d %+v", got.status, got.problem)
		}
		if got := r.kid(kid["sync_id"].(string)).MaxContentRating; got != "R" {
			t.Fatalf("rating after the clear = %q", got)
		}
	})
	t.Run("a mirrored setting whose other half changed is not restored by a stale clear", func(t *testing.T) {
		r := newOverrideClearRig(t)
		kid := setDBItem(uuid.NewString(), "Kid")
		kid["settings"] = map[string]any{settingskeys.PlaybackIntroSkipMode: "always"}
		r.apply(kid)
		p := r.kid(kid["sync_id"].(string))
		store, _ := pgstore.NewPostgresProvider(r.pool).ForUser(r.admin, r.userID)
		if _, err := store.UpsertSettingValue(r.admin, userstore.SettingIdentity{Key: settingskeys.PlaybackAutoSkipIntro, Scope: "profile", ProfileID: p.ID}, json.RawMessage(`false`)); err != nil {
			t.Fatal(err)
		}
		field := "settings." + settingskeys.PlaybackAutoSkipIntro
		shown := r.listed(field)
		kid["settings"] = map[string]any{settingskeys.PlaybackIntroSkipMode: "never"}
		r.apply(kid)
		if got := r.clear(p.ID, field, clearedBody, shown.ETag); got.status != http.StatusPreconditionFailed {
			t.Fatalf("stale clear answered %d %+v", got.status, got.problem)
		}
		if got := r.clear(p.ID, field, clearedBody, r.listed(field).ETag); got.status != http.StatusOK {
			t.Fatalf("current clear answered %d %+v", got.status, got.problem)
		}
		mode, err := store.GetSettingValue(t.Context(), userstore.SettingIdentity{Key: settingskeys.PlaybackIntroSkipMode, Scope: "profile", ProfileID: p.ID})
		if err != nil || mode == nil || string(mode.Value) != `"never"` {
			t.Fatalf("mode after the clear = %+v, %v", mode, err)
		}
	})
	t.Run("no If-Match, or a body that is not the cleared state, is refused and writes nothing", func(t *testing.T) {
		r := newOverrideClearRig(t)
		kid := setDBItem(uuid.NewString(), "Kid")
		kid["max_content_rating"] = "PG"
		r.apply(kid)
		p := r.kid(kid["sync_id"].(string))
		if _, err := r.service.UpdateProfile(r.admin, handlers.ProfileUpdateCommand{UserID: r.userID, ProfileID: p.ID,
			Request: handlers.ProfileUpdateRequest{MaxContentRating: new("G")}, AccountAdmin: true}); err != nil {
			t.Fatal(err)
		}
		etag := r.listed("max_content_rating").ETag
		if got := r.clear(p.ID, "max_content_rating", clearedBody, ""); got.status != http.StatusPreconditionRequired {
			t.Fatalf("no If-Match answered %d %+v", got.status, got.problem)
		}
		for _, body := range []string{`{"state":"kept"}`, `{"state":"cleared","value":"PG"}`, `{"state":null}`, `{}`, `null`} {
			if got := r.clear(p.ID, "max_content_rating", body, etag); got.status != http.StatusUnprocessableEntity && got.status != http.StatusBadRequest {
				t.Fatalf("body %s answered %d %+v", body, got.status, got.problem)
			}
		}
		if r.kid(kid["sync_id"].(string)).MaxContentRating != "G" || r.listed("max_content_rating") == nil {
			t.Fatal("a refused clear wrote something")
		}
	})
	t.Run("refusals come in order: not found, then the precondition, then a name", func(t *testing.T) {
		r := newOverrideClearRig(t)
		kid := setDBItem(uuid.NewString(), "Kid")
		r.apply(kid)
		p := r.kid(kid["sync_id"].(string))
		if _, err := r.service.UpdateProfile(r.admin, handlers.ProfileUpdateCommand{UserID: r.userID, ProfileID: p.ID,
			Request: handlers.ProfileUpdateRequest{Name: new("Kiddo")}, AccountAdmin: true}); err != nil {
			t.Fatal(err)
		}
		shown := r.listed("name")
		// The set renames the kid, so the owner's etag is stale; then a
		// profile here takes the set's new name, so a clear would clash.
		kid["name"] = "Kidd"
		r.apply(kid)
		if _, err := r.service.CreateProfile(r.admin, handlers.ProfileCreateCommand{UserID: r.userID,
			Request: handlers.ProfileCreateRequest{Name: "Kidd"}, AccountAdmin: true}); err != nil {
			t.Fatal(err)
		}
		if got := r.clear(p.ID, "name", clearedBody, shown.ETag); got.status != http.StatusPreconditionFailed {
			t.Fatalf("stale etag and a taken name answered %d, want 412 first", got.status)
		}
		current := r.listed("name").ETag
		if got := r.clear(p.ID, "name", clearedBody, current); got.status != http.StatusConflict {
			t.Fatalf("current etag and a taken name answered %d, want 409", got.status)
		}
		if got := r.clear(p.ID, "pin", clearedBody, ""); got.status != http.StatusNotFound {
			t.Fatalf("no override and no If-Match answered %d, want 404 first", got.status)
		}
		if got := r.clear(p.ID, "pin", clearedBody, current); got.status != http.StatusNotFound {
			t.Fatalf("no override and a stale etag answered %d, want 404 first", got.status)
		}
	})
	for _, woke := range []bool{true, false} {
		t.Run(fmt.Sprintf("a current clear applies and answers wake_queued %v as the waker did", woke), func(t *testing.T) {
			r := newOverrideClearRig(t)
			r.woke = woke
			kid := setDBItem(uuid.NewString(), "Kid")
			kid["max_content_rating"] = "PG"
			r.apply(kid)
			p := r.kid(kid["sync_id"].(string))
			if _, err := r.service.UpdateProfile(r.admin, handlers.ProfileUpdateCommand{UserID: r.userID, ProfileID: p.ID,
				Request: handlers.ProfileUpdateRequest{MaxContentRating: new("G")}, AccountAdmin: true}); err != nil {
				t.Fatal(err)
			}
			rec := do(t, r.h, http.MethodPatch, Prefix+"/admin/users/"+strconv.Itoa(r.userID)+"/profiles/"+p.ID+"/overrides/max_content_rating",
				clearedBody, with(actingRequestAdmin, "If-Match", r.listed("max_content_rating").ETag))
			var receipt AdminProfileSetCleared
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &receipt) != nil || !receipt.Applied || receipt.WakeQueued != woke {
				t.Fatalf("clear = %d %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("ETag") != "" {
				t.Fatal("a receipt carries no ETag")
			}
		})
	}
}
