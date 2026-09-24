import { describe, expect, it, vi } from "vitest";
import { v2 } from "@/api/v2/request";
import {
  connectWatchProviderAPIKey,
  fetchWatchProviders,
  fetchWatchProviderConnection,
  fetchWatchProviderSyncRuns,
  pollWatchProviderDeviceAuth,
  startWatchProviderDeviceAuth,
  triggerWatchProviderSync,
  updateWatchProviderConnection,
  deleteWatchProviderConnection,
  syncRunFinished,
  type WatchProviderSyncRun,
} from "./watchProviders";

vi.mock("@/api/v2/request", () => ({
  v2: vi.fn(async (operation: string) => {
    if (operation.endsWith("/auth/device-code")) return { id: "auth-1", user_code: "PUBLIC" };
    if (operation.endsWith("/sync"))
      return { run: { id: "run-1", status: "running" }, retry_after_seconds: 0 };
    return { items: [] };
  }),
  V2ProblemError: class extends Error {},
}));

describe("watch provider v2 queries", () => {
  it("retains the server version with the displayed connection", async () => {
    vi.mocked(v2).mockResolvedValueOnce({ connected: true, display_name: "Provider" } as never);
    vi.mocked(v2).mockImplementationOnce(async (_operation, options) => {
      options?.onResponse?.(new Response(null, { headers: { ETag: '"version-1"' } }));
      return { import_watched_enabled: true } as never;
    });
    expect(await fetchWatchProviderConnection("trakt")).toMatchObject({
      connected: true,
      etag: '"version-1"',
    });
  });

  it("unwraps provider and bounded activity collections", async () => {
    await expect(fetchWatchProviders()).resolves.toEqual({ providers: [] });
    await expect(fetchWatchProviderSyncRuns("trakt")).resolves.toEqual({ runs: [] });
    expect(v2).toHaveBeenLastCalledWith("GET /api/v2/watch-providers/{provider}/sync-runs", {
      path: { provider: "trakt" },
      query: { limit: 10 },
    });
  });
  it("passes credentials in typed bodies and keeps provider keys as path values", async () => {
    await expect(startWatchProviderDeviceAuth("trakt")).resolves.toMatchObject({
      id: "auth-1",
      user_code: "PUBLIC",
    });
    await pollWatchProviderDeviceAuth("trakt", "auth-1");
    expect(v2).toHaveBeenLastCalledWith("POST /api/v2/watch-providers/{provider}/auth/poll", {
      path: { provider: "trakt" },
      body: { auth_session_id: "auth-1" },
    });
    await connectWatchProviderAPIKey("plugin:4:floppy", "token", { floppy: { region: "test" } });
    expect(v2).toHaveBeenLastCalledWith("POST /api/v2/watch-providers/{provider}/auth/api-key", {
      path: { provider: "plugin:4:floppy" },
      body: { api_key: "token", connection_config: { floppy: { region: "test" } } },
    });
    await updateWatchProviderConnection("trakt", { scrobble_enabled: true }, '"version-1"');
    expect(v2).toHaveBeenLastCalledWith("PATCH /api/v2/watch-providers/{provider}/connection", {
      path: { provider: "trakt" },
      body: { scrobble_enabled: true },
      headers: { "If-Match": '"version-1"' },
      onResponse: expect.any(Function),
    });
    await deleteWatchProviderConnection("trakt");
    expect(v2).toHaveBeenLastCalledWith("DELETE /api/v2/watch-providers/{provider}/connection", {
      path: { provider: "trakt" },
    });
    await expect(triggerWatchProviderSync("trakt")).resolves.toMatchObject({
      run: { id: "run-1" },
    });
  });
});

describe("syncRunFinished", () => {
  const run = (id: string, status: WatchProviderSyncRun["status"]) =>
    ({ id, status }) as WatchProviderSyncRun;

  it("fires when a watched run leaves queued or running", () => {
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

  it("does not fire on the first read", () => {
    expect(syncRunFinished(undefined, run("a", "success"))).toBe(false);
  });
});
