package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/progresssync"
)

const bootstrapFixtureID = "5e6af827-e7fc-4ebc-84f3-597c888b77ae"

type fakeBootstrap struct {
	err     error
	limit   int
	request string
}

func (f *fakeBootstrap) Capabilities(ctx context.Context, a progresssync.Actor) (progresssync.Support, error) {
	if _, err := a.Recheck(ctx); err != nil {
		return progresssync.Support{}, err
	}
	return progresssync.Support{InstallationID: "installation", Generation: "generation"}, f.err
}
func (f *fakeBootstrap) CheckSnapshotVisibility(ctx context.Context, a progresssync.Actor, id string) error {
	if id != bootstrapFixtureID || a.Input.UserID != 1 || a.Input.ProfileID != "p-owner" {
		return progresssync.ErrNotFound
	}
	return nil
}
func (f *fakeBootstrap) CreateSnapshot(ctx context.Context, a progresssync.Actor, id string, limit int) (progresssync.Page, error) {
	if _, err := a.Recheck(ctx); err != nil {
		return progresssync.Page{}, err
	}
	f.limit = limit
	f.request = id
	return bootstrapFixture(false), f.err
}
func (f *fakeBootstrap) ReadSnapshot(ctx context.Context, a progresssync.Actor, pos progresssync.Position) (progresssync.Page, error) {
	if _, err := a.Recheck(ctx); err != nil {
		return progresssync.Page{}, err
	}
	if pos.Generation != "generation" || pos.InstallationID != "installation" || pos.PageSize != 1 || pos.After != 1 {
		return progresssync.Page{}, progresssync.ErrResetRequired
	}
	return bootstrapFixture(true), f.err
}
func bootstrapFixture(last bool) progresssync.Page {
	at := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	s := progresssync.Snapshot{ID: bootstrapFixtureID, Identity: progresssync.Identity{UserID: 1, ProfileID: "p-owner"}, InstallationID: "installation", Generation: "generation", AccessDigest: "access", PageSize: 1, CapturedAt: at, ExpiresAt: at.Add(15 * time.Minute), ItemCount: 2}
	page := progresssync.Page{Snapshot: s, Items: []progresssync.Entry{{MediaItemID: "movie-a", PositionSeconds: 1.5, UpdatedAt: at}}}
	if !last {
		page.Next = &progresssync.Position{SnapshotID: s.ID, Identity: s.Identity, InstallationID: s.InstallationID, Generation: s.Generation, AccessDigest: s.AccessDigest, PageSize: 1, After: 1}
	} else {
		page.Items[0].MediaItemID = "movie-b"
	}
	return page
}
func TestProgressBootstrapSignedPages(t *testing.T) {
	f := &fakeBootstrap{}
	deps := parityDeps(false)
	deps.CursorSecret = []byte("synthetic-shared-bootstrap-secret")
	deps.ProgressBootstrap = f
	h := newTestHandler(t, deps)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	rec := do(t, h, http.MethodPost, progressSnapshotPath, `{"request_id":"9a91f367-e3bc-4305-b2ba-133bb507ce2d","limit":1}`, owner)
	if rec.Code != 201 || rec.Header().Get("Location") != progressSnapshotPath+"/"+bootstrapFixtureID || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}
	var first ProgressSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.CompletionToken != "" || !first.Page.HasMore || first.Items[0].PositionSeconds != 1.5 {
		t.Fatalf("first=%+v", first)
	}
	read := progressSnapshotPath + "/" + bootstrapFixtureID + "?cursor=" + url.QueryEscape(first.Page.NextCursor)
	rec = do(t, h, http.MethodGet, read, "", owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var last ProgressSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
		t.Fatal(err)
	}
	if !last.Complete || last.Page.HasMore || last.CompletionToken == "" || last.Page.NextCursor != "" {
		t.Fatalf("last=%+v", last)
	}
	for _, cursor := range []string{last.CompletionToken, first.Page.NextCursor + "x", "12345"} {
		rec = do(t, h, http.MethodGet, progressSnapshotPath+"/"+bootstrapFixtureID+"?cursor="+url.QueryEscape(cursor), "", owner)
		if rec.Code != 400 {
			t.Fatalf("invalid token=%d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, http.MethodGet, read, "", with(bearer(memberToken), "X-Profile-Id", "p-other"))
	if rec.Code != 404 {
		t.Fatalf("foreign profile=%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, progressSnapshotPath+"/00000000-0000-0000-0000-000000000000?cursor="+url.QueryEscape(first.Page.NextCursor), "", owner)
	if rec.Code != 404 {
		t.Fatalf("unknown snapshot=%d %s", rec.Code, rec.Body.String())
	}
}
func TestProgressBootstrapErrorsAndLimits(t *testing.T) {
	f := &fakeBootstrap{}
	deps := parityDeps(false)
	deps.ProgressBootstrap = f
	h := newTestHandler(t, deps)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct {
		err    error
		status int
		kind   string
	}{
		{progresssync.ErrUnsupported, 501, "capability_unsupported"}, {progresssync.ErrResetRequired, 409, "sync_reset_required"}, {progresssync.ErrRequestConflict, 409, "snapshot_request_conflict"}, {progresssync.ErrTooLarge, 413, "progress_snapshot_too_large"}, {progresssync.ErrQuota, 429, "rate_limited"}, {errors.New("private backend details"), 503, "dependency_unavailable"},
	} {
		f.err = tc.err
		rec := do(t, h, http.MethodPost, progressSnapshotPath, `{"request_id":"9a91f367-e3bc-4305-b2ba-133bb507ce2d"}`, owner)
		if rec.Code != tc.status {
			t.Fatalf("error=%v status=%d %s", tc.err, rec.Code, rec.Body.String())
		}
		var p problemDoc
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Type != ProblemTypeOrigin+tc.kind {
			t.Fatal(p.Type)
		}
		if tc.status == 429 && rec.Header().Get("Retry-After") != "30" {
			t.Fatal("missing retry delay")
		}
	}
	f.err = nil
	for _, body := range []string{`{"request_id":"bad"}`, `{"request_id":"9a91f367-e3bc-4305-b2ba-133bb507ce2d","limit":201}`, `{"request_id":"9a91f367-e3bc-4305-b2ba-133bb507ce2d","limit":0}`} {
		rec := do(t, h, http.MethodPost, progressSnapshotPath, body, owner)
		if rec.Code != 422 {
			t.Fatalf("invalid body=%d %s", rec.Code, rec.Body.String())
		}
	}
	f.err = progresssync.ErrUnsupported
	rec := do(t, h, http.MethodGet, Prefix+"/sync/progress/capabilities", "", owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var c ProgressBootstrapCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.State != StateUnsupported || c.Allowed == nil || *c.Allowed || c.InstallationID != "" || c.Incremental {
		t.Fatalf("capability=%+v", c)
	}
}

func TestBootstrapCurrentCredentialRevalidation(t *testing.T) {
	claims := &auth.Claims{UserID: 1, SessionID: "session", TokenType: auth.TokenTypeAccess}
	tokens := fakeTokens{claims: map[string]*auth.Claims{"credential": claims}}
	sessions := fakeSessions{valid: map[string]bool{"session": true}}
	users := fakeUsers{users: map[int]*models.User{1: {ID: 1, Enabled: true}}}
	keys := fakeAPIKeys{map[string]*models.APIKey{"sa_synthetic": {ID: 7, UserID: 1}}}
	gate := apimw.NewAuthMiddleware(tokens, sessions, keys, users)
	recheck := func() error {
		return gate.RevalidateCurrent(t.Context(), "Bearer credential", claims, http.MethodPost, progressSnapshotPath)
	}
	if err := recheck(); err != nil {
		t.Fatal(err)
	}
	sessions.valid["session"] = false
	if err := recheck(); !errors.Is(err, apimw.ErrCurrentCredentialInvalid) {
		t.Fatalf("revoked session=%v", err)
	}
	sessions.valid["session"] = true
	users.users[1].Enabled = false
	if err := recheck(); !errors.Is(err, apimw.ErrCurrentCredentialInvalid) {
		t.Fatalf("disabled account=%v", err)
	}
	users.users[1].Enabled = true
	delete(tokens.claims, "credential")
	if err := recheck(); !errors.Is(err, apimw.ErrCurrentCredentialInvalid) {
		t.Fatalf("invalidated token=%v", err)
	}
	keyClaims := &auth.Claims{UserID: 1, APIKeyID: 7, TokenType: auth.TokenTypeAPIKey}
	if err := gate.RevalidateCurrent(t.Context(), "Bearer sa_synthetic", keyClaims, http.MethodPost, progressSnapshotPath); err != nil {
		t.Fatal(err)
	}
	keys.keys["sa_synthetic"].Scopes = []string{auth.ScopeAdminUsers}
	if err := gate.RevalidateCurrent(t.Context(), "Bearer sa_synthetic", keyClaims, http.MethodPost, progressSnapshotPath); !errors.Is(err, apimw.ErrCurrentCredentialForbidden) {
		t.Fatalf("reduced scope=%v", err)
	}
	delete(keys.keys, "sa_synthetic")
	if err := gate.RevalidateCurrent(t.Context(), "Bearer sa_synthetic", keyClaims, http.MethodPost, progressSnapshotPath); !errors.Is(err, apimw.ErrCurrentCredentialInvalid) {
		t.Fatalf("deleted key=%v", err)
	}
}

// The fake service invokes the production actor recheck after the router's
// initial authentication, so accepted header spellings must pass both gates.
func TestProgressBootstrapBearerHeaderRecheck(t *testing.T) {
	deps := parityDeps(false)
	deps.ProgressBootstrap = &fakeBootstrap{}
	h := newTestHandler(t, deps)
	for _, header := range []string{"Bearer " + memberToken, "bearer " + memberToken, "bEaReR " + memberToken, "Bearer   " + memberToken + " \t"} {
		t.Run(header[:6], func(t *testing.T) {
			owner := map[string]string{"Authorization": header, "X-Profile-Id": "p-owner"}
			for _, request := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, Prefix + "/sync/progress/capabilities", "", http.StatusOK},
				{http.MethodPost, progressSnapshotPath, `{"request_id":"9a91f367-e3bc-4305-b2ba-133bb507ce2d","limit":1}`, http.StatusCreated},
			} {
				rec := do(t, h, request.method, request.path, request.body, owner)
				if rec.Code != request.status {
					t.Fatalf("%s = %d %s", request.method, rec.Code, rec.Body.String())
				}
				if request.method == http.MethodPost {
					var page ProgressSnapshot
					if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
						t.Fatal(err)
					}
					read := progressSnapshotPath + "/" + bootstrapFixtureID + "?cursor=" + url.QueryEscape(page.Page.NextCursor)
					rec = do(t, h, http.MethodGet, read, "", owner)
					if rec.Code != http.StatusOK {
						t.Fatalf("page = %d %s", rec.Code, rec.Body.String())
					}
				}
			}
		})
	}
	for _, header := range []string{"bearer invalid", "bearer " + expiredToken, "Bearer   ", ""} {
		rec := do(t, h, http.MethodGet, Prefix+"/sync/progress/capabilities", "", map[string]string{"Authorization": header, "X-Profile-Id": "p-owner"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("invalid credential = %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodGet, Prefix+"/sync/progress/capabilities?token="+url.QueryEscape(memberToken), "", map[string]string{"X-Profile-Id": "p-owner"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("query-only credential = %d %s", rec.Code, rec.Body.String())
	}
}
