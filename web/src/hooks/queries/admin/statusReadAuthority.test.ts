import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId } from "@/api/client";
import { useDiagnosticsStatus } from "./diagnostics";
import { useJellyfinCompatStatus } from "./settings";
import { useAdminStats } from "./stats";
function wrapper(client: QueryClient) {
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken(null);
  setProfileId("profile-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
// Each reader checks its own request authority after decoding.
it.each(
  [useAdminStats, useDiagnosticsStatus, useJellyfinCompatStatus].map((useRead) => ({
    name: useRead.name,
    useRead,
  })),
)("$name rejects metadata decoded under a different account or profile", async ({ useRead }) => {
  let finish!: (body: string) => void;
  let start!: () => void;
  const reading = new Promise<void>((resolve) => {
    start = resolve;
  });
  const response = new Response(null, { headers: { "Content-Type": "application/json" } });
  vi.spyOn(response, "text").mockImplementation(() => {
    start();
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderHook(() => useRead(), { wrapper: wrapper(client) });
  await reading;
  const captured = client.getQueryCache().getAll()[0]!;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ status: "available" }));
  });
  await waitFor(() => expect(captured.state.status).toBe("error"));
  expect(captured.state.data).toBeUndefined();
});
