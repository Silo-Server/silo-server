# Autoscan Source Labels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Label autoscan scan sources by a generic, plugin-agnostic resolution chain (operator label → connection name → manifest display_name → capability_id) shared by the Sources and Activity admin panels, plus an operator-editable per-source label.

**Architecture:** A new backend `label` column on `autoscan_sources` (migration 174), threaded through the Go domain/repository/handler layers with server-side trim + length cap. A new pure frontend helper `web/src/lib/autoscanLabels.ts` implements the resolution chain; both admin panels consume it. The Sources panel adds an inline, on-blur-saved label input.

**Tech Stack:** Go (chi handlers, pgx repository), PostgreSQL, React + TypeScript (TanStack Query), Vitest, Docker (the build/test toolchain runs in containers; the host has no Go toolchain).

Commands assume the repository root is the cwd.

---

## Running tests (host has no Go toolchain)

Go tests run in a throwaway container. A named volume caches the module download between runs.

**Pure-Go packages (e.g. `internal/autoscan`)** — no libvips needed:

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/autoscan/ -run TestNormalizeSourceLabel -v
```

**`internal/api/handlers`** — needs libvips (CGO image deps), so install it first:

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  sh -c 'apt-get update >/dev/null && apt-get install -y --no-install-recommends libvips-dev >/dev/null && go test ./internal/api/handlers/ -run TestAutoscanHandleUpdateSourceNormalizesLabel -v'
```

**Frontend** — runs natively:

```bash
cd web && pnpm exec vitest run src/lib/autoscanLabels.test.ts
```

---

## File structure

- `migrations/174_autoscan_source_label.{up,down}.sql` — **create**. Adds/removes the `label` column.
- `internal/autoscan/labels.go` — **create**. `NormalizeSourceLabel` (trim + rune cap) and `MaxSourceLabelLen`.
- `internal/autoscan/labels_test.go` — **create**. Unit tests for the normalizer.
- `internal/autoscan/types.go` — **modify**. Add `Label` to `Source`.
- `internal/autoscan/repository.go` — **modify**. Add `label` to `sourceColumns`, `scanSource`, `CreateSource`, `UpdateSource`.
- `internal/api/handlers/autoscan.go` — **modify**. Add `label` to request/response structs; normalize on create/update.
- `internal/api/handlers/autoscan_test.go` — **modify**. Add a handler test for label normalization round-trip.
- `web/src/api/types.ts` — **modify**. Add `label` to `AutoscanSource` and `AutoscanSourceInput`.
- `web/src/lib/autoscanLabels.ts` — **create**. The shared resolution helper.
- `web/src/lib/autoscanLabels.test.ts` — **create**. Helper unit tests.
- `web/src/pages/admin/autoscan/SourcesPanel.tsx` — **modify**. Use the helper; add the label input.
- `web/src/pages/admin/autoscan/ActivityPanel.tsx` — **modify**. Use the helper via `SourceLabelLookups`.

---

## Task 1: Migration — add `label` column

**Files:**
- Create: `migrations/174_autoscan_source_label.up.sql`
- Create: `migrations/174_autoscan_source_label.down.sql`

- [ ] **Step 1: Write the up migration**

Create `migrations/174_autoscan_source_label.up.sql`:

```sql
-- Operator-editable display label for an autoscan source. Empty string = unset;
-- the admin UI falls back to connection name / plugin display_name / capability.
ALTER TABLE public.autoscan_sources ADD COLUMN label text NOT NULL DEFAULT '';
```

- [ ] **Step 2: Write the down migration**

Create `migrations/174_autoscan_source_label.down.sql`:

```sql
ALTER TABLE public.autoscan_sources DROP COLUMN label;
```

- [ ] **Step 3: Commit**

```bash
git add migrations/174_autoscan_source_label.up.sql migrations/174_autoscan_source_label.down.sql
git commit -m "feat(autoscan): migration for source label column"
```

---

## Task 2: Backend domain — `NormalizeSourceLabel` + `Source.Label`

**Files:**
- Create: `internal/autoscan/labels.go`
- Create: `internal/autoscan/labels_test.go`
- Modify: `internal/autoscan/types.go` (Source struct)

- [ ] **Step 1: Write the failing test**

Create `internal/autoscan/labels_test.go`:

```go
package autoscan

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeSourceLabel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trims surrounding whitespace", "  4K Movies  ", "4K Movies"},
		{"empty stays empty", "", ""},
		{"all whitespace becomes empty", "   ", ""},
		{"under cap unchanged", strings.Repeat("x", 120), strings.Repeat("x", 120)},
		{"over cap truncated to 120 runes", strings.Repeat("x", 200), strings.Repeat("x", 120)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeSourceLabel(c.in); got != c.want {
				t.Fatalf("NormalizeSourceLabel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeSourceLabelRuneSafe(t *testing.T) {
	// 200 multi-byte runes must cap to 120 runes and stay valid UTF-8.
	got := NormalizeSourceLabel(strings.Repeat("é", 200))
	if utf8.RuneCountInString(got) != 120 {
		t.Fatalf("rune count = %d, want 120", utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("result is not valid UTF-8")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/autoscan/ -run TestNormalizeSourceLabel -v
```

Expected: FAIL — `undefined: NormalizeSourceLabel`.

- [ ] **Step 3: Write the implementation**

Create `internal/autoscan/labels.go`:

```go
package autoscan

import (
	"strings"
	"unicode/utf8"
)

// MaxSourceLabelLen bounds an operator-set source label in runes.
const MaxSourceLabelLen = 120

// NormalizeSourceLabel trims surrounding whitespace and caps the label at
// MaxSourceLabelLen runes without splitting a multi-byte rune. An all-whitespace
// label normalizes to "" (unset).
func NormalizeSourceLabel(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= MaxSourceLabelLen {
		return s
	}
	return string([]rune(s)[:MaxSourceLabelLen])
}
```

- [ ] **Step 4: Add `Label` to the `Source` struct**

In `internal/autoscan/types.go`, in the `Source` struct, add the `Label` field directly after the `SourceConfig` line:

```go
	SourceConfig        map[string]string
	Label               string // operator-set display label; "" = unset
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/autoscan/ -run TestNormalizeSourceLabel -v
```

Expected: PASS (both `TestNormalizeSourceLabel` and `TestNormalizeSourceLabelRuneSafe`).

- [ ] **Step 6: Commit**

```bash
git add internal/autoscan/labels.go internal/autoscan/labels_test.go internal/autoscan/types.go
git commit -m "feat(autoscan): source label domain field + normalizer"
```

---

## Task 3: Backend repository — persist `label`

**Files:**
- Modify: `internal/autoscan/repository.go` (`sourceColumns`, `scanSource`, `CreateSource`, `UpdateSource`)

- [ ] **Step 1: Add `label` to `sourceColumns`**

In `internal/autoscan/repository.go`, replace the `sourceColumns` const:

```go
const sourceColumns = `id, installation_id, capability_id, connection_id, enabled,
	poll_interval_seconds, path_rewrites, source_config, label, marker, last_run_at, last_error`
```

- [ ] **Step 2: Scan `label` in `scanSource`**

In `scanSource`, replace the `row.Scan(...)` call so `&s.Label` is read in the same position `label` now occupies (immediately after `&sourceConfig`):

```go
	if err := row.Scan(&s.ID, &s.InstallationID, &s.CapabilityID, &s.ConnectionID,
		&s.Enabled, &s.PollIntervalSeconds, &pathRewrites, &sourceConfig, &s.Label, &s.Marker, &s.LastRunAt, &s.LastError); err != nil {
		return Source{}, err
	}
```

- [ ] **Step 3: Insert `label` in `CreateSource`**

In `CreateSource`, replace the `INSERT` statement and its args:

```go
	row := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_sources (
			installation_id, capability_id, connection_id, enabled, poll_interval_seconds, path_rewrites, source_config, label
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+sourceColumns,
		s.InstallationID, s.CapabilityID, connectionIDArg(s.ConnectionID), s.Enabled, s.PollIntervalSeconds, rewrites, sourceConfig, s.Label)
```

- [ ] **Step 4: Update `label` in `UpdateSource`**

In `UpdateSource`, replace the `UPDATE` statement and its args:

```go
	row := r.pool.QueryRow(ctx, `
		UPDATE autoscan_sources
		SET connection_id = $2,
		    enabled = $3,
		    poll_interval_seconds = $4,
		    path_rewrites = $5,
		    source_config = $6,
		    label = $7,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+sourceColumns,
		s.ID, connectionIDArg(s.ConnectionID), s.Enabled, s.PollIntervalSeconds, rewrites, sourceConfig, s.Label)
```

- [ ] **Step 5: Verify the package compiles**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go build ./internal/autoscan/
```

Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/autoscan/repository.go
git commit -m "feat(autoscan): persist source label in repository"
```

---

## Task 4: Backend handler — accept, normalize, and return `label`

**Files:**
- Modify: `internal/api/handlers/autoscan.go` (`autoscanSourceResponse`, `sourceResponse`, `autoscanCreateSourceInput`, `autoscanSourceInput`, `HandleCreateSource`, `HandleUpdateSource`)
- Modify: `internal/api/handlers/autoscan_test.go` (new test)

- [ ] **Step 1: Write the failing handler test**

In `internal/api/handlers/autoscan_test.go`, add this test (it uses the existing `fakeAutoscanStore`, `fakeAutoscanTriggerer`, and `newAutoscanRequest` helpers already in the file):

```go
func TestAutoscanHandleUpdateSourceNormalizesLabel(t *testing.T) {
	var got autoscan.Source
	store := &fakeAutoscanStore{
		updateSourceFn: func(s autoscan.Source) (autoscan.Source, error) {
			got = s
			return s, nil
		},
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	body := `{"connection_id":null,"enabled":false,"label":"  4K Movies  "}`
	req := newAutoscanRequest("PATCH", "/api/v1/admin/autoscan/sources/src-1", body, "src-1")
	rec := httptest.NewRecorder()
	h.HandleUpdateSource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.Label != "4K Movies" {
		t.Fatalf("stored label = %q, want %q", got.Label, "4K Movies")
	}
	var resp autoscanSourceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Label != "4K Movies" {
		t.Fatalf("response label = %q, want %q", resp.Label, "4K Movies")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  sh -c 'apt-get update >/dev/null && apt-get install -y --no-install-recommends libvips-dev >/dev/null && go test ./internal/api/handlers/ -run TestAutoscanHandleUpdateSourceNormalizesLabel -v'
```

Expected: FAIL — `resp.Label` undefined (struct field missing) / compile error.

- [ ] **Step 3: Add `Label` to the response struct**

In `autoscanSourceResponse`, add after the `SourceConfig` field:

```go
	SourceConfig        map[string]string      `json:"source_config"`
	Label               string                 `json:"label"`
```

- [ ] **Step 4: Set `Label` in `sourceResponse`**

In `sourceResponse`, add `Label: s.Label,` after the `SourceConfig` line:

```go
		PathRewrites:        rewrites,
		SourceConfig:        config,
		Label:               s.Label,
```

- [ ] **Step 5: Add `Label` to both input structs**

In `autoscanCreateSourceInput`, add after `SourceConfig`:

```go
	SourceConfig        map[string]string      `json:"source_config"`
	Label               string                 `json:"label"`
```

In `autoscanSourceInput`, add after `SourceConfig`:

```go
	SourceConfig        map[string]string      `json:"source_config"`
	Label               string                 `json:"label"`
```

- [ ] **Step 6: Normalize and store `Label` on create and update**

In `HandleCreateSource`, in the `autoscan.Source{...}` passed to `h.repo.CreateSource`, add after the `SourceConfig` line:

```go
		PathRewrites:        normalizePathRewrites(in.PathRewrites),
		SourceConfig:        normalizeSourceConfig(in.SourceConfig),
		Label:               autoscan.NormalizeSourceLabel(in.Label),
```

In `HandleUpdateSource`, in the `autoscan.Source{...}` passed to `h.repo.UpdateSource`, add after the `SourceConfig` line:

```go
		PathRewrites:        normalizePathRewrites(in.PathRewrites),
		SourceConfig:        normalizeSourceConfig(in.SourceConfig),
		Label:               autoscan.NormalizeSourceLabel(in.Label),
```

- [ ] **Step 7: Run the test to verify it passes**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  sh -c 'apt-get update >/dev/null && apt-get install -y --no-install-recommends libvips-dev >/dev/null && go test ./internal/api/handlers/ -run TestAutoscanHandleUpdateSourceNormalizesLabel -v'
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/api/handlers/autoscan.go internal/api/handlers/autoscan_test.go
git commit -m "feat(autoscan): accept, normalize, and return source label"
```

---

## Task 5: Frontend types — `label` on source models

**Files:**
- Modify: `web/src/api/types.ts` (`AutoscanSource`, `AutoscanSourceInput`)

- [ ] **Step 1: Add `label` to `AutoscanSource`**

In `web/src/api/types.ts`, in `AutoscanSource`, add after `source_config`:

```ts
  source_config: Record<string, string>;
  label: string;
```

- [ ] **Step 2: Add optional `label` to `AutoscanSourceInput`**

In `AutoscanSourceInput`, add after `source_config`:

```ts
  source_config?: Record<string, string>;
  label?: string;
```

- [ ] **Step 3: Typecheck**

```bash
cd web && pnpm exec tsc -b
```

Expected: exit 0 (no errors).

- [ ] **Step 4: Commit**

```bash
git add web/src/api/types.ts
git commit -m "feat(autoscan): add label to source API types"
```

---

## Task 6: Shared label helper — `autoscanLabels.ts`

**Files:**
- Create: `web/src/lib/autoscanLabels.ts`
- Create: `web/src/lib/autoscanLabels.test.ts`

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/autoscanLabels.test.ts`:

```ts
import { describe, expect, it } from "vitest";

import type { AutoscanSource } from "@/api/types";
import {
  composeSourceLabel,
  resolveEventSourceName,
  type SourceLabelLookups,
} from "./autoscanLabels";

describe("composeSourceLabel", () => {
  const base = { capabilityId: "arr", installationId: 4 };

  it("uses the operator label first, demoting connection to detail", () => {
    expect(
      composeSourceLabel({ ...base, operatorLabel: "4K Movies", connectionName: "Radarr4k" }),
    ).toEqual({ name: "4K Movies", detail: "Radarr4k · plugin #4" });
  });

  it("uses the connection name when no operator label", () => {
    expect(
      composeSourceLabel({ ...base, connectionName: "Radarr4k", displayName: "Arr Watcher" }),
    ).toEqual({ name: "Radarr4k", detail: "Arr Watcher · plugin #4" });
  });

  it("uses the manifest display name when no connection", () => {
    expect(
      composeSourceLabel({
        capabilityId: "cephfs",
        installationId: 5,
        displayName: "CephFS Watcher",
      }),
    ).toEqual({ name: "CephFS Watcher", detail: "plugin #5" });
  });

  it("falls back to capability id when nothing else is set", () => {
    expect(composeSourceLabel(base)).toEqual({ name: "arr", detail: "plugin #4" });
  });

  it("ignores whitespace-only rungs", () => {
    expect(composeSourceLabel({ ...base, operatorLabel: "   ", connectionName: "  " })).toEqual({
      name: "arr",
      detail: "plugin #4",
    });
  });
});

describe("resolveEventSourceName", () => {
  const source: AutoscanSource = {
    id: "src-1",
    installation_id: 4,
    capability_id: "arr",
    connection_id: "conn-1",
    enabled: true,
    poll_interval_seconds: null,
    last_run_at: null,
    last_error: null,
    path_rewrites: [],
    source_config: {},
    label: "",
  };
  const lookups: SourceLabelLookups = {
    sourceByID: new Map([["src-1", source]]),
    connectionByID: new Map([["conn-1", "Radarr4k"]]),
    displayNames: new Map([["4:arr", "Arr Watcher"]]),
  };

  it("resolves the connection name via the source reference", () => {
    expect(
      resolveEventSourceName({ source_id: "src-1", capability_id: "arr", installation_id: 4 }, lookups),
    ).toBe("Radarr4k");
  });

  it("prefers the operator label on the source", () => {
    const withLabel: SourceLabelLookups = {
      ...lookups,
      sourceByID: new Map([["src-1", { ...source, label: "4K Movies" }]]),
    };
    expect(
      resolveEventSourceName({ source_id: "src-1", capability_id: "arr", installation_id: 4 }, withLabel),
    ).toBe("4K Movies");
  });

  it("falls back to display name when the source was deleted (null source_id)", () => {
    expect(
      resolveEventSourceName({ source_id: null, capability_id: "arr", installation_id: 4 }, lookups),
    ).toBe("Arr Watcher");
  });

  it("returns empty string when the reference has no capability", () => {
    expect(resolveEventSourceName({ source_id: null }, lookups)).toBe("");
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd web && pnpm exec vitest run src/lib/autoscanLabels.test.ts
```

Expected: FAIL — cannot resolve `./autoscanLabels`.

- [ ] **Step 3: Write the helper**

Create `web/src/lib/autoscanLabels.ts`:

```ts
import type { AutoscanAvailableSource, AutoscanSource } from "@/api/types";

export interface SourceLabelParts {
  operatorLabel?: string | null;
  connectionName?: string | null;
  displayName?: string | null;
  capabilityId: string;
  installationId: number;
}

export interface SourceLabel {
  name: string;
  detail: string;
}

/**
 * Resolve a scan source's display label through four rungs, most-specific first:
 *   operator label -> connection name -> manifest display_name -> capability_id.
 * The winning rung is `name`; the remaining plugin identity is `detail`.
 */
export function composeSourceLabel(parts: SourceLabelParts): SourceLabel {
  const operator = parts.operatorLabel?.trim() ?? "";
  const connection = parts.connectionName?.trim() ?? "";
  const display = parts.displayName?.trim() ?? "";
  const plugin = `plugin #${parts.installationId}`;
  const pluginIdentity = display || parts.capabilityId;

  if (operator) {
    return { name: operator, detail: `${connection || pluginIdentity} · ${plugin}` };
  }
  if (connection) {
    return { name: connection, detail: `${pluginIdentity} · ${plugin}` };
  }
  if (display) {
    return { name: display, detail: plugin };
  }
  return { name: parts.capabilityId, detail: plugin };
}

/** Stable key for the (installation, capability) -> manifest display_name map. */
export function pluginDisplayNameKey(installationId: number, capabilityId: string): string {
  return `${installationId}:${capabilityId}`;
}

/** Build the (installation, capability) -> display_name lookup from the picker list. */
export function buildPluginDisplayNames(available: AutoscanAvailableSource[]): Map<string, string> {
  const map = new Map<string, string>();
  for (const a of available) {
    map.set(pluginDisplayNameKey(a.installation_id, a.capability_id), a.display_name);
  }
  return map;
}

/** Lookups the Activity panel threads through to resolve an event/scan's source label. */
export interface SourceLabelLookups {
  sourceByID: Map<string, AutoscanSource>;
  connectionByID: Map<string, string>;
  displayNames: Map<string, string>;
}

/**
 * Resolve the `name` rung for an Activity event/scan that references a source by
 * id. Returns "" when the reference carries no capability (caller supplies its
 * own fallback, e.g. "Autoscan").
 */
export function resolveEventSourceName(
  ref: { source_id?: string | null; capability_id?: string; installation_id?: number | null },
  lookups: SourceLabelLookups,
): string {
  if (!ref.capability_id || ref.installation_id == null) return "";
  const source = ref.source_id ? lookups.sourceByID.get(ref.source_id) : undefined;
  const connectionName = source?.connection_id
    ? lookups.connectionByID.get(source.connection_id)
    : undefined;
  const displayName = lookups.displayNames.get(
    pluginDisplayNameKey(ref.installation_id, ref.capability_id),
  );
  return composeSourceLabel({
    operatorLabel: source?.label,
    connectionName,
    displayName,
    capabilityId: ref.capability_id,
    installationId: ref.installation_id,
  }).name;
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd web && pnpm exec vitest run src/lib/autoscanLabels.test.ts
```

Expected: PASS (9 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/autoscanLabels.ts web/src/lib/autoscanLabels.test.ts
git commit -m "feat(autoscan): shared source-label resolution helper"
```

---

## Task 7: Sources panel — use the helper + add the label input

**Files:**
- Modify: `web/src/pages/admin/autoscan/SourcesPanel.tsx`

- [ ] **Step 1: Import the helper and `useMemo`**

At the top of `web/src/pages/admin/autoscan/SourcesPanel.tsx`, change the React import to include `useMemo`:

```ts
import { useId, useMemo, useState } from "react";
```

Add a new import for the helper (next to the other `@/lib` / hook imports):

```ts
import { buildPluginDisplayNames, composeSourceLabel, pluginDisplayNameKey } from "@/lib/autoscanLabels";
```

- [ ] **Step 2: Add `label` to `RowEdit` and `sourceToRowEdit`**

In the `RowEdit` interface, add:

```ts
  sourceConfig: Record<string, string>;
  label: string;
```

In `sourceToRowEdit`, add to the returned object:

```ts
    sourceConfig: sourceConfigForEdit(source),
    label: source.label ?? "",
```

- [ ] **Step 3: Send `label` in `fullBody`**

In `SourceRow`'s `fullBody`, add `label` to the returned object (before `...overrides`):

```ts
      source_config: normalizeSourceConfig(edit.sourceConfig),
      label: edit.label.trim(),
      ...overrides,
```

- [ ] **Step 4: Add `pluginDisplayNames` prop to `SourceRow`**

Change the `SourceRow` signature to accept the display-name map. Update both the destructure and the prop types:

```ts
function SourceRow({
  source,
  connectionOptions,
  pluginDisplayNames,
  globalPollInterval,
  onDelete,
  layout = "table",
}: {
  source: AutoscanSource;
  connectionOptions: Array<{ id: string; name: string }>;
  pluginDisplayNames: Map<string, string>;
  globalPollInterval: number | null;
  onDelete: (source: AutoscanSource) => void;
  layout?: "table" | "card";
}) {
```

- [ ] **Step 5: Add the `handleLabelBlur` saver**

Inside `SourceRow`, next to `handleIntervalBlur`, add:

```ts
  function handleLabelBlur() {
    if (edit.label.trim() === (source.label ?? "").trim()) return;
    update.mutate({ id: source.id, body: fullBody({}) });
  }
```

- [ ] **Step 6: Replace the `sourceIdentity` block**

Replace the existing `sourceIdentity` declaration (the one that currently computes `boundConnectionId`/`connectionName` and renders the title/subtitle) with:

```ts
  const sourceLabel = composeSourceLabel({
    operatorLabel: edit.label,
    connectionName: connectionOptions.find(
      (c) => c.id === (edit.connectionId || source.connection_id || ""),
    )?.name,
    displayName: pluginDisplayNames.get(
      pluginDisplayNameKey(source.installation_id, source.capability_id),
    ),
    capabilityId: source.capability_id,
    installationId: source.installation_id,
  });
  const sourceIdentity = (
    <div className="min-w-0 space-y-1">
      <div className="min-w-0 space-y-0.5">
        <p className="truncate leading-none font-medium">{sourceLabel.name}</p>
        <p className="text-muted-foreground text-xs">{sourceLabel.detail}</p>
      </div>
      <Input
        value={edit.label}
        placeholder="Custom label (optional)"
        aria-label={`Custom label for ${sourceLabel.name}`}
        className="h-7 text-xs"
        onChange={(e) => setEdit((ed) => ({ ...ed, label: e.target.value }))}
        onBlur={handleLabelBlur}
      />
    </div>
  );
```

- [ ] **Step 7: Build and pass `pluginDisplayNames` from the main panel**

In `SourcesPanel` (the default export), add the available-sources query and the memoized map near the other hooks (after `const settings = useAutoscanSettings();`):

```ts
  const available = useAvailableScanSources();
  const pluginDisplayNames = useMemo(
    () => buildPluginDisplayNames(available.data ?? []),
    [available.data],
  );
```

`useAvailableScanSources` is already imported in this file. Then pass the prop to **both** `<SourceRow>` instances (the card-layout map and the table-layout map), adding this line to each:

```tsx
            source={source}
            connectionOptions={connectionOptions}
            pluginDisplayNames={pluginDisplayNames}
            globalPollInterval={globalPollInterval}
```

- [ ] **Step 8: Typecheck, lint, format**

```bash
cd web && pnpm exec tsc -b && pnpm exec eslint src/pages/admin/autoscan/SourcesPanel.tsx && pnpm exec prettier --write src/pages/admin/autoscan/SourcesPanel.tsx
```

Expected: tsc exit 0, eslint exit 0, prettier rewrites/confirms formatting.

- [ ] **Step 9: Commit**

```bash
git add web/src/pages/admin/autoscan/SourcesPanel.tsx
git commit -m "feat(autoscan): label sources via shared helper + operator label input"
```

---

## Task 8: Activity panel — resolve labels via `SourceLabelLookups`

**Files:**
- Modify: `web/src/pages/admin/autoscan/ActivityPanel.tsx`

This task replaces the existing `sourceNames: Map<string, string>` plumbing (connection-name-only) with the full `SourceLabelLookups` and the shared resolver.

- [ ] **Step 1: Update imports**

Add `useAvailableScanSources` to the existing `@/hooks/queries/useAutoscan` import block:

```ts
import {
  useAutoscanConnections,
  useAutoscanEvents,
  useAutoscanScans,
  useAutoscanSources,
  useAutoscanStatus,
  useAvailableScanSources,
} from "@/hooks/queries/useAutoscan";
```

Add a helper import (next to the other `@/lib` imports such as `@/lib/scanRuns`):

```ts
import {
  buildPluginDisplayNames,
  resolveEventSourceName,
  type SourceLabelLookups,
} from "@/lib/autoscanLabels";
```

- [ ] **Step 2: Rewrite the two name functions**

Replace the existing `pollSourceName` and `scanSourceName` functions (and the comment block above them) with:

```ts
// arr-plugin sources fan out one-per-connection under a single generic
// capability, so resolve every Activity row through the shared label chain:
// operator label -> connection name -> manifest display_name -> capability_id.
function pollSourceName(event: AutoscanEvent, lookups: SourceLabelLookups): string {
  return resolveEventSourceName(event, lookups);
}

function scanSourceName(scan: AutoscanScan, lookups: SourceLabelLookups): string {
  return resolveEventSourceName(scan, lookups) || "Autoscan";
}
```

- [ ] **Step 3: Swap the prop type on the four presentational components**

In each of `ScanHistoryCard`, `ScanHistoryTable`, `PollEventCard`, and `PollEventTable`, rename the `sourceNames` prop to `lookups` and change its type from `Map<string, string>` to `SourceLabelLookups`. For example, `ScanHistoryCard` becomes:

```ts
function ScanHistoryCard({
  scan,
  librariesByID,
  lookups,
}: {
  scan: AutoscanScan;
  librariesByID: Map<number, Library>;
  lookups: SourceLabelLookups;
}) {
```

Apply the identical rename (`sourceNames` → `lookups`, `Map<string, string>` → `SourceLabelLookups`) to `ScanHistoryTable`, `PollEventCard`, and `PollEventTable` prop lists.

- [ ] **Step 4: Update the call sites inside those components**

Within those four components, update every call and pass-through:
- `scanSourceName(scan, sourceNames)` → `scanSourceName(scan, lookups)` (in `ScanHistoryCard` and the `ScanHistoryTable` table row).
- `pollSourceName(event, sourceNames)` → `pollSourceName(event, lookups)` (in `PollEventCard` and the `PollEventTable` table row).
- In `ScanHistoryTable`, the `<ScanHistoryCard ... sourceNames={sourceNames} />` becomes `<ScanHistoryCard ... lookups={lookups} />`.
- In `PollEventTable`, the `<PollEventCard ... sourceNames={sourceNames} />` becomes `<PollEventCard ... lookups={lookups} />`.

- [ ] **Step 5: Build `labelLookups` in the main component**

Add the available-sources query alongside the existing `useAutoscanSources()` / `useAutoscanConnections()` calls:

```ts
  const available = useAvailableScanSources();
```

Replace the existing `sourceNames` `useMemo` with:

```ts
  const labelLookups: SourceLabelLookups = useMemo(
    () => ({
      sourceByID: new Map(autoscanSources.map((s) => [s.id, s])),
      connectionByID: new Map(autoscanConnections.map((c) => [c.id, c.name])),
      displayNames: buildPluginDisplayNames(available.data ?? []),
    }),
    [autoscanSources, autoscanConnections, available.data],
  );
```

- [ ] **Step 6: Pass `labelLookups` to the two tables**

Update the `<ScanHistoryTable ... />` and `<PollEventTable ... />` render sites: replace `sourceNames={sourceNames}` with `lookups={labelLookups}`.

- [ ] **Step 7: Typecheck, lint, format**

```bash
cd web && pnpm exec tsc -b && pnpm exec eslint src/pages/admin/autoscan/ActivityPanel.tsx && pnpm exec prettier --write src/pages/admin/autoscan/ActivityPanel.tsx
```

Expected: tsc exit 0, eslint exit 0, prettier confirms.

- [ ] **Step 8: Run the full frontend test + lint suite**

```bash
cd web && pnpm exec vitest run src/lib/autoscanLabels.test.ts && pnpm run lint && pnpm run format:check
```

Expected: tests pass, lint clean, format clean.

- [ ] **Step 9: Commit**

```bash
git add web/src/pages/admin/autoscan/ActivityPanel.tsx
git commit -m "feat(autoscan): resolve activity source labels via shared helper"
```

---

## Task 9: Integration — rebuild, migrate, and verify end-to-end

**Files:** none (build + runtime verification)

- [ ] **Step 1: Build the image (compiles backend + frontend)**

```bash
docker build \
  --build-arg BUILD_REVISION=$(git rev-parse --short HEAD) \
  --build-arg BUILD_DIRTY=false \
  -t silo-server:autoscan-test .
```

Expected: build completes, exit 0.

- [ ] **Step 2: Recreate the container (applies migration 174 on startup)**

```bash
docker compose up -d silo
```

Then wait for health:

```bash
for i in $(seq 1 24); do s=$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1); echo "$s"; [ "$s" = healthy ] && break; sleep 5; done
```

Expected: `healthy`.

- [ ] **Step 3: Verify migration 174 applied and the column exists**

```bash
docker compose exec -T postgres psql -U silo -d silo -c \
  "SELECT version, filename FROM schema_versions WHERE version=174;"
docker compose exec -T postgres psql -U silo -d silo -c \
  "SELECT column_name FROM information_schema.columns WHERE table_name='autoscan_sources' AND column_name='label';"
```

Expected: version 174 row present; `label` column listed.

- [ ] **Step 4: Verify the API round-trips a label**

Pick a source id, set a label, and confirm it persists. Run:

```bash
SID=$(docker compose exec -T postgres psql -U silo -d silo -t -A -c "SELECT id FROM autoscan_sources LIMIT 1;")
docker compose exec -T postgres psql -U silo -d silo -c \
  "UPDATE autoscan_sources SET label='4K Movies' WHERE id='${SID}';"
docker compose exec -T postgres psql -U silo -d silo -c \
  "SELECT id, capability_id, label FROM autoscan_sources WHERE id='${SID}';"
```

Expected: the row shows `label = 4K Movies`. (This seeds data so the next step shows the label in the UI; the operator path is the inline input.)

- [ ] **Step 5: Manual UI verification**

In the admin UI, open **Admin → Autoscan → Sources**. Confirm:
- The seeded source's row title shows `4K Movies` with subtitle `<connection> · plugin #N`.
- Each row has a "Custom label (optional)" input; editing one and clicking away (blur) persists it (reload the page — the label remains).
- Open the **Activity** tab; the Scans/Polls "Source" column shows the operator label for the labeled source and the connection/display name for the others.

- [ ] **Step 6: Revert the seed (optional)**

```bash
docker compose exec -T postgres psql -U silo -d silo -c \
  "UPDATE autoscan_sources SET label='' WHERE label='4K Movies';"
```

- [ ] **Step 7: Final commit (if any working-tree changes remain)**

```bash
git status
```

Expected: clean (all changes committed in earlier tasks).

---

## Self-review notes

- **Spec coverage:** resolution chain (Task 6), operator label storage (Tasks 1–4), shared helper consumed by both panels (Tasks 6–8), inline on-blur edit (Task 7), Activity deleted-source fallback (Task 6 test + helper), testing (Tasks 2, 4, 6). All spec sections map to a task.
- **Type consistency:** `composeSourceLabel`, `SourceLabelParts`, `SourceLabel`, `SourceLabelLookups`, `resolveEventSourceName`, `pluginDisplayNameKey`, `buildPluginDisplayNames` are defined in Task 6 and used unchanged in Tasks 7–8. Backend `NormalizeSourceLabel` / `MaxSourceLabelLen` defined in Task 2, used in Task 4. `Source.Label` (Task 2) used in Task 3; `autoscanSourceResponse.Label` / input `Label` (Task 4) consistent with `AutoscanSource.label` / `AutoscanSourceInput.label` (Task 5).
- **No placeholders:** every code step shows complete code; every run step shows the command and expected result.
