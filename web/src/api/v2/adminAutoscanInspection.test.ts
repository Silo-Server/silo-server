import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
  StaleApiRequestContextError,
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
for (const { read, body } of [
  {
    read: readAdminAutoscanSettings,
    body: { enabled: true, default_poll_interval_seconds: 60, debounce_seconds: 5 },
  },
  {
    read: readAdminAutoscanStatus,
    body: {
      enabled: true,
      sources: [],
      running_polls: [],
      active_scans: 0,
      accepted_scans: 0,
      running_scans: 0,
    },
  },
]) {
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
    const res = response(body);
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
    finish(JSON.stringify(body));
    await expect(result).rejects.toBeInstanceOf(StaleApiRequestContextError);
  });
}
