import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
} from "@/api/client";
import { readAdminAutoscanSettings, readAdminAutoscanStatus } from "./adminAutoscanInspection";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken(null);
  setProfileId("a");
});
afterEach(() => vi.unstubAllGlobals());
const response = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
it("reads desired configuration and observed status separately without losing opaque poll identity", async () => {
  const config = { enabled: true, default_poll_interval_seconds: 60, debounce_seconds: 5 };
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(config))
    .mockResolvedValueOnce(
      response({
        enabled: true,
        sources: [{ id: "source" }],
        running_polls: [{ id: "9007199254740993", elapsed_ms: 90 }],
        active_scans: 3,
        accepted_scans: 2,
        running_scans: 1,
      }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const authority = captureProfileRequestContext()!;
  expect(await readAdminAutoscanSettings(authority)).toMatchObject(config);
  const status = await readAdminAutoscanStatus(authority);
  expect(status.running_polls[0]?.id).toBe("9007199254740993");
  expect(status.sources[0]).toMatchObject({ last_run_at: null, last_error: null });
  expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
    "/api/v2/admin/autoscan/settings",
    "/api/v2/admin/autoscan/status",
  ]);
});
for (const read of [readAdminAutoscanSettings, readAdminAutoscanStatus]) {
  it(`rejects ${read.name} before dispatch after an authority change`, async () => {
    const authority = captureProfileRequestContext()!;
    setProfileId("b");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(read(authority)).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });
  it(`rejects ${read.name} after body decode under changed authority`, async () => {
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
    const result = read(captureProfileRequestContext()!);
    await reading;
    setProfileId("b");
    finish("{}");
    await expect(result).rejects.toThrow();
  });
}
