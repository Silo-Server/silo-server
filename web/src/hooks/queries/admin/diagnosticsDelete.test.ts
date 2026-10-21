import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useDeleteDiagnosticReport } from "./diagnostics";

function fixture() {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { client, wrapper };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});
it("retains queued profile authority and fences late view effects", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const remove = vi.spyOn(client, "removeQueries");
  const onSuccess = vi.fn();
  const { result } = renderHook(() => useDeleteDiagnosticReport(), { wrapper });
  act(() => result.current.mutate("report-1", { onSuccess }));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
  act(() => {
    setProfileId("profile-b");
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock.mock.calls[0]?.[1]?.headers).toMatchObject({
    "X-Profile-Id": "profile-a",
    "X-Profile-Token": "pin-a",
  });
  expect(onSuccess).not.toHaveBeenCalled();
  expect(remove).not.toHaveBeenCalled();
});
