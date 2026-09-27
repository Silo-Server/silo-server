import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { setProfileId } from "@/api/client";
import type { PersonSearchMediaScope } from "@/api/v2/people";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { usePersonSearch } from "./people";

beforeEach(() => {
  installPolicyStorageMocks();
  setProfileId("test-profile");
});
afterEach(() => vi.unstubAllGlobals());

it("keeps cached people results separate for Media, Audiobooks, and All", async () => {
  const fetchMock = vi.fn<typeof fetch>(async (input) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname.endsWith("/capabilities")) {
      return jsonResponse({ state: "available", people_media_scope: true });
    }
    const scope = url.searchParams.get("media_scope");
    return jsonResponse({ items: [{ id: "9007199254740993", name: scope ?? "all" }] });
  });
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result, rerender, unmount } = renderHook(
    ({ scope }: { scope?: PersonSearchMediaScope }) => usePersonSearch("Nathan", 20, true, scope),
    { wrapper, initialProps: { scope: "video" as PersonSearchMediaScope | undefined } },
  );
  await waitFor(() => expect(result.current.data?.[0]?.name).toBe("video"));
  rerender({ scope: "audiobook" });
  expect(result.current.data).toBeUndefined();
  await waitFor(() => expect(result.current.data?.[0]?.name).toBe("audiobook"));
  rerender({ scope: undefined });
  await waitFor(() => expect(result.current.data?.[0]?.name).toBe("all"));
  rerender({ scope: "video" });
  expect(result.current.data?.[0]?.name).toBe("video");
  expect(fetchMock).toHaveBeenCalledTimes(4);
  unmount();
  client.clear();
});

it.each([undefined, false])(
  "does not send any people search when capability is %s",
  async (supported) => {
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      if (String(input).includes("/capabilities")) {
        return jsonResponse({ state: "available", people_media_scope: supported });
      }
      return jsonResponse({ items: [{ id: "7", name: "Unscoped result" }] });
    });
    vi.stubGlobal("fetch", fetchMock);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result, rerender, unmount } = renderHook(
      ({ scope }: { scope?: PersonSearchMediaScope }) => usePersonSearch("Actor", 20, true, scope),
      { wrapper, initialProps: { scope: "video" as PersonSearchMediaScope | undefined } },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    rerender({ scope: undefined });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    unmount();
    client.clear();
  },
);

it.each([undefined, "video"] as const)(
  "surfaces a capability failure without searching scope %s",
  async (scope) => {
    const fetchMock = vi.fn<typeof fetch>(async () => {
      throw new Error("offline");
    });
    vi.stubGlobal("fetch", fetchMock);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result, unmount } = renderHook(() => usePersonSearch("Actor", 20, true, scope), {
      wrapper,
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/catalog/search/capabilities");
    unmount();
    client.clear();
  },
);
