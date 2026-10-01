import {
  setAccessToken,
  setProfileId,
  setProfileToken,
  captureProfileRequestContext,
} from "@/api/client";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));
vi.mock("@/api/v2/request", () => ({ v2: mocks.v2 }));
import {
  useAdminServerSettings,
  useAdminRestartKeys,
  useAdminSensitiveStatus,
  useCheckAdminSettingsConnection,
} from "./settings";

beforeEach(() => {
  mocks.v2.mockReset();
  localStorage.clear();
  setAccessToken("synthetic-admin");
  setProfileId("admin-profile");
  setProfileToken(null);
});
afterEach(cleanup);

function wrapper({ children }: { children: ReactNode }) {
  return createElement(
    QueryClientProvider,
    { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) },
    children,
  );
}

describe("administrator settings inspection", () => {
  it.each([
    [
      useAdminServerSettings,
      "GET /api/v2/admin/settings/effective",
      { "server.log_level": "debug" },
    ],
    [useAdminRestartKeys, "GET /api/v2/admin/settings/restart-keys", { keys: [], prefixes: [] }],
    [
      useAdminSensitiveStatus,
      "GET /api/v2/admin/settings/sensitive-status",
      { configured: [], managed_by_env: [] },
    ],
  ] as const)("uses v2 for %s", async (hook, operation, response) => {
    mocks.v2.mockImplementationOnce(async (_operation, options) => {
      options?.onResponse?.(new Response(null, { headers: { ETag: '"settings-1"' } }));
      return response;
    });
    const { result } = renderHook(() => hook(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual(response);
    if (hook === useAdminServerSettings) {
      expect(mocks.v2).toHaveBeenLastCalledWith(operation, {
        profileContext: captureProfileRequestContext(),
        onResponse: expect.any(Function),
      });
    } else expect(mocks.v2).toHaveBeenLastCalledWith(operation);
  });
});

it("sends a connection check once even when global mutation retries are enabled", async () => {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3, retryDelay: 0 } } });
  function retryWrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  }
  mocks.v2.mockClear();
  mocks.v2.mockRejectedValueOnce(new Error("response lost"));
  const { result } = renderHook(() => useCheckAdminSettingsConnection(), { wrapper: retryWrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ kind: "redis", body: { values: {}, dirty_keys: [] } }),
    ).rejects.toThrow("response lost");
  });
  expect(mocks.v2).toHaveBeenCalledTimes(1);
  expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/admin/settings/check/{kind}", {
    path: { kind: "redis" },
    body: { values: {}, dirty_keys: [] },
    retryAuthentication: false,
  });
});
