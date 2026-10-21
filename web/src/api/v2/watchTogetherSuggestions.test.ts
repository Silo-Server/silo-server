import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import {
  deleteWatchTogetherSuggestion,
  listWatchTogetherSuggestions,
  voteWatchTogetherSuggestion,
} from "@/lib/watchTogether";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("profile");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
const row = (id: string, votes: number) => ({
  id,
  room_id: "room",
  suggester_user_id: "7",
  suggester_profile_id: "profile",
  content_id: "movie",
  content_type: "movie",
  title: id,
  subtitle: "",
  poster_url: "",
  note: "",
  vote_count: votes,
  voted_by_me: false,
  created_at: "2026-09-06T00:00:00Z",
});
const response = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
it("drains bounded pages and restores vote ranking without putting room proof in the URL", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      response({ items: [row("a", 0)], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockResolvedValueOnce(response({ items: [row("b", 3)], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetch);
  const result = await listWatchTogetherSuggestions("room", "room-proof");
  expect(result.suggestions.map((r) => r.id)).toEqual(["b", "a"]);
  expect(result.suggestions[0]!.suggester_user_id).toBe(7);
  expect(String(fetch.mock.calls[0]![0])).not.toContain("room-proof");
  expect(new Headers(fetch.mock.calls[0]![1].headers).get("X-Room-Token")).toBe("room-proof");
  expect(String(fetch.mock.calls[1]![0])).toContain("cursor=next");
});
it("refreshes after the empty vote receipt under the same authority", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response(null, { status: 204 }))
    .mockResolvedValueOnce(response({ items: [row("a", 1)], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetch);
  expect((await voteWatchTogetherSuggestion("room", "room-proof", "a")).suggestions).toHaveLength(
    1,
  );
  expect(fetch.mock.calls.map((c) => c[1].method)).toEqual(["POST", "GET"]);
});
it("deletes exactly the requested suggestion then refreshes under the same proof", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response(null, { status: 204 }))
    .mockResolvedValueOnce(response({ items: [], page: { has_more: false } }));
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
// Vote and delete each check the authority after their own 204 receipt.
it.each([
  [
    "does not fetch or publish after vote authority changes",
    () => setProfileId("other"),
    () => voteWatchTogetherSuggestion("room", "room-proof", "a"),
  ],
  [
    "refuses a stale deletion receipt before fetching or publishing",
    () => setProfileToken("replacement"),
    () => deleteWatchTogetherSuggestion("room", "proof", "target"),
  ],
])("%s", async (_name, replaceAuthority, call) => {
  const fetch = vi.fn().mockImplementation(async () => {
    replaceAuthority();
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(call()).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
