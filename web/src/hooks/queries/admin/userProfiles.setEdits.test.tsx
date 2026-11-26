// @vitest-environment jsdom
// The "Changed on this server" card reads its list once and doesn't poll, so
// a profile write on the same tab must refetch it: an administrator's edit of
// a profile from the person's app adds an override, and a delete hides the
// profile, both of which the card has to show.
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import {
  useAdminUserProfileSetEdits,
  useClearAdminUserProfileSetEdit,
  useDeleteAdminUserProfile,
} from "./userProfiles";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

beforeEach(() => {
  setAccessToken("synthetic-access");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-admin");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("refetches the edits list after a profile delete on the tab", async () => {
  let editReads = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input instanceof Request ? input.url : input), "http://silo.test");
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (url.pathname.endsWith("/profile-set/edits")) {
        editReads++;
        return new Response(JSON.stringify({ edits: [] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (method === "DELETE") return new Response(null, { status: 204 });
      return new Response(JSON.stringify({ items: [] }), {
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(
    () => ({ edits: useAdminUserProfileSetEdits(7, true), remove: useDeleteAdminUserProfile(7) }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.edits.isSuccess).toBe(true));
  expect(editReads).toBe(1);
  act(() => result.current.remove.mutate("profile-guest"));
  await waitFor(() => expect(result.current.remove.isSuccess).toBe(true));
  await waitFor(() => expect(editReads).toBe(2));
});

// A clear refused because the override changed since the owner saw it must
// bring the current record onto the card, so the owner reads it again.
it("refetches the edits list after a stale clear, and sends the shown etag", async () => {
  let editReads = 0;
  let sentIfMatch: string | null = null;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request =
        input instanceof Request
          ? input
          : new Request(new URL(String(input), "http://silo.test"), init);
      const url = new URL(request.url);
      if (url.pathname.endsWith("/profile-set/edits")) {
        editReads++;
        return new Response(JSON.stringify({ edits: [] }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (request.method === "PATCH") {
        sentIfMatch = request.headers.get("If-Match");
        return new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
            title: "Precondition failed",
            status: 412,
          }),
          { status: 412, headers: { "Content-Type": "application/problem+json" } },
        );
      }
      return new Response(JSON.stringify({ items: [] }), {
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(
    () => ({
      edits: useAdminUserProfileSetEdits(7, true),
      clear: useClearAdminUserProfileSetEdit(7),
    }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.edits.isSuccess).toBe(true));
  act(() =>
    result.current.clear.mutate({
      kind: "override",
      profileId: "p-kid",
      field: "pin",
      etag: '"pso1-shown"',
    }),
  );
  await waitFor(() => expect(result.current.clear.isError).toBe(true));
  expect(sentIfMatch).toBe('"pso1-shown"');
  await waitFor(() => expect(editReads).toBe(2));
});
