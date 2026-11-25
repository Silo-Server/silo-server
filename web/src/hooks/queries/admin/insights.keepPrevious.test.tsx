import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useAdminInsightsPeopleInfinite, useAdminInsightsTitles } from "./insights";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  profileContext: {
    accessToken: "token",
    authContextVersion: 1,
    serverOrigin: "http://silo.test",
    profileId: "p1",
    profileToken: null,
    profileTokenGeneration: 0,
  },
}));
vi.mock("@/api/client", async () => ({
  ...(await vi.importActual<typeof import("@/api/client")>("@/api/client")),
  captureProfileRequestContext: () => mocks.profileContext,
  isCapturedProfileAuthorityActive: () => true,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

type Sort = "plays" | "watch_time";
const window7d = { range: "7d" as const, tz: "UTC" };

// The first read answers at once; later reads hang, so the test can look at
// what the hook shows while a changed key loads.
function answerFirstReadOnly(body: (query: Record<string, unknown>) => unknown) {
  let calls = 0;
  mocks.request.mockImplementation((_route: string, opts: { query: Record<string, unknown> }) =>
    calls++ === 0 ? Promise.resolve(body(opts.query)) : new Promise(() => {}),
  );
}

describe("Insights tab queries keep the previous result while a new key loads", () => {
  beforeEach(() => {
    mocks.request.mockReset();
    mocks.profileContext = { ...mocks.profileContext, authContextVersion: 1 };
  });
  afterEach(cleanup);

  it("shows the previous sort's rows as placeholder data when the tab opts in", async () => {
    answerFirstReadOnly((query) => ({ movies: [], sort: query.sort }));
    const { result, rerender } = renderHook(
      ({ sort }: { sort: Sort }) =>
        useAdminInsightsTitles({ ...window7d, sort }, { keepPrevious: true }),
      { wrapper, initialProps: { sort: "plays" } },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    rerender({ sort: "watch_time" });

    expect(result.current.isLoading).toBe(false);
    expect(result.current.isPlaceholderData).toBe(true);
    expect(result.current.data).toMatchObject({ sort: "plays" });
  });

  it("drops the previous result for a detail sheet, which does not opt in", async () => {
    answerFirstReadOnly((query) => ({ movies: [], sort: query.sort }));
    const { result, rerender } = renderHook(
      ({ sort }: { sort: Sort }) => useAdminInsightsTitles({ ...window7d, sort }),
      { wrapper, initialProps: { sort: "plays" } },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    rerender({ sort: "watch_time" });

    expect(result.current.isLoading).toBe(true);
    expect(result.current.data).toBeUndefined();
  });

  it("never carries a result across a change of signed-in profile", async () => {
    answerFirstReadOnly((query) => ({ movies: [], sort: query.sort }));
    const { result, rerender } = renderHook(
      ({ sort }: { sort: Sort }) =>
        useAdminInsightsTitles({ ...window7d, sort }, { keepPrevious: true }),
      { wrapper, initialProps: { sort: "plays" } },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    mocks.profileContext = { ...mocks.profileContext, authContextVersion: 2 };
    rerender({ sort: "plays" });

    expect(result.current.isPlaceholderData).toBe(false);
    expect(result.current.data).toBeUndefined();
  });

  it("keeps the People table's loaded pages while a search loads", async () => {
    answerFirstReadOnly(() => ({ accounts: [{ user_id: "u1" }], total_accounts: 1 }));
    const { result, rerender } = renderHook(
      ({ search }: { search?: string }) =>
        useAdminInsightsPeopleInfinite({ ...window7d, ...(search ? { search } : {}) }),
      { wrapper, initialProps: {} as { search?: string } },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    rerender({ search: "nobody" });

    expect(result.current.isLoading).toBe(false);
    expect(result.current.isPlaceholderData).toBe(true);
    expect(result.current.data?.pages[0]?.accounts).toEqual([{ user_id: "u1" }]);
  });
});
