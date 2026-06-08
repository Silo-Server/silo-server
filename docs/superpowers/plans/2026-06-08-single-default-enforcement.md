# Single-default exclusivity enforcement — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce "at most one default per service type, per quality tier" for request-router connections, the schema-driven (plugin-agnostic) way.

**Architecture:** A new `AdminFormField.exclusive_group_field` declares the rule. The plugin's `Validate` enforces it against host-supplied `ValidateRequest.siblings` (config only, no credentials); the admin UI auto-clears conflicts as the operator toggles. The host stays agnostic — it only supplies siblings.

**Tech Stack:** protobuf via `buf` (SDK), Go (SDK/plugin/host), React+TypeScript+vitest (host frontend).

**Repos & order:** Task 1 = `silo-plugin-sdk` (`/opt/silo-plugin-sdk`, branch `feat/request-router-capability`); Task 2 = `silo-plugin-requests-arr` (`/opt/silo-plugin-requests-arr`, `master`); Tasks 3–4 = `silo-server` (`/opt/silo`, `feat/requests-pluginization`); Task 5 = deploy. Both plugin repos consume the SDK via a local `replace`, so Task 1 must land before 2/3. Commands assume each repo root is the cwd. Go: prefix with `PATH=$PATH:/tmp/go/bin`. Frontend: `/opt/silo/web/node_modules/.bin/`.

---

### Task 1: SDK — add `siblings` + `exclusive_group_field` and regenerate

**Files:**
- Modify: `/opt/silo-plugin-sdk/proto/silo/plugin/v1/request_router.proto`
- Modify: `/opt/silo-plugin-sdk/proto/silo/plugin/v1/common.proto`
- Regenerate: `pkg/pluginproto/silo/plugin/v1/request_router.pb.go`, `common.pb.go`

- [ ] **Step 1: Edit `request_router.proto` — add `siblings` to `ValidateRequest`**

Change the `ValidateRequest` message to:

```proto
message ValidateRequest {
  string capability_id = 1;
  RouterConnection connection = 2;
  // siblings are the other connections bound to this installation+capability, so
  // a plugin can enforce cross-connection rules (e.g. one default per service
  // type). They carry id + config only — no base_url/api_key (validation reads
  // flags, not credentials).
  repeated RouterConnection siblings = 3;
}
```

- [ ] **Step 2: Edit `common.proto` — add `exclusive_group_field` to `AdminFormField`**

Add field 15 after `validation = 14;`:

```proto
  AdminFormValidation validation = 14;        // per-field constraints
  // exclusive_group_field names another config field whose value defines a
  // group; at most one connection per distinct value of that field may have THIS
  // field truthy. Empty = no exclusivity.
  string exclusive_group_field = 15;
```

- [ ] **Step 3: Regenerate Go (buf compiles protos itself; protoc is NOT needed)**

Run:
```bash
cd /opt/silo-plugin-sdk && PATH="$PWD/bin:/tmp/go/bin:$PATH" buf generate
```
Expected: regenerates `pkg/pluginproto/...`. Review `git diff --stat` — only `request_router.pb.go` and `common.pb.go` should change meaningfully.

- [ ] **Step 4: Verify the new getters exist and the module builds**

Run:
```bash
cd /opt/silo-plugin-sdk && PATH=$PATH:/tmp/go/bin go build ./... \
  && grep -q "func (x \*ValidateRequest) GetSiblings()" pkg/pluginproto/silo/plugin/v1/request_router.pb.go \
  && grep -q "func (x \*AdminFormField) GetExclusiveGroupField()" pkg/pluginproto/silo/plugin/v1/common.pb.go \
  && echo OK
```
Expected: `OK`.

- [ ] **Step 5: Commit (SDK repo)**

```bash
cd /opt/silo-plugin-sdk
git add proto/ pkg/pluginproto/
git commit -m "feat(request_router): ValidateRequest.siblings + AdminFormField.exclusive_group_field"
```

---

### Task 2: arr plugin — `Validate` enforces one default per `service_kind`

**Files:**
- Modify: `/opt/silo-plugin-requests-arr/internal/router/server.go` (`Validate`)
- Modify: `/opt/silo-plugin-requests-arr/manifest.json` (`is_default`, `is_default_4k` field defs)
- Test: `/opt/silo-plugin-requests-arr/internal/router/server_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `server_test.go` (it imports `structpb` and `pluginv1`). These build a connection + siblings via `structpb.NewStruct`:

```go
func sibConn(id string, cfg map[string]any) *pluginv1.RouterConnection {
	s, _ := structpb.NewStruct(cfg)
	return &pluginv1.RouterConnection{Id: id, Config: s}
}

func TestValidateRejectsSecondHDDefaultSameKind(t *testing.T) {
	resp, err := (&Server{}).Validate(context.Background(), &pluginv1.ValidateRequest{
		CapabilityId: "arr",
		Connection:   sibConn("c2", map[string]any{"service_kind": "radarr", "is_default": true}),
		Siblings:     []*pluginv1.RouterConnection{sibConn("c1", map[string]any{"service_kind": "radarr", "is_default": true})},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resp.GetFieldErrors()["is_default"] == "" {
		t.Fatalf("expected is_default conflict error, got %+v", resp.GetFieldErrors())
	}
}

func TestValidateAllowsHDDefaultDifferentKind(t *testing.T) {
	resp, _ := (&Server{}).Validate(context.Background(), &pluginv1.ValidateRequest{
		CapabilityId: "arr",
		Connection:   sibConn("c2", map[string]any{"service_kind": "radarr", "is_default": true}),
		Siblings:     []*pluginv1.RouterConnection{sibConn("c1", map[string]any{"service_kind": "sonarr", "is_default": true})},
	})
	if resp.GetFieldErrors()["is_default"] != "" {
		t.Fatalf("different kind must not conflict, got %+v", resp.GetFieldErrors())
	}
}

func TestValidateRejectsSecond4KDefaultSameKind(t *testing.T) {
	resp, _ := (&Server{}).Validate(context.Background(), &pluginv1.ValidateRequest{
		CapabilityId: "arr",
		Connection:   sibConn("c2", map[string]any{"service_kind": "radarr", "is_4k": true, "is_default_4k": true}),
		Siblings:     []*pluginv1.RouterConnection{sibConn("c1", map[string]any{"service_kind": "radarr", "is_4k": true, "is_default_4k": true})},
	})
	if resp.GetFieldErrors()["is_default_4k"] == "" {
		t.Fatalf("expected is_default_4k conflict, got %+v", resp.GetFieldErrors())
	}
}

func TestValidateToleratesSiblingWithoutConfig(t *testing.T) {
	resp, err := (&Server{}).Validate(context.Background(), &pluginv1.ValidateRequest{
		CapabilityId: "arr",
		Connection:   sibConn("c2", map[string]any{"service_kind": "radarr", "is_default": true}),
		Siblings:     []*pluginv1.RouterConnection{{Id: "c1"}}, // nil config
	})
	if err != nil {
		t.Fatalf("Validate must not error on a config-less sibling: %v", err)
	}
	if resp.GetFieldErrors()["is_default"] != "" {
		t.Fatalf("a config-less sibling must not conflict, got %+v", resp.GetFieldErrors())
	}
}
```

If `context` is not yet imported in `server_test.go`, add it to the import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /opt/silo-plugin-requests-arr && PATH=$PATH:/tmp/go/bin go test ./internal/router/ -run TestValidate -v`
Expected: the four new tests FAIL (no sibling logic yet; conflicts not detected). The existing `TestValidateRejectsHDDefaultThatIsAlso4K` / `...4KDefaultOnNon4K` still PASS.

- [ ] **Step 3: Add cross-sibling checks to `Validate`**

In `server.go`, replace the body of `Validate` with (keeps the existing single-connection checks, adds the sibling loop):

```go
func (s *Server) Validate(ctx context.Context, req *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error) {
	in := instanceFromConnection(req.GetConnection())
	fieldErrors := map[string]string{}
	if in.IsDefault && in.Is4K {
		fieldErrors["is_default"] = "the HD default cannot be a 4K server"
	}
	if in.IsDefault4K && !in.Is4K {
		fieldErrors["is_default_4k"] = "the 4K default must be a 4K server"
	}
	// One default per service_kind, per tier. Compare only against siblings of
	// the same kind; a config-less or different-kind sibling never conflicts.
	for _, sib := range req.GetSiblings() {
		if sib.GetId() == req.GetConnection().GetId() {
			continue
		}
		other := instanceFromConnection(sib)
		if other.Kind != in.Kind {
			continue
		}
		if in.IsDefault && other.IsDefault {
			fieldErrors["is_default"] = fmt.Sprintf("%s already has an HD default; unset it on the other connection first", in.Kind)
		}
		if in.IsDefault4K && other.IsDefault4K {
			fieldErrors["is_default_4k"] = fmt.Sprintf("%s already has a 4K default; unset it on the other connection first", in.Kind)
		}
	}
	return &pluginv1.ValidateResponse{FieldErrors: fieldErrors}, nil
}
```

Ensure `fmt` is in `server.go`'s import block (add it if missing).

- [ ] **Step 4: Add `exclusive_group_field` to the manifest**

In `manifest.json`, in `capabilities[0].config_schema[0].admin_form.fields`, add `"exclusive_group_field": "service_kind"` to the `is_default` field object and the `is_default_4k` field object (leave all other fields unchanged).

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /opt/silo-plugin-requests-arr && PATH=$PATH:/tmp/go/bin go test ./...`
Expected: all PASS (router tests incl. the four new ones, and `TestAdminFormLayout`/`TestEmbeddedManifestLoads`).

- [ ] **Step 6: Commit (arr repo)**

```bash
cd /opt/silo-plugin-requests-arr
git add internal/router/server.go internal/router/server_test.go manifest.json
git commit -m "feat: enforce one default per service_kind in Validate (siblings)"
```

---

### Task 3: Host — gather siblings and thread them through `Validate`

**Files:**
- Modify: `/opt/silo/internal/requests/provider.go` (`RequestRouterProvider` interface + `pluginRouterProvider.Validate`)
- Modify: `/opt/silo/internal/requests/service.go` (`validateViaPlugin` + new `siblingConnections` helper)
- Test: `/opt/silo/internal/requests/service_test.go` (`fakeRouterProvider.Validate` + new test)

- [ ] **Step 1: Write the failing test**

In `service_test.go`, add a `gotValidateSiblings` field to `fakeRouterProvider` (next to `gotValidateCapability`):

```go
	gotValidateSiblings   []ResolvedRouterConnection
```

Append this test:

```go
func TestUpdateIntegrationPassesSiblingsToValidate(t *testing.T) {
	store := newFakeStore()
	inst := 1
	a := routerInstOn("conn-a", inst)
	b := routerInstOn("conn-b", inst)
	b.PluginConfig = map[string]any{"service_kind": "radarr"}
	store.integrations = []Integration{a, b}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	if _, err := service.UpdateIntegration(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:             "conn-a",
		Name:           "conn-a",
		CapabilityID:   "arr",
		BaseURL:        "http://conn-a.local",
		APIKeyRef:      "key-conn-a",
		InstallationID: &inst,
	}); err != nil {
		t.Fatalf("UpdateIntegration: %v", err)
	}
	if len(router.gotValidateSiblings) != 1 {
		t.Fatalf("siblings = %d, want 1 (the other installation-1 connection)", len(router.gotValidateSiblings))
	}
	sib := router.gotValidateSiblings[0]
	if sib.ID != "conn-b" {
		t.Fatalf("sibling id = %q, want conn-b (self excluded)", sib.ID)
	}
	if sib.APIKey != "" || sib.BaseURL != "" {
		t.Fatalf("sibling must carry no credentials, got APIKey=%q BaseURL=%q", sib.APIKey, sib.BaseURL)
	}
	if sib.Config["service_kind"] != "radarr" {
		t.Fatalf("sibling config not passed: %+v", sib.Config)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails to compile / fail**

Run: `cd /opt/silo && PATH=$PATH:/tmp/go/bin go test ./internal/requests/ -run TestUpdateIntegrationPassesSiblings`
Expected: COMPILE FAILURE — `fakeRouterProvider.Validate` doesn't take a siblings arg / `gotValidateSiblings` unused, and the provider interface lacks the param. (You'll fix the signature in Step 3–4.)

- [ ] **Step 3: Update the provider interface + impl (`provider.go`)**

Change the `RequestRouterProvider` interface `Validate` signature (the one with `conn ResolvedRouterConnection`) to add `siblings`:

```go
	Validate(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection, siblings []ResolvedRouterConnection) (fieldErrors map[string]string, formError string, err error)
```

Update `pluginRouterProvider.Validate` to encode and pass siblings:

```go
func (p *pluginRouterProvider) Validate(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection, siblings []ResolvedRouterConnection) (map[string]string, string, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	pc, err := routerProtoConn(conn)
	if err != nil {
		return nil, "", err
	}
	sibs, err := routerProtoConns(siblings)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Validate(ctx, &pluginv1.ValidateRequest{CapabilityId: capabilityID, Connection: pc, Siblings: sibs})
	if err != nil {
		return nil, "", err
	}
	return resp.GetFieldErrors(), resp.GetFormError(), nil
}
```

(`routerProtoConns` already exists and encodes each connection's `Config`; siblings built with empty `BaseURL`/`APIKey` serialize as empty strings.)

- [ ] **Step 4: Gather siblings in `validateViaPlugin` (`service.go`) + update the fake**

In `service.go`, add the helper:

```go
// siblingConnections returns the other connections bound to the same plugin
// installation as `in` (self excluded), carrying only id + config so a plugin
// can enforce cross-connection rules without the host resolving sibling
// credentials.
func (s *Service) siblingConnections(ctx context.Context, in Integration) ([]ResolvedRouterConnection, error) {
	if in.InstallationID == nil {
		return nil, nil
	}
	all, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	var out []ResolvedRouterConnection
	for _, other := range all {
		if other.ID == in.ID || other.InstallationID == nil || *other.InstallationID != *in.InstallationID {
			continue
		}
		out = append(out, ResolvedRouterConnection{ID: other.ID, Config: other.PluginConfig})
	}
	return out, nil
}
```

In `validateViaPlugin`, change the call site (the line `fe, form, err := s.router.Validate(ctx, *in.InstallationID, in.CapabilityID, conn)`) to gather and pass siblings:

```go
	conn := ResolvedRouterConnection{ID: in.ID, BaseURL: in.BaseURL, APIKey: apiKey, Config: in.PluginConfig}
	siblings, err := s.siblingConnections(ctx, in)
	if err != nil {
		return err
	}
	fe, form, err := s.router.Validate(ctx, *in.InstallationID, in.CapabilityID, conn, siblings)
```

In `service_test.go`, update `fakeRouterProvider.Validate` to the new signature and record siblings:

```go
func (f *fakeRouterProvider) Validate(_ context.Context, _ int, capabilityID string, _ ResolvedRouterConnection, siblings []ResolvedRouterConnection) (map[string]string, string, error) {
	f.mu.Lock()
	f.validateCalls++
	f.gotValidateCapability = capabilityID
	f.gotValidateSiblings = siblings
	f.mu.Unlock()
	return f.validateFieldErrors, f.validateFormError, f.validateErr
}
```

- [ ] **Step 5: Run the full requests package tests**

Run: `cd /opt/silo && PATH=$PATH:/tmp/go/bin go test ./internal/requests/`
Expected: PASS (the new sibling test + all existing tests). Then `PATH=$PATH:/tmp/go/bin go vet ./internal/requests/`.

- [ ] **Step 6: Commit (silo repo)**

```bash
cd /opt/silo
git add internal/requests/provider.go internal/requests/service.go internal/requests/service_test.go
git commit -m "feat(requests): pass sibling connections to plugin Validate for cross-connection rules"
```

---

### Task 4: Host frontend — generic mutual-exclusion

**Files:**
- Create: `/opt/silo/web/src/pages/requestExclusivity.ts`
- Test: `/opt/silo/web/src/pages/requestExclusivity.test.ts`
- Modify: `/opt/silo/web/src/api/types.ts` (`PluginAdminFormField`)
- Modify: `/opt/silo/web/src/pages/AdminRequests.tsx` (`updateCardConfig`)

- [ ] **Step 1: Add the type field**

In `web/src/api/types.ts`, add to `interface PluginAdminFormField` (after `validation?: PluginAdminFormValidation;`):

```ts
  exclusive_group_field?: string;
```

- [ ] **Step 2: Write the failing test for the pure helper**

Create `web/src/pages/requestExclusivity.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import type { PluginAdminFormField } from "@/api/types";
import { applyExclusivity, type ExclusivityCard } from "./requestExclusivity";

const isDefault: PluginAdminFormField = {
  key: "is_default", label: "HD default", control: "SWITCH",
  required: false, secret: false, multiline: false, exclusive_group_field: "service_kind",
};
const fieldsFor = () => [isDefault];

describe("applyExclusivity", () => {
  it("clears the default on a same-group sibling when a card turns it on", () => {
    const cards: ExclusivityCard[] = [
      { key: "a", installationId: "5", config: { service_kind: "radarr", is_default: true } },
      { key: "b", installationId: "5", config: { service_kind: "radarr", is_default: false } },
    ];
    const out = applyExclusivity(cards, "b", { service_kind: "radarr", is_default: true }, fieldsFor);
    expect(out.find((c) => c.key === "a")!.config.is_default).toBe(false);
    expect(out.find((c) => c.key === "b")!.config.is_default).toBe(true);
  });

  it("leaves a different-group sibling untouched", () => {
    const cards: ExclusivityCard[] = [
      { key: "a", installationId: "5", config: { service_kind: "sonarr", is_default: true } },
      { key: "b", installationId: "5", config: { service_kind: "radarr", is_default: false } },
    ];
    const out = applyExclusivity(cards, "b", { service_kind: "radarr", is_default: true }, fieldsFor);
    expect(out.find((c) => c.key === "a")!.config.is_default).toBe(true);
  });

  it("no-ops when the changed field is turned off", () => {
    const cards: ExclusivityCard[] = [
      { key: "a", installationId: "5", config: { service_kind: "radarr", is_default: true } },
      { key: "b", installationId: "5", config: { service_kind: "radarr", is_default: false } },
    ];
    const out = applyExclusivity(cards, "b", { service_kind: "radarr", is_default: false }, fieldsFor);
    expect(out.find((c) => c.key === "a")!.config.is_default).toBe(true);
  });
});
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd /opt/silo/web && node_modules/.bin/vitest run src/pages/requestExclusivity.test.ts`
Expected: FAIL (module `./requestExclusivity` does not exist).

- [ ] **Step 4: Create the pure helper**

Create `web/src/pages/requestExclusivity.ts`:

```ts
import type { PluginAdminFormField } from "@/api/types";

export type ExclusivityCard = {
  key: string;
  installationId: string;
  config: Record<string, unknown>;
};

function isTruthy(value: unknown): boolean {
  return value === true || value === "true";
}

// applyExclusivity clears a mutually-exclusive field on sibling cards when the
// changed card turns it on. A field with exclusive_group_field=G permits at most
// one card per distinct value of config[G] (within the same installation) to
// hold that field truthy. Generic — no plugin-specific keys.
export function applyExclusivity(
  cards: ExclusivityCard[],
  changedKey: string,
  nextConfig: Record<string, unknown>,
  fieldsFor: (installationId: string) => PluginAdminFormField[],
): ExclusivityCard[] {
  const changed = cards.find((card) => card.key === changedKey);
  const withChange = cards.map((card) =>
    card.key === changedKey ? { ...card, config: nextConfig } : card,
  );
  if (!changed) return withChange;

  const exclusive = fieldsFor(changed.installationId).filter(
    (field) => field.exclusive_group_field && isTruthy(nextConfig[field.key]),
  );
  if (exclusive.length === 0) return withChange;

  return withChange.map((card) => {
    if (card.key === changedKey || card.installationId !== changed.installationId) {
      return card;
    }
    let config = card.config;
    let mutated = false;
    for (const field of exclusive) {
      const group = field.exclusive_group_field as string;
      if (isTruthy(card.config[field.key]) && card.config[group] === nextConfig[group]) {
        if (!mutated) {
          config = { ...config };
          mutated = true;
        }
        config[field.key] = false;
      }
    }
    return mutated ? { ...card, config } : card;
  });
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd /opt/silo/web && node_modules/.bin/vitest run src/pages/requestExclusivity.test.ts`
Expected: all 3 PASS.

- [ ] **Step 6: Wire it into `AdminRequests.tsx`**

Add the import near the other local imports:

```ts
import { applyExclusivity } from "./requestExclusivity";
```

Replace the `updateCardConfig` function inside `RequestIntegrationsForm` with:

```ts
  function updateCardConfig(key: string, pluginConfig: Record<string, unknown>) {
    setCards((current) => {
      const mapped = current.map((card) => ({
        key: card.key,
        installationId: card.form.installation_id,
        config: card.key === key ? pluginConfig : card.pluginConfig,
      }));
      const fieldsFor = (installationId: string) =>
        routerInstallations.find((entry) => String(entry.installationID) === installationId)
          ?.capability.config_schema?.[0]?.admin_form?.fields ?? [];
      const next = applyExclusivity(mapped, key, pluginConfig, fieldsFor);
      const byKey = new Map(next.map((entry) => [entry.key, entry.config]));
      return current.map((card) => ({ ...card, pluginConfig: byKey.get(card.key) ?? card.pluginConfig }));
    });
  }
```

- [ ] **Step 7: Typecheck, lint, format, full vitest, commit**

```bash
cd /opt/silo/web
node_modules/.bin/tsc -b && node_modules/.bin/eslint src/pages/requestExclusivity.ts src/pages/requestExclusivity.test.ts src/pages/AdminRequests.tsx src/api/types.ts
node_modules/.bin/prettier --write src/pages/requestExclusivity.ts src/pages/requestExclusivity.test.ts src/pages/AdminRequests.tsx src/api/types.ts
node_modules/.bin/vitest run src/pages/requestExclusivity.test.ts
cd /opt/silo && git add web/src/pages/requestExclusivity.ts web/src/pages/requestExclusivity.test.ts web/src/pages/AdminRequests.tsx web/src/api/types.ts
git commit -m "feat(web): auto-clear mutually-exclusive defaults across request connection cards"
```

---

### Task 5: Build, deploy, reinstall, verify

No new tests — ships Tasks 1–4 to the live box.

- [ ] **Step 1: Re-vendor the SDK into the host (so the new proto reaches the vendored image build)**

```bash
cd /opt/silo && PATH=$PATH:/tmp/go/bin go mod vendor
grep -q "GetSiblings" vendor/github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1/request_router.pb.go && echo "vendored SDK has siblings"
```
Expected: `vendored SDK has siblings`.

- [ ] **Step 2: Rebuild the silo-server frontend + image + redeploy**

```bash
cd /opt/silo/web && PATH=$PATH:/tmp/go/bin pnpm build
cd /opt/silo && PATH=$PATH:/tmp/go/bin
docker build -f Dockerfile.deploy -t silo-server:main --build-arg BUILD_REVISION="$(git rev-parse --short HEAD)" .
docker compose up -d silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
docker exec silo-silo-1 sh -c 'curl -s http://127.0.0.1:8080/' | grep -oE 'assets/index-[A-Za-z0-9_-]+\.js'
```
Expected: container healthy; served bundle hash matches the new `web/dist/index.html`.

- [ ] **Step 3: Rebuild the arr plugin + reinstall on the box**

```bash
cd /opt/silo-plugin-requests-arr && PATH=$PATH:/tmp/go/bin CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o plugin .
cd /opt/silo && PATH=$PATH:/tmp/go/bin CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -o /tmp/plugininstall ./cmd/plugininstall
docker cp /tmp/plugininstall silo-silo-1:/tmp/plugininstall
docker cp /opt/silo-plugin-requests-arr/plugin silo-silo-1:/tmp/arr-plugin-new
docker exec silo-silo-1 chmod +x /tmp/plugininstall /tmp/arr-plugin-new
docker exec silo-silo-1 /tmp/plugininstall /tmp/arr-plugin-new
docker compose up -d silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
```
Expected: `installed: id=5 plugin=silo.requests.arr version=0.1.0 enabled=true`; container healthy.

- [ ] **Step 4: Verify the manifest declares the exclusivity**

```bash
docker exec silo-silo-1 sh -c 'cat /var/lib/silo/plugins/silo.requests.arr/0.1.0/install-*/manifest.json' | \
  python3 -c "import json,sys; m=json.load(sys.stdin); fs=m['capabilities'][0]['configSchema'][0]['adminForm']['fields']; print({f['key']: f.get('exclusiveGroupField') for f in fs if f['key'] in ('is_default','is_default_4k')})"
```
Expected: `{'is_default': 'service_kind', 'is_default_4k': 'service_kind'}`.

- [ ] **Step 5: Manual verification (browser, hard-refresh)**

- On a radarr connection, toggling **HD default** on a second radarr connection **clears it on the first** (UI auto-exclusion).
- Force a conflict via the API (set two radarr `is_default` without the UI) → save is rejected with an inline `is_default` error.
- Sonarr defaults are unaffected by radarr toggles.

- [ ] **Step 6: Update deploy-state memory**

Append to `/opt/deployarr/.claude/projects/-opt-silo/memory/requests-pluginization-deploy-state.md`: Spec B (single-default exclusivity) implemented + deployed; note SDK `feat/request-router-capability` gained `ValidateRequest.siblings` + `AdminFormField.exclusive_group_field` (another pre-publish breaking proto change), new silo HEAD, arr reinstalled.

---

## Notes for the implementer

- Do not edit the spec or this plan during implementation.
- Task order matters: SDK (Task 1) first — the plugin and host see the new getters via their `replace` directives. The host image build (Task 5) needs the re-vendor (Task 5 Step 1) because the Dockerfile builds `-mod=vendor`.
- Tasks 1, 2 are in sibling repos with their own git history — commit there.
- This is **Spec B**. Spec A already shipped.
