import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId } from "@/api/client";
import { useDiagnosticsStatus } from "./diagnostics";
function wrapper(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
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
it.each(["available", "disabled", "storage_unavailable"])(
  "retains %s and advertised limits without initiating an upload",
  async (status) => {
    const body = {
      status,
      state:
        status === "available"
          ? "available"
          : status === "disabled"
            ? "disabled"
            : "not_configured",
      revision: "1",
      server_instance_id: "synthetic-instance",
      accepted_schema_versions: [1],
      max_bundle_bytes: 1024,
      max_manifest_bytes: 128,
      retention_days: 30,
      consent_notice_version: 1,
      upload_chunk_bytes: 256,
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
      );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useDiagnosticsStatus(), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual(body);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/diagnostics/capabilities");
  },
);
it("rejects metadata decoded under a different account or profile", async () => {
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
  renderHook(() => useDiagnosticsStatus(), { wrapper: wrapper(client) });
  await reading;
  const captured = client.getQueryCache().getAll()[0]!;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ status: "available" }));
  });
  await waitFor(() => expect(captured.state.status).toBe("error"));
  expect(captured.state.data).toBeUndefined();
});
