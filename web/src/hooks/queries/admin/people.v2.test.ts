// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { adminRefreshPerson, adminUpdatePerson } from "@/api/v2/people";
import { useRefreshPerson, useUpdatePersonMetadata } from "../people";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
function setup(response: () => Response) {
  const calls: Array<{ path: string; init?: RequestInit }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ path: new URL(String(input), "http://localhost").pathname, init });
      return response();
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  return {
    calls,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
it.each(["refresh", "update"] as const)(
  "does not refresh or replay administrator %s after401",
  async (action) => {
    const { calls, wrapper } = setup(
      () =>
        new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/invalid_token",
            title: "Invalid token",
            status: 401,
            detail: "Expired",
            instance: "urn:silo:request:test",
          }),
          { status: 401, headers: { "Content-Type": "application/problem+json" } },
        ),
    );
    const { result } = renderHook(
      () => ({ refresh: useRefreshPerson("7", true), update: useUpdatePersonMetadata("7") }),
      { wrapper },
    );
    await act(async () => {
      await expect(
        action === "refresh"
          ? result.current.refresh.mutateAsync()
          : result.current.update.mutateAsync({ name: "Updated" }),
      ).rejects.toThrow();
    });
    expect(calls).toHaveLength(1);
    expect(calls[0]?.path).toBe(`/api/v2/admin/people/7${action === "refresh" ? "/refresh" : ""}`);
  },
);
it("uses the typed person response and preserves null date no-op", async () => {
  const { calls } = setup(() =>
    jsonResponse({
      id: "7",
      name: "Updated",
      bio: "",
      birthplace: "",
      homepage: "",
      tmdb_id: "",
      imdb_id: "",
      tvdb_id: "",
      plex_guid: "",
    }),
  );
  expect((await adminRefreshPerson("7")).id).toBe("7");
  expect(
    (await adminUpdatePerson("7", { name: "Updated", birth_date: null, death_date: "" })).name,
  ).toBe("Updated");
  expect(JSON.parse(String(calls[1]?.init?.body))).toEqual({ name: "Updated", death_date: "" });
});
