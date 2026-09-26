import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { deleteWatchTogetherSuggestion } from "@/lib/watchTogether";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("profile");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("deletes exactly the requested suggestion then refreshes under the same proof", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response(null, { status: 204 }))
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ items: [], page: { has_more: false } }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
  vi.stubGlobal("fetch", fetch);
  expect(await deleteWatchTogetherSuggestion("room", "room-proof", "target")).toEqual({
    suggestions: [],
  });
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room/suggestions/target");
  expect(fetch.mock.calls.map((c) => c[1].method)).toEqual(["DELETE", "GET"]);
  for (const call of fetch.mock.calls) {
    const headers = new Headers(call[1].headers);
    expect(headers.get("X-Room-Token")).toBe("room-proof");
    expect(headers.get("X-Profile-Id")).toBe("profile");
    expect(String(call[0])).not.toContain("room-proof");
  }
});
it.each([401, 403, 404, 500])("does not replay or hide a %s deletion failure", async (status) => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ status, code: "failed", detail: "Delete failed" }), {
      status,
      headers: { "Content-Type": "application/problem+json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  await expect(deleteWatchTogetherSuggestion("room", "proof", "target")).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("refuses a stale deletion receipt before fetching or publishing", async () => {
  const fetch = vi.fn().mockImplementation(async () => {
    setProfileToken("replacement");
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(deleteWatchTogetherSuggestion("room", "proof", "target")).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
