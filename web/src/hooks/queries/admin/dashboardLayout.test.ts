import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  useAdminDashboardLayout,
  useResetAdminDashboardLayout,
  useSaveAdminDashboardLayout,
} from "./dashboardLayout";

const layout = { version: 1, entries: [{ id: "libraries", span: 7, rows: 4 }] };
const saved = { layout, updated_at: "2026-09-06T10:00:00.123Z" };
const absent = { layout: null, updated_at: null };
const response = (body: unknown, etag = '"A"') =>
  new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json", ETag: etag },
  });
function fixture() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
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
it.each([saved, absent])("reads canonical v2 saved/null layout", async (body) => {
  const fetchMock = vi.fn().mockResolvedValue(response(body));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useAdminDashboardLayout, fixture());
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/admin/dashboard/layout");
  expect(result.current.data).toEqual({ ...body, etag: '"A"' });
});
it.each([null, "pin-b", "pin-a"])("hides cached success after PIN transition %s", async (pin) => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(saved))
    .mockImplementation(() => new Promise(() => {}));
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const { result, rerender } = renderHook(useAdminDashboardLayout, { wrapper });
  await waitFor(() => expect(result.current.data).toEqual({ ...saved, etag: '"A"' }));
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
  ).not.toMatch(/pin-a|pin-b|synthetic-admin/);
});
it("rejects an old authority response after asynchronous decode", async () => {
  let release!: (s: string) => void;
  const deferred = response({});
  deferred.text = () =>
    new Promise((resolve) => {
      release = resolve;
    });
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValueOnce(deferred)
      .mockImplementation(() => new Promise(() => {})),
  );
  const { client, wrapper } = fixture();
  renderHook(useAdminDashboardLayout, { wrapper });
  await waitFor(() => expect(release).toBeTypeOf("function"));
  const key = client.getQueryCache().getAll()[0]!.queryKey;
  act(() => setProfileId("profile-b"));
  await act(async () => release(JSON.stringify(saved)));
  await waitFor(() => expect(client.getQueryState(key)?.status).toBe("error"));
  expect(client.getQueryData(key)).toBeUndefined();
});
it("serializes guarded v2 save/reset and refetches canonical data without fabricating a document from the acknowledgement", async () => {
  let canonical: unknown = absent;
  let canonicalTag = '"A"';
  let release!: () => void;
  const writes: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((_url: unknown, init: RequestInit) => {
      if (init.method === "PUT") {
        expect(new Headers(init.headers).get("If-Match")).toBe('"A"');
        writes.push("PUT");
        return new Promise<Response>((resolve) => {
          release = () => {
            canonical = saved;
            canonicalTag = '"B"';
            resolve(new Response(null, { status: 204, headers: { ETag: canonicalTag } }));
          };
        });
      }
      if (init.method === "DELETE") {
        writes.push("DELETE");
        canonical = absent;
        canonicalTag = '"C"';
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      return Promise.resolve(response(canonical, canonicalTag));
    }),
  );
  const { result } = renderHook(
    () => ({
      read: useAdminDashboardLayout(),
      save: useSaveAdminDashboardLayout(),
      reset: useResetAdminDashboardLayout(),
    }),
    fixture(),
  );
  await waitFor(() => expect(result.current.read.data).toEqual({ ...absent, etag: '"A"' }));
  act(() => {
    result.current.save.mutate(layout, result.current.read.data!.etag);
    result.current.reset.mutate();
  });
  await waitFor(() => expect(writes).toEqual(["PUT"]));
  act(() => release());
  await waitFor(() => expect(writes).toEqual(["PUT", "DELETE"]));
  await waitFor(() => expect(result.current.reset.isSuccess).toBe(true));
  expect(result.current.read.data).toEqual({ ...absent, etag: '"C"' });
});
it("a late acknowledged v2 write is rejected locally and never seeds the new authority cache", async () => {
  let release!: () => void;
  const { client, wrapper } = fixture();
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((_url: unknown, init: RequestInit) =>
      init.method === "PUT"
        ? new Promise<Response>((resolve) => {
            release = () => resolve(new Response(null, { status: 204, headers: { ETag: '"B"' } }));
          })
        : Promise.resolve(response(absent)),
    ),
  );
  const { result, rerender } = renderHook(
    () => ({ read: useAdminDashboardLayout(), save: useSaveAdminDashboardLayout() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  act(() => result.current.save.mutate(layout, result.current.read.data!.etag));
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  rerender();
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  act(() => release());
  await waitFor(() => expect(result.current.save.isError).toBe(true));
  expect(result.current.read.data).toEqual({ ...absent, etag: '"A"' });
  expect(client.getQueryData(["admin", "dashboard", "layout"])).toBeUndefined();
});
