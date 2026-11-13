import { describe, expect, it, vi } from "vitest";
import { v2 } from "@/api/v2/request";
import {
  fetchWatchProviderConnection,
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
    expect(await fetchWatchProviderConnection("simkl")).toMatchObject({
      connected: true,
      etag: '"version-1"',
    });
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
