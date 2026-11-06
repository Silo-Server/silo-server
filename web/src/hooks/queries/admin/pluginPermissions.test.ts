import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import { useAdminPluginPermissions, useApprovePluginPermissions } from "./plugins";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("primary-one");
});
afterEach(() => {
  setAccessToken(null);
  setRefreshToken(null);
  setProfileId(null);
  vi.unstubAllGlobals();
});

const permissions = (required: string[]) => ({
  installation_id: "7",
  event_access: { required: [], approved: [] },
  host_api_scopes: { requested: required, required, unsupported: [], approved: [] },
  pending: true,
  host_api_key: { issued: false, usable: false },
});
const response = (body: unknown, status = 200, etag?: string) =>
  new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
      ...(etag ? { ETag: etag } : {}),
    },
  });

it("approves with the read's validator and, on 412, reloads the new requirements without approving them", async () => {
  let current = { body: permissions(["admin:monitor:read"]), etag: '"seen"' };
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    if (init?.method === "PUT") {
      // An update asking for more landed after the read.
      current = { body: permissions(["admin:users"]), etag: '"updated"' };
      return response(
        {
          type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
          title: "Precondition Failed",
          status: 412,
        },
        412,
        current.etag,
      );
    }
    expect(String(input)).toContain("/admin/plugins/installations/7/permissions");
    return response(current.body, 200, current.etag);
  });
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(
    () => ({ read: useAdminPluginPermissions(7), approve: useApprovePluginPermissions() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.read.data?.etag).toBe('"seen"'));

  act(() => result.current.approve.mutate(7, result.current.read.data!.etag));
  await waitFor(() => expect(result.current.approve.isError).toBe(true));
  const approvals = fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT");
  expect(approvals).toHaveLength(1);
  expect(new Headers(approvals[0]?.[1]?.headers).get("If-Match")).toBe('"seen"');

  await waitFor(() => expect(result.current.read.data?.etag).toBe('"updated"'));
  expect(result.current.read.data?.host_api_scopes.required).toEqual(["admin:users"]);
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1);
});
