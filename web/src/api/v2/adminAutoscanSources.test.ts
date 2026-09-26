import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
} from "@/api/client";
import { readAdminAutoscanSources } from "./adminAutoscanSources";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken(null);
  setProfileId("a");
});
afterEach(() => vi.unstubAllGlobals());
const row = {
  id: "source",
  plugin_id: "plugin",
  capability_id: "scan_source",
  connection_id: null,
  enabled: true,
  delivery_mode: "webhook",
  path_rewrites: [],
  source_config: {},
  label: "Source",
  webhook_configured: true,
  webhook_url: "https://server.example.test/api/v2/autoscan/webhooks/existing-token",
};
function response(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}
it("drains pages and preserves redisplayable v2 callback while adapting absent values", async () => {
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(
      response({ items: [row], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockResolvedValueOnce(response({ items: [], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetchMock);
  const result = await readAdminAutoscanSources(captureProfileRequestContext()!);
  expect(result).toEqual([
    { ...row, last_run_at: null, last_error: null, poll_interval_seconds: null },
  ]);
  expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=next");
});
it("rejects repeated continuation without returning a partial list", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockImplementation(async () =>
        response({ items: [row], page: { has_more: true, next_cursor: "same" } }),
      ),
  );
  await expect(readAdminAutoscanSources(captureProfileRequestContext()!)).rejects.toThrow(
    "continuation",
  );
});
it("discards a source URL decoded after the selected profile changes", async () => {
  let finish!: (body: string) => void;
  let start!: () => void;
  const reading = new Promise<void>((resolve) => {
    start = resolve;
  });
  const res = response({});
  vi.spyOn(res, "text").mockImplementation(() => {
    start();
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(res));
  const result = readAdminAutoscanSources(captureProfileRequestContext()!);
  await reading;
  setProfileId("b");
  finish(JSON.stringify({ items: [row], page: { has_more: false } }));
  await expect(result).rejects.toThrow();
});
