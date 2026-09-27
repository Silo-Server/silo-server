import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { renderHook, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useAdminUserProfiles } from "./history";
function wrapper() {
  const client = new QueryClient();
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
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
it("uses the typed complete profile collection while preserving profile IDs", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        items: [{ id: "household-profile", name: "Member" }],
        page: { has_more: false },
      }),
      { headers: { "Content-Type": "application/json" } },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useAdminUserProfiles(7), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.[0]?.id).toBe("household-profile");
  expect(String(fetch.mock.calls[0]![0])).toBe("/api/v2/admin/users/7/profiles");
});
it("fails incomplete profile results without returning a truncated picker", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        items: [{ id: "household-profile", name: "Member" }],
        page: { has_more: true, next_cursor: "later" },
      }),
      { headers: { "Content-Type": "application/json" } },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useAdminUserProfiles(7), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
  expect(fetch).toHaveBeenCalledTimes(1);
});
