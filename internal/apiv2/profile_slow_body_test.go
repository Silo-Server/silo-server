package apiv2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Hold the body after the real viewer gate has verified the parent's token.
// A second HTTP request then changes the household before the held mutation
// reaches its service. No timers determine the ordering.
func TestHouseholdMutationRechecksPINAfterSlowBody(t *testing.T) {
	engine, err := policy.NewEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, setup := range []string{"policy", "access", "policy legacy revision zero", "policy initially unlocked"} {
		for _, mutation := range []struct {
			name, method, path, body string
			status                   int
		}{
			{"create", http.MethodPost, "/api/v2/profiles", `{"name":"New member"}`, http.StatusCreated},
			{"relax limits", http.MethodPatch, "/api/v2/profiles/child", `{"max_content_rating":null}`, http.StatusOK},
			{"clear PIN", http.MethodPatch, "/api/v2/profiles/parent", `{"pin":null}`, http.StatusOK},
		} {
			for _, concurrent := range []struct {
				name, path, body string
				revoked          bool
			}{
				{"parent PIN changed", "/api/v2/profiles/parent", `{"pin":"4321"}`, true},
				{"parent policy changed", "/api/v2/profiles/parent", `{"max_content_rating":"PG-13"}`, false},
				{"child PIN changed", "/api/v2/profiles/child", `{"pin":"9876"}`, false},
			} {
				t.Run(setup+"/"+mutation.name+"/"+concurrent.name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
					defer cancel()
					db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = db.Close() })
					db.DB.SetMaxOpenConns(1)
					store := userdb.NewSQLiteUserStore(db.DB)
					for _, p := range []userstore.Profile{{ID: "parent", Name: "Parent"}, {ID: "child", Name: "Child", MaxContentRating: "PG"}} {
						if err := store.CreateProfile(ctx, p); err != nil {
							t.Fatal(err)
						}
					}
					if setup != "policy initially unlocked" {
						if err := store.UpdateProfile(ctx, "parent", userstore.UpdateProfileInput{PIN: new("1234")}); err != nil {
							t.Fatal(err)
						}
					}
					if setup == "policy legacy revision zero" {
						if _, err := db.DB.ExecContext(ctx, "UPDATE profiles SET pin_revision = 0 WHERE id = 'parent'"); err != nil {
							t.Fatal(err)
						}
					}
					users := &revisionUsers{user: &models.User{ID: 1, Role: "user", Enabled: true, MaxProfiles: new(5)}}
					stores := singleStoreProvider{store: store}
					tokens := access.NewProfileTokenService("slow-body-profile-test-secret", 0)
					profiles := handlers.NewProfileHandler(stores)
					profiles.UserRepo, profiles.ProfileTokens = users, tokens
					deps := pilotDeps(nil, nil)
					deps.Profiles = profiles
					deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policy.NewViewerResolver(users, stores, tokens, policy.NewPDP(engine)))
					if setup == "access" {
						deps.ViewerAccess = apimw.NewViewerAccessMiddleware(access.NewResolver(users, stores, tokens))
					}
					h := newTestHandler(t, deps)
					var proof ProfileVerification
					if setup != "policy initially unlocked" {
						verified := do(t, h, http.MethodPost, "/api/v2/profiles/parent/verify-pin", `{"pin":"1234"}`, bearer(memberToken))
						if verified.Code != http.StatusOK {
							t.Fatalf("verify PIN: %d %s", verified.Code, verified.Body.String())
						}
						if err := json.Unmarshal(verified.Body.Bytes(), &proof); err != nil {
							t.Fatal(err)
						}
					}
					headers := with(with(bearer(memberToken), "X-Profile-Id", "parent"), "X-Profile-Token", proof.ProfileToken)
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					reading := make(chan struct{})
					body := &bodyReadSignal{Reader: reader, started: reading}
					req := httptest.NewRequest(mutation.method, mutation.path, body).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					for k, v := range headers {
						req.Header.Set(k, v)
					}
					rec := httptest.NewRecorder()
					done := make(chan struct{})
					go func() { defer close(done); h.ServeHTTP(rec, req) }()
					select {
					case <-reading:
					case <-done:
						t.Fatalf("request returned before reading body: %d %s", rec.Code, rec.Body.String())
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					changed := do(t, h, http.MethodPatch, concurrent.path, concurrent.body, headers)
					if changed.Code != http.StatusOK {
						t.Fatalf("concurrent edit: %d %s", changed.Code, changed.Body.String())
					}
					users.user.AccessPolicyRevision++ // PG advances this for every policy edit above.
					if _, err := io.Copy(writer, strings.NewReader(mutation.body)); err != nil {
						t.Fatal(err)
					}
					_ = writer.Close()
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					if !concurrent.revoked {
						if rec.Code != mutation.status {
							t.Fatalf("unchanged parent PIN rejected: %d %s", rec.Code, rec.Body.String())
						}
						return
					}
					// Check persisted effects even when the response is wrong.
					rows, err := store.ListProfiles(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if len(rows) != 2 {
						t.Errorf("revoked proof created a profile: count=%d", len(rows))
					}
					child, err := store.GetProfile(ctx, "child")
					if err != nil || child.MaxContentRating != "PG" {
						t.Errorf("revoked proof changed child limits: profile=%+v error=%v", child, err)
					}
					valid, err := store.VerifyPIN(ctx, "parent", "4321")
					if err != nil || !valid {
						t.Errorf("revoked proof changed replacement PIN: valid=%v error=%v", valid, err)
					}
					requireProblem(t, rec, TypeProfileVerificationRequired)
				})
			}
		}
	}
}

type bodyReadSignal struct {
	io.Reader
	started chan struct{}
}

func (r *bodyReadSignal) Read(p []byte) (int, error) {
	if r.started != nil {
		close(r.started)
		r.started = nil
	}
	return r.Reader.Read(p)
}
