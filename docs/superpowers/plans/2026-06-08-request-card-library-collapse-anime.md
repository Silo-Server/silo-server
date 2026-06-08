# Request card: collapsible Library + anime gate/nesting — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the request connection card's Library section collapsible (collapsed by default, auto-expanding on errors) and move the anime override fields into one gated section directly below Library instead of a detached sibling card.

**Architecture:** Two coordinated changes. The generic `SchemaForm` renderer (silo-server) gains (a) error-aware auto-expand for collapsible sections and (b) a nested-field affordance for `show_when`-revealed fields — both plugin-agnostic. The arr plugin's `manifest.json` `admin_form` is restructured: Library becomes collapsible/collapsed and sheds `anime_enabled`; the Anime section becomes always-visible with `anime_enabled` as its gate toggle.

**Tech Stack:** React + TypeScript + vitest/testing-library (silo-server `web/`); Go + embedded JSON manifest + SDK `LoadWithChecksum` (silo-plugin-requests-arr).

**Repos:** `silo-server` (`/opt/silo`) for Tasks 1–2; `silo-plugin-requests-arr` (`/opt/silo-plugin-requests-arr`) for Task 3; both deploy in Task 4. Commands assume each repo root is the cwd. `go` is on PATH via `/tmp/go/bin`; frontend tooling runs via `web/node_modules/.bin`.

---

### Task 1: SchemaForm — collapsible sections auto-expand on validation errors

**Files:**
- Modify: `web/src/components/admin/plugins/SchemaForm.tsx` (the `SchemaFormSection` component + its call site in `SchemaForm`)
- Test: `web/src/components/admin/plugins/SchemaForm.test.tsx`

- [ ] **Step 1: Write the failing tests**

Append to `SchemaForm.test.tsx` (it already imports `render, screen, fireEvent`, `vi`, `SchemaForm`, and `PluginAdminForm`):

```tsx
const collapsibleDescriptor: PluginAdminForm = {
  fields: [
    { key: "api_path", label: "API path", control: "TEXT", required: true, secret: false, multiline: false },
    { key: "verbose", label: "Verbose", control: "SWITCH", required: false, secret: false, multiline: false },
  ],
  sections: [
    { key: "lib", title: "Library", collapsible: true, collapsed_default: true, field_keys: ["api_path", "verbose"] },
  ],
};

describe("SchemaForm collapsible sections", () => {
  it("honors collapsed_default when the section has no field errors", () => {
    render(<SchemaForm descriptor={collapsibleDescriptor} values={{ api_path: "/v3" }} onChange={vi.fn()} />);
    expect(screen.queryByText("Verbose")).toBeNull(); // collapsed -> field hidden
    expect(screen.getByText("Show")).toBeTruthy();
  });

  it("auto-expands a collapsed section that has a validation error (empty required field)", () => {
    render(<SchemaForm descriptor={collapsibleDescriptor} values={{}} onChange={vi.fn()} />);
    // api_path is required + empty -> validateSchemaValues flags it -> section force-expands
    expect(screen.getByText("Verbose")).toBeTruthy();
  });

  it("expands a clean collapsed section when Show is clicked", () => {
    render(<SchemaForm descriptor={collapsibleDescriptor} values={{ api_path: "/v3" }} onChange={vi.fn()} />);
    fireEvent.click(screen.getByText("Show"));
    expect(screen.getByText("Verbose")).toBeTruthy();
  });
});
```

- [ ] **Step 2: Run the tests to verify the new one fails**

Run: `cd /opt/silo/web && node_modules/.bin/vitest run src/components/admin/plugins/SchemaForm.test.tsx`
Expected: "honors collapsed_default" and "Show click" PASS; **"auto-expands … validation error" FAILS** ("Unable to find an element with the text: Verbose") because the section starts collapsed regardless of errors.

- [ ] **Step 3: Implement error-aware open state**

In `SchemaForm.tsx`, change `SchemaFormSection` to accept a `forceOpen` prop and replace its `useState(!section.collapsed_default)` with an effective-open computation:

```tsx
function SchemaFormSection({
  section,
  values,
  forceOpen,
  renderFields,
}: {
  section: PluginAdminFormSection;
  values: Record<string, unknown>;
  forceOpen: boolean;
  renderFields: (keys: string[]) => React.ReactNode;
}) {
  // null = operator hasn't toggled; fall back to collapsed_default. forceOpen
  // (the section has unresolved errors) always wins so setup can't be hidden.
  const [userOpen, setUserOpen] = useState<boolean | null>(null);

  if (!evaluateShowWhen(section.show_when, values)) {
    return null;
  }

  const open = forceOpen || (userOpen ?? !section.collapsed_default);
  const showFields = section.collapsible ? open : true;

  return (
    <section className="border-border/70 bg-muted/10 space-y-3 rounded-lg border p-4">
      <div className="flex items-center justify-between gap-2">
        <div className="space-y-0.5">
          <Label className="text-foreground text-sm font-semibold">{section.title}</Label>
          {section.description ? (
            <p className="text-muted-foreground text-xs leading-relaxed">{section.description}</p>
          ) : null}
        </div>
        {section.collapsible ? (
          <Button type="button" size="xs" variant="ghost" onClick={() => setUserOpen(!open)}>
            {open ? "Hide" : "Show"}
          </Button>
        ) : null}
      </div>
      {showFields ? renderFields(section.field_keys) : null}
    </section>
  );
}
```

Then at the call site inside `SchemaForm`'s return, pass `forceOpen` computed from the merged errors:

```tsx
      {sections.map((section) => (
        <SchemaFormSection
          key={section.key}
          section={section}
          values={values}
          forceOpen={section.field_keys.some((key) => mergedErrors[key] != null)}
          renderFields={(keys) => renderFieldList(resolveKeys(keys))}
        />
      ))}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /opt/silo/web && node_modules/.bin/vitest run src/components/admin/plugins/SchemaForm.test.tsx`
Expected: all SchemaForm tests PASS.

- [ ] **Step 5: Typecheck + lint + commit**

```bash
cd /opt/silo/web
node_modules/.bin/tsc -b && node_modules/.bin/eslint src/components/admin/plugins/SchemaForm.tsx src/components/admin/plugins/SchemaForm.test.tsx
node_modules/.bin/prettier --write src/components/admin/plugins/SchemaForm.tsx src/components/admin/plugins/SchemaForm.test.tsx
cd /opt/silo && git add web/src/components/admin/plugins/SchemaForm.tsx web/src/components/admin/plugins/SchemaForm.test.tsx
git commit -m "feat(web): auto-expand collapsible schema sections that have validation errors"
```

---

### Task 2: SchemaForm — nested affordance for `show_when`-revealed fields

**Files:**
- Modify: `web/src/components/admin/plugins/SchemaForm.tsx` (the `renderField` function; add a `cn` import)
- Test: `web/src/components/admin/plugins/SchemaForm.test.tsx`

- [ ] **Step 1: Write the failing test**

Append to `SchemaForm.test.tsx`:

```tsx
it("marks a show_when-gated field as nested when it is revealed", () => {
  const d: PluginAdminForm = {
    fields: [
      { key: "service_kind", label: "Service", control: "SELECT", required: false, secret: false, multiline: false,
        options: [{ value: "sonarr", label: "Sonarr" }] },
      { key: "series_type", label: "Series type", control: "SELECT", required: false, secret: false, multiline: false,
        show_when: [{ field: "service_kind", equals: ["sonarr"] }], options: [{ value: "standard", label: "Standard" }] },
    ],
  };
  const { container } = render(<SchemaForm descriptor={d} values={{ service_kind: "sonarr" }} onChange={vi.fn()} />);
  expect(container.querySelector('[data-nested="true"]')).not.toBeNull();
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /opt/silo/web && node_modules/.bin/vitest run src/components/admin/plugins/SchemaForm.test.tsx -t "nested"`
Expected: FAIL (`expected null not to be null` — no `data-nested` attribute exists yet).

- [ ] **Step 3: Add the nesting affordance to `renderField`**

Add the import near the other `@/` imports at the top of `SchemaForm.tsx`:

```tsx
import { cn } from "@/lib/utils";
```

Replace the outer wrapper `<div>` in `renderField` (the non-switch field renderer) so a `show_when` field gets a marker + subtle left rail. Switch fields (`renderSwitchRow`) are intentionally left unchanged so the grouped toggle list stays flush:

```tsx
  function renderField(field: PluginAdminFormField): React.ReactNode {
    const err = mergedErrors[field.key];
    // A field that only appears because its show_when passed reads as nested
    // under whatever toggle gates it (e.g. the anime overrides under anime_enabled).
    const nested = Boolean(field.show_when);
    return (
      <div
        key={field.key}
        data-nested={nested ? "true" : undefined}
        className={cn("space-y-2", nested && "border-border/60 ml-0.5 border-l pl-3")}
      >
        <div className="space-y-1">
          <Label htmlFor={`${idPrefix}-${field.key}`}>{field.label || field.key}</Label>
          {field.description ? (
            <p className="text-muted-foreground text-xs leading-relaxed">{field.description}</p>
          ) : null}
        </div>
        {renderControl(field)}
        {err ? <p className="text-destructive text-xs">{err}</p> : null}
      </div>
    );
  }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /opt/silo/web && node_modules/.bin/vitest run src/components/admin/plugins/SchemaForm.test.tsx`
Expected: all PASS.

- [ ] **Step 5: Typecheck + lint + commit**

```bash
cd /opt/silo/web
node_modules/.bin/tsc -b && node_modules/.bin/eslint src/components/admin/plugins/SchemaForm.tsx src/components/admin/plugins/SchemaForm.test.tsx
node_modules/.bin/prettier --write src/components/admin/plugins/SchemaForm.tsx src/components/admin/plugins/SchemaForm.test.tsx
cd /opt/silo && git add web/src/components/admin/plugins/SchemaForm.tsx web/src/components/admin/plugins/SchemaForm.test.tsx
git commit -m "feat(web): indent show_when-revealed schema fields to read as nested"
```

---

### Task 3: arr plugin — Library collapsible + anime gate section (manifest)

**Files:**
- Modify: `/opt/silo-plugin-requests-arr/manifest.json` (the single capability's `config_schema[0].admin_form` → `sections`)
- Test: `/opt/silo-plugin-requests-arr/main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `main_test.go`. It already imports `publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"`. Add the proto import `pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"` to the import block, then:

```go
func TestAdminFormLayout(t *testing.T) {
	m, err := publicmanifest.LoadWithChecksum(manifestJSON, version)
	if err != nil {
		t.Fatalf("LoadWithChecksum: %v", err)
	}
	af := m.GetCapabilities()[0].GetConfigSchema()[0].GetAdminForm()

	var library, anime *pluginv1.AdminFormSection
	for _, s := range af.GetSections() {
		switch s.GetKey() {
		case "library":
			library = s
		case "anime":
			anime = s
		}
	}
	if library == nil || anime == nil {
		t.Fatalf("expected both library and anime sections, got %+v", af.GetSections())
	}

	// Library: collapsible, collapsed by default, and no longer owns the gate toggle.
	if !library.GetCollapsible() || !library.GetCollapsedDefault() {
		t.Errorf("library: collapsible=%v collapsed_default=%v, want both true", library.GetCollapsible(), library.GetCollapsedDefault())
	}
	for _, k := range library.GetFieldKeys() {
		if k == "anime_enabled" {
			t.Errorf("anime_enabled must not be in the library section")
		}
	}

	// Anime: always visible (no section-level show_when, not collapsible) and
	// gated by anime_enabled as its first field.
	if anime.GetCollapsible() {
		t.Errorf("anime section must not be collapsible (the gate toggle must stay visible)")
	}
	if len(anime.GetShowWhen()) != 0 {
		t.Errorf("anime section must not carry a section-level show_when, got %+v", anime.GetShowWhen())
	}
	keys := anime.GetFieldKeys()
	if len(keys) == 0 || keys[0] != "anime_enabled" {
		t.Errorf("anime section must list anime_enabled first, got %v", keys)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /opt/silo-plugin-requests-arr && PATH=$PATH:/tmp/go/bin go test ./... -run TestAdminFormLayout -v`
Expected: FAIL — current manifest has `library.collapsible=false`, `anime_enabled` inside library, and an `anime` section with a `show_when`.

- [ ] **Step 3: Edit `manifest.json`**

In `config_schema[0].admin_form.sections`, change the two sections:

**Library section** — set `"collapsible": true`, `"collapsed_default": true`, and remove `"anime_enabled"` from `field_keys` (it becomes):
```json
"field_keys": [
  "service_kind", "root_folder", "quality_profile_id", "tags",
  "is_default", "is_4k", "is_default_4k", "search_on_add",
  "minimum_availability", "series_type", "season_folder"
]
```

**Anime section** — set `"collapsible": false`, delete the `"show_when": [...]` property entirely, and prepend `"anime_enabled"` to `field_keys`:
```json
{
  "key": "anime",
  "title": "Anime overrides",
  "collapsible": false,
  "collapsed_default": false,
  "field_keys": ["anime_enabled", "anime_root_folder", "anime_quality_profile_id", "anime_tags"]
}
```

Leave every field definition under `admin_form.fields` unchanged (the field-level `show_when: anime_enabled == true` on `anime_root_folder`/`anime_quality_profile_id`/`anime_tags` keeps them hidden until the toggle is on).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /opt/silo-plugin-requests-arr && PATH=$PATH:/tmp/go/bin go test ./...`
Expected: PASS (`TestAdminFormLayout` and the existing `TestEmbeddedManifestLoads`).

- [ ] **Step 5: Commit (arr repo)**

```bash
cd /opt/silo-plugin-requests-arr
git add manifest.json main_test.go
git commit -m "feat: collapsible Library + anime gate section in admin form"
```

---

### Task 4: Build, deploy, reinstall, and verify on the live box

This task has no new tests — it ships Tasks 1–3 to `silo.happyville.to` and verifies behavior manually.

- [ ] **Step 1: Rebuild the silo-server frontend bundle**

```bash
cd /opt/silo/web && PATH=$PATH:/tmp/go/bin pnpm build
```
Expected: `✓ built` with a new `web/dist/assets/index-*.js` hash.

- [ ] **Step 2: Rebuild + redeploy the silo-server image** (vendored workaround, as established this session)

```bash
cd /opt/silo && PATH=$PATH:/tmp/go/bin
docker build -f Dockerfile.deploy -t silo-server:main --build-arg BUILD_REVISION="$(git rev-parse --short HEAD)" .
docker compose up -d silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
docker exec silo-silo-1 sh -c 'curl -s http://127.0.0.1:8080/' | grep -oE 'assets/index-[A-Za-z0-9_-]+\.js'
```
Expected: container healthy; served bundle hash matches the new `web/dist/index.html`.

- [ ] **Step 3: Rebuild the arr plugin binary (new embedded manifest)**

```bash
cd /opt/silo-plugin-requests-arr && PATH=$PATH:/tmp/go/bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o plugin .
```
Expected: a fresh `plugin` ELF binary embedding the updated `manifest.json`.

- [ ] **Step 4: Reinstall the arr plugin on the live box**

The host serves `admin_form` from the *installed* manifest (falling back to `plugin_capabilities`), so the new manifest must be re-installed for installation id 5. Use the same in-container install path used to install it originally (the host's `plugins.Service` binary-upload against the container DB + `/var/lib/silo/plugins`), or the Admin → Plugins raw-binary upload in the UI. After install, restart so the runtime reloads:

```bash
docker compose restart silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
```
Verify the stored capability picked up the new layout:
```bash
docker exec silo-postgres-1 psql -U silo -d silo -t -c \
  "SELECT metadata->'config_schema'->0->'admin_form'->'sections' FROM plugin_capabilities WHERE installation_id=5 AND capability_type='request_router.v1';"
```
Expected: library section shows `"collapsible": true`; anime section has no `show_when` and lists `anime_enabled` first.

- [ ] **Step 5: Manual verification (browser, hard-refresh first)**

- A **saved/valid** Radarr connection opens with **Library collapsed** (a "Show" control visible).
- A **new** connection (Add connection → pick the plugin) opens with **Library expanded** (empty required root folder/quality profile force it open); it collapses once those are filled.
- The **Anime overrides** box sits directly **below** Library and always shows the **"Enable anime overrides" toggle**; enabling it reveals the anime root folder / quality / tags fields **nested** (left rail) inside that same box — nothing pops out elsewhere.

- [ ] **Step 6: Update deploy-state memory**

Append to `/opt/deployarr/.claude/projects/-opt-silo/memory/requests-pluginization-deploy-state.md`: Spec A (collapsible Library + anime gate) implemented and deployed; note the new silo-server HEAD and that the arr plugin was reinstalled with the new manifest. Spec B (single-default enforcement) still pending.

---

## Notes for the implementer

- Do not edit the spec (`docs/superpowers/specs/2026-06-08-request-card-library-collapse-anime-design.md`) or this plan during implementation.
- Tasks 1 and 2 are independent and both touch `SchemaForm.tsx`/`SchemaForm.test.tsx`; do them in order to avoid edit conflicts.
- Task 3 lives in a *different repo* (`/opt/silo-plugin-requests-arr`) with its own git history — commit there separately.
- This is **Spec A only**. Single-default-per-service-type enforcement is Spec B and is intentionally not in this plan.
