// @vitest-environment jsdom
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { act, renderHook, cleanup, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { adminKeys } from "@/hooks/queries/keys";
import { setAccessToken, setRefreshToken } from "@/api/client";
import { fetchPolicySnapshot } from "@/api/adminPolicy";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import {
  usePolicyCapability,
  useActivatePolicyVersion,
  useCreatePolicyDocument,
  useCreatePolicyVersion,
  useDeletePolicyDocument,
  useSetPolicyDocumentEnabled,
  useSimulatePolicy,
  useValidatePolicy,
} from "./policy";

beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("old-access");
  setRefreshToken("refresh-token");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { mutations: { retry: 2 }, queries: { retry: false } } })
      }
    >
      {children}
    </QueryClientProvider>
  );
}
it("sends each nonretryable policy mutation only once on401 even with refresh available", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    jsonResponse(
      {
        type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
        title: "Authentication required",
        status: 401,
        detail: "Sign in again.",
      },
      401,
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({
      create: useCreatePolicyDocument(),
      save: useCreatePolicyVersion(),
      activate: useActivatePolicyVersion(),
      enable: useSetPolicyDocumentEnabled(),
      remove: useDeletePolicyDocument(),
      validate: useValidatePolicy(),
      simulate: useSimulatePolicy(),
    }),
    { wrapper },
  );
  const calls: Array<() => Promise<unknown>> = [
    () => result.current.create.mutateAsync({ domain: "scope", name: "Fixture" }),
    () => result.current.save.mutateAsync({ documentId: "12", source: "source" }),
    () =>
      result.current.activate.mutateAsync({ documentId: "12", version: "95", etag: '"original"' }),
    () =>
      result.current.enable.mutateAsync({ documentId: "12", enabled: false, etag: '"original"' }),
    () => result.current.remove.mutateAsync({ documentId: "12", etag: '"original"' }),
    () => result.current.validate.mutateAsync({ domain: "scope", source: "source" }),
    () => result.current.simulate.mutateAsync({ domain: "scope", input: {} }),
  ];
  for (const call of calls) {
    const before = fetchMock.mock.calls.length;
    await act(async () => {
      await expect(call()).rejects.toMatchObject({ status: 401 });
    });
    expect(fetchMock).toHaveBeenCalledTimes(before + 1);
  }
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/auth/refresh"))).toBe(false);
  const activation = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/active-version"));
  expect(JSON.parse(String(activation?.[1]?.body))).toEqual({ version_id: "95" });
  expect(new Headers(activation?.[1]?.headers).get("If-Match")).toBe('"original"');
});
it("keeps normal authentication refresh for canonical reads", async () => {
  let reads = 0;
  const fetchMock = vi.fn<typeof fetch>(async (url) => {
    if (String(url).endsWith("/auth/refresh"))
      return jsonResponse({ access_token: "new-access", refresh_token: "new-refresh" });
    reads++;
    if (reads === 1)
      return jsonResponse(
        {
          type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
          title: "Authentication required",
          status: 401,
        },
        401,
      );
    return jsonResponse({
      id: "12",
      domain: "scope",
      name: "Read",
      enabled: false,
      active_version_id: null,
      created_at: "2026-09-05T00:00:00.000Z",
      updated_at: "2026-09-05T00:00:00.000Z",
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  expect((await fetchPolicySnapshot("12")).etag).toBe('"revision-1"');
  expect(reads).toBe(2);
  expect(fetchMock).toHaveBeenCalledTimes(3);
});

it("caches a saved compile-invalid version by its ID rather than its ordinal", async () => {
  const saved = {
    id: "95",
    document_id: "12",
    version_number: 3,
    source: "invalid",
    source_sha256: "fixture",
    compiled_ok: false,
    compile_error: "Invalid source",
    comment: null,
    created_by_user_id: null,
    created_at: "2026-09-05T00:00:00.000Z",
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse(saved, 201)),
  );
  const { result } = renderHook(
    () => ({ save: useCreatePolicyVersion(), client: useQueryClient() }),
    { wrapper },
  );
  await act(async () => {
    expect(
      await result.current.save.mutateAsync({ documentId: "12", source: "invalid" }),
    ).toMatchObject({ id: "95", compiled_ok: false });
  });
  expect(result.current.client.getQueryData(adminKeys.policyVersion("12", "95"))).toEqual(saved);
  expect(result.current.client.getQueryData(adminKeys.policyVersion("12", "3"))).toBeUndefined();
});

it("discovers policy through v2 without requiring the editor", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    jsonResponse({
      enabled: false,
      editor_available: false,
      decision_types: ["scope"],
      generation: 0,
      degraded: false,
      eval_timeouts: 0,
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => usePolicyCapability(), { wrapper });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/policy/capability");
  expect(result.current.data?.enabled).toBe(false);
});
