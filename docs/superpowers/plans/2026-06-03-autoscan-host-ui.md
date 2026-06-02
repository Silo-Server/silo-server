# Autoscan Admin UI Implementation Plan (Part 2 of 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Autoscan its own admin category (page + sidebar nav), separate from Requests: manage Connections (reuse a Requests arr server or enter own), Sources (per scan-source plugin: enable, interval, connection), and global settings — backed by the Part-1 v2 API.

**Architecture:** Extract the Autoscan tab out of `AdminRequests.tsx` into a standalone `AdminAutoscan.tsx` page; register a route and a sidebar item; rebuild `useAutoscan.ts` hooks + `types.ts` against the v2 endpoints (`/admin/autoscan/{settings,connections,sources,trigger,status}`).

**Tech Stack:** React + TypeScript, React Query (`@tanstack/react-query`), the project's `ui/` component library (`Tabs`, `Table`, `Card`, etc.), `react-router` routes in `App.tsx`, 2-space indent / double quotes / 100-col (`web/.prettierrc`).

Commands assume the silo-server repository root (`/opt/silo`) is the cwd. **Depends on Part 1 (backend)** being implemented (the v2 endpoints must exist).

---

## Prerequisites

1. **Part-1 backend merged/available** — the v2 endpoints and response shapes (connections with `request_integration_id`, sources with `marker`/`last_error`, settings with `default_poll_interval_seconds`).
2. **Reference the existing autoscan tab** at `web/src/pages/AdminRequests.tsx` (the `autoscan` `TabsContent`, the `AutoscanTab`/`AutoscanSourceEditor` components) and hooks at `web/src/hooks/queries/useAutoscan.ts` — these are the starting material; they are reshaped, not discarded.
3. **Run before MR:** `cd web && pnpm run lint && pnpm run format:check`. Note the 20 pre-existing failing frontend tests are unrelated (project memory); do not be alarmed if they appear — only newly-introduced failures matter.

---

## File Structure

| File | Responsibility | Action |
|---|---|---|
| `web/src/api/types.ts` | `AutoscanConnection`, `AutoscanSource`, `AutoscanSettings` (v2 shapes) | Modify |
| `web/src/hooks/queries/useAutoscan.ts` | hooks for settings/connections/sources/trigger/status | Rewrite |
| `web/src/hooks/queries/keys.ts` | query keys for connections/sources | Modify |
| `web/src/pages/AdminAutoscan.tsx` | the new standalone Autoscan page (Connections + Sources + Settings tabs) | Create |
| `web/src/pages/admin/autoscan/ConnectionsPanel.tsx` | list/add/edit/delete connections (own or Requests-linked) | Create |
| `web/src/pages/admin/autoscan/SourcesPanel.tsx` | per-source enable, interval, connection binding, status | Create |
| `web/src/pages/AdminRequests.tsx` | remove the Autoscan tab | Modify |
| `web/src/App.tsx` | add `<Route path="autoscan" element={<AdminAutoscan />} />` | Modify |
| `web/src/components/AdminSidebar.tsx` | add the Autoscan nav item under "Content" | Modify |

---

## Task 1: v2 types + hooks

**Files:** Modify `web/src/api/types.ts`, `web/src/hooks/queries/keys.ts`; rewrite `web/src/hooks/queries/useAutoscan.ts`.

- [ ] **Step 1: Types**

Add to `web/src/api/types.ts`:

```ts
export interface AutoscanSettings {
  enabled: boolean;
  default_poll_interval_seconds: number;
  debounce_seconds: number;
}

export interface AutoscanConnection {
  id: string;
  name: string;
  kind: string;
  // present when the connection has its own credentials:
  base_url?: string;
  // present when linked to a Requests integration (live reuse):
  request_integration_id?: string | null;
  // NOTE: api_key_ref / resolved keys are never sent by the backend.
}

export interface AutoscanSource {
  id: string;
  installation_id: number;
  capability_id: string;
  connection_id: string;
  enabled: boolean;
  poll_interval_seconds: number | null;
  last_run_at: string | null;
  last_error: string | null;
}
```

- [ ] **Step 2: Hooks**

Rewrite `useAutoscan.ts` with: `useAutoscanSettings`/`useUpdateAutoscanSettings`, `useAutoscanConnections`/`useCreateAutoscanConnection`/`useUpdateAutoscanConnection`/`useDeleteAutoscanConnection`, `useAutoscanSources`/`useUpdateAutoscanSource`, `useAutoscanStatus`, `useTriggerAutoscan`. Follow the existing hook style (the current `useAutoscan.ts` is the template for query/mutation + invalidation). Add query keys in `keys.ts`.

- [ ] **Step 3: Typecheck**

Run: `cd web && pnpm exec tsc --noEmit`
Expected: no type errors in the autoscan files.

- [ ] **Step 4: Commit**

```bash
git add web/src/api/types.ts web/src/hooks/queries/useAutoscan.ts web/src/hooks/queries/keys.ts
git commit -m "feat(web): autoscan v2 types and query hooks"
```

---

## Task 2: Connections panel

**Files:** Create `web/src/pages/admin/autoscan/ConnectionsPanel.tsx`.

- [ ] **Step 1: Build the panel**

A `Card` + `Table` listing connections (name, kind, and a "Reused from Requests" / "Own" badge derived from `request_integration_id`). An "Add connection" dialog offers two modes:
- **Reuse from Requests** — a select populated from the existing request-integrations hook (filter to arr kinds), storing `request_integration_id`.
- **Enter own** — name + URL + API key fields, posting own credentials.

Edit + delete actions per row. Never render any key material (the backend doesn't send it).

- [ ] **Step 2: Lint/format/typecheck**

Run: `cd web && pnpm run lint && pnpm exec tsc --noEmit`
Expected: clean.

- [ ] **Step 3: Commit** `feat(web): autoscan connections panel (reuse or own)`.

---

## Task 3: Sources panel

**Files:** Create `web/src/pages/admin/autoscan/SourcesPanel.tsx`.

- [ ] **Step 1: Build the panel**

A `Table` of sources, one row per installed `scan_source` capability instance: plugin/capability label, a connection `<select>` (bound to `connection_id`), an enable toggle, a poll-interval input (blank = settings default), and a status column (`last_run_at`, `last_error`). Reuse the spinner/"Syncing…" affordances from the old `AutoscanSourceEditor` where relevant. Wire to `useAutoscanSources`/`useUpdateAutoscanSource`.

- [ ] **Step 2: Lint/format/typecheck** — clean.
- [ ] **Step 3: Commit** `feat(web): autoscan sources panel`.

---

## Task 4: The Autoscan page + global settings + manual trigger

**Files:** Create `web/src/pages/AdminAutoscan.tsx`.

- [ ] **Step 1: Compose the page**

A page with a `Tabs` (or sections): **Sources**, **Connections**, **Settings**. Settings = global enable toggle, default poll interval, debounce. A "Run now" button calling `useTriggerAutoscan` (202 → toast "Autoscan triggered"). Mirror the header/layout of `AdminRequests.tsx` for visual consistency.

- [ ] **Step 2: Lint/format/typecheck** — clean.
- [ ] **Step 3: Commit** `feat(web): standalone Autoscan admin page`.

---

## Task 5: Route + sidebar nav

**Files:** Modify `web/src/App.tsx`, `web/src/components/AdminSidebar.tsx`.

- [ ] **Step 1: Route**

In `web/src/App.tsx`, next to `<Route path="requests" element={<AdminRequests />} />` (l.374), add:

```tsx
<Route path="autoscan" element={<AdminAutoscan />} />
```

with the matching `import AdminAutoscan from "@/pages/AdminAutoscan";`.

- [ ] **Step 2: Sidebar item**

In `web/src/components/AdminSidebar.tsx`, under the **Content** group (after the Requests item, ~l.112), add:

```tsx
{
  label: "Autoscan",
  icon: <RefreshCw className="h-[18px] w-[18px]" />,
  href: "/admin/autoscan",
},
```

(Import an appropriate icon from the icon set already used in the file.)

- [ ] **Step 3: Verify navigation**

Run: `cd web && pnpm run build`
Expected: build succeeds. Manually confirm `/admin/autoscan` renders and the sidebar item highlights.

- [ ] **Step 4: Commit** `feat(web): route and sidebar nav for Autoscan category`.

---

## Task 6: Remove the Autoscan tab from Requests

**Files:** Modify `web/src/pages/AdminRequests.tsx`.

- [ ] **Step 1: Excise the tab**

Remove the `<TabsTrigger value="autoscan">` (l.146) and its `<TabsContent value="autoscan">`, the `AutoscanTab`/`AutoscanSourceEditor` definitions (now superseded by the new panels), and `autoscan` from `ADMIN_REQUEST_TABS`. Remove now-unused imports.

- [ ] **Step 2: Lint/format/typecheck/build**

Run: `cd web && pnpm run lint && pnpm run format:check && pnpm run build`
Expected: clean; build succeeds; no dangling references to removed components.

- [ ] **Step 3: Commit** `refactor(web): move Autoscan out of Requests into its own category`.

---

## Task 7: Final sweep

- [ ] **Step 1:** `cd web && pnpm run lint && pnpm run format:check && pnpm run build` — all clean.
- [ ] **Step 2:** `cd web && pnpm test` — confirm no **newly** failing tests (the 20 pre-existing failures, per project memory, are unrelated; compare against baseline).
- [ ] **Step 3:** Manual pass: add a connection (both modes), bind a source, toggle enable, "Run now", confirm status updates.

---

## Self-Review notes

- **Spec coverage:** §9 (Autoscan as its own category, decoupled from Requests; Connections reuse-or-own; per-source enable/interval/status) — Tasks 1–6. The reuse-from-Requests UI (Task 2) surfaces the soft link from spec §8.
- **Salvage:** the old `AutoscanTab`/`AutoscanSourceEditor` and `useAutoscan.ts` are reshaped (Tasks 1–3), then the Requests tab is removed (Task 6).
- **Out of scope:** plugin-side config UI (the path-rewrite table lives on the plugin's own settings screen per spec §10 — that is the arr-plugin plan / spec §14 risk #2, not this page).
- **Type consistency:** `AutoscanConnection{request_integration_id?}`, `AutoscanSource{connection_id,poll_interval_seconds,last_error}`, `AutoscanSettings{default_poll_interval_seconds}` used consistently across hooks (Task 1) and panels (Tasks 2–4).
