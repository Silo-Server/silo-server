import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
} from "@/api/client";
import { readAdminAutoscanAvailableSources } from "./adminAutoscanAvailableSources";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken(null);
  setProfileId("a");
});
afterEach(() => vi.unstubAllGlobals());
const row = {
  plugin_id: "p",
  capability_id: "c",
  display_name: "Source",
  descriptor: {
    delivery_modes: ["poll"],
    connection: "optional",
    connection_kinds: [],
    emits_native_paths: false,
    summary: "",
    icon_url: "",
  },
};
function response(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}
it("drains descriptor pages preserving discovery identity and defaults", async () => {
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(
      response({ items: [row], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockResolvedValueOnce(response({ items: [], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetchMock);
  const result = await readAdminAutoscanAvailableSources(captureProfileRequestContext()!);
  expect(result).toEqual([row]);
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
  await expect(readAdminAutoscanAvailableSources(captureProfileRequestContext()!)).rejects.toThrow(
    "continuation",
  );
});
it("discards a descriptor decoded after the selected profile changes", async () => {
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
  const result = readAdminAutoscanAvailableSources(captureProfileRequestContext()!);
  await reading;
  setProfileId("b");
  finish(JSON.stringify({ items: [row], page: { has_more: false } }));
  await expect(result).rejects.toThrow();
});
