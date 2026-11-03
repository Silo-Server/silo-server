import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor, cleanup, act } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useDiagnosticReport } from "./diagnostics";

const summary = {
  id: "report-1",
  short_id: "SILO-ABCDEF123456",
  user_id: "9007199254740993",
  state: "ready",
  captured_at: "2026-09-01T00:00:00.000Z",
  received_at: "2026-09-02T00:00:00.000Z",
  report_type: "manual",
  platform: "ios",
  app_version: "1",
  app_build: "1",
  playback_session_ids: [],
};
function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("rejects an asynchronously decoded detail after a profile switch", async () => {
  let finish!: (body: string) => void;
  let started!: () => void;
  const reading = new Promise<void>((resolve) => {
    started = resolve;
  });
  const response = new Response(null, { headers: { "Content-Type": "application/json" } });
  vi.spyOn(response, "text").mockImplementation(() => {
    started();
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const { result } = renderHook(() => useDiagnosticReport("report-1"), { wrapper: wrapper() });
  await reading;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ ...summary, manifest: { schema_version: 1 } }));
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
});
