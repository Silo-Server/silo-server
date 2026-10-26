// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import { useRegenerateItemTrickplay } from "./trickplay";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("admin-profile");
});
afterEach(() => vi.unstubAllGlobals());

// Regeneration is non-retryable: a 401 after the queue update took effect
// must not refresh the session and send it again.
it("does not refresh or replay a regeneration after 401", async () => {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      calls.push(decodeURIComponent(new URL(String(input), "http://localhost").pathname));
      return new Response(
        JSON.stringify({
          type: "https://siloserver.org/docs/api/v2/problems/invalid_token",
          title: "Invalid token",
          status: 401,
          detail: "Expired",
          instance: "urn:silo:request:test",
        }),
        { status: 401, headers: { "Content-Type": "application/problem+json" } },
      );
    }),
  );
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(useRegenerateItemTrickplay, { wrapper });

  await act(async () => {
    await expect(result.current.mutateAsync("movie:heat-1995")).rejects.toThrow();
  });

  expect(calls).toEqual(["/api/v2/admin/items/movie:heat-1995/trickplay/regenerate"]);
  expect(localStorage.getItem("refresh_token")).toBe("synthetic-refresh");
});
