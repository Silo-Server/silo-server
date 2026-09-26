import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  useSavePluginAuthBinding,
  useSavePluginConfig,
  useSavePluginTaskBinding,
  useTestPluginConfig,
} from "./plugins";

const configBody = { key: "account", value: { region: "us-east" }, clear_secrets: ["api_key"] };
const noContent = () => new Response(null, { status: 204 });
const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
const problem = (status: number, type: string, detail: string) =>
  new Response(
    JSON.stringify({
      type: `https://siloserver.org/docs/api/v2/problems/${type}`,
      title: type,
      status,
      detail,
      instance: "synthetic",
    }),
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
function fixture() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3, retryDelay: 0 }, queries: { retry: false } },
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
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});
it("saves configuration through v2 once with the captured body", async () => {
  const fetchMock = vi.fn().mockResolvedValue(noContent());
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useSavePluginConfig, fixture());
  const draft = { ...configBody, value: { ...configBody.value } };
  act(() => result.current.mutate({ id: 7, body: draft }));
  draft.value.region = "later";
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/api/v2/admin/plugins/installations/7/config",
  );
  const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
  expect(init.method).toBe("PUT");
  expect(JSON.parse(String(init.body))).toEqual(configBody);
  expect((init.headers as Record<string, string>)["X-Profile-Id"]).toBe("profile-a");
});
it.each(["401", "network"])(
  "never replays an uncertain %s config save under global retry",
  async (kind) => {
    const fetchMock =
      kind === "network"
        ? vi.fn().mockRejectedValue(new Error("connection lost"))
        : vi.fn().mockResolvedValue(problem(401, "authentication_required", "Expired"));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useSavePluginConfig, fixture());
    act(() => result.current.mutate({ id: 7, body: configBody }));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
  },
);
it("refuses a queued save after authority replacement", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useSavePluginConfig, fixture());
  act(() => result.current.mutate({ id: 7, body: configBody }));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});
it("returns a completed failed probe as data and never retries the probe", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(json({ success: false, message: "Connection check failed: 401" }))
    .mockResolvedValue(problem(500, "internal_error", "boom"));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useTestPluginConfig, fixture());
  const outcome = await act(() => result.current.mutateAsync({ id: 7, body: configBody }));
  expect(outcome).toEqual({ success: false, message: "Connection check failed: 401" });
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/installations/7/config/test");
  await expect(
    act(() => result.current.mutateAsync({ id: 7, body: configBody })),
  ).rejects.toThrow();
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
it("saves an auth binding and does not invalidate after late completion under another authority", async () => {
  let release!: () => void;
  const res = noContent();
  res.text = () =>
    new Promise((resolve) => {
      release = () => resolve("");
    });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(res));
  const { client, wrapper } = fixture();
  const invalidation = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(useSavePluginAuthBinding, { wrapper });
  act(() =>
    result.current.mutate({
      id: 7,
      body: {
        capability_id: "oidc",
        enabled: true,
        display_order: 1,
        auto_provision: true,
        default_login: false,
      },
    }),
  );
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  await act(async () => release());
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(invalidation).not.toHaveBeenCalled();
});
it("saves a task binding through the capability path and surfaces a 409 message", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(json({ restart_required: true }))
    .mockResolvedValueOnce(problem(409, "conflict", "Built-in host providers cannot be modified."));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useSavePluginTaskBinding, fixture());
  const input = {
    id: 7,
    capabilityId: "sync/nightly",
    body: { enabled: true, trigger: { type: "startup" } },
  };
  const data = await act(() => result.current.mutateAsync(input));
  expect(data.restart_required).toBe(true);
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/installations/7/task-bindings/sync%2Fnightly",
  );
  await expect(act(() => result.current.mutateAsync({ ...input, id: 1 }))).rejects.toMatchObject({
    status: 409,
  });
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
