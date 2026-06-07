# Schema-Driven Plugin Config Form — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bespoke arr-shaped request-connection admin form with one generic `SchemaForm` engine driven entirely by a plugin's manifest schema (dynamic options, multi-select, conditional visibility, sections, validation) + a plugin `Validate` RPC, so any `request_router.v1` backend renders its config UI with zero host changes.

**Architecture:** Extend the SDK `AdminFormDescriptor` into a full form language + add a `Validate` RPC; build a reusable controlled `SchemaForm` React component (pure render) fed by a small dynamic-options hook and a validator; refactor the existing `PluginConfigForm` onto it; retire the hand-coded arr form and `integrationOptionsFromRouter`; enrich the arr manifest and implement `Validate`.

**Tech Stack:** Go 1.26 + protobuf (buf) + hashicorp/go-plugin; React 18 + TypeScript + TanStack Query + shadcn UI; Vitest + @testing-library/react.

**Repos & branches (local-only — `go.mod replace` stays, no push/PR):**
- `/opt/silo-plugin-sdk` — branch `feat/request-router-capability`. Module `github.com/Silo-Server/silo-plugin-sdk`, proto alias `pluginv1`. Codegen: `make proto` (buf; or `PATH="$(pwd)/bin:$PATH" buf generate`). Go toolchain `/opt/deployarr/.local/go-sdk/go/bin/go`.
- `/opt/silo` (silo-server) — branch `feat/requests-pluginization`. Host backend needs CGO/libvips → build/test `cmd/silo`+`internal/api` only in the container (recipe below). Pure packages (`internal/requests`, `internal/pluginhost`) build bare-host. Frontend in `web/` via `pnpm`.
- `/opt/silo-plugin-requests-arr` — branch `master`. `go.mod` has `replace … => /opt/silo-plugin-sdk`.

**Container build/test recipe (host packages needing libvips):**
```bash
docker run --rm -v /opt/silo:/opt/silo -v /opt/silo-plugin-sdk:/opt/silo-plugin-sdk -w /opt/silo golang:1.26 \
  bash -c 'export PATH=/usr/local/go/bin:$PATH; apt-get update -qq && apt-get install -y -qq libvips-dev pkg-config >/dev/null 2>&1 && <CMD>'
```

---

## Contract reference (names used across phases — keep identical)

**Proto additions** (`common.proto`): `AdminFormControl.ADMIN_FORM_CONTROL_MULTI_SELECT = 7`; `AdminFormCondition{field, repeated equals}`; `AdminFormValidation{has_min,min,has_max,max,pattern,min_length,max_length}`; `AdminFormField` += `bool dynamic_options=12; repeated AdminFormCondition show_when=13; AdminFormValidation validation=14;`; `AdminFormSection{key,title,description,collapsible,collapsed_default,repeated field_keys,repeated AdminFormCondition show_when}`; `AdminFormDescriptor` += `repeated AdminFormSection sections=3;`.

**Proto additions** (`request_router.proto`): `rpc Validate(ValidateRequest) returns (ValidateResponse);` `ValidateRequest{string capability_id=1; RouterConnection connection=2;}` `ValidateResponse{map<string,string> field_errors=1; string form_error=2;}`.

**TS types** (`web/src/api/types.ts`): `PluginAdminFormField.control` union gains `"MULTI_SELECT"`; `PluginAdminFormField` += `dynamic_options?: boolean; show_when?: PluginAdminFormCondition[]; validation?: PluginAdminFormValidation;`; new `PluginAdminFormCondition{field:string; equals:string[]}`, `PluginAdminFormValidation{has_min?:boolean;min?:number;has_max?:boolean;max?:number;pattern?:string;min_length?:number;max_length?:number}`, `PluginAdminFormSection{key:string;title:string;description?:string;collapsible:boolean;collapsed_default:boolean;field_keys:string[];show_when?:PluginAdminFormCondition[]}`; `PluginAdminForm` += `sections?: PluginAdminFormSection[];`.

**`SchemaForm` component API** (controlled, pure):
```ts
type Option = { value: string; label: string };
type SchemaFormProps = {
  descriptor: PluginAdminForm;                       // fields + sections + submit_label
  values: Record<string, unknown>;                   // current plugin_config
  onChange: (next: Record<string, unknown>) => void;
  errors?: Record<string, string>;                   // server/Validate field errors, keyed by field key
  dynamicOptions?: Record<string, Option[]>;         // resolved options for dynamic_options fields, keyed by field key
  idPrefix?: string;                                  // for input ids (default "schemaform")
};
```

**Pure utils** (`schemaForm.ts`): `evaluateShowWhen(conditions, values): boolean`; `validateSchemaValues(descriptor, values): Record<string,string>` (per-field client errors, only for visible fields); `coerceFieldValue(field, raw)` and `buildSchemaValues(descriptor, draft)` (typed payload incl. multi-select arrays).

---

# PHASE 1 — SDK: form-language + Validate RPC (`/opt/silo-plugin-sdk`)

### Task 1: Extend `AdminFormDescriptor` in `common.proto`

**Files:** Modify `proto/silo/plugin/v1/common.proto`.

- [ ] **Step 1: Add `MULTI_SELECT` to the control enum.** In `AdminFormControl`, after `ADMIN_FORM_CONTROL_SELECT = 6;` add:
```proto
  ADMIN_FORM_CONTROL_MULTI_SELECT = 7;  // array-of-values (e.g. tags)
```

- [ ] **Step 2: Add the new messages** (place near `AdminFormField`):
```proto
// AdminFormCondition: the owning field/section is shown only when, for every
// listed condition, the named field's stringified value is in `equals`.
message AdminFormCondition {
  string field = 1;
  repeated string equals = 2;
}

// AdminFormValidation: per-field constraints, enforced client-side and
// re-checked server-side. has_min/has_max distinguish "unset" from a real 0.
message AdminFormValidation {
  bool   has_min = 1;
  double min = 2;
  bool   has_max = 3;
  double max = 4;
  string pattern = 5;       // RE2
  int32  min_length = 6;
  int32  max_length = 7;
}

// AdminFormSection groups fields (by key) into an ordered, optionally
// collapsible section with its own visibility.
message AdminFormSection {
  string key = 1;
  string title = 2;
  string description = 3;
  bool   collapsible = 4;
  bool   collapsed_default = 5;
  repeated string field_keys = 6;
  repeated AdminFormCondition show_when = 7;
}
```

- [ ] **Step 3: Extend `AdminFormField`** — add three fields after `int32 rows = 11;`:
```proto
  bool dynamic_options = 12;                  // populate options from ListConfigOptions[key]
  repeated AdminFormCondition show_when = 13; // conditional visibility
  AdminFormValidation validation = 14;        // per-field constraints
```

- [ ] **Step 4: Extend `AdminFormDescriptor`** — add after `string submit_label = 2;`:
```proto
  repeated AdminFormSection sections = 3;     // optional; absent -> flat render of `fields`
```

- [ ] **Step 5: Regenerate + build.**
Run: `cd /opt/silo-plugin-sdk && make proto && /opt/deployarr/.local/go-sdk/go/bin/go build ./...`
Expected: regenerates `pkg/pluginproto/silo/plugin/v1/common.pb.go`; build exit 0. (If `make proto` aborts on its `protoc` precheck, run `PATH="$(pwd)/bin:$PATH" buf generate` directly — same output.)

- [ ] **Step 6: Commit.**
```bash
git add proto/silo/plugin/v1/common.proto pkg/pluginproto/silo/plugin/v1/common.pb.go
git commit -m "feat(proto): extend AdminFormDescriptor with sections, conditions, validation, multi-select"
```

---

### Task 2: Add the `Validate` RPC to `request_router.proto`

**Files:** Modify `proto/silo/plugin/v1/request_router.proto`; regenerate.

- [ ] **Step 1: Add the RPC** to `service RequestRouter` (after `TestConnection`):
```proto
  rpc Validate(ValidateRequest) returns (ValidateResponse);
```

- [ ] **Step 2: Add the messages** (after `TestConnectionResponse`):
```proto
message ValidateRequest {
  string capability_id = 1;
  RouterConnection connection = 2;
}

message ValidateResponse {
  map<string, string> field_errors = 1; // keyed by config field; empty = no field errors
  string form_error = 2;                 // form-level error; empty = none
}
```

- [ ] **Step 3: Regenerate + verify the generated symbols.**
Run: `cd /opt/silo-plugin-sdk && PATH="$(pwd)/bin:$PATH" buf generate && grep -l 'Validate' pkg/pluginproto/silo/plugin/v1/request_router_grpc.pb.go`
Expected: file path prints; the `RequestRouterServer`/`RequestRouterClient` interfaces now include `Validate`, and `UnimplementedRequestRouterServer.Validate` exists.

- [ ] **Step 4: Build.** Run: `/opt/deployarr/.local/go-sdk/go/bin/go build ./...` → exit 0.

- [ ] **Step 5: Commit.**
```bash
git add proto/silo/plugin/v1/request_router.proto pkg/pluginproto/silo/plugin/v1/request_router.pb.go pkg/pluginproto/silo/plugin/v1/request_router_grpc.pb.go
git commit -m "feat(proto): add RequestRouter.Validate(config) RPC"
```

---

### Task 3: Manifest-load test for the enriched descriptor

**Files:** Create `pkg/pluginsdk/manifest/admin_form_rich_test.go`.

- [ ] **Step 1: Write the test** (mirrors the existing `manifest_test.go` package/style; asserts a manifest declaring sections + multi-select + show_when + dynamic_options + validation loads):
```go
package manifest_test

import (
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

func TestLoadAcceptsRichAdminForm(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"service_kind\":{\"type\":\"string\"},\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"integer\"}},\"is_default\":{\"type\":\"boolean\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"service_kind","label":"Service","control":"ADMIN_FORM_CONTROL_SELECT","options":[{"value":"radarr","label":"Radarr"}]},
	          {"key":"tags","label":"Tags","control":"ADMIN_FORM_CONTROL_MULTI_SELECT","dynamic_options":true},
	          {"key":"is_default","label":"Default","control":"ADMIN_FORM_CONTROL_SWITCH","show_when":[{"field":"service_kind","equals":["radarr"]}],"validation":{"max_length":0}}
	        ],
	        "sections": [{"key":"main","title":"Main","field_keys":["service_kind","tags","is_default"]}]
	      }
	    }]
	  }]
	}`)
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	cap := m.GetCapabilities()[0]
	form := cap.GetConfigSchema()[0].GetAdminForm()
	if len(form.GetSections()) != 1 {
		t.Fatalf("expected 1 section, got %d", len(form.GetSections()))
	}
	if got := form.GetFields()[1].GetControl(); got.String() != "ADMIN_FORM_CONTROL_MULTI_SELECT" {
		t.Fatalf("tags control = %v, want MULTI_SELECT", got)
	}
	if !form.GetFields()[1].GetDynamicOptions() {
		t.Fatal("tags should declare dynamic_options")
	}
	if len(form.GetFields()[2].GetShowWhen()) != 1 {
		t.Fatal("is_default should carry a show_when condition")
	}
}
```

- [ ] **Step 2: Run.** Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go test ./pkg/pluginsdk/manifest/ -run TestLoadAcceptsRichAdminForm -v`
Expected: PASS. (If `manifest.Load` rejects the form because `admin_form` validation requires every field key to exist in `json_schema.properties`, ensure the test's `json_schema` lists all three property keys — it does. If it still fails, READ `manifest.go`'s admin-form validation and adjust the test's json_schema to satisfy it; do NOT loosen the loader.)

- [ ] **Step 3: Commit.**
```bash
git add pkg/pluginsdk/manifest/admin_form_rich_test.go
git commit -m "test(manifest): accept rich admin_form (sections, multi-select, conditions, validation)"
```

---

# PHASE 2 — `SchemaForm` renderer engine (`/opt/silo/web`)

### Task 4: Extend the TS schema types

**Files:** Modify `web/src/api/types.ts`.

- [ ] **Step 1: Add the new interfaces + extend existing** (near `PluginAdminFormField`):
```ts
export interface PluginAdminFormCondition {
  field: string;
  equals: string[];
}

export interface PluginAdminFormValidation {
  has_min?: boolean;
  min?: number;
  has_max?: boolean;
  max?: number;
  pattern?: string;
  min_length?: number;
  max_length?: number;
}

export interface PluginAdminFormSection {
  key: string;
  title: string;
  description?: string;
  collapsible: boolean;
  collapsed_default: boolean;
  field_keys: string[];
  show_when?: PluginAdminFormCondition[];
}
```
Change `PluginAdminFormField.control` union to include `"MULTI_SELECT"`, and append:
```ts
  dynamic_options?: boolean;
  show_when?: PluginAdminFormCondition[];
  validation?: PluginAdminFormValidation;
```
Append to `PluginAdminForm`:
```ts
  sections?: PluginAdminFormSection[];
```

- [ ] **Step 2: Typecheck.** Run: `cd /opt/silo/web && pnpm exec tsc -b` → exit 0 (additive optional fields; nothing breaks).

- [ ] **Step 3: Commit.**
```bash
git add web/src/api/types.ts
git commit -m "feat(web): extend plugin admin-form TS types (sections, conditions, validation, multi-select)"
```

---

### Task 5: Pure schema-form utilities + tests

**Files:** Create `web/src/components/admin/plugins/schemaForm.ts`; Test `web/src/components/admin/plugins/schemaForm.test.ts`.

- [ ] **Step 1: Write the failing test** (`schemaForm.test.ts`):
```ts
import { describe, expect, it } from "vitest";
import type { PluginAdminForm } from "@/api/types";
import { evaluateShowWhen, validateSchemaValues, buildSchemaValues } from "./schemaForm";

const descriptor: PluginAdminForm = {
  fields: [
    { key: "service_kind", label: "Service", control: "SELECT", required: true, secret: false, multiline: false,
      options: [{ value: "radarr", label: "Radarr" }, { value: "sonarr", label: "Sonarr" }] },
    { key: "quality_profile_id", label: "QP", control: "SELECT", required: true, secret: false, multiline: false, dynamic_options: true },
    { key: "tags", label: "Tags", control: "MULTI_SELECT", required: false, secret: false, multiline: false, dynamic_options: true },
    { key: "season_folder", label: "Season folder", control: "SWITCH", required: false, secret: false, multiline: false,
      show_when: [{ field: "service_kind", equals: ["sonarr"] }] },
  ],
};

describe("evaluateShowWhen", () => {
  it("shows when all conditions match (stringified)", () => {
    expect(evaluateShowWhen([{ field: "service_kind", equals: ["sonarr"] }], { service_kind: "sonarr" })).toBe(true);
    expect(evaluateShowWhen([{ field: "service_kind", equals: ["sonarr"] }], { service_kind: "radarr" })).toBe(false);
  });
  it("matches booleans by stringified value", () => {
    expect(evaluateShowWhen([{ field: "anime_enabled", equals: ["true"] }], { anime_enabled: true })).toBe(true);
    expect(evaluateShowWhen([{ field: "anime_enabled", equals: ["true"] }], { anime_enabled: false })).toBe(false);
  });
  it("empty conditions => always visible", () => {
    expect(evaluateShowWhen(undefined, {})).toBe(true);
  });
});

describe("validateSchemaValues", () => {
  it("flags required visible fields that are empty", () => {
    const errs = validateSchemaValues(descriptor, { service_kind: "radarr" });
    expect(errs.quality_profile_id).toMatch(/required/i);
    expect(errs.service_kind).toBeUndefined();
  });
  it("ignores required fields hidden by show_when", () => {
    // season_folder is not required, but prove hidden fields are skipped entirely:
    const d: PluginAdminForm = { fields: [{ key: "x", label: "X", control: "TEXT", required: true, secret: false, multiline: false, show_when: [{ field: "k", equals: ["yes"] }] }] };
    expect(validateSchemaValues(d, { k: "no" })).toEqual({});
  });
});

describe("buildSchemaValues", () => {
  it("coerces multi-select to an array and numbers to numbers", () => {
    const out = buildSchemaValues(descriptor, { service_kind: "sonarr", quality_profile_id: "3", tags: ["1", "2"], season_folder: true });
    expect(out.quality_profile_id).toBe(3);
    expect(out.tags).toEqual([1, 2]);
    expect(out.season_folder).toBe(true);
  });
});
```

- [ ] **Step 2: Run → fail.** Run: `cd /opt/silo/web && pnpm exec vitest run src/components/admin/plugins/schemaForm.test.ts`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement `schemaForm.ts`:**
```ts
import type { PluginAdminForm, PluginAdminFormCondition, PluginAdminFormField } from "@/api/types";

export type SchemaOption = { value: string; label: string };

function stringify(value: unknown): string {
  if (typeof value === "boolean") return value ? "true" : "false";
  if (value === null || value === undefined) return "";
  return String(value);
}

export function evaluateShowWhen(
  conditions: PluginAdminFormCondition[] | undefined,
  values: Record<string, unknown>,
): boolean {
  if (!conditions || conditions.length === 0) return true;
  return conditions.every((c) => c.equals.includes(stringify(values[c.field])));
}

function isNumberControl(field: PluginAdminFormField): boolean {
  return field.control === "NUMBER";
}

function isEmpty(value: unknown): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === "string") return value.trim() === "";
  if (Array.isArray(value)) return value.length === 0;
  return false;
}

// validateSchemaValues returns per-field error messages for the VISIBLE fields
// only (hidden-by-show_when fields are skipped). Used for both inline display
// and the submit gate.
export function validateSchemaValues(
  descriptor: PluginAdminForm,
  values: Record<string, unknown>,
): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const field of descriptor.fields) {
    if (!evaluateShowWhen(field.show_when, values)) continue;
    const raw = values[field.key];
    if (field.required && isEmpty(raw)) {
      errors[field.key] = `${field.label || field.key} is required`;
      continue;
    }
    if (isEmpty(raw)) continue;
    const v = field.validation;
    if (typeof raw === "string") {
      if (v?.pattern && !new RegExp(v.pattern).test(raw)) {
        errors[field.key] = `${field.label || field.key} is invalid`;
      } else if (v?.min_length && raw.length < v.min_length) {
        errors[field.key] = `${field.label || field.key} must be at least ${v.min_length} characters`;
      } else if (v?.max_length && raw.length > v.max_length) {
        errors[field.key] = `${field.label || field.key} must be at most ${v.max_length} characters`;
      }
    }
    if (isNumberControl(field) && typeof raw === "string" && raw.trim() !== "") {
      const n = Number(raw);
      if (Number.isNaN(n)) errors[field.key] = `${field.label || field.key} must be a number`;
      else if (v?.has_min && n < (v.min ?? 0)) errors[field.key] = `${field.label || field.key} must be ≥ ${v.min}`;
      else if (v?.has_max && n > (v.max ?? 0)) errors[field.key] = `${field.label || field.key} must be ≤ ${v.max}`;
    }
  }
  return errors;
}

// coerceFieldValue turns the form's draft value into the typed value the
// backend expects for this control.
export function coerceFieldValue(field: PluginAdminFormField, raw: unknown): unknown {
  if (field.control === "SWITCH") return Boolean(raw);
  if (field.control === "MULTI_SELECT") {
    const arr = Array.isArray(raw) ? raw : [];
    // tag-style multi-selects carry integer-ish values; coerce when numeric.
    return arr.map((v) => (typeof v === "string" && /^-?\d+$/.test(v) ? Number(v) : v));
  }
  if (field.control === "NUMBER") {
    if (typeof raw === "number") return raw;
    if (typeof raw === "string" && raw.trim() !== "") {
      const n = Number(raw);
      return Number.isNaN(n) ? raw : n;
    }
    return undefined;
  }
  if (typeof raw === "string") {
    const t = raw.trim();
    return t === "" ? undefined : raw;
  }
  return raw;
}

// buildSchemaValues produces the typed plugin_config payload from a draft,
// dropping undefined/empty scalars (so omitted optionals don't persist "").
export function buildSchemaValues(
  descriptor: PluginAdminForm,
  draft: Record<string, unknown>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of descriptor.fields) {
    const coerced = coerceFieldValue(field, draft[field.key]);
    if (coerced === undefined) continue;
    out[field.key] = coerced;
  }
  return out;
}
```

- [ ] **Step 4: Run → pass.** Run: `pnpm exec vitest run src/components/admin/plugins/schemaForm.test.ts` → PASS.

- [ ] **Step 5: Commit.**
```bash
git add web/src/components/admin/plugins/schemaForm.ts web/src/components/admin/plugins/schemaForm.test.ts
git commit -m "feat(web): schema-form pure utils (show_when, validation, value coercion)"
```

---

### Task 6: The `SchemaForm` component + tests

**Files:** Create `web/src/components/admin/plugins/SchemaForm.tsx`; Test `web/src/components/admin/plugins/SchemaForm.test.tsx`.

- [ ] **Step 1: Write the failing test** (`SchemaForm.test.tsx`) — uses `@testing-library/react` (installed) for interaction:
```tsx
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { PluginAdminForm } from "@/api/types";
import { SchemaForm } from "./SchemaForm";

const descriptor: PluginAdminForm = {
  fields: [
    { key: "service_kind", label: "Service", control: "SELECT", required: true, secret: false, multiline: false,
      options: [{ value: "radarr", label: "Radarr" }, { value: "sonarr", label: "Sonarr" }] },
    { key: "season_folder", label: "Season folder", control: "SWITCH", required: false, secret: false, multiline: false,
      show_when: [{ field: "service_kind", equals: ["sonarr"] }] },
    { key: "root_folder", label: "Root folder", control: "SELECT", required: false, secret: false, multiline: false, dynamic_options: true },
  ],
  sections: [{ key: "main", title: "Library", collapsible: false, collapsed_default: false, field_keys: ["service_kind", "season_folder", "root_folder"] }],
};

function renderForm(values: Record<string, unknown>, extra: Partial<React.ComponentProps<typeof SchemaForm>> = {}) {
  const onChange = vi.fn();
  render(<SchemaForm descriptor={descriptor} values={values} onChange={onChange} {...extra} />);
  return { onChange };
}

describe("SchemaForm", () => {
  it("hides a field whose show_when is unmet", () => {
    renderForm({ service_kind: "radarr" });
    expect(screen.queryByText("Season folder")).toBeNull();
  });
  it("shows a field whose show_when is met", () => {
    renderForm({ service_kind: "sonarr" });
    expect(screen.getByText("Season folder")).toBeTruthy();
  });
  it("renders dynamic options for a dynamic_options select", () => {
    renderForm({}, { dynamicOptions: { root_folder: [{ value: "/movies", label: "/movies" }] } });
    expect(screen.getByText("Root folder")).toBeTruthy();
  });
  it("renders a server field error", () => {
    renderForm({ service_kind: "radarr" }, { errors: { service_kind: "bad service" } });
    expect(screen.getByText("bad service")).toBeTruthy();
  });
  it("emits onChange when a switch toggles", () => {
    const { onChange } = renderForm({ service_kind: "sonarr", season_folder: false });
    fireEvent.click(screen.getByRole("switch"));
    expect(onChange).toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run → fail.** Run: `cd /opt/silo/web && pnpm exec vitest run src/components/admin/plugins/SchemaForm.test.tsx` → FAIL (no module).

- [ ] **Step 3: Implement `SchemaForm.tsx`** (controlled; renders sections then any ungrouped fields; uses the shadcn controls from `PluginConfigForm.tsx`; MULTI_SELECT as a chip toggle list; merges `dynamicOptions` over static `options`; shows client errors from `validateSchemaValues` merged with the `errors` prop):
```tsx
import { useMemo, useState } from "react";

import type { PluginAdminForm, PluginAdminFormField, PluginAdminFormSection } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { evaluateShowWhen, validateSchemaValues, type SchemaOption } from "./schemaForm";

type Props = {
  descriptor: PluginAdminForm;
  values: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  errors?: Record<string, string>;
  dynamicOptions?: Record<string, SchemaOption[]>;
  idPrefix?: string;
};

function optionsFor(field: PluginAdminFormField, dynamic?: Record<string, SchemaOption[]>): SchemaOption[] {
  if (field.dynamic_options) return dynamic?.[field.key] ?? [];
  return (field.options ?? []).map((o) => ({ value: o.value, label: o.label }));
}

export function SchemaForm({ descriptor, values, onChange, errors, dynamicOptions, idPrefix = "schemaform" }: Props) {
  const clientErrors = useMemo(() => validateSchemaValues(descriptor, values), [descriptor, values]);
  const allErrors = { ...clientErrors, ...(errors ?? {}) };
  const byKey = useMemo(
    () => Object.fromEntries(descriptor.fields.map((f) => [f.key, f])),
    [descriptor.fields],
  );
  const grouped = new Set((descriptor.sections ?? []).flatMap((s) => s.field_keys));
  const ungrouped = descriptor.fields.filter((f) => !grouped.has(f.key));

  function setField(key: string, value: unknown) {
    onChange({ ...values, [key]: value });
  }

  function renderField(field: PluginAdminFormField) {
    if (!evaluateShowWhen(field.show_when, values)) return null;
    const id = `${idPrefix}-${field.key}`;
    const err = allErrors[field.key];
    const opts = optionsFor(field, dynamicOptions);
    return (
      <div key={field.key} className="space-y-2">
        <div className="space-y-1">
          <Label htmlFor={id}>{field.label || field.key}</Label>
          {field.description ? <p className="text-muted-foreground text-xs">{field.description}</p> : null}
        </div>
        {field.control === "SWITCH" ? (
          <div className="flex items-center gap-3 rounded-md border px-3 py-2">
            <Switch checked={Boolean(values[field.key])} onCheckedChange={(c) => setField(field.key, c)} />
            <span className="text-sm">{field.label || field.key}</span>
          </div>
        ) : field.control === "SELECT" ? (
          <Select value={String(values[field.key] ?? "")} onValueChange={(v) => setField(field.key, v)}>
            <SelectTrigger id={id}><SelectValue placeholder={field.placeholder || "Select"} /></SelectTrigger>
            <SelectContent>
              {opts.map((o) => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}
            </SelectContent>
          </Select>
        ) : field.control === "MULTI_SELECT" ? (
          <div className="flex flex-wrap gap-2">
            {opts.length === 0 ? <span className="text-muted-foreground text-xs">No options</span> : null}
            {opts.map((o) => {
              const selected = Array.isArray(values[field.key]) && (values[field.key] as unknown[]).map(String).includes(o.value);
              return (
                <Button key={o.value} type="button" size="xs" variant={selected ? "secondary" : "outline"}
                  onClick={() => {
                    const cur = (Array.isArray(values[field.key]) ? values[field.key] : []) as unknown[];
                    const curStr = cur.map(String);
                    const next = curStr.includes(o.value) ? curStr.filter((v) => v !== o.value) : [...curStr, o.value];
                    setField(field.key, next);
                  }}>
                  {o.label}
                </Button>
              );
            })}
          </div>
        ) : field.control === "TEXTAREA" || field.multiline ? (
          <textarea id={id} className="border-border bg-background min-h-24 w-full rounded-md border px-3 py-2 text-sm"
            rows={field.rows && field.rows > 0 ? field.rows : 4} value={String(values[field.key] ?? "")}
            placeholder={field.placeholder} onChange={(e) => setField(field.key, e.target.value)} />
        ) : (
          <Input id={id}
            type={field.control === "PASSWORD" || field.secret ? "password" : field.control === "NUMBER" ? "number" : "text"}
            value={String(values[field.key] ?? "")} placeholder={field.placeholder}
            onChange={(e) => setField(field.key, e.target.value)} />
        )}
        {err ? <p className="text-destructive text-xs">{err}</p> : null}
      </div>
    );
  }

  function renderSection(section: PluginAdminFormSection) {
    if (!evaluateShowWhen(section.show_when, values)) return null;
    const [open, setOpen] = useState(!section.collapsed_default);
    const fields = section.field_keys.map((k) => byKey[k]).filter(Boolean);
    return (
      <div key={section.key} className="space-y-3 rounded-md border p-3">
        <button type="button" className="flex w-full items-center justify-between text-left"
          onClick={() => section.collapsible && setOpen((o) => !o)}>
          <span className="text-sm font-medium">{section.title}</span>
          {section.collapsible ? <span className="text-muted-foreground text-xs">{open ? "Hide" : "Show"}</span> : null}
        </button>
        {section.description ? <p className="text-muted-foreground text-xs">{section.description}</p> : null}
        {open ? <div className="grid gap-4">{fields.map(renderField)}</div> : null}
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      {ungrouped.map(renderField)}
      {(descriptor.sections ?? []).map(renderSection)}
    </div>
  );
}
```
**Note:** `useState` inside `renderSection` violates the rules-of-hooks (it's called in a loop/closure). Extract `renderSection` into a top-level `<SchemaFormSection>` component (props: `section`, `values`, `byKey`, `renderField`) so the hook is at component top level. Apply this during implementation; the test above still passes either way, but ESLint `react-hooks/rules-of-hooks` will (correctly) flag the inline version — fix it by extracting the section component.

- [ ] **Step 4: Run → pass.** Run: `pnpm exec vitest run src/components/admin/plugins/SchemaForm.test.tsx` → PASS.

- [ ] **Step 5: Lint.** Run: `cd /opt/silo/web && pnpm run lint` → 0 errors (confirm the section-component extraction satisfied `react-hooks/rules-of-hooks`).

- [ ] **Step 6: Commit.**
```bash
git add web/src/components/admin/plugins/SchemaForm.tsx web/src/components/admin/plugins/SchemaForm.test.tsx
git commit -m "feat(web): SchemaForm renderer (controls, sections, show_when, dynamic options, errors)"
```

---

### Task 7: Refactor `PluginConfigForm` onto `SchemaForm`

**Files:** Modify `web/src/components/admin/plugins/PluginConfigForm.tsx`.

- [ ] **Step 1: Refactor** so `PluginConfigForm` keeps its public contract `{ schema, value, onSave(key,value), onTest?, isSaving, isTesting }` but delegates field rendering to `SchemaForm`. Replace the per-field JSX block (the `fields.map(...)` render) with:
```tsx
  // local controlled values for this schema
  const [values, setValues] = useState<Record<string, unknown>>(() =>
    Object.fromEntries(fields.map((f) => [f.key, valueForField(f, value)])),
  );
  useEffect(() => {
    setValues(Object.fromEntries(fields.map((f) => [f.key, valueForField(f, value)])));
  }, [fields, value]);

  const descriptor: PluginAdminForm = schema.admin_form ?? { fields };
  // …header/unsupported handling unchanged…
  return (
    <div className="space-y-3 rounded-md border p-3">
      {/* title/description block unchanged */}
      <SchemaForm descriptor={descriptor} values={values} onChange={setValues} />
      <div className="flex flex-wrap items-center gap-3">
        {/* Test button unchanged, but use buildSchemaValues(descriptor, values) */}
        <Button size="sm" variant="outline" disabled={isSaving || isTesting}
          onClick={() => onSave(schema.key, buildSchemaValues(descriptor, values))}>
          {schema.admin_form?.submit_label || "Save config"}
        </Button>
      </div>
    </div>
  );
```
Keep `parseJSONSchema`/`valueForField`/`humanizeKey` (the json_schema fallback that builds a `fields` array for plugins without `admin_form`). Replace `buildPayload` usages with `buildSchemaValues` from `schemaForm.ts` (they're equivalent for the static case; delete the local `buildPayload`). Import `SchemaForm` and `buildSchemaValues`.

- [ ] **Step 2: Verify the two existing call sites still build.** Run: `cd /opt/silo/web && pnpm exec tsc -b && pnpm run build` → exit 0. (Call sites: `pages/admin-settings/PluginsSettings.tsx:99`, `pages/AdminPlugins.tsx:295` — the contract is unchanged, so no edits there.)

- [ ] **Step 3: Run the existing/related tests + lint.** Run: `pnpm exec vitest run src/components/admin/plugins/ && pnpm run lint` → PASS / 0 errors.

- [ ] **Step 4: Commit.**
```bash
git add web/src/components/admin/plugins/PluginConfigForm.tsx
git commit -m "refactor(web): render PluginConfigForm via the shared SchemaForm engine"
```

---

# PHASE 3 — host: Validate plumbing + generic options + legacy derivation (`/opt/silo` backend)

### Task 8: `pluginhost` Validate client + provider Validate

**Files:** Modify `internal/pluginhost/client.go`; `internal/requests/provider.go`; Test `internal/requests/provider_test.go`.

- [ ] **Step 1: Add the pluginhost wrapper method** to `internal/pluginhost/client.go` (next to the other `RequestRouterClient` methods):
```go
func (c *RequestRouterClient) Validate(ctx context.Context, req *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error) {
	callCtx, cancel := ensureDeadline(ctx, c.timeout)
	defer cancel()
	return c.client.Validate(callCtx, req)
}
```

- [ ] **Step 2: Extend the provider seam** in `internal/requests/provider.go`:
  - Add to the `RouterClient` interface: `Validate(ctx context.Context, req *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error)`.
  - Add to the `RequestRouterProvider` interface: `Validate(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (fieldErrors map[string]string, formError string, err error)`.
  - Implement on `pluginRouterProvider`:
```go
func (p *pluginRouterProvider) Validate(ctx context.Context, installationID int, capabilityID string, conn ResolvedRouterConnection) (map[string]string, string, error) {
	client, err := p.resolver.RequestRouterClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, "", err
	}
	pc, err := routerProtoConn(conn)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Validate(ctx, &pluginv1.ValidateRequest{CapabilityId: capabilityID, Connection: pc})
	if err != nil {
		return nil, "", err
	}
	return resp.GetFieldErrors(), resp.GetFormError(), nil
}
```

- [ ] **Step 3: Add a provider test** in `provider_test.go` — extend `fakeRouterClient` with a configurable `Validate` (a `validateResp *pluginv1.ValidateResponse` field) and assert the translation:
```go
func TestPluginRouterProviderValidateTranslates(t *testing.T) {
	fc := &fakeRouterClient{validateResp: &pluginv1.ValidateResponse{
		FieldErrors: map[string]string{"is_default": "cannot be 4K"}, FormError: "",
	}}
	p := NewPluginRouterProvider(fakeRouterResolver{c: fc})
	fe, form, err := p.Validate(context.Background(), 1, "arr",
		ResolvedRouterConnection{ID: "c1", Config: map[string]any{"service_kind": "radarr"}})
	if err != nil || form != "" || fe["is_default"] != "cannot be 4K" {
		t.Fatalf("unexpected: fe=%v form=%q err=%v", fe, form, err)
	}
}
```
(Add `validateResp` to `fakeRouterClient` and a `Validate` method returning it.)

- [ ] **Step 4: Build + test.** Run: `cd /opt/silo && /opt/deployarr/.local/go-sdk/go/bin/go build ./internal/pluginhost/ ./internal/requests/ && /opt/deployarr/.local/go-sdk/go/bin/go test ./internal/requests/ -run Validate -count=1` → PASS.

- [ ] **Step 5: Commit.**
```bash
git add internal/pluginhost/client.go internal/requests/provider.go internal/requests/provider_test.go
git commit -m "feat(requests): RequestRouter Validate client + provider seam"
```

---

### Task 9: Service — call Validate on save, generic options, legacy derivation; retire arr options type

**Files:** Modify `internal/requests/service.go`, `internal/requests/types.go`; Test `internal/requests/service_test.go`.

- [ ] **Step 1: Add a structured validation error type** (in `internal/requests/errors.go` or service.go):
```go
// ValidationError carries plugin Validate results back to the API layer.
type ValidationError struct {
	FieldErrors map[string]string
	FormError   string
}

func (e *ValidationError) Error() string {
	if e.FormError != "" {
		return e.FormError
	}
	return "validation failed"
}
```

- [ ] **Step 2: Call `Validate` in `CreateIntegration`/`UpdateIntegration`** before persisting. After the existing `validateInstance(&in)` host checks and once `installation_id`/`plugin_config` are known, add a helper and call it from both:
```go
func (s *Service) validateViaPlugin(ctx context.Context, in Integration) error {
	if s.router == nil || in.InstallationID == nil {
		return nil // no backend to validate against; host-side validateInstance already ran
	}
	apiKey, err := s.resolveAPIKey(ctx, in)
	if err != nil {
		return err
	}
	conn := ResolvedRouterConnection{ID: in.ID, BaseURL: in.BaseURL, APIKey: apiKey, Config: in.PluginConfig}
	fe, form, err := s.router.Validate(ctx, *in.InstallationID, in.CapabilityID, conn)
	if err != nil {
		return err
	}
	if len(fe) > 0 || form != "" {
		return &ValidationError{FieldErrors: fe, FormError: form}
	}
	return nil
}
```
Call `if err := s.validateViaPlugin(ctx, in); err != nil { return nil, err }` in both Create and Update, after `validateInstance` and after `in.ID` is set (Create assigns the id before this).

- [ ] **Step 3: Derive legacy columns from `plugin_config` on save.** Add a helper and call it in Create/Update before persisting, so the client no longer needs to send `kind`/`is_default`/`is_default_4k`:
```go
func deriveLegacyColumns(in *Integration) {
	if in.PluginConfig == nil {
		return
	}
	if k, ok := in.PluginConfig["service_kind"].(string); ok && k != "" {
		in.Kind = k
	}
	if v, ok := in.PluginConfig["is_default"].(bool); ok {
		in.IsDefault = v
	}
	if v, ok := in.PluginConfig["is_default_4k"].(bool); ok {
		in.IsDefault4K = v
	}
	if v, ok := in.PluginConfig["is_4k"].(bool); ok {
		in.Is4K = v
	}
}
```
Call `deriveLegacyColumns(&in)` before `validateInstance`/persist in both Create and Update. (This keeps `ClearDefault` + admin grouping + the `validateInstance` is_default/is_4k checks working off the derived columns.)

- [ ] **Step 4: Make `LoadIntegrationOptions` return the generic map; delete `integrationOptionsFromRouter`.** Change `LoadIntegrationOptions` to return `map[string][]RouterOption` (the raw provider result) instead of `*IntegrationOptions`:
```go
func (s *Service) LoadIntegrationOptions(ctx context.Context, viewer Viewer, integration Integration) (map[string][]RouterOption, error) {
	// …existing admin check + stored-row backfill of base_url/api_key_ref/installation_id/capability_id/plugin_config…
	apiKey, err := s.resolveAPIKey(ctx, integration)
	if err != nil {
		return nil, err
	}
	if s.router == nil || integration.InstallationID == nil {
		return nil, fmt.Errorf("no fulfillment backend configured")
	}
	conn := ResolvedRouterConnection{ID: integration.ID, BaseURL: integration.BaseURL, APIKey: apiKey, Config: integration.PluginConfig}
	return s.router.ListConfigOptions(ctx, *integration.InstallationID, integration.CapabilityID, conn)
}
```
Delete `integrationOptionsFromRouter` and the `IntegrationOptions`/`IntegrationRootFolder`/`IntegrationQualityProfile`/`IntegrationTag` types in `types.go` (confirm via grep that nothing else references them; the handler in Task 10 changes to the generic shape).

- [ ] **Step 5: Tests.** In `service_test.go`: (a) `TestCreateIntegrationRejectedByPluginValidate` — fake provider returns a `field_errors` map → `CreateIntegration` returns a `*ValidationError` with those fields; (b) `TestSaveDerivesLegacyColumnsFromPluginConfig` — create with `PluginConfig{service_kind:"sonarr", is_default:true}` and empty top-level `Kind`/`IsDefault` → persisted integration has `Kind=="sonarr"`, `IsDefault==true` (assert via the fakeStore's saved row); (c) update `fakeRouterProvider` to implement `Validate` (default: no errors) so existing tests compile.

- [ ] **Step 6: Build + test.** Run: `/opt/deployarr/.local/go-sdk/go/bin/go build ./internal/requests/ && /opt/deployarr/.local/go-sdk/go/bin/go vet ./internal/requests/ && /opt/deployarr/.local/go-sdk/go/bin/go test ./internal/requests/ -count=1` → PASS.

- [ ] **Step 7: Commit.**
```bash
git add internal/requests/service.go internal/requests/errors.go internal/requests/types.go internal/requests/service_test.go
git commit -m "feat(requests): plugin Validate on save, generic options, derive legacy columns from plugin_config"
```

---

### Task 10: API handler — generic options response + validation 400

**Files:** Modify `internal/api/handlers/requests.go`.

- [ ] **Step 1: `HandleLoadIntegrationOptions`** now returns the generic map. Replace the body of the response with `writeJSON(w, http.StatusOK, options)` where `options` is the `map[string][]mediarequests.RouterOption` from the service (JSON-encodes to `{ "root_folder": [{"value":…,"label":…}], … }`). Ensure `RouterOption` has `json:"value"`/`json:"label"` tags (add them in provider.go if absent).

- [ ] **Step 2: Map `*ValidationError` to HTTP 400** in `HandleCreateIntegration`/`HandleUpdateIntegration`. After calling the service, before the generic error path:
```go
var verr *mediarequests.ValidationError
if errors.As(err, &verr) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error":        "validation_failed",
		"field_errors": verr.FieldErrors,
		"form_error":   verr.FormError,
	})
	return
}
```
(Place it inside the existing error handling, ahead of `writeRequestServiceError`.)

- [ ] **Step 3: Build (container — handlers need libvips).** Run the container recipe with `<CMD>` = `go build -buildvcs=false ./internal/api/... && go test -buildvcs=false ./internal/api/handlers/ -count=1`. Expected: build exit 0; handler tests pass (or "no tests" for the integration handlers).

- [ ] **Step 4: Commit.**
```bash
git add internal/api/handlers/requests.go internal/requests/provider.go
git commit -m "feat(api): generic options response + 400 with field_errors on plugin validation failure"
```

---

# PHASE 4 — requests admin page → SchemaForm (`/opt/silo/web`)

### Task 11: Generic options + validation-aware hooks

**Files:** Modify `web/src/api/types.ts`, `web/src/hooks/queries/useRequests.ts`.

- [ ] **Step 1: Replace the arr-shaped options type** in `types.ts`:
```ts
export type RequestIntegrationOptions = Record<string, { value: string; label: string }[]>;
```
Delete `RequestIntegrationRootFolder`/`RequestIntegrationQualityProfile`/`RequestIntegrationTag` (grep first; they were only used by the old options shape + the bespoke form being removed). Add a validation-error response type:
```ts
export interface RequestIntegrationValidationError {
  error: "validation_failed";
  field_errors?: Record<string, string>;
  form_error?: string;
}
```

- [ ] **Step 2: `useLoadRequestIntegrationOptions`** already returns `RequestIntegrationOptions` (now the generic map) — no signature change, just the type. Confirm it compiles. For create/update, the `api` client throws `ApiClientError` on 400; ensure the editor can read `field_errors` from the thrown error. READ `web/src/api/client.ts`'s `ApiClientError` — if it exposes the parsed body, use it; if not, add an optional `body?: unknown` to `ApiClientError` populated from the error JSON so the editor can surface `field_errors`. (Minimal, additive.)

- [ ] **Step 3: Typecheck.** Run: `cd /opt/silo/web && pnpm exec tsc -b` → exit 0.

- [ ] **Step 4: Commit.**
```bash
git add web/src/api/types.ts web/src/hooks/queries/useRequests.ts web/src/api/client.ts
git commit -m "feat(web): generic request-integration options type + surface validation errors"
```

---

### Task 12: Rewrite the connection editor to use `SchemaForm`

**Files:** Modify `web/src/pages/AdminRequests.tsx`.

This replaces the bespoke arr body with `SchemaForm`, keeps the host chrome, and switches grouping from radarr/sonarr to per-plugin.

- [ ] **Step 1: Add a dynamic-options hook** (top of the file or a sibling util): debounced `ListConfigOptions` fetch keyed on the connection draft:
```tsx
function useConnectionOptions(connectionID: string, draft: { base_url: string; api_key_ref: string; capability_id: string; installation_id?: number; plugin_config: Record<string, unknown> }) {
  const load = useLoadRequestIntegrationOptions();
  const [options, setOptions] = useState<RequestIntegrationOptions>({});
  const sig = JSON.stringify({ u: draft.base_url, k: draft.api_key_ref, i: draft.installation_id, c: draft.plugin_config });
  useEffect(() => {
    if (!draft.base_url.trim() || !draft.installation_id) return;
    const t = setTimeout(() => {
      load.mutateAsync({ id: connectionID || "new", body: { kind: "radarr", base_url: draft.base_url, api_key_ref: draft.api_key_ref || undefined, capability_id: draft.capability_id, installation_id: draft.installation_id, plugin_config: draft.plugin_config } })
        .then(setOptions).catch(() => setOptions({}));
    }, 400);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig]);
  return options;
}
```
(`kind` is now vestigial in the request body; keep sending a value to satisfy the existing type, or drop `kind` from `LoadRequestIntegrationOptionsRequest` and the server — drop it for cleanliness if the server no longer reads it after Task 9/10.)

- [ ] **Step 2: Replace `IntegrationFormState`'s arr fields with a generic `plugin_config` blob.** The editor's local state becomes the host chrome (`id`, `name`, `enabled`, `base_url`, `api_key_ref`, `has_api_key`, `installation_id`) + `pluginConfig: Record<string, unknown>`. Delete `integrationToForm`/`formToIntegration`'s arr-field packing; seed `pluginConfig` directly from `integration.plugin_config ?? {}` and emit it on save. Delete `boolOption`/`stringOption`/`numberConfig`/`numberArrayConfig`.

- [ ] **Step 3: Render the schema body via `SchemaForm`.** The editor resolves the capability descriptor from the selected installation: `const descriptor = installations.find(i => i.installationID === selectedID)?.capability.config_schema?.[0]?.admin_form;` Then:
```tsx
const options = useConnectionOptions(form.id, { base_url, api_key_ref, capability_id: REQUEST_ROUTER_CAPABILITY, installation_id: selectedInstallationID, plugin_config: pluginConfig });
// …chrome (name/enabled/base_url/api_key/plugin selector) unchanged…
{descriptor ? (
  <SchemaForm descriptor={descriptor} values={pluginConfig} onChange={setPluginConfig}
    dynamicOptions={options} errors={fieldErrors} />
) : <p className="text-muted-foreground text-sm">Select a plugin to configure this connection.</p>}
```
where `fieldErrors` is component state populated from a caught `ApiClientError.body.field_errors` on save.

- [ ] **Step 4: Save flow** builds the payload from chrome + `buildSchemaValues(descriptor, pluginConfig)`:
```tsx
function handleSave() {
  const payload: RequestIntegration = {
    id: form.id, name: form.name.trim(), enabled: form.enabled,
    base_url: form.base_url.trim(), api_key_ref: form.api_key_ref.trim() || undefined,
    capability_id: REQUEST_ROUTER_CAPABILITY, installation_id: selectedInstallationID,
    supported_media_types: [], // server derives nothing from this now; optional
    plugin_config: descriptor ? buildSchemaValues(descriptor, pluginConfig) : pluginConfig,
    // legacy top-level fields no longer sent; server derives kind/is_default* from plugin_config
    kind: "", is_4k: false, is_default: false, is_default_4k: false, root_folder: "", tags: [], anime_enabled: false, anime_tags: [],
  } as RequestIntegration;
  const mut = isNew ? createIntegration : updateIntegration;
  mut.mutate(payload, { onError: (e) => setFieldErrors(extractFieldErrors(e)) });
}
```
(`RequestIntegration`'s legacy fields are still required by the TS type until #8's cleanup; send zero-values — the server overwrites `kind`/`is_default*` via `deriveLegacyColumns`. `extractFieldErrors` reads `(e as ApiClientError).body?.field_errors`.) Gate `canSave` on `validateSchemaValues(descriptor, pluginConfig)` being empty + the existing name/base_url/api_key/installation checks.

- [ ] **Step 5: Switch grouping from radarr/sonarr to per-plugin.** Replace `INTEGRATION_KINDS.map(...)` grouping in `RequestIntegrationsForm` with grouping by `installation_id`/plugin display name (or a single flat list with an "Add connection" + plugin picker). `service_kind` is now just a schema field. Delete the `INTEGRATION_KINDS` constant and the radarr/sonarr section headers; `addCard()` no longer takes a `kind`.

- [ ] **Step 6: Lint + typecheck + build + tests.** Run: `cd /opt/silo/web && pnpm run lint && pnpm exec tsc -b && pnpm run build && pnpm exec vitest run src/` → all pass (fix any test that referenced the deleted `formToIntegration`/`IntegrationFormState`).

- [ ] **Step 7: Commit.**
```bash
git add web/src/pages/AdminRequests.tsx
git commit -m "feat(web): render request connections via SchemaForm; per-plugin grouping; retire bespoke arr form"
```

---

# PHASE 5 — arr plugin: enriched manifest + Validate (`/opt/silo-plugin-requests-arr`)

### Task 13: Enrich the manifest to one rich descriptor

**Files:** Modify `manifest.json`.

- [ ] **Step 1: Replace the 14 separate `config_schema` entries with ONE entry** whose `admin_form` declares all fields + two sections + show_when + dynamic_options + multi-select. Its `json_schema` is one object schema listing every property. Full content:
```json
{
  "type": "request_router.v1",
  "id": "arr",
  "display_name": "Sonarr / Radarr",
  "description": "Fulfills movie/series requests against multi-instance Sonarr/Radarr.",
  "config_schema": [
    {
      "key": "connection",
      "title": "Connection",
      "json_schema": "{\"type\":\"object\",\"properties\":{\"service_kind\":{\"type\":\"string\"},\"root_folder\":{\"type\":\"string\"},\"quality_profile_id\":{\"type\":\"integer\"},\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"integer\"}},\"is_default\":{\"type\":\"boolean\"},\"is_4k\":{\"type\":\"boolean\"},\"is_default_4k\":{\"type\":\"boolean\"},\"search_on_add\":{\"type\":\"boolean\"},\"minimum_availability\":{\"type\":\"string\"},\"series_type\":{\"type\":\"string\"},\"season_folder\":{\"type\":\"boolean\"},\"anime_enabled\":{\"type\":\"boolean\"},\"anime_root_folder\":{\"type\":\"string\"},\"anime_quality_profile_id\":{\"type\":\"integer\"},\"anime_tags\":{\"type\":\"array\",\"items\":{\"type\":\"integer\"}}}}",
      "admin_form": {
        "submit_label": "Save connection",
        "fields": [
          {"key":"service_kind","label":"Service","control":"ADMIN_FORM_CONTROL_SELECT","required":true,"options":[{"value":"radarr","label":"Radarr (movies)"},{"value":"sonarr","label":"Sonarr (series)"}]},
          {"key":"root_folder","label":"Root folder","control":"ADMIN_FORM_CONTROL_SELECT","dynamic_options":true},
          {"key":"quality_profile_id","label":"Quality profile","control":"ADMIN_FORM_CONTROL_SELECT","required":true,"dynamic_options":true},
          {"key":"tags","label":"Tags","control":"ADMIN_FORM_CONTROL_MULTI_SELECT","dynamic_options":true},
          {"key":"is_default","label":"Default (HD/1080p)","control":"ADMIN_FORM_CONTROL_SWITCH"},
          {"key":"is_4k","label":"4K instance","control":"ADMIN_FORM_CONTROL_SWITCH"},
          {"key":"is_default_4k","label":"Default 4K (2160p)","control":"ADMIN_FORM_CONTROL_SWITCH","show_when":[{"field":"is_4k","equals":["true"]}]},
          {"key":"search_on_add","label":"Search on add","control":"ADMIN_FORM_CONTROL_SWITCH"},
          {"key":"minimum_availability","label":"Minimum availability","control":"ADMIN_FORM_CONTROL_SELECT","show_when":[{"field":"service_kind","equals":["radarr"]}],"options":[{"value":"announced","label":"Announced"},{"value":"inCinemas","label":"In cinemas"},{"value":"released","label":"Released"}]},
          {"key":"series_type","label":"Series type","control":"ADMIN_FORM_CONTROL_SELECT","show_when":[{"field":"service_kind","equals":["sonarr"]}],"options":[{"value":"standard","label":"Standard"},{"value":"daily","label":"Daily"},{"value":"anime","label":"Anime"}]},
          {"key":"season_folder","label":"Season folder","control":"ADMIN_FORM_CONTROL_SWITCH","show_when":[{"field":"service_kind","equals":["sonarr"]}]},
          {"key":"anime_enabled","label":"Enable anime overrides","control":"ADMIN_FORM_CONTROL_SWITCH"},
          {"key":"anime_root_folder","label":"Anime root folder","control":"ADMIN_FORM_CONTROL_SELECT","dynamic_options":true,"show_when":[{"field":"anime_enabled","equals":["true"]}]},
          {"key":"anime_quality_profile_id","label":"Anime quality profile","control":"ADMIN_FORM_CONTROL_SELECT","dynamic_options":true,"show_when":[{"field":"anime_enabled","equals":["true"]}]},
          {"key":"anime_tags","label":"Anime tags","control":"ADMIN_FORM_CONTROL_MULTI_SELECT","dynamic_options":true,"show_when":[{"field":"anime_enabled","equals":["true"]}]}
        ],
        "sections": [
          {"key":"library","title":"Library","collapsible":false,"collapsed_default":false,"field_keys":["service_kind","root_folder","quality_profile_id","tags","is_default","is_4k","is_default_4k","search_on_add","minimum_availability","series_type","season_folder","anime_enabled"]},
          {"key":"anime","title":"Anime overrides","collapsible":true,"collapsed_default":true,"field_keys":["anime_root_folder","anime_quality_profile_id","anime_tags"],"show_when":[{"field":"anime_enabled","equals":["true"]}]}
        ]
      }
    }
  ]
}
```

- [ ] **Step 2: Validate the manifest loads** (the plugin embeds + checksums it at startup; manifest.Load validates admin_form field keys ⊆ json_schema properties). Run: `cd /opt/silo-plugin-requests-arr && python3 -m json.tool manifest.json >/dev/null && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1` and confirm the existing `TestEmbeddedManifestLoads` still passes (it asserts plugin_id + one request_router.v1 capability; the rich admin_form must `manifest.Load` cleanly — if it errors on the field-keys-⊆-properties check, ensure every `admin_form` field key appears in the json_schema `properties` above; it does).

- [ ] **Step 3: Commit.**
```bash
git add manifest.json
git commit -m "feat: declare rich request_router config schema (sections, dynamic options, conditions, multi-select)"
```

---

### Task 14: Implement the `Validate` RPC in the arr plugin

**Files:** Modify `internal/router/server.go`; Test `internal/router/server_test.go`.

- [ ] **Step 1: Write the failing test** (`server_test.go`):
```go
func TestValidateRejectsHDDefaultThatIsAlso4K(t *testing.T) {
	cfg, _ := structpb.NewStruct(map[string]any{"service_kind": "radarr", "is_default": true, "is_4k": true})
	resp, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		CapabilityId: "arr", Connection: &pluginv1.RouterConnection{Id: "c1", Config: cfg},
	})
	if err != nil {
		t.Fatalf("Validate err: %v", err)
	}
	if resp.GetFieldErrors()["is_default"] == "" && resp.GetFormError() == "" {
		t.Fatal("expected a validation error for HD default that is also 4K")
	}
}

func TestValidateRejects4KDefaultOnNon4K(t *testing.T) {
	cfg, _ := structpb.NewStruct(map[string]any{"service_kind": "radarr", "is_default_4k": true, "is_4k": false})
	resp, _ := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		CapabilityId: "arr", Connection: &pluginv1.RouterConnection{Id: "c1", Config: cfg},
	})
	if resp.GetFieldErrors()["is_default_4k"] == "" && resp.GetFormError() == "" {
		t.Fatal("expected a validation error for 4K default on a non-4K server")
	}
}

func TestValidateAcceptsConsistentConfig(t *testing.T) {
	cfg, _ := structpb.NewStruct(map[string]any{"service_kind": "radarr", "is_default": true, "is_4k": false})
	resp, _ := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		CapabilityId: "arr", Connection: &pluginv1.RouterConnection{Id: "c1", Config: cfg},
	})
	if len(resp.GetFieldErrors()) != 0 || resp.GetFormError() != "" {
		t.Fatalf("expected no errors, got fe=%v form=%q", resp.GetFieldErrors(), resp.GetFormError())
	}
}
```

- [ ] **Step 2: Run → fail.** Run: `cd /opt/silo-plugin-requests-arr && /opt/deployarr/.local/go-sdk/go/bin/go test ./internal/router/ -run TestValidate -count=1` → FAIL (no `Validate` method).

- [ ] **Step 3: Implement `Validate`** on `*Server` in `internal/router/server.go` (reuse `instanceFromConnection` to parse the config):
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
	return &pluginv1.ValidateResponse{FieldErrors: fieldErrors}, nil
}
```

- [ ] **Step 4: Run → pass.** Run: `/opt/deployarr/.local/go-sdk/go/bin/go test ./internal/router/ -run TestValidate -count=1 -v` → PASS. Then full module: `go build ./... && go test ./... -count=1` → PASS (the `Server` now satisfies the regenerated `RequestRouterServer` interface incl. `Validate`).

- [ ] **Step 5: Commit.**
```bash
git add internal/router/server.go internal/router/server_test.go
git commit -m "feat(router): implement Validate (HD!=4K, 4K-default-must-be-4K)"
```

---

# PHASE 6 — Verification

### Task 15: Full cross-repo verification

- [ ] **Step 1: SDK suite.** `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go test ./... ` → all PASS.
- [ ] **Step 2: Plugin suite.** `cd /opt/silo-plugin-requests-arr && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1` → PASS.
- [ ] **Step 3: Host container build + touched tests.** Run the container recipe with `<CMD>` = `go build -buildvcs=false ./... && go test -buildvcs=false ./internal/requests/... ./internal/api/... ./internal/pluginhost/... -count=1`. Expected: whole tree compiles; suites PASS.
- [ ] **Step 4: Frontend gates.** `cd /opt/silo/web && pnpm run lint && pnpm exec tsc -b && pnpm run build && pnpm exec vitest run src/` → lint 0 errors, tsc/build exit 0, all tests PASS.
- [ ] **Step 5: Local-paths guard.** `cd /opt/silo && make verify-local-paths` → PASS.
- [ ] **Step 6: Manual back-compat smoke (deferred to a live env):** an existing connection (with `plugin_config`) renders all fields in the new `SchemaForm`; dynamic dropdowns populate via the options endpoint; toggling `is_4k` reveals `is_default_4k`; setting `service_kind=sonarr` reveals series_type/season_folder and hides minimum_availability; saving an HD-default-that-is-4K shows the inline `Validate` error. Note this is post-publish (needs the installed plugin + a live arr) and is recorded as deferred.
- [ ] **Step 7: Confirm no client follow-up** — the request lifecycle/API for end users is unchanged (only admin config UX changed), so no `silo-android`/`silo-apple` work. Note in the eventual PR.

---

## Self-Review

- **Spec coverage:** §4 SDK contract → Tasks 1-3. §5 SchemaForm renderer → Tasks 4-7. §6 host Validate plumbing + generic options + legacy derivation + retirements → Tasks 8-10, 12. §7 arr manifest + Validate → Tasks 13-14. §8 data flow (dynamic options + validate-on-save) → Tasks 9, 10, 12. §9 errors → Tasks 6 (field errors), 10 (400 mapping), 12 (surface). §10 testing → Tasks 3, 5, 6, 8, 9, 14, 15. §11 sequencing → phase order. §12 out-of-scope respected (legacy column DROP not done; Seerr not built). Covered.
- **Type consistency:** `SchemaForm` props (`descriptor`/`values`/`onChange`/`errors`/`dynamicOptions`) identical in Tasks 6, 7, 12. Pure utils (`evaluateShowWhen`/`validateSchemaValues`/`buildSchemaValues`/`coerceFieldValue`) defined in Task 5, used in 6/7/12. Proto names (`AdminFormCondition`/`AdminFormValidation`/`AdminFormSection`, `ValidateRequest`/`ValidateResponse{field_errors,form_error}`) consistent across Tasks 1-2 (SDK), 8-10 (host), 14 (plugin). `RouterOption{value,label}` JSON tags added in Task 10 and consumed by Task 11's generic type.
- **Known risks flagged inline:** the `useState`-in-loop rules-of-hooks issue (Task 6 Step 3 note → extract section component); `kind` becoming vestigial in the options request (Task 12 Step 1); `ApiClientError.body` may need an additive field to carry `field_errors` (Task 11 Step 2). Each names the concrete fix.
- **Placeholders:** none — every code step shows the code; relocation/deletion steps name exact symbols + grep-before-delete.
