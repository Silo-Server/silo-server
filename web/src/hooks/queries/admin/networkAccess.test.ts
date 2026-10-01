import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
} from "@/api/client";
import { adminKeys } from "../keys";
import { useAdminNetworkAccessStatus, useConnectNetworkAccess } from "./networkAccess";

function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}

const status = {
  provider: "tailscale",
  hosts: [
    {
      host: { id: "api", role: "api", name: "API server" },
      state: "awaiting_authorization",
      auth_url: "https://login.example.test/enroll/secret",
      addresses: [],
    },
  ],
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});

it("writes command results only to the requesting administrator's cache", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify(status), { headers: { "Content-Type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const context = fixture();
  const authority = captureProfileRequestContext()!;
  const oldKey = [
    ...adminKeys.networkAccessStatus("tailscale"),
    authority.serverOrigin,
    authority.authContextVersion,
    authority.profileId,
    authority.profileTokenGeneration,
  ];
  const oldStatus = { provider: "tailscale", hosts: [] };
  context.client.setQueryData(oldKey, oldStatus);
  setProfileToken("pin-b");
  const { result } = renderHook(useConnectNetworkAccess, context);
  act(() => result.current.mutate({ provider: "tailscale", hosts: ["api"] }));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(context.client.getQueryData(oldKey)).toEqual(oldStatus);
  const currentKey = [
    ...oldKey.slice(0, -1),
    captureProfileRequestContext()!.profileTokenGeneration,
  ];
  expect(context.client.getQueryData(currentKey)).toEqual(status);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it.each([null, "pin-b", "pin-a"])(
  "hides cached enrollment URLs after PIN transition %s",
  async (pin) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(status), { headers: { "Content-Type": "application/json" } }),
      )
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result, rerender } = renderHook(() => useAdminNetworkAccessStatus("tailscale"), {
      wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.hosts[0]?.auth_url).toBe(status.hosts[0]?.auth_url);
    act(() => setProfileToken(pin));
    rerender();
    expect(result.current.data).toBeUndefined();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((query) => query.queryKey),
      ),
    ).not.toMatch(/pin-a|pin-b/);
  },
);
