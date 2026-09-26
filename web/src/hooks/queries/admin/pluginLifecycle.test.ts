import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  useApplyPluginUpdate,
  useDeletePluginInstallation,
  useInstallPlugin,
  useRestartPluginInstallation,
  useUpdatePluginInstallation,
} from "./plugins";
import { adminKeys } from "../keys";

const installation = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  repository_id: "4",
  plugin_id: "org.example.a",
  version: "1.0.0",
  install_path: "/plugins/a",
  enabled: true,
  kind: "plugin",
  update_policy: "auto",
  source_kind: "silo",
  updates_paused: false,
  capabilities: [],
  global_config_schema: [],
  user_config_schema: [],
  routes: [],
  assets: [],
  metadata: {},
  global_configs: [],
  auth_bindings: [],
  task_bindings: [],
  created_at: "2026-09-07T00:00:00.000Z",
  updated_at: "2026-09-07T00:00:00.000Z",
  ...extra,
});
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (type: string, status: number, detail = "synthetic") =>
  new Response(
    JSON.stringify({
      type: `https://silo.dev/problems/${type}`,
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

it("installs from the catalog once with string identifiers and projects the row", async () => {
  const fetchMock = vi.fn().mockResolvedValue(json(installation("21"), 201));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useInstallPlugin, fixture());
  let outcome: Awaited<ReturnType<typeof result.current.mutateAsync>> | undefined;
  await act(async () => {
    outcome = await result.current.mutateAsync({
      repository_id: 4,
      plugin_id: "org.example.a",
      version: "1.0.0",
    });
  });
  expect(outcome?.id).toBe(21);
  expect(outcome?.repository_id).toBe(4);
  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(String(url)).toBe("/api/v2/admin/plugins/installations");
  expect(init.method).toBe("POST");
  expect(JSON.parse(String(init.body))).toEqual({
    repository_id: "4",
    plugin_id: "org.example.a",
    version: "1.0.0",
  });
  expect((init.headers as Record<string, string>)["X-Profile-Id"]).toBe("profile-a");
});

it("assigns update_policy through PUT once and keeps the projected row", async () => {
  const fetchMock = vi.fn().mockResolvedValue(json(installation("7", { update_policy: "notify" })));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useUpdatePluginInstallation, fixture());
  act(() => result.current.mutate({ id: 7, body: { update_policy: "notify" } }));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(String(url)).toBe("/api/v2/admin/plugins/installations/7");
  expect(init.method).toBe("PUT");
  expect(JSON.parse(String(init.body))).toEqual({ update_policy: "notify" });
  expect(result.current.data?.update_policy).toBe("notify");
});

it("restarts a failed resident and refreshes installation and network status", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      json(installation("7", { runtime: { resident: true, state: "running", restart_count: 0 } })),
    );
  vi.stubGlobal("fetch", fetchMock);
  const context = fixture();
  const invalidate = vi.spyOn(context.client, "invalidateQueries");
  const { result } = renderHook(useRestartPluginInstallation, context);
  act(() => result.current.mutate(7));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(String(url)).toBe("/api/v2/admin/plugins/installations/7/restart");
  expect(init.method).toBe("POST");
  expect((init.headers as Record<string, string>)["X-Profile-Id"]).toBe("profile-a");
  expect(result.current.data?.runtime.state).toBe("running");
  expect(invalidate).toHaveBeenCalledWith({ queryKey: adminKeys.pluginInstallations() });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: adminKeys.networkAccessStatusRoot() });
});

it.each(["401", "network"])("does not replay a restart after %s failure", async (failure) => {
  const fetchMock =
    failure === "network"
      ? vi.fn().mockRejectedValue(new Error("connection lost"))
      : vi.fn().mockResolvedValue(problem("authentication_required", 401));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useRestartPluginInstallation, fixture());
  act(() => result.current.mutate(7));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
});

it("does not restart after an offline request's administrator authority changes", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useRestartPluginInstallation, fixture());
  act(() => result.current.mutate(7));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});

it.each(["401", "network"])(
  "never replays an uncertain %s apply-update under global retry3",
  async (failure) => {
    const fetchMock =
      failure === "network"
        ? vi.fn().mockRejectedValue(new Error("connection lost"))
        : vi.fn().mockResolvedValue(problem("authentication_required", 401));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useApplyPluginUpdate, fixture());
    act(() => result.current.mutate(7));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v2/admin/plugins/installations/7/update",
    );
    expect(fetchMock.mock.calls[0]?.[1].method).toBe("POST");
  },
);

it("captures the deletion target before offline queueing and refuses after authority replacement", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useDeletePluginInstallation, fixture());
  act(() => result.current.mutate(7));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});

it("deletes once and surfaces a 409 conflict detail", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(problem("conflict", 409, "Built-in host providers cannot be modified."));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useDeletePluginInstallation, fixture());
  act(() => result.current.mutate(7));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/admin/plugins/installations/7");
  expect(fetchMock.mock.calls[0]?.[1].method).toBe("DELETE");
});
