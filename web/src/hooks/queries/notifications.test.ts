import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor, act, cleanup } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import {
  notificationInboxKeys,
  useNotifications,
  applyNotificationRead,
  applyNotificationCreated,
} from "./notifications";
const row = {
  id: "delivery-1",
  type: "episode.available",
  profile_id: "owner",
  reason_flags: {},
  created_at: "2026-01-01T00:00:00.000Z",
  read_at: null,
};
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
it("invalidates cutoff events without marking ambiguous millisecond deliveries read", () => {
  const client = new QueryClient();
  const key = notificationInboxKeys.list("all");
  client.setQueryData(key, {
    pages: [{ notifications: [row, { ...row, id: "delivery-2" }], read_cutoff: "frozen" }],
    pageParams: [undefined],
  });
  client.setQueryData(notificationInboxKeys.count(), 2);
  applyNotificationRead(client, {
    profile_id: "owner",
    through_created_at: "2026-01-01T00:00:00.000100Z",
    through_id: "delivery-1",
  });
  expect(client.getQueryState(key)?.isInvalidated).toBe(true);
  expect(client.getQueryData(notificationInboxKeys.count())).toBe(2);
  expect(client.getQueryData(key)).toMatchObject({
    pages: [{ notifications: [{ read_at: null }, { read_at: null }], read_cutoff: "frozen" }],
  });
});
it("preserves single-item and legacy all-read reducers and ignores another profile", () => {
  const client = new QueryClient();
  const key = notificationInboxKeys.list("all");
  client.setQueryData(key, { pages: [{ notifications: [row] }], pageParams: [undefined] });
  client.setQueryData(notificationInboxKeys.count(), 1);
  applyNotificationRead(client, { profile_id: "other", all: true });
  expect(client.getQueryData(notificationInboxKeys.count())).toBe(1);
  applyNotificationRead(client, { profile_id: "owner", id: row.id });
  expect(client.getQueryData(notificationInboxKeys.count())).toBe(0);
  applyNotificationRead(client, { profile_id: "owner", all: true });
  expect(client.getQueryData(key)).toMatchObject({
    pages: [{ notifications: [{ id: row.id, read_at: expect.any(String) }] }],
  });
});
it("realtime prepends retain the displayed cutoff", () => {
  const client = new QueryClient();
  const key = notificationInboxKeys.list("all");
  client.setQueryData(key, {
    pages: [{ notifications: [row], read_cutoff: "original" }],
    pageParams: [undefined],
  });
  applyNotificationCreated(client, { ...row, id: "new" });
  expect(client.getQueryData(key)).toMatchObject({
    pages: [{ notifications: [{ id: "new" }, { id: row.id }], read_cutoff: "original" }],
  });
});
it("validates cursor traversal while allowing ordinary refetch of cached pages", async () => {
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const next = new URL(String(input), "http://localhost").searchParams.has("cursor");
    return new Response(
      JSON.stringify({
        items: [{ ...row, id: next ? "two" : "one" }],
        page: next ? { has_more: false } : { has_more: true, next_cursor: "next" },
        read_cutoff: "cutoff",
      }),
      { headers: { "Content-Type": "application/json" } },
    );
  });
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useNotifications(), { wrapper });
  await waitFor(() => expect(result.current.data?.pages).toHaveLength(1));
  await act(async () => {
    await result.current.fetchNextPage();
  });
  await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
  await act(async () => {
    await result.current.refetch();
  });
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(4));
  expect(result.current.isError).toBe(false);
});

it("keeps the scoped email reader current after the v2 address admission", async () => {
  const { useEmailNotificationPreferences, useRequestEmailNotificationAddress } =
    await import("./notifications");
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const original = { mode: "off", custom_email: "", pending_email: "", can_edit_address: true };
  let pending = false;
  const fetch = vi.fn<typeof globalThis.fetch>(async (input, init) => {
    const url = String(input);
    let body: unknown;
    if (url === "/api/v2/notifications/email-preferences/address" && init?.method === "PUT") {
      const intent = JSON.parse(String(init.body));
      expect(intent.email).toBe("pending@example.test");
      expect(intent.verification_id).toEqual(expect.any(String));
      pending = true;
      body = {
        verification_id: intent.verification_id,
        current: true,
        expires_at: "2026-09-08T00:00:00Z",
      };
    } else if (url === "/api/v2/notifications/email-preferences" && init?.method === "GET") {
      body = pending ? { ...original, pending_email: "pending@example.test" } : original;
    } else throw new Error(`Unexpected request: ${init?.method} ${url}`);
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(
    () => ({
      query: useEmailNotificationPreferences(),
      request: useRequestEmailNotificationAddress(),
    }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.query.data).toEqual(original));
  await act(async () => {
    await result.current.request.mutateAsync("pending@example.test");
  });
  await waitFor(() =>
    expect(result.current.query.data?.pending_email).toBe("pending@example.test"),
  );
  expect(String(fetch.mock.calls[0]![0])).toContain("/api/v2/notifications/email-preferences");
  expect(String(fetch.mock.calls[1]![0])).toContain(
    "/api/v2/notifications/email-preferences/address",
  );
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(fetch.mock.calls.map(([, init]) => init?.method)).toEqual(["GET", "PUT", "GET"]);
});
