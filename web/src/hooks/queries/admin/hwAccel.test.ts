import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useHWAccelDetection } from "./system";
function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
function page() {
  return {
    resolved: "qsv",
    source: "local",
    intel_detected: true,
    render_devices: [],
    render_device_details: [],
    detected_backends: [{ backend: "qsv", verified: false, skipped: true }],
  };
}
const response = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("reads real v2 inventory without treating configured backend as verified", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response(page()));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useHWAccelDetection(), fixture());
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/admin/system/hw-accel");
  expect(result.current.data).toMatchObject(page());
});
it.each([null, "pin-b", "pin-a"])(
  "hides cached hardware inventory after PIN setter %s",
  async (pin) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(page()))
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result, rerender } = renderHook(() => useHWAccelDetection(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    act(() => setProfileToken(pin));
    rerender();
    expect(result.current.data).toBeUndefined();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((q) => q.queryKey),
      ),
    ).not.toMatch(/pin-a|pin-b/);
  },
);
it("does not read when disabled and never replays a 401", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        type: "https://silo.dev/problems/authentication_required",
        title: "Authentication required",
        status: 401,
        detail: "Expired",
        instance: "synthetic",
      }),
      { status: 401, headers: { "Content-Type": "application/problem+json" } },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result, rerender } = renderHook(({ enabled }) => useHWAccelDetection(enabled), {
    ...fixture(),
    initialProps: { enabled: false },
  });
  expect(fetchMock).not.toHaveBeenCalled();
  rerender({ enabled: true });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
});
it("rejects inventory decoded after profile replacement", async () => {
  let release!: (s: string) => void;
  const res = response({});
  res.text = () =>
    new Promise((resolve) => {
      release = resolve;
    });
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValueOnce(res)
      .mockImplementation(() => new Promise(() => {})),
  );
  const { client, wrapper } = fixture();
  renderHook(() => useHWAccelDetection(), { wrapper });
  await waitFor(() => expect(release).toBeTypeOf("function"));
  const key = client.getQueryCache().getAll()[0]!.queryKey;
  act(() => setProfileId("profile-b"));
  await act(async () => release(JSON.stringify(page())));
  await waitFor(() => expect(client.getQueryState(key)?.status).toBe("error"));
  expect(client.getQueryData(key)).toBeUndefined();
});
