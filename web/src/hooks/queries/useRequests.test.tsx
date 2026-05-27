import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { requestKeys } from "./keys";

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

  it("forwards the react-query signal to api()", async () => {
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

  it("keeps the existing Requests page staleTime by default", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: "p" } });
    render(<CallHook mediaType="all" q="dune" />);

    const options = mocks.useQuery.mock.calls[0]![0] as { staleTime: number };
    expect(options.staleTime).toBe(30 * 1000);
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
