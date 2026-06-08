# Simplify Seerr mapped-user permissions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce the Seerr mapped-user permission config to two toggles (`request_4k_all`, `auto_approve`), always grant 1080p request, and derive per-user 4K eligibility from the request's qualities (host-decided, same as arr).

**Architecture:** Seerr-plugin-only. The created user's Overseerr permission bitfield is computed at create time from `requestHas4K` (any 4K quality in the FulfillRequest) + the two connection toggles. No SDK/host change.

**Tech Stack:** Go; Overseerr REST. Repo: `silo-plugin-requests-seerr` (`/opt/silo-plugin-requests-seerr`, branch `master`). Go: prefix `PATH=$PATH:/tmp/go/bin`. Tests use httptest.

---

### Task 1: Replace the five permission toggles with two + per-request 4K

**Files:**
- Modify: `internal/router/config.go` (`Connection` + parse)
- Modify: `internal/seerr/types.go` (drop `PermManageRequests` + `PermissionBits`)
- Modify: `internal/router/server.go` (`userPermissions` + `Fulfill` `requestHas4K` + `resolveRequesterUserID` signature)
- Modify: `manifest.json` (admin_form + json_schema)
- Test: `internal/router/config_test.go`, `internal/router/server_test.go`

- [ ] **Step 1: Update the tests to the new model (they won't compile yet).**

In `internal/router/config_test.go`, replace `TestConnectionFromRouterParsesRequesterConfig` with:
```go
func TestConnectionFromRouterParsesRequesterConfig(t *testing.T) {
	s, _ := structpb.NewStruct(map[string]any{
		"supports_4k": true, "requester_mode": "mapped", "require_mapped_user": true,
		"request_4k_all": true, "auto_approve": true,
	})
	conn := connectionFromRouter(&pluginv1.RouterConnection{Id: "c1", BaseUrl: "http://x", ApiKey: "k", Config: s})
	if !conn.Mapped || !conn.Supports4K || !conn.RequireMappedUser || !conn.Request4KAll || !conn.AutoApprove {
		t.Fatalf("parsed wrong: %+v", conn)
	}
}
```

In `internal/router/server_test.go`:
- In `TestFulfillMappedUsesExistingSeerrUser`, `TestFulfillFallsBackToAdminOnResolveFailure`, and `TestFulfillRequireMappedUserFailsWhenUnmapped`, change each `structpb.NewStruct(map[string]any{...})` config to drop any `perm_*` keys (they no longer exist). The minimal configs:
  - existing-user test: `map[string]any{"requester_mode": "mapped"}`
  - fallback test: `map[string]any{"requester_mode": "mapped"}`
  - require test: `map[string]any{"requester_mode": "mapped", "require_mapped_user": true}`
- Replace `TestFulfillMappedCreatesMissingUserWithPermissions` with the new permission model, and add three permission tests:
```go
func TestFulfillMappedCreatesMissingUserWithPermissions(t *testing.T) {
	var createBody map[string]any
	srv := newUserCreateStub(t, &createBody, `{"id":99,"email":"new@example.com"}`)
	defer srv.Close()

	// mapped + auto_approve, 1080p-only request -> REQUEST | AUTO_APPROVE
	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "auto_approve": true})
	mustFulfill(t, srv.URL, cfg, []*pluginv1.RequestedQuality{{Id: "1080p"}})

	if createBody["email"] != "new@example.com" {
		t.Fatalf("create email = %v", createBody["email"])
	}
	if got := int(createBody["permissions"].(float64)); got != seerr.PermRequest|seerr.PermAutoApprove {
		t.Fatalf("permissions = %d, want %d (REQUEST|AUTO_APPROVE)", got, seerr.PermRequest|seerr.PermAutoApprove)
	}
}

func TestFulfillMappedGrants4KWhenRequestHas4K(t *testing.T) {
	var createBody map[string]any
	srv := newUserCreateStub(t, &createBody, `{"id":99}`)
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "auto_approve": true, "supports_4k": true})
	mustFulfill(t, srv.URL, cfg, []*pluginv1.RequestedQuality{{Id: "1080p"}, {Id: "2160p", Is4K: true}})

	want := seerr.PermRequest | seerr.PermRequest4K | seerr.PermAutoApprove | seerr.PermAutoApprove4K
	if got := int(createBody["permissions"].(float64)); got != want {
		t.Fatalf("permissions = %d, want %d (incl 4K)", got, want)
	}
}

func TestFulfillMappedRequest4KAllGrantsWithoutQuality(t *testing.T) {
	var createBody map[string]any
	srv := newUserCreateStub(t, &createBody, `{"id":99}`)
	defer srv.Close()

	// request_4k_all on, no auto_approve, 1080p-only -> REQUEST | REQUEST_4K (no auto)
	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "request_4k_all": true})
	mustFulfill(t, srv.URL, cfg, []*pluginv1.RequestedQuality{{Id: "1080p"}})

	want := seerr.PermRequest | seerr.PermRequest4K
	if got := int(createBody["permissions"].(float64)); got != want {
		t.Fatalf("permissions = %d, want %d (REQUEST|REQUEST_4K, no auto)", got, want)
	}
}

func TestFulfillMappedAutoApproveOffGrantsRequestOnly(t *testing.T) {
	var createBody map[string]any
	srv := newUserCreateStub(t, &createBody, `{"id":99}`)
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped"})
	mustFulfill(t, srv.URL, cfg, []*pluginv1.RequestedQuality{{Id: "1080p"}})

	if got := int(createBody["permissions"].(float64)); got != seerr.PermRequest {
		t.Fatalf("permissions = %d, want %d (REQUEST only)", got, seerr.PermRequest)
	}
}
```
Add these two test helpers to `server_test.go` (a stub that returns no existing user, captures the create body, and answers /api/v1/request; and a Fulfill caller):
```go
func newUserCreateStub(t *testing.T, createBody *map[string]any, createResp string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[]}`)) // no existing user -> create
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(createBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(createResp))
		case r.URL.Path == "/api/v1/request":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":55,"status":2,"media":{"status":3,"tmdbId":42}}`))
		}
	}))
}

func mustFulfill(t *testing.T, baseURL string, cfg *structpb.Struct, qs []*pluginv1.RequestedQuality) {
	t.Helper()
	if _, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "new@example.com"},
		Qualities:   qs,
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: baseURL, ApiKey: "k", Config: cfg}},
	}); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
}
```

- [ ] **Step 2: Run to confirm it doesn't compile / fails.** `cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin go test ./internal/router/ 2>&1 | head` → COMPILE FAILURE (`conn.Request4KAll`/`conn.AutoApprove` undefined; `seerr.PermissionBits` still referenced in server.go). You fix in Steps 3–5.

- [ ] **Step 3: Update the config struct + parse (`internal/router/config.go`).** Replace the `Connection` struct's permission fields and the parse block:
```go
type Connection struct {
	ID         string
	BaseURL    string
	APIKey     string
	Supports4K bool

	Mapped            bool // requester_mode == "mapped"
	RequireMappedUser bool // mapped + can't map -> fail the request (else admin fallback)
	Request4KAll      bool // grant REQUEST_4K to every mapped user (override)
	AutoApprove       bool // grant AUTO_APPROVE(+4K) to mapped users
}
```
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
		conn.Request4KAll, _ = m["request_4k_all"].(bool)
		conn.AutoApprove, _ = m["auto_approve"].(bool)
	}
```

- [ ] **Step 4: Drop the dead permission helper (`internal/seerr/types.go`).** Remove the `PermManageRequests = 16` const line and the entire `PermissionBits(...)` function. Keep `PermRequest = 32`, `PermRequest4K = 1024`, `PermAutoApprove = 128`, `PermAutoApprove4K = 32768`.

- [ ] **Step 5: Compute permissions in `internal/router/server.go`.** Add the helper:
```go
// userPermissions builds a created Seerr user's Overseerr permission bitfield:
// everyone may request 1080p; REQUEST_4K when the request carries a 4K quality
// (host-decided per the requester's max quality) or the connection forces it for
// all users; AUTO_APPROVE(+4K) when the connection auto-approves.
func userPermissions(conn Connection, requestHas4K bool) int {
	allow4K := requestHas4K || conn.Request4KAll
	bits := seerr.PermRequest
	if allow4K {
		bits |= seerr.PermRequest4K
	}
	if conn.AutoApprove {
		bits |= seerr.PermAutoApprove
		if allow4K {
			bits |= seerr.PermAutoApprove4K
		}
	}
	return bits
}
```
Change `resolveRequesterUserID` to take `requestHas4K` and use the new helper:
```go
func resolveRequesterUserID(ctx context.Context, client *httpclient.Client, conn Connection, email string, requestHas4K bool) (userID int, mapFailed bool) {
	if !conn.Mapped {
		return 0, false
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
	created, err := seerr.CreateUser(ctx, client, email, userPermissions(conn, requestHas4K))
	if err != nil {
		log.Printf("seerr: user create failed for %q: %v", email, err)
		return 0, true
	}
	return created.ID, false
}
```
In `Fulfill`, compute `requestHas4K` and pass it into the resolve call. Replace the resolve block:
```go
	requestHas4K := false
	for _, q := range req.GetQualities() {
		if q.GetIs4K() {
			requestHas4K = true
			break
		}
	}

	userID := 0
	if len(req.GetConnections()) > 0 {
		first := connectionFromRouter(req.GetConnections()[0])
		fc := httpclient.New(first.BaseURL, first.APIKey, nil)
		id, mapFailed := resolveRequesterUserID(ctx, fc, first, d.GetRequesterEmail(), requestHas4K)
		if mapFailed && first.RequireMappedUser {
			return &pluginv1.FulfillResponse{Message: fmt.Sprintf("could not map requester %q to a Seerr user", d.GetRequesterEmail())}, nil
		}
		userID = id
	}
```

- [ ] **Step 6: Run tests to verify they pass.** `cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin go test ./...` → all pass. (The manifest is untouched so far — its old `perm_*` keys are still present in BOTH `admin_form` and `json_schema`, so it stays internally valid and `TestEmbeddedManifestLoads` passes. Step 7 updates the manifest so the UI actually shows the new toggles.)

- [ ] **Step 7: Update `manifest.json`.** In `capabilities[0].config_schema[0].admin_form.fields`, REMOVE the five `perm_*` field objects and ADD two switches (keep `supports_4k`, `requester_mode`, and `require_mapped_user`). The mapped-gated fields become exactly:
```json
{ "key": "request_4k_all",  "label": "Allow all users to request 4K", "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": false, "description": "Grant 4K request permission to every mapped user. Off: only users whose Silo max quality is 4K/Any can request 4K (the host decides which qualities are sent).", "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] },
{ "key": "auto_approve",    "label": "Auto-approve requests", "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": true, "description": "Mapped users' requests are auto-approved. Off: they land in Seerr's per-user approval queue.", "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] },
{ "key": "require_mapped_user", "label": "Fail the request if the user can't be mapped", "control": "ADMIN_FORM_CONTROL_SWITCH", "default_value": false, "description": "On: a request whose Silo user can't be matched/created on Seerr fails instead of being submitted under the admin.", "show_when": [{ "field": "requester_mode", "equals": ["mapped"] }] }
```
In the SAME config_schema entry's `json_schema` string, update the `properties` so it lists exactly: `supports_4k` (boolean), `requester_mode` (string), `request_4k_all` (boolean), `auto_approve` (boolean), `require_mapped_user` (boolean) — REMOVE the five `perm_*` properties. (`LoadWithChecksum` rejects the manifest if an admin_form key is absent from the schema, and `TestAdminFormLayout`/`TestEmbeddedManifestLoads` enforce it.) Validate: `python3 -m json.tool manifest.json >/dev/null`.

- [ ] **Step 8: Run everything + commit.** `cd /opt/silo-plugin-requests-seerr && PATH=$PATH:/tmp/go/bin go test ./...` → all packages pass.
```bash
git add internal/router/config.go internal/router/config_test.go internal/seerr/types.go internal/router/server.go internal/router/server_test.go manifest.json
git commit -m "feat: simplify Seerr mapped-user permissions to 4K + auto-approve toggles"
```

---

### Task 2: Rebuild + reinstall + verify

- [ ] **Step 1: Rebuild the seerr plugin + reinstall (installation id 6).**
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
Expected: `installed: id=6 plugin=silo.requests.seerr …`; healthy. (No host redeploy — host/SDK unchanged.)

- [ ] **Step 2: Verify the admin_form.**
```bash
docker exec silo-silo-1 sh -c 'cat /var/lib/silo/plugins/silo.requests.seerr/0.1.0/install-*/manifest.json' | python3 -c "import json,sys;print([f['key'] for f in json.load(sys.stdin)['capabilities'][0]['configSchema'][0]['adminForm']['fields']])"
```
Expected: `['supports_4k', 'requester_mode', 'request_4k_all', 'auto_approve', 'require_mapped_user']` — no `perm_*`.

- [ ] **Step 3: Manual verification.** On a mapped Seerr connection: a 1080p-capped Silo user → created Seerr user can request (1080p) and (if Auto-approve on) auto-approves, no 4K. A 4K/Any Silo user → their created Seerr user also has 4K request permission. Flip "Allow all users to request 4K" → new mapped users get 4K permission regardless.

- [ ] **Step 4: Update deploy-state memory.** Append to `/opt/deployarr/.claude/projects/-opt-silo/memory/requests-pluginization-deploy-state.md`: Seerr permission model simplified to `request_4k_all` + `auto_approve` (1080p always; 4K per-user from request qualities; manage_requests removed); seerr reinstalled (inst 6).

---

## Notes for the implementer
- Do not edit the spec or this plan.
- Seerr-plugin-only — no SDK/host/frontend change, no host redeploy.
- `requestHas4K` is read from `req.GetQualities()` (any `Is4K`), independent of the connection's `supports_4k` (a 4K target is still skipped per-connection when `!Supports4K`, but the user's permission reflects what was requested).
- After committing, the `seerr.PermManageRequests` const and `seerr.PermissionBits` must be gone (grep to confirm nothing references them).
