import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { toast } from "sonner";
import { describe, expect, it, vi } from "vitest";
import { v2, V2ProblemError, V2TimeoutError } from "@/api/v2/request";
import { t } from "@/i18n";
import {
  fetchWatchProviderConnection,
  syncRunFinished,
  usePollWatchProviderDeviceAuth,
  useTriggerWatchProviderSync,
  type WatchProviderSyncRun,
} from "./watchProviders";

vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(async (operation: string) => {
    if (operation.endsWith("/auth/device-code")) return { id: "auth-1", user_code: "PUBLIC" };
    if (operation.endsWith("/sync"))
      return { run: { id: "run-1", status: "running" }, retry_after_seconds: 0 };
    return { items: [] };
  }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return createElement(QueryClientProvider, { client }, children);
}

describe("watch provider v2 queries", () => {
  it("retains the server version with the displayed connection", async () => {
    vi.mocked(v2).mockResolvedValueOnce({ connected: true, display_name: "Provider" } as never);
    vi.mocked(v2).mockImplementationOnce(async (_operation, options) => {
      options?.onResponse?.(new Response(null, { headers: { ETag: '"version-1"' } }));
      return { import_watched_enabled: true } as never;
    });
    expect(await fetchWatchProviderConnection("simkl")).toMatchObject({
      connected: true,
      etag: '"version-1"',
    });
  });
});

// A failure toast names the action and says why below it, in the interface
// language, never as a request's English developer message.
describe("watch provider failure toasts", () => {
  it("explains a sync that got no answer", async () => {
    vi.mocked(v2).mockRejectedValueOnce(new V2TimeoutError("triggerWatchProviderSync", 30_000));
    const { result } = renderHook(() => useTriggerWatchProviderSync("simkl"), { wrapper });
    await act(() => result.current.mutateAsync().catch(() => undefined));
    expect(toast.error).toHaveBeenLastCalledWith(t("web.common.toast.watchProviders.syncFailed"), {
      description: t("web.common.ui.loadError.timeout"),
    });
  });

  it.each([
    ["conflict", 409, "web.common.toast.watchProviders.authNotApproved"],
    ["not_permitted", 403, "web.common.toast.watchProviders.authDeclined"],
  ] as const)("explains a %s answer to finishing the connection", async (type, status, reason) => {
    vi.mocked(v2).mockRejectedValueOnce(
      new V2ProblemError("pollWatchProviderDeviceAuth", {
        type: `https://siloserver.org/docs/api/v2/problems/${type}`,
        title: type,
        status,
      } as never),
    );
    const { result } = renderHook(() => usePollWatchProviderDeviceAuth("simkl"), { wrapper });
    await act(() => result.current.mutateAsync("auth-1").catch(() => undefined));
    expect(toast.error).toHaveBeenLastCalledWith(
      t("web.common.toast.watchProviders.finishAuthFailed"),
      { description: t(reason) },
    );
  });
});

describe("syncRunFinished", () => {
  const run = (id: string, status: WatchProviderSyncRun["status"]) =>
    ({ id, status }) as WatchProviderSyncRun;

  it("fires when a watched run leaves queued or running", () => {
    expect(syncRunFinished(undefined, run("a", "success"))).toBe(false);
    expect(syncRunFinished(run("a", "running"), run("a", "success"))).toBe(true);
    expect(syncRunFinished(run("a", "queued"), run("a", "warning"))).toBe(true);
    expect(syncRunFinished(run("a", "queued"), run("a", "running"))).toBe(false);
    expect(syncRunFinished(run("a", "success"), run("a", "success"))).toBe(false);
  });

  it("fires for a newer run first seen already final", () => {
    // A scheduled run can start and finish between two reads.
    expect(syncRunFinished(run("a", "success"), run("b", "success"))).toBe(true);
    expect(syncRunFinished(run("a", "running"), run("b", "failed"))).toBe(true);
    expect(syncRunFinished(run("a", "success"), run("b", "running"))).toBe(false);
  });
});
