import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  listAdminSettingValues,
  setAdminSettingValue,
  deleteAdminSettingValue,
  captureAdminSettingAuthority,
} from "./adminAccountSettings";
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("preserves false, empty arrays, and exact dimensional identity without mutation replay", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementation(async () => response({ value: false }));
  vi.stubGlobal("fetch", fetch);
  const identity = { scope: "profile_library" as const, profileId: "household", libraryId: 7 };
  await setAdminSettingValue(2, "playback.flag", identity, false);
  expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ value: false });
  expect(String(fetch.mock.calls[0]![0])).toContain("library_id=7");
  for (const action of [
    () => setAdminSettingValue(2, "playback.flag", identity, []),
    () => deleteAdminSettingValue(2, "playback.flag", identity),
  ]) {
    fetch.mockClear();
    fetch.mockResolvedValue(
      response(
        {
          type: "https://silo.example/problems/authentication_required",
          title: "Unauthorized",
          status: 401,
        },
        401,
      ),
    );
    await expect(action()).rejects.toMatchObject({ status: 401 });
    expect(fetch).toHaveBeenCalledTimes(1);
  }
});
it("fails incomplete page walks and refuses a superseded authority", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementation(async () =>
      response({ items: [], revision: 1, page: { has_more: true, next_cursor: "repeat" } }),
    );
  vi.stubGlobal("fetch", fetch);
  await expect(listAdminSettingValues(7)).rejects.toThrow("page");
  expect(fetch).toHaveBeenCalledTimes(2);
  const authority = captureAdminSettingAuthority();
  setProfileId("other");
  await expect(
    deleteAdminSettingValue(7, "key", { scope: "account" }, authority),
  ).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});

it("refuses library IDs that existing numeric controls cannot represent exactly", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>().mockResolvedValue(
      response({
        items: [
          {
            key: "playback.flag",
            scope: "profile_library",
            library_id: "9007199254740993",
            value: false,
            revision: 1,
          },
        ],
        revision: 1,
        page: { has_more: false },
      }),
    ),
  );
  await expect(listAdminSettingValues(7)).rejects.toThrow("Unsupported library ID");
});
