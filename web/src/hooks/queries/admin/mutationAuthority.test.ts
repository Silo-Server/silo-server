// Each admin mutation hook inlines its own profile-authority fence, so every
// hook keeps its own row here rather than relying on a sibling's test.
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  useCreateAutoscanConnection,
  useCreateAutoscanSource,
  useCreateAutoscanWebhook,
  useDeleteAutoscanConnection,
  useDeleteAutoscanSource,
  useTestAutoscanConnection,
  useTriggerAutoscan,
  useUpdateAutoscanConnection,
  useUpdateAutoscanSettings,
  useUpdateAutoscanSource,
} from "../useAutoscan";
import { useDeleteBrandingAsset, useUploadBrandingAsset } from "./branding";
import { useResetAdminDashboardLayout, useSaveAdminDashboardLayout } from "./dashboardLayout";
import {
  useCreatePluginRepository,
  useDeletePluginInstallation,
  useDeletePluginRepository,
  useRestartPluginInstallation,
  useSavePluginConfig,
  useUpdatePluginRepository,
} from "./plugins";

const connection = {
  name: "Synthetic",
  kind: "sonarr",
  base_url: "https://example.invalid",
  api_key_ref: "synthetic-ref",
};
const connectionCheck = { base_url: "https://example.invalid", api_key_ref: "synthetic-ref" };
const autoscanSettings = { enabled: true, default_poll_interval_seconds: 120, debounce_seconds: 0 };
const source = () => ({
  plugin_id: "plugin",
  capability_id: "cap",
  enabled: true,
  connection_id: null,
  poll_interval_seconds: null,
  path_rewrites: [{ from: "/old", to: "/new" }],
  source_config: { key: "old" },
});
const layout = { version: 1, entries: [{ id: "users", span: 5, rows: 4 }] };
const repository = {
  url: "https://example.invalid/index.json",
  display_name: "Synthetic",
  enabled: true,
};
const pluginConfig = { key: "account", value: { region: "us-east" }, clear_secrets: ["api_key"] };
const png = () => new File(["\x89PNG"], "logo.png", { type: "image/png" });

type Mutation = { isPaused: boolean; isError: boolean };
type Row = { name: string; useHook: () => Mutation; send: (hook: Mutation) => void };
function row<T extends Mutation>(useHook: () => T, send: (hook: T) => void): Row {
  return { name: useHook.name, useHook, send: send as (hook: Mutation) => void };
}

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

it.each([
  row(useCreateAutoscanConnection, (h) => h.mutate(connection)),
  row(useDeleteAutoscanConnection, (h) => h.mutate("connection-a")),
  row(useTestAutoscanConnection, (h) => h.mutate(connectionCheck)),
  row(useUpdateAutoscanConnection, (h) => h.mutate({ id: "connection-a", body: connection })),
  row(useUpdateAutoscanSettings, (h) => h.mutate(autoscanSettings)),
  row(useCreateAutoscanSource, (h) => h.mutate(source())),
  row(useDeleteAutoscanSource, (h) => h.mutate("source-a")),
  row(useCreateAutoscanWebhook, (h) => h.mutate("source-a")),
  row(useTriggerAutoscan, (h) => h.mutate()),
  row(useDeleteBrandingAsset, (h) => h.mutate({ kind: "login_bg" })),
  row(useResetAdminDashboardLayout, (h) => h.mutate()),
  row(useSaveAdminDashboardLayout, (h) => h.mutate(layout, '"A"')),
  row(useSavePluginConfig, (h) => h.mutate({ id: 7, body: pluginConfig })),
  row(useRestartPluginInstallation, (h) => h.mutate(7)),
  row(useDeletePluginInstallation, (h) => h.mutate(7)),
  row(useCreatePluginRepository, (h) => h.mutate(repository)),
  row(useDeletePluginRepository, (h) => h.mutate(7)),
  row(useUpdatePluginRepository, (h) => h.mutate({ id: 7, body: repository })),
])("$name refuses a queued mutation after authority replacement", async ({ useHook, send }) => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useHook, fixture());
  act(() => send(result.current));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});

// Non-idempotent writes must not refresh and resend after an uncertain outcome,
// even when the query client would retry three times.
const uncertain = (failures: string[], { name, useHook, send }: Row) =>
  failures.map((failure) => ({ name, failure, useHook, send }));
it.each([
  ...uncertain(
    ["401", "network"],
    row(useCreateAutoscanConnection, (h) => h.mutate(connection)),
  ),
  ...uncertain(
    ["401", "network"],
    row(useTestAutoscanConnection, (h) => h.mutate(connectionCheck)),
  ),
  ...uncertain(
    ["401", "network"],
    row(useDeleteAutoscanSource, (h) => h.mutate("source-a")),
  ),
  ...uncertain(
    ["401", "network"],
    row(useCreateAutoscanSource, (h) => h.mutate(source())),
  ),
  ...uncertain(
    ["401", "network"],
    row(useUpdateAutoscanSource, (h) => h.mutate({ id: "source-a", body: source() })),
  ),
  ...uncertain(
    ["401", "network"],
    row(useCreateAutoscanWebhook, (h) => h.mutate("source-a")),
  ),
  ...uncertain(
    ["401", "409", "network"],
    row(useTriggerAutoscan, (h) => h.mutate()),
  ),
  ...uncertain(
    ["401", "network"],
    row(useRestartPluginInstallation, (h) => h.mutate(7)),
  ),
  ...uncertain(
    ["401", "network"],
    row(useCreatePluginRepository, (h) => h.mutate(repository)),
  ),
  ...uncertain(
    ["401"],
    row(useUploadBrandingAsset, (h) => h.mutate({ kind: "mark", file: png() })),
  ),
])("$name never replays an uncertain $failure under global retry3", async (c) => {
  const status = Number(c.failure);
  const fetchMock =
    c.failure === "network"
      ? vi.fn().mockRejectedValue(new Error("connection lost"))
      : vi.fn().mockResolvedValue(
          new Response(
            JSON.stringify({
              type: `https://silo.dev/problems/${status === 401 ? "authentication_required" : "conflict"}`,
              title: status === 401 ? "Unauthorized" : "Conflict",
              status,
              detail: "Expired",
              instance: "synthetic",
            }),
            { status, headers: { "Content-Type": "application/problem+json" } },
          ),
        );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(c.useHook, fixture());
  act(() => c.send(result.current));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
});

it.each([
  {
    ...row(useCreateAutoscanConnection, (h) => h.mutate(connection)),
    ack: { id: "created", name: "Synthetic", kind: "sonarr", has_api_key: true },
  },
  {
    ...row(useCreatePluginRepository, (h) => h.mutate(repository)),
    ack: { ...repository, id: "7", source_kind: "external", managed: false },
  },
])(
  "$name does not invalidate the new authority after a late creation acknowledgement",
  async ({ useHook, send, ack }) => {
    let release!: (s: string) => void;
    const res = new Response(null, {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
    res.text = () =>
      new Promise((resolve) => {
        release = resolve;
      });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(res));
    const { client, wrapper } = fixture();
    const invalidation = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(useHook, { wrapper });
    act(() => send(result.current));
    await waitFor(() => expect(release).toBeTypeOf("function"));
    act(() => setProfileId("profile-b"));
    await act(async () => release(JSON.stringify(ack)));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(invalidation).not.toHaveBeenCalled();
  },
);
