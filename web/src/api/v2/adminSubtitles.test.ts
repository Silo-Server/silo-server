import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope, listAdminSubtitles } from "./adminSubtitles";

const item = { id: "9007199254740993", media_file_id: "42", downloaded_by: "2" };
const body = {
  items: [item],
  page: { has_more: true, next_cursor: "next" },
  total: 3,
  uploads: 1,
  provider_downloads: 2,
};
const response = (value: unknown) =>
  new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin-a");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("sends exact string filters and cursor through v2 and preserves large IDs", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(response(body));
  vi.stubGlobal("fetch", fetch);
  const out = await listAdminSubtitles(
    { limit: 25, cursor: "prior", user_id: "9007199254740993", q: "Release" },
    adminSubtitleListScope(),
  );
  expect(out.items[0]!.id).toBe(item.id);
  const url = new URL(String(fetch.mock.calls[0]![0]), "https://example.test");
  expect(url.pathname).toBe("/api/v2/admin/subtitles");
  expect(url.searchParams.get("cursor")).toBe("prior");
  expect(url.searchParams.get("user_id")).toBe("9007199254740993");
  expect(url.searchParams.has("offset")).toBe(false);
  expect(new Headers(fetch.mock.calls[0]![1]?.headers).get("X-Profile-Id")).toBe("owner");
});
it.each(["profile", "pin", "account"])(
  "rejects a late reply after %s authority changes",
  async (kind) => {
    let finish!: (r: Response) => void;
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetch);
    const old = adminSubtitleListScope();
    const pending = listAdminSubtitles({}, old);
    if (kind === "profile") setProfileId("other");
    if (kind === "pin") setProfileToken("private-pin");
    if (kind === "account") setAccessToken("other-account");
    finish(response(body));
    await expect(pending).rejects.toThrow();
    expect(adminSubtitleListScope()).not.toBe(old);
    expect(adminSubtitleListScope()).not.toContain("private-pin");
    await expect(listAdminSubtitles({}, old)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
  },
);
it.each([
  { ...body, page: { has_more: true, next_cursor: "prior" } },
  { ...body, page: { has_more: true } },
  { ...body, page: { has_more: false, next_cursor: "unexpected" } },
  { ...body, items: [{ ...item, id: 9007199254740992 }] },
])("rejects malformed pagination or non-string IDs", async (invalid) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(invalid)));
  await expect(listAdminSubtitles({ cursor: "prior" }, adminSubtitleListScope())).rejects.toThrow();
});
