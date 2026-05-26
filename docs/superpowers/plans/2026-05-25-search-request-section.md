# Search Request Section Implementation Plan

Commands assume the repository root is the cwd.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a TMDB-backed "Request to Add" section beneath library results in both the Cmd+K search dialog (`GlobalSearch`) and the Catalog search results page, so users can discover and request items missing from the library without leaving the search flow.

**Architecture:** No backend changes. Frontend fires two parallel react-query queries — library FTS via the existing `/api/v1/catalog` endpoint, and TMDB via the existing `/api/v1/requests/search` endpoint. A new `useCanRequest()` hook gates whether the TMDB query fires (admin `RequestsEnabled` + authenticated viewer with a profile). Per-row UI state (blocked / quota / pending / etc.) is driven by the backend-enriched `request.requestable` and `request.reason` fields already returned per result. The existing `useRequestSearch` hook is extended to forward `AbortSignal`, key its cache by viewer identity, and be invalidated on auth/profile/policy mutations.

**Tech Stack:** React 18, TypeScript, vitest, @tanstack/react-query, react-router, Tailwind. All changes are in `web/` (Go backend untouched).

**Reference spec:** `docs/superpowers/specs/2026-05-25-search-request-section-design.md`

---

## File Structure

**New files:**

- `web/src/hooks/useCanRequest.ts` — gating hook returning `{ discoveryEnabled, submitDisabledReason }`.
- `web/src/hooks/useCanRequest.test.ts` — hook unit tests.
- `web/src/components/RequestToAddSection.tsx` — shared section component with `variant="dialog"` and `variant="grid"`.
- `web/src/components/RequestToAddSection.test.tsx` — component tests.

**Modified files:**

- `web/src/api/client.ts` — extend `api()` to forward `AbortSignal` from `RequestInit`.
- `web/src/hooks/queries/keys.ts` — extend `requestKeys.search()` to include viewer key.
- `web/src/hooks/queries/useRequests.ts` — extend `useRequestSearch` to accept `signal`, include viewer in key, and add invalidation helpers; wire invalidation into existing settings/limit mutations.
- `web/src/components/RequestPosterCard.tsx` — make `onRequest` and `isSubmitting` optional on `DiscoverProps`; suppress the hover Request button when `onRequest` is undefined.
- `web/src/components/GlobalSearch.tsx` — wire the second query and render `RequestToAddSection` with `variant="dialog"`.
- `web/src/components/GlobalSearch.test.tsx` — add tests for the new section behavior.
- `web/src/pages/Catalog.tsx` — render `RequestToAddSection` with `variant="grid"` below the existing `ItemGrid` when `source === "query"`.
- `web/src/pages/Catalog.test.ts` (or `.tsx` if new) — add tests for the section behavior in the full-page surface.

---

## Design notes on `submitDisabledReason`

The spec calls for `useCanRequest()` to return `submitDisabledReason: string | null`. The backend already enriches each TMDB result with per-row `request.requestable: boolean` and `request.reason?: string` via `enrichPage()` → `presence.Lookup()`. That per-row data is the canonical source of truth for the disabled state. The viewer-level field is included in the hook's return type for spec conformance and future use, but its value is `null` in this implementation. Per-row UI uses the result's own `request.requestable` and `request.reason` directly. This is consistent with the existing `RequestPosterCard` which already renders a "blocked" StatusRibbon when a row is not requestable.

---

## Task 1: Pin `api()` `AbortSignal` forwarding via test

**Files:**
- Create: `web/src/api/client.test.ts`

`api()` at `web/src/api/client.ts:337` calls `fetch(\`/api/v1${path}\`, { ...options, headers })`. The `...options` spread already forwards `signal` to `fetch`, so behavior is correct today. This task does NOT change behavior — it adds a regression test that locks in the contract so a future refactor cannot accidentally drop signal forwarding.

- [ ] **Step 1: Write the test**

Create `web/src/api/client.test.ts`:

```typescript
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { api } from "./client";

describe("api()", () => {
  let originalFetch: typeof fetch;

  beforeEach(() => {
    originalFetch = global.fetch;
  });

  afterEach(() => {
    global.fetch = originalFetch;
  });

  it("forwards AbortSignal from options to fetch", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    global.fetch = fetchMock as unknown as typeof fetch;

    const controller = new AbortController();
    await api("/test", { signal: controller.signal });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const call = fetchMock.mock.calls[0]!;
    const init = call[1] as RequestInit;
    expect(init.signal).toBe(controller.signal);
  });
});
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `cd web && pnpm vitest run src/api/client.test.ts`
Expected: PASS — the existing `...options` spread already forwards `signal`. No code change required.

- [ ] **Step 3: Commit**

```bash
git add web/src/api/client.test.ts
git commit -m "test(api): pin AbortSignal forwarding contract on api()"
```

---

## Task 2: Create `useCanRequest()` gating hook

**Files:**
- Create: `web/src/hooks/useCanRequest.ts`
- Create: `web/src/hooks/useCanRequest.test.ts`

`useCanRequest()` reads `useRequestFeatureStatus()` and `useCurrentProfile()` and returns `{ discoveryEnabled, submitDisabledReason }`. Discovery is enabled only when the admin flag is on AND there is a profile loaded. Per the design note above, `submitDisabledReason` is always `null` in this implementation — per-row data drives the actual UI.

- [ ] **Step 1: Write the failing test**

Create `web/src/hooks/useCanRequest.test.ts`:

```typescript
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useRequestFeatureStatus: vi.fn(),
  useCurrentProfile: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => mocks.useRequestFeatureStatus(),
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => mocks.useCurrentProfile(),
}));

import { useCanRequest } from "./useCanRequest";

function CaptureHook({ onResult }: { onResult: (r: ReturnType<typeof useCanRequest>) => void }) {
  const result = useCanRequest();
  onResult(result);
  return null;
}

function render(child: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(<QueryClientProvider client={client}>{child}</QueryClientProvider>);
}

describe("useCanRequest", () => {
  it("returns discoveryEnabled=false when requests_enabled is false", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: { requests_enabled: false } });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(<CaptureHook onResult={(r) => { captured = r; }} />);

    expect(captured).toEqual({ discoveryEnabled: false, submitDisabledReason: null });
  });

  it("returns discoveryEnabled=false when there is no profile", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: { requests_enabled: true } });
    mocks.useCurrentProfile.mockReturnValue({ profile: null });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(<CaptureHook onResult={(r) => { captured = r; }} />);

    expect(captured).toEqual({ discoveryEnabled: false, submitDisabledReason: null });
  });

  it("returns discoveryEnabled=true when requests are enabled and there is a profile", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: { requests_enabled: true } });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(<CaptureHook onResult={(r) => { captured = r; }} />);

    expect(captured).toEqual({ discoveryEnabled: true, submitDisabledReason: null });
  });

  it("returns discoveryEnabled=false while the feature status is still loading", () => {
    mocks.useRequestFeatureStatus.mockReturnValue({ data: undefined });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p1" } });

    let captured: ReturnType<typeof useCanRequest> | null = null;
    render(<CaptureHook onResult={(r) => { captured = r; }} />);

    expect(captured).toEqual({ discoveryEnabled: false, submitDisabledReason: null });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && pnpm vitest run src/hooks/useCanRequest.test.ts`
Expected: FAIL — `useCanRequest` does not exist yet.

- [ ] **Step 3: Create the hook**

Create `web/src/hooks/useCanRequest.ts`:

```typescript
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";

export interface CanRequestState {
  discoveryEnabled: boolean;
  submitDisabledReason: string | null;
}

export function useCanRequest(): CanRequestState {
  const status = useRequestFeatureStatus();
  const { profile } = useCurrentProfile();
  const discoveryEnabled = Boolean(status.data?.requests_enabled) && Boolean(profile?.id);
  return {
    discoveryEnabled,
    submitDisabledReason: null,
  };
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && pnpm vitest run src/hooks/useCanRequest.test.ts`
Expected: PASS, all four cases.

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/useCanRequest.ts web/src/hooks/useCanRequest.test.ts
git commit -m "feat(hooks): add useCanRequest gating hook for discovery eligibility"
```

---

## Task 3: Extend `requestKeys.search` to include viewer identity

**Files:**
- Modify: `web/src/hooks/queries/keys.ts:135-136`

Add a `viewerKey` parameter so the cache cannot serve results across viewer changes.

- [ ] **Step 1: Update the key shape**

Open `web/src/hooks/queries/keys.ts` and replace lines 135-136:

```typescript
  search: (mediaType: string, query: string, page: number, viewerKey: string) =>
    ["requests", "search", viewerKey, mediaType, query, page] as const,
```

- [ ] **Step 2: Run the type check to see callers that need updating**

Run: `cd web && pnpm tsc --noEmit`
Expected: TypeScript errors at every call site of `requestKeys.search(...)`. Note the file paths reported.

- [ ] **Step 3: Commit the key change alone**

The next task updates the callers. Keep this commit focused.

```bash
git add web/src/hooks/queries/keys.ts
git commit -m "refactor(keys): add viewerKey to requestKeys.search"
```

---

## Task 4: Extend `useRequestSearch` with signal, viewer key, staleTime, and enabled option

**Files:**
- Modify: `web/src/hooks/queries/useRequests.ts:151-166`

Update `useRequestSearch` so it (a) accepts and forwards a `signal` from react-query, (b) keys the cache by the current viewer's `profile.id`, (c) uses a 5-minute `staleTime` (the spec value), and (d) accepts an optional `enabled` override so callers can gate it on `discoveryEnabled` without firing the query when disallowed.

- [ ] **Step 1: Write the failing test**

Append to `web/src/hooks/queries/useRequests.test.ts` (create the file if missing):

```typescript
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useQuery: vi.fn(),
  useCurrentProfile: vi.fn(),
  api: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: (...args: unknown[]) => mocks.useQuery(...args),
  };
});

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => mocks.useCurrentProfile(),
}));

vi.mock("@/api/client", () => ({
  api: (...args: unknown[]) => mocks.api(...args),
}));

import { useRequestSearch } from "./useRequests";

function render(node: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

function CallHook(props: { mediaType: "movie" | "series" | "all"; q: string; page?: number }) {
  useRequestSearch(props.mediaType, props.q, props.page ?? 1);
  return null;
}

describe("useRequestSearch", () => {
  it("includes the current profile id in the query key", () => {
    mocks.useQuery.mockReset();
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "profile-1" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { queryKey: readonly unknown[] };
    expect(options.queryKey).toEqual(["requests", "search", "profile-1", "all", "dune", 1]);
  });

  it("uses 'anon' as the viewer key when there is no profile", () => {
    mocks.useQuery.mockReset();
    mocks.useCurrentProfile.mockReturnValue({ profile: null });
    render(<CallHook mediaType="movie" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { queryKey: readonly unknown[] };
    expect(options.queryKey).toEqual(["requests", "search", "anon", "movie", "dune", 1]);
  });

  it("forwards the react-query signal to api()", async () => {
    mocks.useQuery.mockReset();
    mocks.api.mockResolvedValue({ page: 1, total_pages: 0, total_results: 0, results: [] });
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "profile-1" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as {
      queryFn: (ctx: { signal: AbortSignal }) => unknown;
    };
    const controller = new AbortController();
    await options.queryFn({ signal: controller.signal });

    expect(mocks.api).toHaveBeenCalledTimes(1);
    const apiCall = mocks.api.mock.calls[0]!;
    expect(apiCall[0]).toContain("/requests/search?");
    const init = apiCall[1] as RequestInit;
    expect(init.signal).toBe(controller.signal);
  });

  it("uses a 5-minute staleTime", () => {
    mocks.useQuery.mockReset();
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { staleTime: number };
    expect(options.staleTime).toBe(5 * 60 * 1000);
  });

  it("respects the enabled option override", () => {
    mocks.useQuery.mockReset();
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });

    function CallHookWithOpt({ enabled }: { enabled: boolean }) {
      useRequestSearch("all", "dune", 1, { enabled });
      return null;
    }
    render(<CallHookWithOpt enabled={false} />);

    const options = mocks.useQuery.mock.calls[0]![0] as { enabled: boolean };
    expect(options.enabled).toBe(false);
  });

  it("does not include enabled override when option omitted (defaults to true)", () => {
    mocks.useQuery.mockReset();
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { enabled: boolean };
    // Internally `normalizedQuery.length > 1` is true, and the default enabled override
    // is true, so this should resolve to true.
    expect(options.enabled).toBe(true);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && pnpm vitest run src/hooks/queries/useRequests.test.ts`
Expected: FAIL — the existing hook does not include profile in the key, does not pass signal, and uses `REQUESTS_STALE_TIME` (30s).

- [ ] **Step 3: Update the hook**

Replace lines 151-166 of `web/src/hooks/queries/useRequests.ts` with:

```typescript
import { useCurrentProfile } from "@/hooks/useCurrentProfile";

const REQUEST_SEARCH_STALE_TIME = 5 * 60 * 1000;

export interface UseRequestSearchOptions {
  /** When false, suppresses the query regardless of the query string. Default: true. */
  enabled?: boolean;
}

export function useRequestSearch(
  mediaType: RequestSearchMediaType,
  query: string,
  page = 1,
  options: UseRequestSearchOptions = {},
) {
  const normalizedQuery = query.trim();
  const { profile } = useCurrentProfile();
  const viewerKey = profile?.id ?? "anon";
  const enabledOverride = options.enabled ?? true;
  return useQuery({
    queryKey: requestKeys.search(mediaType, normalizedQuery, page, viewerKey),
    queryFn: ({ signal }) => {
      const params = new URLSearchParams({
        q: normalizedQuery,
        media_type: mediaType,
        page: String(page),
      });
      return api<RequestMediaPage>(`/requests/search?${params}`, { signal });
    },
    enabled: enabledOverride && normalizedQuery.length > 1,
    staleTime: REQUEST_SEARCH_STALE_TIME,
  });
}
```

Note: the `useCurrentProfile` import must be added near the top of the file. The `REQUEST_SEARCH_STALE_TIME` constant goes near the top alongside `REQUESTS_STALE_TIME`. Existing callers (e.g., `Requests.tsx:140`) pass three arguments and continue to work — the new fourth `options` parameter defaults to `{}`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && pnpm vitest run src/hooks/queries/useRequests.test.ts`
Expected: PASS, all four cases.

- [ ] **Step 5: Run the full type check**

Run: `cd web && pnpm tsc --noEmit`
Expected: PASS. No call sites should break (this hook's external signature is unchanged).

- [ ] **Step 6: Commit**

```bash
git add web/src/hooks/queries/useRequests.ts web/src/hooks/queries/useRequests.test.ts
git commit -m "feat(requests): key useRequestSearch by viewer, forward signal, raise staleTime"
```

---

## Task 5: Invalidate request search cache on policy & settings mutations

**Files:**
- Modify: `web/src/hooks/queries/useRequests.ts:53-56` (extend `invalidateRequestSurfaces`)
- Modify: `web/src/hooks/queries/useRequests.ts:262-288` (`useUpdateRequestSettings`)
- Modify: `web/src/hooks/queries/useRequests.ts:345-362` (`useUpdateRequestUserLimit`)

The existing `invalidateRequestSurfaces` invalidates `requestKeys.all`, which is `["requests"]`. React-query's invalidation matches by key prefix, so this *already* invalidates `requestKeys.search(...)` because that key starts with `["requests", "search", ...]`. Verify this and add a focused test rather than introducing new helpers.

- [ ] **Step 1: Add a test asserting invalidation behavior**

Append to `web/src/hooks/queries/useRequests.test.ts`:

```typescript
import { QueryClient as RealQueryClient } from "@tanstack/react-query";
import { requestKeys } from "./keys";

describe("requestKeys.all invalidation", () => {
  it("invalidates entries under requestKeys.search() when invalidating requestKeys.all", async () => {
    const client = new RealQueryClient();
    client.setQueryData(requestKeys.search("all", "dune", 1, "profile-1"), { sentinel: true });

    expect(client.getQueryData(requestKeys.search("all", "dune", 1, "profile-1"))).toEqual({
      sentinel: true,
    });

    await client.invalidateQueries({ queryKey: requestKeys.all });

    const state = client.getQueryState(requestKeys.search("all", "dune", 1, "profile-1"));
    expect(state?.isInvalidated).toBe(true);
  });
});
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `cd web && pnpm vitest run src/hooks/queries/useRequests.test.ts`
Expected: PASS. This documents that the existing `invalidateRequestSurfaces` already cascades to search results.

- [ ] **Step 3: Add a comment in useRequests.ts**

In `web/src/hooks/queries/useRequests.ts`, replace the `invalidateRequestSurfaces` function (lines 53-56) with:

```typescript
function invalidateRequestSurfaces(queryClient: ReturnType<typeof useQueryClient>) {
  // requestKeys.all = ["requests"] — invalidating it cascades to every nested key,
  // including requestKeys.search(...). Settings and per-user limit mutations rely
  // on this to re-fetch viewer-scoped search results when policy changes.
  queryClient.invalidateQueries({ queryKey: requestKeys.all });
  queryClient.invalidateQueries({ queryKey: adminKeys.requestsRoot() });
}
```

- [ ] **Step 4: Add a test that profile change invalidates results**

Append to `web/src/hooks/queries/useRequests.test.ts`:

```typescript
describe("viewer-scoped cache isolation", () => {
  it("does not return profile-1 results when keyed by profile-2", () => {
    const client = new RealQueryClient();
    client.setQueryData(requestKeys.search("all", "dune", 1, "profile-1"), {
      results: [{ tmdb_id: 1 }],
    });

    expect(client.getQueryData(requestKeys.search("all", "dune", 1, "profile-2"))).toBeUndefined();
  });
});
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd web && pnpm vitest run src/hooks/queries/useRequests.test.ts`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/hooks/queries/useRequests.ts web/src/hooks/queries/useRequests.test.ts
git commit -m "test(requests): document viewer-keyed cache isolation and invalidation cascade"
```

---

## Task 6: Make `RequestPosterCard.DiscoverProps` request handler optional

**Files:**
- Modify: `web/src/components/RequestPosterCard.tsx:9-16` (DiscoverProps)
- Modify: `web/src/components/RequestPosterCard.tsx:40-50` (DiscoverCard signature)
- Modify: `web/src/components/RequestPosterCard.tsx:95-120` (hover button render)

For the new search context, we don't want the inline-submit hover button. Make `onRequest` and `isSubmitting` optional, and only render the hover button when `onRequest` is defined.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/RequestPosterCard.test.tsx`:

```typescript
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import RequestPosterCard from "./RequestPosterCard";
import type { RequestMediaResult } from "@/api/types";

const requestable: RequestMediaResult = {
  media_type: "movie",
  tmdb_id: 42,
  title: "Test Movie",
  availability: "missing",
  request: { requestable: true },
};

describe("RequestPosterCard (discover variant)", () => {
  it("renders the hover Request button when onRequest is provided", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={requestable}
          isSubmitting={false}
          onRequest={() => {}}
        />
      </MemoryRouter>,
    );
    expect(markup).toContain("Request");
  });

  it("does not render the hover Request button when onRequest is omitted", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );
    // The hover button has class "rounded-full bg-white"; check that pattern is absent.
    expect(markup).not.toContain("rounded-full bg-white");
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && pnpm vitest run src/components/RequestPosterCard.test.tsx`
Expected: FAIL — the second test fails because `RequestPosterCard` currently requires `onRequest` and `isSubmitting`, and even with placeholder values it would still render the button.

- [ ] **Step 3: Update DiscoverProps**

In `web/src/components/RequestPosterCard.tsx`, replace lines 9-16 with:

```typescript
type DiscoverProps = {
  variant: "discover";
  item: RequestMediaResult;
  /** Called when the inline hover Request button is clicked. Omit to suppress the button. */
  onRequest?: () => void;
  /** Displays the spinner state on the hover Request button. Ignored when onRequest is omitted. */
  isSubmitting?: boolean;
  /** When true, fills the parent (use inside grids). Default: fixed carousel width. */
  fluid?: boolean;
};
```

- [ ] **Step 4: Update the DiscoverCard component signature and render**

Replace lines 40-50 of `RequestPosterCard.tsx`:

```typescript
function DiscoverCard({
  item,
  isSubmitting,
  onRequest,
  fluid,
}: {
  item: RequestMediaResult;
  isSubmitting?: boolean;
  onRequest?: () => void;
  fluid?: boolean;
}) {
```

Replace lines 95-120 (the conditional hover button) with:

```typescript
      {requestable && onRequest && (
        <div className="pointer-events-none absolute inset-x-0 top-0 flex aspect-[2/3] translate-y-2 items-end justify-center bg-gradient-to-t from-black/85 via-black/45 to-transparent p-3 opacity-0 transition-all duration-200 ease-out group-focus-within/req-card:translate-y-0 group-focus-within/req-card:opacity-100 group-hover/req-card:translate-y-0 group-hover/req-card:opacity-100">
          <button
            type="button"
            disabled={Boolean(isSubmitting)}
            onClick={(e) => {
              e.preventDefault();
              e.stopPropagation();
              onRequest();
            }}
            className="pointer-events-auto inline-flex items-center gap-1.5 rounded-full bg-white px-3.5 py-1.5 text-[12px] font-semibold tracking-wide text-black shadow-lg shadow-black/40 transition-all hover:scale-[1.03] active:scale-[0.97] disabled:opacity-70"
          >
            {isSubmitting ? (
              <>
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                Sending
              </>
            ) : (
              <>
                <Plus className="h-3.5 w-3.5 stroke-[2.5]" />
                Request
              </>
            )}
          </button>
        </div>
      )}
```

Also update the call site at line 30 (in the dispatcher) to spread props correctly:

```typescript
  return (
    <DiscoverCard
      item={props.item}
      isSubmitting={props.isSubmitting}
      onRequest={props.onRequest}
      fluid={props.fluid}
    />
  );
```

(This is already the existing shape — verify it still type-checks now that the inner DiscoverProps fields are optional.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && pnpm vitest run src/components/RequestPosterCard.test.tsx`
Expected: PASS, both cases.

- [ ] **Step 6: Run the full type check**

Run: `cd web && pnpm tsc --noEmit`
Expected: PASS. Existing callers still pass both fields, so no breaks.

- [ ] **Step 7: Commit**

```bash
git add web/src/components/RequestPosterCard.tsx web/src/components/RequestPosterCard.test.tsx
git commit -m "feat(request-card): make onRequest optional on discover variant"
```

---

## Task 7: Create `RequestToAddSection` — dialog variant

**Files:**
- Create: `web/src/components/RequestToAddSection.tsx`
- Create: `web/src/components/RequestToAddSection.test.tsx`

A self-contained component that owns:
- The TMDB query (via `useRequestSearch`) gated by `useCanRequest().discoveryEnabled`
- Filtering out results already in the library (`availability === "available"`)
- Section header copy: "Request to Add" when `libraryHadHits=true`, "Not in your library, but you can request" when `libraryHadHits=false`
- Two render variants: `dialog` (compact rows, max 4) and `grid` (poster cards, max 20)
- Silent omit on error or empty TMDB

This task implements the dialog variant only; Task 8 adds the grid variant.

- [ ] **Step 1: Write the failing test for the dialog variant**

Create `web/src/components/RequestToAddSection.test.tsx`:

```typescript
import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
  useDebounce: vi.fn(),
}));

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mocks.useCanRequest(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
}));

vi.mock("@/hooks/useDebounce", () => ({
  useDebounce: <T,>(v: T) => mocks.useDebounce(v) ?? v,
}));

import { RequestToAddSection } from "./RequestToAddSection";
import type { RequestMediaResult } from "@/api/types";

function render(child: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter>{child}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const missingResult = (overrides: Partial<RequestMediaResult> = {}): RequestMediaResult => ({
  media_type: "movie",
  tmdb_id: 1,
  title: "Dune: Prophecy",
  year: 2024,
  availability: "missing",
  request: { requestable: true },
  ...overrides,
});

const availableResult = (overrides: Partial<RequestMediaResult> = {}): RequestMediaResult => ({
  media_type: "movie",
  tmdb_id: 2,
  title: "Dune",
  year: 2021,
  availability: "available",
  request: { requestable: false },
  ...overrides,
});

describe("RequestToAddSection (dialog variant)", () => {
  beforeEach(() => {
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useDebounce.mockReset();
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useDebounce.mockImplementation((v: unknown) => v);
  });

  it("renders nothing when discovery is disabled", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: false, submitDisabledReason: null });
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: false });

    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");
  });

  it("passes enabled=false to useRequestSearch when discovery is disabled so no network call fires", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: false, submitDisabledReason: null });
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: false });

    render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    const call = mocks.useRequestSearch.mock.calls.at(-1);
    expect(call?.[0]).toBe("all");
    expect(call?.[1]).toBe("dune");
    expect(call?.[2]).toBe(1);
    expect(call?.[3]).toEqual({ enabled: false });
  });

  it("passes enabled=true to useRequestSearch when discovery is enabled", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 0, results: [] },
      isLoading: false,
      isError: false,
    });

    render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    const call = mocks.useRequestSearch.mock.calls.at(-1);
    expect(call?.[3]).toEqual({ enabled: true });
  });

  it("renders 'Request to Add' header when library had hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 1, results: [missingResult()] },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("Request to Add");
    expect(markup).toContain("Dune: Prophecy");
  });

  it("renders soft framing when library had 0 hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 1, results: [missingResult()] },
      isLoading: false,
      isError: false,
    });
    const markup = render(
      <RequestToAddSection variant="dialog" query="dune" libraryHadHits={false} />,
    );
    expect(markup).toContain("Not in your library, but you can request");
    expect(markup).not.toContain("Request to Add");
  });

  it("filters out results already available in the library", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 2,
        results: [availableResult(), missingResult()],
      },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("Dune: Prophecy");
    expect(markup).not.toContain('"Dune"');
  });

  it("renders nothing when TMDB returned an error", () => {
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: true });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");
  });

  it("renders nothing when all TMDB results are already in the library", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 1, results: [availableResult()] },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");
  });

  it("limits the dialog variant to at most 4 rows", () => {
    const many = Array.from({ length: 10 }, (_, i) =>
      missingResult({ tmdb_id: i + 100, title: `Result ${i}` }),
    );
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: many.length, results: many },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("Result 0");
    expect(markup).toContain("Result 3");
    expect(markup).not.toContain("Result 4");
  });

  it("renders the disabled affordance and reason when a row is not requestable", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          missingResult({
            tmdb_id: 7,
            title: "Quota Capped Movie",
            request: { requestable: false, reason: "quota_exhausted" },
          }),
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    expect(markup).toContain("Quota Capped Movie");
    // The active "Request" amber chip is suppressed; a muted reason chip is shown instead.
    expect(markup).not.toContain("bg-amber-400/15");
    // formatRequestReason("quota_exhausted") yields a human label that must be present.
    expect(markup).toMatch(/title="[^"]+"/);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && pnpm vitest run src/components/RequestToAddSection.test.tsx`
Expected: FAIL — the component does not exist yet.

- [ ] **Step 3: Create the component**

Create `web/src/components/RequestToAddSection.tsx`:

```typescript
import { Link } from "react-router";
import { Film, Tv } from "lucide-react";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useRequestSearch } from "@/hooks/queries/useRequests";
import type { RequestMediaResult } from "@/api/types";
import { formatRequestReason, tmdbImageURL } from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";

const DIALOG_LIMIT = 4;
const GRID_LIMIT = 20;

export type RequestToAddSectionProps = {
  variant: "dialog" | "grid";
  query: string;
  /** True when the library FTS returned ≥1 hit. Drives header copy. */
  libraryHadHits: boolean;
};

export function RequestToAddSection({ variant, query, libraryHadHits }: RequestToAddSectionProps) {
  const { discoveryEnabled } = useCanRequest();
  // Gate the TMDB query firing on discovery eligibility. The `!discoveryEnabled`
  // early return below hides the UI, but the hook still runs unconditionally
  // (rules of hooks) — passing `enabled` is what prevents the network call.
  const search = useRequestSearch("all", query, 1, { enabled: discoveryEnabled });

  if (!discoveryEnabled) return null;
  if (search.isError) return null;

  const filtered = (search.data?.results ?? []).filter(
    (item) => item.availability !== "available",
  );
  if (filtered.length === 0) return null;

  const limit = variant === "dialog" ? DIALOG_LIMIT : GRID_LIMIT;
  const visible = filtered.slice(0, limit);

  if (variant === "dialog") {
    return <DialogVariant items={visible} libraryHadHits={libraryHadHits} />;
  }
  return <GridVariant items={visible} libraryHadHits={libraryHadHits} />;
}

function HeaderCopy({ libraryHadHits, count }: { libraryHadHits: boolean; count: number }) {
  if (libraryHadHits) {
    return (
      <div className="text-muted-foreground flex items-center gap-2 px-3 pt-2 pb-1 text-[10px] font-medium tracking-[0.1em] uppercase">
        <span>Request to Add</span>
        <span className="bg-muted text-muted-foreground rounded-full px-1.5 text-[10px]">
          {count}
        </span>
      </div>
    );
  }
  return (
    <div className="px-3 pt-3 pb-1 text-[12px] text-amber-300/85">
      Not in your library, but you can request:
    </div>
  );
}

function DialogVariant({
  items,
  libraryHadHits,
}: {
  items: RequestMediaResult[];
  libraryHadHits: boolean;
}) {
  return (
    <div className="border-t border-white/5 pt-1">
      <HeaderCopy libraryHadHits={libraryHadHits} count={items.length} />
      <ul className="px-1 py-1">
        {items.map((item) => (
          <li key={`${item.media_type}-${item.tmdb_id}`}>
            <DialogRow item={item} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function DialogRow({ item }: { item: RequestMediaResult }) {
  const poster = tmdbImageURL(item.poster_path);
  const Icon = item.media_type === "series" ? Tv : Film;
  const requestable = item.request.requestable;
  const reasonLabel = !requestable
    ? item.request.reason
      ? formatRequestReason(item.request.reason)
      : "Blocked"
    : null;
  return (
    <Link
      to={`/requests/${item.media_type}/${item.tmdb_id}`}
      className="hover:bg-muted/80 flex w-full items-center gap-3 rounded-md px-3 py-2 text-left transition-colors"
    >
      <div
        className={cn(
          "bg-muted relative h-14 w-10 shrink-0 overflow-hidden rounded-md",
          !requestable && "opacity-70",
        )}
      >
        {poster ? (
          <img src={poster} alt="" className="h-full w-full object-cover" loading="lazy" />
        ) : (
          <div className="text-muted-foreground flex h-full items-center justify-center">
            <Icon className="h-4 w-4" />
          </div>
        )}
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{item.title}</div>
        <div className="text-muted-foreground text-xs">
          {item.year ? `${item.year} · ` : ""}
          {item.media_type === "series" ? "Series" : "Movie"}
        </div>
      </div>
      {requestable ? (
        <span className="rounded-full border border-amber-400/30 bg-amber-400/15 px-2 py-0.5 text-[9px] font-semibold tracking-[0.5px] text-amber-300 uppercase">
          Request
        </span>
      ) : (
        <span
          className="rounded-full border border-white/10 bg-white/5 px-2 py-0.5 text-[9px] font-medium tracking-[0.5px] text-white/60 uppercase"
          title={reasonLabel ?? "Blocked"}
        >
          {reasonLabel}
        </span>
      )}
    </Link>
  );
}

function GridVariant({
  items: _items,
  libraryHadHits: _libraryHadHits,
}: {
  items: RequestMediaResult[];
  libraryHadHits: boolean;
}) {
  // Implemented in Task 8.
  return null;
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && pnpm vitest run src/components/RequestToAddSection.test.tsx`
Expected: PASS, all dialog-variant cases.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/RequestToAddSection.tsx web/src/components/RequestToAddSection.test.tsx
git commit -m "feat(search): add RequestToAddSection dialog variant"
```

---

## Task 8: Add the grid variant to `RequestToAddSection`

**Files:**
- Modify: `web/src/components/RequestToAddSection.tsx` (`GridVariant`)
- Modify: `web/src/components/RequestToAddSection.test.tsx` (add grid coverage)

- [ ] **Step 1: Write the failing test**

Append to `web/src/components/RequestToAddSection.test.tsx`:

```typescript
describe("RequestToAddSection (grid variant)", () => {
  beforeEach(() => {
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
  });

  it("renders a card per result with the Request to Add header when library had hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 2,
        results: [
          missingResult({ tmdb_id: 1, title: "Dune: Prophecy" }),
          missingResult({ tmdb_id: 2, title: "Dune (1984)" }),
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="grid" query="dune" libraryHadHits />);
    expect(markup).toContain("Request to Add");
    expect(markup).toContain("Dune: Prophecy");
    expect(markup).toContain("Dune (1984)");
  });

  it("renders the soft framing in the grid variant when library had 0 hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [missingResult({ tmdb_id: 1, title: "Dune: Prophecy" })],
      },
      isLoading: false,
      isError: false,
    });
    const markup = render(
      <RequestToAddSection variant="grid" query="dune" libraryHadHits={false} />,
    );
    expect(markup).toContain("Not in your library, but you can request");
  });

  it("limits the grid to at most 20 cards", () => {
    const many = Array.from({ length: 30 }, (_, i) =>
      missingResult({ tmdb_id: i + 100, title: `Result ${i}` }),
    );
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: many.length, results: many },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="grid" query="dune" libraryHadHits />);
    expect(markup).toContain("Result 0");
    expect(markup).toContain("Result 19");
    expect(markup).not.toContain("Result 20");
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && pnpm vitest run src/components/RequestToAddSection.test.tsx`
Expected: FAIL — the grid variant renders `null`.

- [ ] **Step 3: Implement `GridVariant`**

Replace the `GridVariant` placeholder in `web/src/components/RequestToAddSection.tsx`:

```typescript
import RequestPosterCard from "./RequestPosterCard";

function GridVariant({
  items,
  libraryHadHits,
}: {
  items: RequestMediaResult[];
  libraryHadHits: boolean;
}) {
  return (
    <section className="space-y-3">
      <div className="flex items-center gap-3 text-amber-300/85">
        <div className="h-px flex-1 bg-amber-400/20" />
        <h2 className="text-[11px] font-semibold tracking-[0.12em] uppercase">
          {libraryHadHits ? "Request to Add" : "Not in your library, but you can request"}
        </h2>
        <div className="h-px flex-1 bg-amber-400/20" />
      </div>
      <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8">
        {items.map((item) => (
          <RequestPosterCard
            key={`${item.media_type}-${item.tmdb_id}`}
            variant="discover"
            item={item}
            fluid
          />
        ))}
      </div>
    </section>
  );
}
```

(`onRequest` and `isSubmitting` are intentionally omitted — Task 6 made them optional so the hover button is suppressed.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && pnpm vitest run src/components/RequestToAddSection.test.tsx`
Expected: PASS, all dialog and grid cases.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/RequestToAddSection.tsx web/src/components/RequestToAddSection.test.tsx
git commit -m "feat(search): add RequestToAddSection grid variant for Catalog page"
```

---

## Task 9: Integrate `RequestToAddSection` into `GlobalSearch`

**Files:**
- Modify: `web/src/components/GlobalSearch.tsx`
- Modify: `web/src/components/GlobalSearch.test.tsx`

GlobalSearch hoists the TMDB query alongside the library query so it can suppress the "No matches" empty state while TMDB is still pending or has results to show. The section renders inside the same scrollable list. The TMDB debounce is 400ms (vs library's 200ms).

- [ ] **Step 1: Write the failing tests**

The existing `GlobalSearch.test.tsx` mocks `useQuery` globally. Because GlobalSearch now calls multiple hooks that internally use `useQuery` (library preview + TMDB search), the mock returns the same response for both. Switch the test scaffolding to mock the specific hooks we use rather than `useQuery` itself.

Replace the top of `web/src/components/GlobalSearch.test.tsx` (the existing `mocks`, the `useQuery` mock, and the `useDebounce` mock) with:

```typescript
const mocks = vi.hoisted(() => ({
  useQuery: vi.fn(),
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: (...args: unknown[]) => mocks.useQuery(...args),
  };
});

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mocks.useCanRequest(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
}));

vi.mock("@/hooks/useDebounce", () => ({
  useDebounce: <T,>(v: T) => v,
}));
```

Then update the `beforeEach` to set default mocks:

```typescript
  beforeEach(() => {
    mocks.useQuery.mockReset();
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: false, submitDisabledReason: null });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: false,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 50, has_more: true, items: [browseFixture] },
      isFetching: false,
      isError: false,
    });
  });
```

Now add a section-wiring `describe` block at the end of the file:

```typescript
vi.mock("@/components/RequestToAddSection", () => ({
  RequestToAddSection: ({
    variant,
    query,
    libraryHadHits,
  }: {
    variant: string;
    query: string;
    libraryHadHits: boolean;
  }) => (
    <div data-testid="request-section">
      variant={variant} query={query} libraryHadHits={String(libraryHadHits)}
    </div>
  ),
}));

describe("GlobalSearch + RequestToAddSection wiring", () => {
  it("renders the section with libraryHadHits=true when library returned results", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          { media_type: "movie", tmdb_id: 1, title: "X", availability: "missing", request: { requestable: true } },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    expect(markup).toContain('data-testid="request-section"');
    expect(markup).toContain('libraryHadHits="true"');
    expect(markup).toContain('variant="dialog"');
  });

  it("renders the section with libraryHadHits=false when library returned 0 results", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          { media_type: "movie", tmdb_id: 1, title: "X", availability: "missing", request: { requestable: true } },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "ThisDoesNotExist" });

    expect(markup).toContain('libraryHadHits="false"');
  });

  it("does not call useRequestSearch with enabled=true when discoveryEnabled is false", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: false, submitDisabledReason: null });
    renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    const call = mocks.useRequestSearch.mock.calls.at(-1);
    expect(call?.[3]).toEqual({ enabled: false });
  });

  it("does not mount RequestToAddSection when discovery is disabled", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: false, submitDisabledReason: null });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    expect(markup).not.toContain('data-testid="request-section"');
  });

  it("suppresses 'No matches' when library is empty and TMDB is still loading", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Pending" });

    expect(markup).not.toContain("No matches");
  });

  it("suppresses 'No matches' when library is empty and TMDB has missing results", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          { media_type: "movie", tmdb_id: 1, title: "X", availability: "missing", request: { requestable: true } },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "FoundOnTmdb" });

    expect(markup).not.toContain("No matches");
  });

  it("still shows 'No matches' when both library and TMDB are empty", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 0, results: [] },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "ZzzNothing" });

    expect(markup).toContain("No matches");
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && pnpm vitest run src/components/GlobalSearch.test.tsx`
Expected: FAIL — the section is not yet wired in, and `useRequestSearch` is not called from GlobalSearch.

- [ ] **Step 3: Wire the section into GlobalSearch**

In `web/src/components/GlobalSearch.tsx`, add imports near the top:

```typescript
import { RequestToAddSection } from "./RequestToAddSection";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useRequestSearch } from "@/hooks/queries/useRequests";
```

Add a new constant near the top with the other constants:

```typescript
const TMDB_DEBOUNCE_MS = 400;
```

Inside the `GlobalSearch` component, after the existing `debouncedQuery` line, add a second debounce for TMDB and lift the TMDB query:

```typescript
  const tmdbDebouncedQuery = useDebounce(query.trim(), TMDB_DEBOUNCE_MS);
  const canRequest = useCanRequest();
  const tmdbQuery = useRequestSearch("all", tmdbDebouncedQuery, 1, {
    enabled: canRequest.discoveryEnabled,
  });
  const tmdbMissingCount =
    tmdbQuery.data?.results?.filter((r) => r.availability !== "available").length ?? 0;
  const tmdbStillLoading =
    canRequest.discoveryEnabled && tmdbDebouncedQuery.length > 1 && tmdbQuery.isLoading;
  const tmdbWillRender = canRequest.discoveryEnabled && tmdbMissingCount > 0;
```

Update the existing `showEmpty` computation to suppress the empty state while TMDB might still produce a result:

```typescript
  const showEmpty =
    !previewQuery.isFetching &&
    debouncedQuery.length > 0 &&
    items.length === 0 &&
    !previewQuery.isError &&
    !tmdbStillLoading &&
    !tmdbWillRender;
```

Then in the `showResultsPanel` JSX block, add the `<RequestToAddSection>` render below the `items.map(...)` loop. Replace lines 237-281 with:

```typescript
        {showResultsPanel && (
          <div className="flex min-h-0 flex-1 flex-col">
            <div
              role="listbox"
              className="max-h-[min(22rem,55vh)] overflow-y-auto overscroll-contain px-2 py-2"
            >
              {showLoading && (
                <div className="text-muted-foreground px-3 py-6 text-center text-sm">
                  Searching...
                </div>
              )}
              {showError && (
                <div className="text-destructive px-3 py-4 text-center text-sm">
                  Could not load results. Press Enter to open the search page.
                </div>
              )}
              {showEmpty && (
                <div className="text-muted-foreground px-3 py-6 text-center text-sm">
                  No matches
                </div>
              )}
              {items.map((item, i) => (
                <GlobalSearchResultRow
                  key={item.content_id}
                  item={item}
                  index={i}
                  isSelected={i === selectedIndex}
                  onPick={handlePickItem}
                />
              ))}
              {tmdbDebouncedQuery.length > 1 && canRequest.discoveryEnabled && (
                <RequestToAddSection
                  variant="dialog"
                  query={tmdbDebouncedQuery}
                  libraryHadHits={items.length > 0}
                />
              )}
            </div>
            <div role="status" aria-live="polite" className="sr-only">
              {items.length} results found
            </div>
            <div className="text-muted-foreground border-t px-3 py-2 text-center text-xs">
              {total > PREVIEW_LIMIT ? (
                <p>
                  Showing top {PREVIEW_LIMIT} of {total}. Press Enter for all results.
                </p>
              ) : (
                <p>Press Enter to open the full search page.</p>
              )}
            </div>
          </div>
        )}
```

Note that `RequestToAddSection` ALSO calls `useRequestSearch` internally — react-query dedupes by query key, so this is a single network call.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && pnpm vitest run src/components/GlobalSearch.test.tsx`
Expected: PASS, including the existing tests plus the new section-wiring tests.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/GlobalSearch.tsx web/src/components/GlobalSearch.test.tsx
git commit -m "feat(search): render RequestToAddSection in the Cmd+K dialog with empty-state suppression"
```

---

## Task 10: Integrate `RequestToAddSection` into `Catalog`

**Files:**
- Modify: `web/src/pages/Catalog.tsx`
- Create: `web/src/pages/Catalog.test.tsx` (if not present)

Add the grid variant below the existing `ItemGrid` when `state.source === "query"` and there is a query. The page also lifts the TMDB query so it can keep the `ItemGrid` in a loading state (instead of showing "No items found") while TMDB is still pending or has missing results.

- [ ] **Step 1: Write the failing tests**

Inspect `web/src/pages/`. If a `Catalog.test.tsx` already exists, append to it; otherwise create it.

```typescript
import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useCatalogWindow: vi.fn(),
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mocks.useCatalogWindow(...args),
  createCatalogSearchState: (source: string, params: Record<string, unknown>) => ({
    source,
    ...params,
  }),
  fetchCatalogPage: vi.fn(),
}));

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mocks.useCanRequest(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
}));

vi.mock("@/components/RequestToAddSection", () => ({
  RequestToAddSection: ({
    variant,
    query,
    libraryHadHits,
  }: {
    variant: string;
    query: string;
    libraryHadHits: boolean;
  }) => (
    <div data-testid="request-section">
      variant={variant} query={query} libraryHadHits={String(libraryHadHits)}
    </div>
  ),
}));

vi.mock("@/components/ItemGrid", () => ({
  default: ({ totalItems, loading }: { totalItems: number; loading: boolean }) => (
    <div data-testid="item-grid" data-loading={String(loading)} data-total={String(totalItems)} />
  ),
}));

import Catalog from "./Catalog";

function render(initialEntry: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Catalog />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Catalog + RequestToAddSection wiring", () => {
  beforeEach(() => {
    mocks.useCatalogWindow.mockReset();
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: false, submitDisabledReason: null });
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: false });
  });

  it("renders the grid variant when source=query and library has results", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useCatalogWindow.mockReturnValue({
      data: {
        title: 'Results for "dune"',
        totalItems: 2,
        pages: new Map([[0, [{ content_id: "lib-1", title: "Dune", type: "movie", year: 2021 }]]]),
      },
      isLoading: false,
    });

    const markup = render("/catalog?source=query&q=dune");

    expect(markup).toContain('data-testid="request-section"');
    expect(markup).toContain('variant="grid"');
    expect(markup).toContain('libraryHadHits="true"');
  });

  it("renders the grid variant with libraryHadHits=false when library has 0 hits", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useCatalogWindow.mockReturnValue({
      data: { title: 'Results for "noresults"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });

    const markup = render("/catalog?source=query&q=noresults");

    expect(markup).toContain('libraryHadHits="false"');
  });

  it("does not render the section when source is not query", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useCatalogWindow.mockReturnValue({
      data: { title: "Favorites", totalItems: 0, pages: new Map() },
      isLoading: false,
    });

    const markup = render("/catalog?source=favorites");
    expect(markup).not.toContain('data-testid="request-section"');
  });

  it("does not render the section when discovery is disabled", () => {
    // Default beforeEach sets discoveryEnabled=false; assert the parent gate blocks the mount.
    mocks.useCatalogWindow.mockReturnValue({
      data: {
        title: 'Results for "dune"',
        totalItems: 2,
        pages: new Map([[0, [{ content_id: "lib-1", title: "Dune", type: "movie", year: 2021 }]]]),
      },
      isLoading: false,
    });

    const markup = render("/catalog?source=query&q=dune");
    expect(markup).not.toContain('data-testid="request-section"');
  });

  it("passes enabled=false to useRequestSearch when discoveryEnabled is false", () => {
    mocks.useCatalogWindow.mockReturnValue({
      data: { title: 'Results for "dune"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });

    render("/catalog?source=query&q=dune");

    const call = mocks.useRequestSearch.mock.calls.at(-1);
    expect(call?.[3]).toEqual({ enabled: false });
  });

  it("keeps ItemGrid in a loading state when library is empty and TMDB is still loading", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useCatalogWindow.mockReturnValue({
      data: { title: 'Results for "dune"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: true, isError: false });

    const markup = render("/catalog?source=query&q=dune");

    expect(markup).toContain('data-loading="true"');
  });

  it("keeps ItemGrid in a loading state when library is empty and TMDB has missing results", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useCatalogWindow.mockReturnValue({
      data: { title: 'Results for "dune"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          { media_type: "movie", tmdb_id: 1, title: "X", availability: "missing", request: { requestable: true } },
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = render("/catalog?source=query&q=dune");

    expect(markup).toContain('data-loading="true"');
  });

  it("renders the normal ItemGrid empty state when both library and TMDB are empty", () => {
    mocks.useCanRequest.mockReturnValue({ discoveryEnabled: true, submitDisabledReason: null });
    mocks.useCatalogWindow.mockReturnValue({
      data: { title: 'Results for "zzz"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 0, results: [] },
      isLoading: false,
      isError: false,
    });

    const markup = render("/catalog?source=query&q=zzz");

    expect(markup).toContain('data-loading="false"');
    expect(markup).toContain('data-total="0"');
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && pnpm vitest run src/pages/Catalog.test.tsx`
Expected: FAIL — the section is not rendered and the loading-state coordination is not implemented.

- [ ] **Step 3: Wire the section into Catalog**

In `web/src/pages/Catalog.tsx`, add imports:

```typescript
import { RequestToAddSection } from "@/components/RequestToAddSection";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useRequestSearch } from "@/hooks/queries/useRequests";
```

Inside `CatalogResults`, after the existing `useCatalogWindow` call (around line 99-103), add:

```typescript
  const canRequest = useCanRequest();
  const isQuerySource = state.source === "query" && Boolean(state.q);
  const tmdbQuery = useRequestSearch("all", state.q ?? "", 1, {
    enabled: canRequest.discoveryEnabled && isQuerySource,
  });
  const tmdbMissingCount =
    tmdbQuery.data?.results?.filter((r) => r.availability !== "available").length ?? 0;
  const libraryEmpty = (catalogQuery.data?.totalItems ?? 0) === 0;
  const tmdbPendingForEmptyLibrary =
    isQuerySource && canRequest.discoveryEnabled && libraryEmpty && tmdbQuery.isLoading;
  const tmdbWillRenderForEmptyLibrary =
    isQuerySource && canRequest.discoveryEnabled && libraryEmpty && tmdbMissingCount > 0;
  const itemGridLoading =
    catalogQuery.isLoading || tmdbPendingForEmptyLibrary || tmdbWillRenderForEmptyLibrary;
```

Update the `<ItemGrid loading=...>` prop:

```typescript
      <ItemGrid
        totalItems={catalogQuery.data?.totalItems ?? 0}
        pages={catalogQuery.data?.pages ?? new Map()}
        pageSize={limit}
        loading={itemGridLoading}
        onVisibleRangeChange={handleVisibleRangeChange}
        selectionMode={isHistorySource && selectionMode}
        selectedIds={selectedIds}
        onToggleSelect={toggleHistorySelection}
      />
```

After the `ItemGrid`, before the `ConfirmDialog`, render the section:

```typescript
      {isQuerySource && canRequest.discoveryEnabled ? (
        <RequestToAddSection
          variant="grid"
          query={state.q!}
          libraryHadHits={(catalogQuery.data?.totalItems ?? 0) > 0}
        />
      ) : null}

      <ConfirmDialog
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && pnpm vitest run src/pages/Catalog.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/Catalog.tsx web/src/pages/Catalog.test.tsx
git commit -m "feat(catalog): render RequestToAddSection grid with empty-state suppression"
```

---

## Task 11: Final lint, format, and full test suite

**Files:**
- All modified files

- [ ] **Step 1: Run lint**

Run: `cd web && pnpm run lint`
Expected: PASS (no lint errors). Fix any issues in the files we touched.

- [ ] **Step 2: Run format check**

Run: `cd web && pnpm run format:check`
Expected: PASS. If it fails, run `pnpm run format` (or `pnpm prettier --write`) on the touched files and re-check.

- [ ] **Step 3: Run the full vitest suite**

Run: `cd web && pnpm test`
Expected: PASS. Investigate any regressions in unrelated tests — typically caused by the `requestKeys.search` shape change if a test was checking the key directly.

- [ ] **Step 4: Run repository verification helper**

Run: `make verify-local-paths`
Expected: PASS.

- [ ] **Step 5: Commit any formatting changes**

```bash
git add -p web
git commit -m "chore(web): lint and format pass"
```

(If no changes, skip the commit.)

---

## Task 12: Manual smoke

**Files:** none — manual verification.

The repository CLAUDE.md says UI changes should be verified in a real browser. Use the existing dev-server workflow.

- [ ] **Step 1: Start the dev backend**

Run: `make dev-backend`
Expected: Server starts on its configured port. Watch the log for "listening on" or similar.

- [ ] **Step 2: In a second shell, start the dev frontend**

Run: `make dev-frontend`
Expected: Vite dev server starts (typically on :5173). Open the URL in a browser.

- [ ] **Step 3: Walk through the scenarios**

In the browser, log in with a profile and:

1. Open Cmd+K and search for an item known to be in the library and on TMDB. Confirm the library row(s) render first, then the "Request to Add" section appears below.
2. Search for an item NOT in the library (e.g., a very new show). Confirm the soft framing "Not in your library, but you can request:" appears in place of "No matches".
3. Navigate to a search results page (e.g., `/catalog?source=query&q=dune`). Confirm the grid renders, then below it the "Request to Add" grid variant appears.
4. Click a TMDB row in the dialog and confirm it navigates to `/requests/<media_type>/<tmdb_id>`.
5. In an admin context, toggle `RequestsEnabled` off via the admin UI. Re-open Cmd+K and confirm the section does NOT appear.
6. Slow the network (devtools throttling) and search again. Confirm library results appear immediately while the section is pending; the section appears once TMDB returns.

- [ ] **Step 4: If any scenario fails, file the gap and stop here**

Do not paper over UI regressions. Each failing scenario gets a short bug report (file path, expected, actual). The implementation plan ends with manual confirmation, not with a brittle "looks good".

---

## Verification summary (run before opening MR)

- `cd web && pnpm run lint` → PASS
- `cd web && pnpm run format:check` → PASS
- `cd web && pnpm test` → PASS
- `make verify-local-paths` → PASS
- Manual smoke per Task 12 → PASS

---

## Deviations from the spec (recorded for the MR description)

- **`submitDisabledReason` is always `null` in this implementation.** The spec defines this as a viewer-level signal fed by `EffectivePolicy.LimitMode` and quota state, but the frontend has no API surface today that exposes the viewer's effective policy as a single value. Per-row disabled state is driven by `result.request.requestable` and `result.request.reason`, which the backend already enriches per result. The `submitDisabledReason` field is retained in the `useCanRequest()` return type as a forward-compatible stub. Populating it would require a small backend addition to `/api/v1/requests/status` (out of scope here per the spec's "no backend changes" framing).
