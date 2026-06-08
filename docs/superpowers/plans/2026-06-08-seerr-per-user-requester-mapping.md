# Seerr per-user requester mapping — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a Seerr connection optionally attribute each request to the Seerr user matching the Silo requester's email (creating that user with operator-chosen permissions when absent), instead of always submitting as the API-key admin.

**Architecture:** A per-connection `requester_mode` (`admin` default | `mapped`). The host resolves the requester's email/username and pushes them into the Fulfill descriptor; the seerr plugin, in `mapped` mode, finds-or-creates the Seerr user by email and sets `userId` on the request body. Any failure falls back to admin.

**Tech Stack:** protobuf via `buf` (SDK); Go (SDK/host/plugin); Overseerr/Jellyseerr REST.

**Repos & order:** Task 1 = `silo-plugin-sdk` (`/opt/silo-plugin-sdk`, branch `feat/request-router-capability`); Task 2 = `silo-server` (`/opt/silo`, branch `feat/requests-pluginization`); Tasks 3–4 = `silo-plugin-requests-seerr` (`/opt/silo-plugin-requests-seerr`, `master`); Task 5 = deploy. Plugin/host see the SDK via local `replace`, so Task 1 lands first. Go: prefix `PATH=$PATH:/tmp/go/bin`.

---

### Task 1: SDK — requester email/username on the descriptor

**Files:** Modify `proto/silo/plugin/v1/request_router.proto`; regenerate `pkg/pluginproto/silo/plugin/v1/request_router.pb.go`.

- [ ] **Step 1: Edit the proto.** In `request_router.proto`, change `RequestDescriptor` to add fields 8 and 9 after `requester_profile_id = 7;`:

```proto
  int64 requester_user_id = 6;
  string requester_profile_id = 7;
  // requester identity for plugins that attribute requests to a per-user account
  // on the downstream service (e.g. Seerr). Empty when the host cannot resolve it.
  string requester_email = 8;
  string requester_username = 9;
```

- [ ] **Step 2: Regenerate + verify.**

```bash
cd /opt/silo-plugin-sdk && PATH="$PWD/bin:/tmp/go/bin:$PATH" buf generate
PATH=$PATH:/tmp/go/bin go build ./... \
  && grep -q "func (x \*RequestDescriptor) GetRequesterEmail()" pkg/pluginproto/silo/plugin/v1/request_router.pb.go \
  && grep -q "func (x \*RequestDescriptor) GetRequesterUsername()" pkg/pluginproto/silo/plugin/v1/request_router.pb.go \
  && echo OK
```
Expected: `OK`. Then `PATH=$PATH:/tmp/go/bin go test ./...` passes.

- [ ] **Step 3: Commit.**
```bash
cd /opt/silo-plugin-sdk && git add proto/ pkg/pluginproto/
git commit -m "feat(request_router): RequestDescriptor requester_email + requester_username"
```

---

### Task 2: Host — resolve requester identity and populate the descriptor

**Files:**
- Modify: `internal/plugins/user_identity_lookup.go` (add `Email`)
- Modify: `internal/requests/types.go` (transient requester fields on `Request`)
- Modify: `internal/requests/provider.go` (`routerDescriptor`)
- Modify: `internal/requests/service.go` (resolver interface + setter + populate at both Fulfill sites)
- Modify: `cmd/silo/main.go` (wire the resolver)
- Test: `internal/plugins/user_identity_lookup_test.go` (if present) and `internal/requests/service_test.go`

- [ ] **Step 1: Add `Email` to the identity lookup.** In `internal/plugins/user_identity_lookup.go`, add `Email string` to `UserIdentity` (after `Username`) and select it:

```go
type UserIdentity struct {
	Username         string
	Email            string
	ProfileName      string
	ProfileIsPrimary bool
}
```
and change the first query to:
```go
	err := l.pool.QueryRow(ctx,
		"SELECT username, email FROM users WHERE id = $1", userID,
	).Scan(&out.Username, &out.Email)
```
(`users.email` exists.) Other callers read only `Username`, so this is additive.

- [ ] **Step 2: Write the failing host test.** In `internal/requests/service_test.go`, add a fake resolver and a test that the descriptor carries the email. First add this fake near the other fakes:

```go
type fakeRequesterIdentity struct {
	email, username string
	err             error
	gotUserID       int
}

func (f *fakeRequesterIdentity) ResolveRequester(_ context.Context, userID int) (string, string, error) {
	f.gotUserID = userID
	return f.email, f.username, f.err
}
```
Extend `fakeRouterProvider` to capture the descriptor's requester identity. Add fields:
```go
	gotRequesterEmail    string
	gotRequesterUsername string
```
and set them in `Fulfill` from the connection... no — `Fulfill` receives `req Request`, so record from `req`:
```go
func (f *fakeRouterProvider) Fulfill(_ context.Context, installationID int, _ string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	f.mu.Lock()
	f.gotRequesterEmail = req.RequesterEmail
	f.gotRequesterUsername = req.RequesterUsername
	f.mu.Unlock()
	// ... existing body unchanged ...
}
```
Then the test:
```go
func TestSubmitApprovedPopulatesRequesterIdentity(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInstOn("router-1", 1)}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)
	service.SetRequesterIdentityResolver(&fakeRequesterIdentity{email: "u@example.com", username: "bob"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := service.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.gotRequesterEmail != "u@example.com" || router.gotRequesterUsername != "bob" {
		t.Fatalf("descriptor identity = %q/%q, want u@example.com/bob", router.gotRequesterEmail, router.gotRequesterUsername)
	}
}
```

- [ ] **Step 3: Run to verify failure.** `cd /opt/silo && PATH=$PATH:/tmp/go/bin go test ./internal/requests/ -run TestSubmitApprovedPopulatesRequesterIdentity` → COMPILE FAILURE (`Request` has no `RequesterEmail`; `SetRequesterIdentityResolver` undefined). You fix these in Step 4.

- [ ] **Step 4: Implement.**

In `internal/requests/types.go`, add transient fields to `Request` (next to `RequestedByProfileID`):
```go
	RequesterEmail    string `json:"-"`
	RequesterUsername string `json:"-"`
```

In `internal/requests/provider.go`, map them in `routerDescriptor`:
```go
		RequesterUserId:    int64(req.RequestedByUserID),
		RequesterProfileId: req.RequestedByProfileID,
		RequesterEmail:     req.RequesterEmail,
		RequesterUsername:  req.RequesterUsername,
```

In `internal/requests/service.go`, add the interface, the service field, the setter, and a populate helper:
```go
// RequesterIdentityResolver resolves a requesting user id into the identity a
// per-user request_router plugin needs (e.g. Seerr attribution by email).
type RequesterIdentityResolver interface {
	ResolveRequester(ctx context.Context, userID int) (email, username string, err error)
}

func (s *Service) SetRequesterIdentityResolver(r RequesterIdentityResolver) { s.requesterIdentity = r }

// populateRequesterIdentity fills req.RequesterEmail/Username from the resolver.
// Nil resolver or any error leaves them empty (the plugin then behaves as admin).
func (s *Service) populateRequesterIdentity(ctx context.Context, req *Request) {
	if s.requesterIdentity == nil || req.RequestedByUserID <= 0 {
		return
	}
	email, username, err := s.requesterIdentity.ResolveRequester(ctx, req.RequestedByUserID)
	if err != nil {
		slog.WarnContext(ctx, "requests: requester identity resolve failed; attributing to admin", "user_id", req.RequestedByUserID, "error", err)
		return
	}
	req.RequesterEmail, req.RequesterUsername = email, username
}
```
Add the field `requesterIdentity RequesterIdentityResolver` to the `Service` struct.

At BOTH `s.router.Fulfill(ctx, installationID, capabilityID, req, want, conns)` call sites (in `submitApprovedRequest` ~line 1349 and the reconcile/retry path ~line 1504), insert immediately before the call:
```go
	s.populateRequesterIdentity(ctx, &req)
```
(`req` is a local `Request` value at both sites, so mutating it before the call is safe.)

- [ ] **Step 5: Run to verify pass.** `cd /opt/silo && PATH=$PATH:/tmp/go/bin go test ./internal/requests/ ./internal/plugins/ && PATH=$PATH:/tmp/go/bin go vet ./internal/requests/ ./internal/plugins/`. Expected: PASS, vet clean.

- [ ] **Step 6: Wire the resolver in `cmd/silo/main.go`.** Find the `mediarequests.NewService(...)` construction (~line 1439). Right after the existing `requestReconcileSvc.Set…` calls, add an adapter over the pg user lookup and wire it:
```go
	requestReconcileSvc.SetRequesterIdentityResolver(plugins.RequesterIdentityFromLookup(plugins.NewPgUserIdentityLookup(deps.DB)))
```
And add the adapter to `internal/plugins/user_identity_lookup.go`:
```go
// RequesterIdentityFromLookup adapts a UserIdentityLookup into the requests
// service's RequesterIdentityResolver (email + username by user id).
type requesterIdentityAdapter struct{ lookup UserIdentityLookup }

func RequesterIdentityFromLookup(l UserIdentityLookup) *requesterIdentityAdapter {
	return &requesterIdentityAdapter{lookup: l}
}

func (a *requesterIdentityAdapter) ResolveRequester(ctx context.Context, userID int) (string, string, error) {
	id, err := a.lookup.LookupIdentity(ctx, userID, "")
	if err != nil {
		return "", "", err
	}
	return id.Email, id.Username, nil
}
```
Build the whole server target (in the libvips container if needed; the requests/plugins packages build bare-host): `cd /opt/silo && PATH=$PATH:/tmp/go/bin go build ./internal/... ./cmd/silo/ 2>&1 | head` — expected: no errors (or only known libvips-CGO skips on bare host; the changed packages compile).

- [ ] **Step 7: Commit.**
```bash
cd /opt/silo
git add internal/plugins/user_identity_lookup.go internal/requests/types.go internal/requests/provider.go internal/requests/service.go cmd/silo/main.go internal/requests/service_test.go
git commit -m "feat(requests): resolve requester email/username into the Fulfill descriptor"
```

---

### Task 3: Seerr plugin — config + Seerr user API

**Files:**
- Modify: `/opt/silo-plugin-requests-seerr/internal/router/config.go` (Connection + parse)
- Modify: `/opt/silo-plugin-requests-seerr/internal/seerr/types.go` (User, CreateRequestBody.UserID, perm consts)
- Create: `/opt/silo-plugin-requests-seerr/internal/seerr/users.go` (FindUserByEmail, CreateUser)
- Test: `/opt/silo-plugin-requests-seerr/internal/seerr/users_test.go`, `internal/router/config_test.go`

- [ ] **Step 1: Pin the Overseerr permission bits + bitfield helper (exported, in `package seerr`).** Before writing constants, confirm the values from Overseerr's `server/lib/permissions.ts` (or read an existing admin user via `GET /api/v1/user` on a live Seerr and inspect `permissions`). Add to `internal/seerr/types.go` — **exported** so `package router` can reference them (the first three are stable; **verify `PermAutoApprove4K`**):
```go
// Overseerr Permission bit values (server/lib/permissions.ts).
const (
	PermManageRequests = 16
	PermRequest        = 32
	PermAutoApprove    = 128
	PermRequest4K      = 1024
	PermAutoApprove4K  = 262144 // VERIFY against Overseerr permissions.ts / a live user
)

// PermissionBits assembles the Overseerr permission bitfield from the connection's
// default-permission toggles.
func PermissionBits(request, request4k, autoApprove, autoApprove4k, manageRequests bool) int {
	bits := 0
	if request {
		bits |= PermRequest
	}
	if request4k {
		bits |= PermRequest4K
	}
	if autoApprove {
		bits |= PermAutoApprove
	}
	if autoApprove4k {
		bits |= PermAutoApprove4K
	}
	if manageRequests {
		bits |= PermManageRequests
	}
	return bits
}
```

- [ ] **Step 2: Add config fields + write the failing config test.** In `internal/router/config.go`, extend `Connection`:
```go
type Connection struct {
	ID         string
	BaseURL    string
	APIKey     string
	Supports4K bool

	Mapped             bool // requester_mode == "mapped"
	RequireMappedUser  bool // mapped + can't map -> fail the request (else admin fallback)
	PermRequest        bool
	PermRequest4K      bool
	PermAutoApprove    bool
	PermAutoApprove4K  bool
	PermManageRequests bool
}
```
and parse in `connectionFromRouter` after the `supports_4k` block:
```go
	if cfg := c.GetConfig(); cfg != nil {
		m := cfg.AsMap()
		if v, ok := m["supports_4k"].(bool); ok {
			conn.Supports4K = v
		}
		if v, ok := m["requester_mode"].(string); ok {
			conn.Mapped = v == "mapped"
		}
		conn.RequireMappedUser, _ = m["require_mapped_user"].(bool)
		conn.PermRequest, _ = m["perm_request"].(bool)
		conn.PermRequest4K, _ = m["perm_request_4k"].(bool)
		conn.PermAutoApprove, _ = m["perm_auto_approve"].(bool)
		conn.PermAutoApprove4K, _ = m["perm_auto_approve_4k"].(bool)
		conn.PermManageRequests, _ = m["perm_manage_requests"].(bool)
	}
```
(Remove the now-duplicated `supports_4k`-only block.) Add `internal/router/config_test.go`:
```go
package router

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestConnectionFromRouterParsesRequesterConfig(t *testing.T) {
	s, _ := structpb.NewStruct(map[string]any{
		"supports_4k": true, "requester_mode": "mapped",
		"perm_request": true, "perm_auto_approve": true,
	})
	conn := connectionFromRouter(&pluginv1.RouterConnection{Id: "c1", BaseUrl: "http://x", ApiKey: "k", Config: s})
	if !conn.Mapped || !conn.Supports4K || !conn.PermRequest || !conn.PermAutoApprove {
		t.Fatalf("parsed wrong: %+v", conn)
	}
	if conn.PermRequest4K || conn.PermManageRequests {
		t.Fatalf("unset perms must be false: %+v", conn)
	}
}
```

- [ ] **Step 3: Verify config test fails, then it passes after Step 2's edit.** `cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin go test ./internal/router/ -run TestConnectionFromRouterParsesRequesterConfig` (fails to compile until the struct fields exist; passes after).

- [ ] **Step 4: Add the `userId` body field + `User` type.** In `internal/seerr/types.go`:
```go
// add to CreateRequestBody (omitempty keeps the admin body unchanged):
	UserID int `json:"userId,omitempty"`
```
```go
// User is a Seerr account (GET/POST /api/v1/user).
type User struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	Permissions int    `json:"permissions"`
}

type userPage struct {
	Results []User `json:"results"`
}
```

- [ ] **Step 5: Write the failing users_test.** Create `internal/seerr/users_test.go`:
```go
package seerr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
)

func TestFindUserByEmailMatchesCaseInsensitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"id":3,"email":"Bob@Example.com","permissions":32}]}`))
	}))
	defer srv.Close()
	u, err := FindUserByEmail(context.Background(), httpclient.New(srv.URL, "k", nil), "bob@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail: %v", err)
	}
	if u == nil || u.ID != 3 {
		t.Fatalf("want user id 3, got %+v", u)
	}
}

func TestFindUserByEmailReturnsNilWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"id":1,"email":"other@x.com"}]}`))
	}))
	defer srv.Close()
	u, err := FindUserByEmail(context.Background(), httpclient.New(srv.URL, "k", nil), "bob@example.com")
	if err != nil || u != nil {
		t.Fatalf("want nil user no error, got %+v / %v", u, err)
	}
}

func TestCreateUserSendsEmailAndPermissions(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":9,"email":"bob@example.com","permissions":160}`))
	}))
	defer srv.Close()
	u, err := CreateUser(context.Background(), httpclient.New(srv.URL, "k", nil), "bob@example.com", 160)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID != 9 {
		t.Fatalf("want id 9, got %+v", u)
	}
	if body["email"] != "bob@example.com" || int(body["permissions"].(float64)) != 160 {
		t.Fatalf("sent body wrong: %+v", body)
	}
}
```

- [ ] **Step 6: Verify failure, then implement `users.go`.** Run the users_test (fails: undefined). Create `internal/seerr/users.go`:
```go
package seerr

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
)

// FindUserByEmail scans Seerr's user list for a case-insensitive email match.
// Returns nil (no error) when no user matches.
func FindUserByEmail(ctx context.Context, c *httpclient.Client, email string) (*User, error) {
	target := strings.ToLower(strings.TrimSpace(email))
	if target == "" {
		return nil, nil
	}
	const take = 100
	for skip := 0; skip < 10000; skip += take {
		var page userPage
		if err := c.GetJSON(ctx, fmt.Sprintf("/api/v1/user?take=%d&skip=%d", take, skip), &page); err != nil {
			return nil, err
		}
		for i := range page.Results {
			if strings.ToLower(strings.TrimSpace(page.Results[i].Email)) == target {
				return &page.Results[i], nil
			}
		}
		if len(page.Results) < take {
			break
		}
	}
	return nil, nil
}

// CreateUser creates a Seerr local user with the given email + permission bitfield.
func CreateUser(ctx context.Context, c *httpclient.Client, email string, permissions int) (*User, error) {
	var out User
	body := map[string]any{"email": email, "permissions": permissions}
	if err := c.PostJSON(ctx, "/api/v1/user", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
```

- [ ] **Step 7: Run + commit.** `cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin go test ./...` (all pass).
```bash
git add internal/router/config.go internal/router/config_test.go internal/seerr/types.go internal/seerr/users.go internal/seerr/users_test.go
git commit -m "feat: seerr user API (find/create by email) + requester-mode config"
```

---

### Task 4: Seerr plugin — Fulfill mapping + manifest

**Files:**
- Modify: `/opt/silo-plugin-requests-seerr/internal/router/server.go` (`Fulfill` + helpers)
- Modify: `/opt/silo-plugin-requests-seerr/manifest.json` (admin_form)
- Test: `/opt/silo-plugin-requests-seerr/internal/router/server_test.go`

- [ ] **Step 1: Write the failing Fulfill tests.** Append to `internal/router/server_test.go` (it already spins up httptest Seerr servers; mirror the existing Fulfill tests). Add three:

```go
func TestFulfillMappedUsesExistingSeerrUser(t *testing.T) {
	var createCalled bool
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[{"id":7,"email":"bob@example.com","permissions":32}]}`))
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodPost:
			createCalled = true
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":99}`))
		case r.URL.Path == "/api/v1/request":
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":55,"status":2,"media":{"status":3,"tmdbId":42}}`))
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "perm_request": true})
	resp, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "bob@example.com"},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}},
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if createCalled {
		t.Fatalf("must reuse existing user, not create")
	}
	if int(reqBody["userId"].(float64)) != 7 {
		t.Fatalf("request userId = %v, want 7", reqBody["userId"])
	}
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetStatus() != "queued" {
		t.Fatalf("targets = %+v", resp.GetTargets())
	}
}

func TestFulfillMappedCreatesMissingUserWithPermissions(t *testing.T) {
	var createBody map[string]any
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[]}`))
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":99,"email":"new@example.com"}`))
		case r.URL.Path == "/api/v1/request":
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":55,"status":1,"media":{"status":2,"tmdbId":42}}`))
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "perm_request": true, "perm_auto_approve": true})
	_, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "new@example.com"},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p"}},
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if createBody["email"] != "new@example.com" {
		t.Fatalf("create email = %v", createBody["email"])
	}
	if got := int(createBody["permissions"].(float64)); got != seerr.PermRequest+seerr.PermAutoApprove {
		t.Fatalf("create permissions = %d, want %d", got, seerr.PermRequest+seerr.PermAutoApprove)
	}
	if int(reqBody["userId"].(float64)) != 99 {
		t.Fatalf("request userId = %v, want 99 (created)", reqBody["userId"])
	}
}

func TestFulfillFallsBackToAdminOnResolveFailure(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusInternalServerError) // user lookup fails
		case r.URL.Path == "/api/v1/request":
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":55,"status":2,"media":{"status":3,"tmdbId":42}}`))
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "perm_request": true})
	_, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "bob@example.com"},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p"}},
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if _, ok := reqBody["userId"]; ok {
		t.Fatalf("admin fallback must omit userId, got %v", reqBody["userId"])
	}
}

func TestFulfillRequireMappedUserFailsWhenUnmapped(t *testing.T) {
	requestCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[]}`)) // no match
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError) // create fails
		case r.URL.Path == "/api/v1/request":
			requestCalled = true
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "require_mapped_user": true, "perm_request": true})
	resp, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "bob@example.com"},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p"}},
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if requestCalled {
		t.Fatalf("require_mapped_user on: must NOT submit the request when the user can't be mapped")
	}
	if resp.GetMessage() == "" || len(resp.GetTargets()) != 0 {
		t.Fatalf("want a request-level failure message and no targets, got msg=%q targets=%d", resp.GetMessage(), len(resp.GetTargets()))
	}
}
```

- [ ] **Step 2: Verify failure.** `cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin go test ./internal/router/ -run TestFulfillMapped` → fails (userId never set; create never happens).

- [ ] **Step 3: Implement the mapping in `server.go`.** Add the resolve helper (it calls the exported `seerr.PermissionBits` from Task 3) and thread `userID` through `fulfillOne`:
```go
// resolveRequesterUserID returns the Seerr user id to attribute the request to.
// mapFailed is true only in mapped mode when no user could be resolved/created
// (missing email or a lookup/create error); the caller decides whether that
// means admin fallback (userID 0) or a hard request failure. Resolved once per
// request against the first connection's Seerr.
func resolveRequesterUserID(ctx context.Context, client *httpclient.Client, conn Connection, email string) (userID int, mapFailed bool) {
	if !conn.Mapped {
		return 0, false // admin mode: not a failure
	}
	if strings.TrimSpace(email) == "" {
		return 0, true
	}
	user, err := seerr.FindUserByEmail(ctx, client, email)
	if err != nil {
		log.Printf("seerr: user lookup failed for %q: %v", email, err)
		return 0, true
	}
	if user != nil {
		return user.ID, false
	}
	created, err := seerr.CreateUser(ctx, client, email,
		seerr.PermissionBits(conn.PermRequest, conn.PermRequest4K, conn.PermAutoApprove, conn.PermAutoApprove4K, conn.PermManageRequests))
	if err != nil {
		log.Printf("seerr: user create failed for %q: %v", email, err)
		return 0, true
	}
	return created.ID, false
}
```
Add imports `strings`, `log`, and `fmt` to `server.go` if not already present (the `seerr` and `httpclient` packages are already imported; `fmt` likely is).

Then change `Fulfill` to resolve once, honor the `require_mapped_user` toggle, and pass `userID` to `fulfillOne`:
```go
	// resolve the per-request Seerr user once (first connection's client)
	userID := 0
	if len(req.GetConnections()) > 0 {
		first := connectionFromRouter(req.GetConnections()[0])
		fc := httpclient.New(first.BaseURL, first.APIKey, nil)
		id, mapFailed := resolveRequesterUserID(ctx, fc, first, d.GetRequesterEmail())
		if mapFailed && first.RequireMappedUser {
			// One request-level failure (like the missing-tmdb path) — do not
			// silently submit as admin when the operator requires a mapped user.
			return &pluginv1.FulfillResponse{Message: fmt.Sprintf("could not map requester %q to a Seerr user", d.GetRequesterEmail())}, nil
		}
		userID = id
	}
	...
	targets = append(targets, s.fulfillOne(ctx, client, conn.ID, q.GetId(), mediaType, tmdbID, is4k, userID))
```
and in `fulfillOne`, add the `userID int` parameter and set it on the body:
```go
	body := seerr.CreateRequestBody{MediaType: mediaType, MediaID: tmdbID, Is4K: is4k}
	if mediaType == "tv" {
		body.Seasons = "all"
	}
	if userID > 0 {
		body.UserID = userID
	}
```

- [ ] **Step 4: Edit `manifest.json` admin_form.** In `capabilities[0].config_schema[0].admin_form.fields`, append after `supports_4k`:
```json
{
  "key": "requester_mode",
  "label": "Attribute requests to",
  "control": "ADMIN_FORM_CONTROL_SELECT",
  "default_value": "admin",
  "description": "Admin user: all requests come from the API-key owner (auto-approved). Map to Silo users: attribute each request to the Seerr user matching the requester's email (created if missing).",
  "options": [
    { "value": "admin", "label": "Admin user (API key owner)" },
    { "value": "mapped", "label": "Map to Silo users" }
  ]
},
{ "key": "perm_request",         "label": "Created users may request",          "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": true,  "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] },
{ "key": "perm_request_4k",      "label": "Created users may request 4K",       "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": false, "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] },
{ "key": "perm_auto_approve",    "label": "Auto-approve created users' requests","control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": true,  "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] },
{ "key": "perm_auto_approve_4k", "label": "Auto-approve their 4K requests",     "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": false, "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] },
{ "key": "perm_manage_requests", "label": "Created users may manage requests",  "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": false, "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] },
{ "key": "require_mapped_user",  "label": "Fail the request if the user can't be mapped", "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": false, "description": "On: a request whose Silo user can't be matched/created on Seerr fails instead of being submitted under the admin.", "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] }
```
Keep the JSON valid (commas between field objects).

- [ ] **Step 5: Run all tests + commit.** `cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin go test ./...` (all pass, incl. the existing admin-mode Fulfill tests — admin mode makes no `/api/v1/user` calls).
```bash
git add internal/router/server.go internal/router/server_test.go internal/seerr/types.go internal/seerr/users.go manifest.json
git commit -m "feat: attribute Seerr requests to the mapped per-user account"
```

---

### Task 5: Build, deploy, reinstall, verify

- [ ] **Step 1: Re-vendor SDK + rebuild/redeploy silo-server.**
```bash
cd /opt/silo && PATH=$PATH:/tmp/go/bin
go mod vendor
grep -q "GetRequesterEmail" vendor/github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1/request_router.pb.go && echo "vendored OK"
(cd web && pnpm build >/dev/null 2>&1)
docker build -f Dockerfile.deploy -t silo-server:main --build-arg BUILD_REVISION="$(git rev-parse --short HEAD)" .
docker compose up -d silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
```

- [ ] **Step 2: Rebuild + reinstall the seerr plugin (installation id 6).**
```bash
cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o plugin .
cd /opt/silo && PATH=$PATH:/tmp/go/bin CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -o /tmp/plugininstall ./cmd/plugininstall
docker cp /tmp/plugininstall silo-silo-1:/tmp/plugininstall
docker cp /opt/silo-plugin-requests-seerr/plugin silo-silo-1:/tmp/seerr-plugin-new
docker exec silo-silo-1 chmod +x /tmp/plugininstall /tmp/seerr-plugin-new
docker exec silo-silo-1 /tmp/plugininstall /tmp/seerr-plugin-new
docker compose restart silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
```
Expected: `installed: id=6 plugin=silo.requests.seerr …`. Verify the new admin_form: 
```bash
docker exec silo-silo-1 sh -c 'cat /var/lib/silo/plugins/silo.requests.seerr/0.1.0/install-*/manifest.json' | python3 -c "import json,sys;f=json.load(sys.stdin)['capabilities'][0]['configSchema'][0]['adminForm']['fields'];print([x['key'] for x in f])"
```
Expected: includes `requester_mode`, the five `perm_*` keys, and `require_mapped_user`.

- [ ] **Step 3: Manual verification.** On a seerr connection set to `mapped` + Auto-Approve on: request as a Silo user whose email matches a Seerr user → Seerr shows that user as requester, approved. Request as a user with no Seerr account → a Seerr local user is created with the configured permissions and the request is attributed to them. Turn Auto-Approve off → a new request sits in Seerr's pending queue under that user.

- [ ] **Step 4: Update deploy-state memory.** Append to `/opt/deployarr/.claude/projects/-opt-silo/memory/requests-pluginization-deploy-state.md`: per-user Seerr requester mapping shipped (SDK `requester_email`/`requester_username`; host resolves via UserIdentityLookup+email; seerr plugin maps/creates by email with permission toggles; admin fallback). Note new HEADs + that seerr was reinstalled.

---

## Notes for the implementer
- Do not edit the spec or this plan.
- Tasks 1→2→3→4 are ordered by the `replace` dependency; the host/plugin see the SDK change immediately via their local replace (re-vendor only needed for the host IMAGE build in Task 5).
- The Overseerr permission bit for 4K auto-approve (`PermAutoApprove4K`) is the one value to verify against Overseerr source / a live `/api/v1/user` before trusting it.
- The permission consts + `PermissionBits(...)` helper are exported in `package seerr`; `package router` (incl. the Task 4 test referencing `seerr.PermRequest`/`seerr.PermAutoApprove`) imports the seerr package — add the import to `server_test.go` if it isn't already there.
