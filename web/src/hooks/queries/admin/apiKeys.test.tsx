import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminApiKeyScope, captureAdminApiKeyAuthority } from "@/api/v2/adminApiKeys";
import { adminKeys } from "../keys";
import { useAdminApiKeys, useAdminCreateApiKey } from "./apiKeys";
const key = {
  id: "1",
  user_id: "2",
  label: "Tool",
  rate_tier: "standard",
  key_prefix: "sa_12345678",
  scopes: [],
  created_at: "2026-01-01T00:00:00.000Z",
  username: "admin",
};
function json(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}
function harness() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("loads explicitly, refetches ordinary two-page results, and restarts a failed cursor", async () => {
  let malformed = false;
  const fetch = vi.fn<typeof globalThis.fetch>(async (url) =>
    String(url).includes("cursor=next")
      ? json({ items: [{ ...key, id: "2" }], page: { has_more: false } })
      : json({
          items: [key],
          page: malformed ? { has_more: true } : { has_more: true, next_cursor: "next" },
        }),
  );
  vi.stubGlobal("fetch", fetch);
  const { client, wrapper } = harness();
  const { result } = renderHook(() => useAdminApiKeys(), { wrapper });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetch).toHaveBeenCalledTimes(1);
  await act(async () => {
    await result.current.fetchNextPage();
  });
  await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
  await act(async () => {
    await result.current.refetch();
  });
  expect(result.current.isError).toBe(false);
  await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
  malformed = true;
  await act(async () => {
    await result.current.restart();
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  malformed = false;
  await act(async () => {
    await result.current.restart();
  });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.pages).toHaveLength(1);
  client.clear();
});
it("does not cache creation secrets and invalidates only captured list scope", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>(async () =>
    json({ ...key, key: "creation-secret" }),
  );
  vi.stubGlobal("fetch", fetch);
  const { client, wrapper } = harness();
  const context = captureAdminApiKeyAuthority();
  const target = [...adminKeys.apiKeys(), adminApiKeyScope(context)];
  const other = [...adminKeys.apiKeys(), "another-profile"];
  client.setQueryData(target, { items: [key] });
  client.setQueryData(other, { items: [key] });
  const { result, unmount } = renderHook(() => useAdminCreateApiKey(), { wrapper });
  await act(async () => {
    await result.current.mutateAsync({ body: { label: "Tool" }, profileContext: context });
  });
  expect(client.getQueryState(target)?.isInvalidated).toBe(true);
  expect(client.getQueryState(other)?.isInvalidated).toBe(false);
  expect(
    JSON.stringify(
      client
        .getQueryCache()
        .getAll()
        .map((q) => q.state.data),
    ),
  ).not.toContain("creation-secret");
  act(() => result.current.reset());
  unmount();
  await waitFor(() => expect(client.getMutationCache().getAll()).toHaveLength(0));
  client.clear();
});
