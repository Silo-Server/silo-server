// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useAvailableUserLibraries } from "./libraries";
import { setAccessToken, setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ profile: { id: "profile" } }) }));
let queryClient: QueryClient;
beforeEach(() => {
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  installPolicyStorageMocks();
  setAccessToken("account");
  setProfileId("profile");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
it("reads the viewer projection and converts IDs without losing display order or posters", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>(async () =>
    jsonResponse({
      items: [
        {
          id: "12",
          name: "Movies",
          type: "movies",
          sort_order: 2,
          poster_url: "https://images.example/poster",
        },
      ],
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useAvailableUserLibraries(), { wrapper });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toEqual([
    {
      id: 12,
      name: "Movies",
      type: "movies",
      sort_order: 2,
      poster_url: "https://images.example/poster",
    },
  ]);
  expect(String(fetch.mock.calls[0]?.[0])).toBe("/api/v2/user/libraries");
});
it("discards a response after account replacement", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi.fn(
    () =>
      new Promise<Response>((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useAvailableUserLibraries(), { wrapper });
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
  setAccessToken("replacement");
  finish(jsonResponse({ items: [{ id: "12", name: "Movies", type: "movies", sort_order: 2 }] }));
  await waitFor(() =>
    expect(
      queryClient
        .getQueryCache()
        .getAll()
        .some((q) => q.state.status === "error"),
    ).toBe(true),
  );
  expect(
    queryClient
      .getQueryCache()
      .getAll()
      .every((q) => q.state.data === undefined),
  ).toBe(true);
  expect(result.current.data).toBeUndefined();
});
