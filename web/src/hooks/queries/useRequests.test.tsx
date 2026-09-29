import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { ReactNode } from "react";
import { renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setAccessToken, setProfileId } from "@/api/client";
import { adminAuthorityScope, captureAdminAuthority } from "@/api/v2/adminAuthority";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import { adminKeys, requestKeys } from "./keys";

const mocks = vi.hoisted(() => ({
  useQuery: vi.fn(),
  useInfiniteQuery: vi.fn(),
  useCurrentProfile: vi.fn(),
  api: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: (...args: unknown[]) => mocks.useQuery(...args),
    useInfiniteQuery: (...args: unknown[]) => mocks.useInfiniteQuery(...args),
  };
});

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => mocks.useCurrentProfile(),
}));

vi.mock("@/api/v2/request", () => ({
  v2: (...args: unknown[]) => mocks.api(...args),
}));

import type { DiscoverBrowseResponse, RequestDiscoverySection } from "@/api/types";
import {
  nextBrowsePage,
  nextDiscoverySectionPage,
  useCancelMediaRequest,
  useCreateMediaRequest,
  useMyMediaRequests,
  useRequestFeatureStatus,
  useRequestMediaDetail,
  useRequestSearch,
  useToggleRequestFollow,
} from "./useRequests";
import {
  useAdminCancelMediaRequest,
  useAdminRequestQueue,
  useRequestGroupLimit,
  useUpdateRequestGroupLimit,
} from "./admin/requests";

function render(node: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

function CallHook(props: { mediaType: "movie" | "series" | "all"; q: string; page?: number }) {
  useRequestSearch(props.mediaType, props.q, props.page ?? 1);
  return null;
}

describe("useRequestSearch", () => {
  beforeEach(() => {
    mocks.useQuery.mockReset();
    mocks.useCurrentProfile.mockReset();
    mocks.api.mockReset();
  });

  it("includes the current profile id in the query key", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "profile-1" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { queryKey: readonly unknown[] };
    expect(options.queryKey).toEqual(["requests", "search", "profile-1", "all", "dune", 1]);
  });

  it("uses 'anon' as the viewer key when there is no profile", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: null });
    render(<CallHook mediaType="movie" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { queryKey: readonly unknown[] };
    expect(options.queryKey).toEqual(["requests", "search", "anon", "movie", "dune", 1]);
  });

  it("forwards the react-query signal to v2()", async () => {
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
    expect(apiCall[0]).toBe("GET /api/v2/requests/search");
    const init = apiCall[1] as { signal?: AbortSignal; query: { q: string } };
    expect(init.signal).toBe(controller.signal);
    expect(init.query.q).toBe("dune");
  });

  it("keeps the existing Requests page staleTime by default", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as {
      staleTime: number;
      gcTime?: number;
      retry?: boolean | number;
    };
    expect(options.staleTime).toBe(30 * 1000);
    expect(options).not.toHaveProperty("gcTime");
    expect(options).not.toHaveProperty("retry");
  });

  it("allows callers to opt into a longer staleTime", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });

    function CallHookWithStaleTime() {
      useRequestSearch("all", "dune", 1, { staleTime: 5 * 60 * 1000 });
      return null;
    }
    render(<CallHookWithStaleTime />);

    const options = mocks.useQuery.mock.calls[0]![0] as { staleTime: number };
    expect(options.staleTime).toBe(5 * 60 * 1000);
  });

  it("allows interactive callers to bound cache retention and disable retries", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });

    function CallInteractiveSearch() {
      useRequestSearch("all", "dune", 1, { gcTime: 30_000, retry: false });
      return null;
    }
    render(<CallInteractiveSearch />);

    const options = mocks.useQuery.mock.calls[0]![0] as {
      gcTime?: number;
      retry?: boolean;
    };
    expect(options).toMatchObject({ gcTime: 30_000, retry: false });
  });

  it("respects the enabled option override", () => {
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
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { enabled: boolean };
    expect(options.enabled).toBe(true);
  });

  it("does not require profile by default", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: null });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { enabled: boolean };
    expect(options.enabled).toBe(true);
  });

  it("can require a profile before fetching", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: null });

    function CallHookRequiringProfile() {
      useRequestSearch("all", "dune", 1, { requireProfile: true });
      return null;
    }
    render(<CallHookRequiringProfile />);

    const options = mocks.useQuery.mock.calls[0]![0] as { enabled: boolean };
    expect(options.enabled).toBe(false);
  });
});

describe("infinite request browse paging", () => {
  function sectionPage(page: number, totalPages: number, nextPage?: number) {
    return { page, total_pages: totalPages, next_page: nextPage } as RequestDiscoverySection;
  }

  it("numbers a Discover row's pages up to its total", () => {
    const first = sectionPage(1, 3);
    expect(nextDiscoverySectionPage(first, [first], 1)).toBe(2);
    const last = sectionPage(3, 3);
    expect(nextDiscoverySectionPage(last, [first, last], 3)).toBeUndefined();
  });

  it("follows a rating-restricted row's cursor and ends where the cursor does", () => {
    // Page 1 covers TMDB pages 1–3 and page 4 covers 4–8; page 9 reaches the end.
    const first = sectionPage(1, 40, 4);
    const second = sectionPage(4, 40, 9);
    const last = sectionPage(9, 40);
    expect(nextDiscoverySectionPage(first, [first], 1)).toBe(4);
    expect(nextDiscoverySectionPage(second, [first, second], 4)).toBe(9);
    // No cursor after cursor pages: the end, even though TMDB counts 40 pages.
    expect(nextDiscoverySectionPage(last, [first, second, last], 9)).toBeUndefined();
  });

  it("ignores a cursor that does not move forward", () => {
    const page = sectionPage(5, 10, 5);
    expect(nextDiscoverySectionPage(page, [page], 5)).toBeUndefined();
  });

  it("stops at TMDB's 500-page cap whatever total it reports", () => {
    const section = sectionPage(500, 900);
    expect(nextDiscoverySectionPage(section, [section], 500)).toBeUndefined();
    const browse = { page: 500, total_pages: 1001 } as DiscoverBrowseResponse;
    expect(nextBrowsePage(browse, [browse], 500)).toBeUndefined();
    expect(nextBrowsePage(browse, [browse], 499)).toBe(500);
  });
});

describe("requestKeys.all invalidation", () => {
  it("invalidates entries under requestKeys.search() when invalidating requestKeys.all", async () => {
    const client = new QueryClient();
    client.setQueryData(requestKeys.search("all", "dune", 1, "profile-1"), { sentinel: true });

    expect(client.getQueryData(requestKeys.search("all", "dune", 1, "profile-1"))).toEqual({
      sentinel: true,
    });

    await client.invalidateQueries({ queryKey: requestKeys.all });

    const state = client.getQueryState(requestKeys.search("all", "dune", 1, "profile-1"));
    expect(state?.isInvalidated).toBe(true);
  });
});

describe("viewer-scoped cache isolation", () => {
  it("does not return profile-1 results when keyed by profile-2", () => {
    const client = new QueryClient();
    client.setQueryData(requestKeys.search("all", "dune", 1, "profile-1"), {
      results: [{ tmdb_id: 1 }],
    });

    expect(client.getQueryData(requestKeys.search("all", "dune", 1, "profile-2"))).toBeUndefined();
  });
});

describe("useCancelMediaRequest", () => {
  const wireRequest = {
    id: "req-1",
    provider: "silo",
    media_type: "movie",
    tmdb_id: 603,
    title: "The Matrix",
    status: "pending",
    outcome: "cancelled",
    targets: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  function wrapperFor(client: QueryClient) {
    return function Wrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    };
  }

  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue(wireRequest);
  });

  it("posts to the viewer's cancel operation", async () => {
    const client = new QueryClient();
    const { result } = renderHook(() => useCancelMediaRequest(), { wrapper: wrapperFor(client) });

    const cancelled = await result.current.mutateAsync("req-1");

    expect(mocks.api).toHaveBeenCalledExactlyOnceWith("POST /api/v2/requests/{id}/cancel", {
      path: { id: "req-1" },
      body: {},
    });
    expect(cancelled).toMatchObject({ id: "req-1", outcome: "cancelled" });
  });

  it("refreshes the same request surfaces a new request does", async () => {
    const createClient = new QueryClient();
    const createInvalidations = vi.spyOn(createClient, "invalidateQueries");
    const create = renderHook(() => useCreateMediaRequest(), {
      wrapper: wrapperFor(createClient),
    });
    await create.result.current.mutateAsync({ media_type: "movie", tmdb_id: 603, title: "X" });

    const cancelClient = new QueryClient();
    const cancelInvalidations = vi.spyOn(cancelClient, "invalidateQueries");
    const cancel = renderHook(() => useCancelMediaRequest(), {
      wrapper: wrapperFor(cancelClient),
    });
    await cancel.result.current.mutateAsync("req-1");

    expect(cancelInvalidations.mock.calls).toEqual(createInvalidations.mock.calls);
    expect(cancelInvalidations).toHaveBeenCalledWith({ queryKey: requestKeys.all });
    expect(cancelInvalidations).toHaveBeenCalledWith({ queryKey: adminKeys.requestsRoot() });
  });
});

describe("useAdminCancelMediaRequest", () => {
  beforeEach(() => {
    mocks.api.mockReset();
  });

  it("refreshes the queue when the cancellation is refused", async () => {
    // Another admin acted first; the obsolete row must not keep its action.
    mocks.api.mockRejectedValue(new Error("This request has changed."));
    const client = new QueryClient();
    const invalidations = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useAdminCancelMediaRequest(), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    });

    await expect(result.current.mutateAsync({ id: "req-1" })).rejects.toThrow();

    expect(invalidations).toHaveBeenCalledWith({ queryKey: adminKeys.requestsRoot() });
    expect(invalidations).toHaveBeenCalledWith({ queryKey: requestKeys.all });
  });
});

describe("access group request limits", () => {
  const wireLimit = {
    group_id: "7",
    limit_mode: "inherit",
    max_requests: null,
    window_days: null,
    approval_mode: "inherit",
  };

  function CallGroupLimitHook() {
    useRequestGroupLimit(7);
    return null;
  }

  function lastQueryOptions() {
    return mocks.useQuery.mock.calls.at(-1)![0] as {
      queryKey: readonly unknown[];
      queryFn: () => Promise<unknown>;
    };
  }

  beforeEach(() => {
    mocks.useQuery.mockReset();
    mocks.api.mockReset();
    mocks.api.mockImplementation(
      async (_route: string, options: { onResponse?: (r: Response) => void }) => {
        options.onResponse?.(new Response(null, { headers: { ETag: '"limit-1"' } }));
        return wireLimit;
      },
    );
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
  });

  it("keys the limit by profile, since its validator names the profile that read it", async () => {
    render(<CallGroupLimitHook />);
    const owner = lastQueryOptions();
    setProfileId("kid");
    render(<CallGroupLimitHook />);
    const kid = lastQueryOptions();

    expect(kid.queryKey).not.toEqual(owner.queryKey);
    await owner.queryFn();
    expect(mocks.api).toHaveBeenLastCalledWith(
      "GET /api/v2/admin/request-groups/{group_id}/limit",
      expect.objectContaining({ profileContext: expect.objectContaining({ profileId: "owner" }) }),
    );
  });

  it("saves under the authority the limit was read with", async () => {
    const owner = captureAdminAuthority();
    setProfileId("kid");
    const client = new QueryClient();
    const { result } = renderHook(() => useUpdateRequestGroupLimit(), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    });

    await result.current.mutateAsync({
      limit: { group_id: 7, etag: '"limit-0"' },
      body: {
        limit_mode: "inherit",
        approval_mode: "inherit",
        max_requests: null,
        window_days: null,
      },
      profileContext: owner,
    });

    expect(mocks.api).toHaveBeenCalledWith(
      "PUT /api/v2/admin/request-groups/{group_id}/limit",
      expect.objectContaining({ profileContext: owner }),
    );
    expect(
      client.getQueryData(adminKeys.requestGroupLimit(7, adminAuthorityScope(owner))),
    ).toMatchObject({ group_id: 7, etag: '"limit-1"' });
  });
});

function CallStatusHook() {
  useRequestFeatureStatus();
  return null;
}

it("reads request capabilities through v2", async () => {
  mocks.useQuery.mockReset();
  mocks.api.mockReset();
  render(<CallStatusHook />);
  const options = mocks.useQuery.mock.calls[0]![0] as { queryFn: () => Promise<unknown> };
  await options.queryFn();
  expect(mocks.api).toHaveBeenCalledWith("GET /api/v2/requests/status");
});

describe("useToggleRequestFollow", () => {
  function wrapperFor(client: QueryClient) {
    return function Wrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    };
  }

  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue({ requestable: false, following: true });
  });

  it("follows and unfollows by title, then refreshes request surfaces", async () => {
    const client = new QueryClient();
    const invalidations = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useToggleRequestFollow(), { wrapper: wrapperFor(client) });

    await result.current.mutateAsync({ mediaType: "movie", tmdbID: 603, follow: true });
    await result.current.mutateAsync({ mediaType: "series", tmdbID: 1399, follow: false });

    expect(mocks.api.mock.calls).toEqual([
      [
        "PUT /api/v2/requests/follows/{media_type}/{tmdb_id}",
        { path: { media_type: "movie", tmdb_id: 603 } },
      ],
      [
        "DELETE /api/v2/requests/follows/{media_type}/{tmdb_id}",
        { path: { media_type: "series", tmdb_id: 1399 } },
      ],
    ]);
    expect(invalidations).toHaveBeenCalledWith({ queryKey: ["requests"] });
  });
});

describe("polling while something downloads", () => {
  const download = { phase: "downloading", downloads: 1, updated_at: "2026-01-02T03:04:05Z" };
  type RefetchInterval = (query: { state: { data: unknown } }) => number | false;

  function refetchInterval(hook: typeof mocks.useQuery): RefetchInterval {
    return (hook.mock.calls.at(-1)![0] as { refetchInterval: RefetchInterval }).refetchInterval;
  }

  beforeEach(() => {
    mocks.useQuery.mockReset();
    mocks.useInfiniteQuery.mockReset();
  });

  it("reads the viewer's requests every 30 seconds while one of them downloads", () => {
    function CallMine() {
      useMyMediaRequests({ limit: 100 });
      return null;
    }
    render(<CallMine />);
    const interval = refetchInterval(mocks.useQuery);

    expect(interval({ state: { data: undefined } })).toBe(false);
    expect(interval({ state: { data: [{ id: "a" }, { id: "b", targets: [{ id: 1 }] }] } })).toBe(
      false,
    );
    expect(interval({ state: { data: [{ id: "a" }, { id: "b", download }] } })).toBe(30_000);
    expect(interval({ state: { data: [{ id: "a", targets: [{ id: 1, download }] }] } })).toBe(
      30_000,
    );
  });

  it("leaves the viewer's requests alone on a surface that opts out", () => {
    function CallMine() {
      useMyMediaRequests({ outcome: "active" }, { pollDownloads: false });
      return null;
    }
    render(<CallMine />);
    const interval = refetchInterval(mocks.useQuery);

    expect(interval({ state: { data: [{ id: "a" }, { id: "b", download }] } })).toBe(false);
    expect(interval({ state: { data: [{ id: "a", targets: [{ id: 1, download }] }] } })).toBe(
      false,
    );
  });

  it("reads a title's request every 30 seconds while it downloads", () => {
    function CallDetail() {
      useRequestMediaDetail("movie", 949);
      return null;
    }
    render(<CallDetail />);
    const interval = refetchInterval(mocks.useQuery);

    expect(interval({ state: { data: undefined } })).toBe(false);
    expect(interval({ state: { data: { request: { requestable: false } } } })).toBe(false);
    expect(interval({ state: { data: { request: { requestable: false, download } } } })).toBe(
      30_000,
    );
  });

  it("reads the admin queue every 30 seconds while a row on any loaded page downloads", () => {
    function CallQueue() {
      useAdminRequestQueue({ view: "in_progress" });
      return null;
    }
    render(<CallQueue />);
    const interval = refetchInterval(mocks.useInfiniteQuery);
    const pages = (...items: unknown[][]) => ({ pages: items.map((rows) => ({ items: rows })) });

    expect(interval({ state: { data: undefined } })).toBe(false);
    expect(interval({ state: { data: pages([{ id: "a", targets: [] }]) } })).toBe(false);
    expect(
      interval({
        state: { data: pages([{ id: "a" }], [{ id: "b", targets: [{ id: 1, download }] }]) },
      }),
    ).toBe(30_000);
  });
});
