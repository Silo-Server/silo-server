import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { captureAdminUserAuthority } from "./adminUsers";
import { listAdminDevices, getAdminDevice } from "./adminDevices";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("admin");
  setProfileId("primary");
});
afterEach(() => vi.unstubAllGlobals());
const row = {
  user_id: "7",
  username: "Account",
  email: "",
  device_id: "screen/one",
  device_name: "Screen",
  device_platform: "web",
  override_count: 1,
  profile_count: 1,
  profiles: [{ profile_id: "child", profile_name: "Child", override_count: 1, last_updated: null }],
  last_updated: null,
};
it("drains pages and converts account IDs while retaining profile/device identity", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      jsonResponse({ items: [row], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockResolvedValueOnce(jsonResponse({ items: [], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetch);
  const items = await listAdminDevices(captureAdminUserAuthority());
  expect(items[0]).toMatchObject({
    user_id: 7,
    device_id: "screen/one",
    last_updated: "",
    profiles: [{ profile_id: "child", last_updated: "" }],
  });
  expect(String(fetch.mock.calls[1]?.[0])).toContain("cursor=next");
});
it("encodes detail identity and retains the legacy array as compatibility data", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(jsonResponse({ ...row, settings: [] }));
  vi.stubGlobal("fetch", fetch);
  const detail = await getAdminDevice(7, "screen/one", captureAdminUserAuthority());
  expect(String(fetch.mock.calls[0]?.[0])).toBe("/api/v2/admin/devices/7/screen%2Fone");
  expect(detail.settings).toEqual([]);
});
it("rejects repeated continuation without returning a partial list", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockImplementation(async () =>
        jsonResponse({ items: [row], page: { has_more: true, next_cursor: "same" } }),
      ),
  );
  await expect(listAdminDevices(captureAdminUserAuthority())).rejects.toThrow("continuation");
});
it("discards a page received after an account switch", async () => {
  let finish!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    ),
  );
  const result = listAdminDevices(captureAdminUserAuthority());
  setAccessToken("other");
  finish(jsonResponse({ items: [row], page: { has_more: false } }));
  await expect(result).rejects.toThrow();
});
