import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { listWatchTogetherSuggestions, voteWatchTogetherSuggestion } from "@/lib/watchTogether";

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
it("does not fetch or publish after vote authority changes", async () => {
  const fetch = vi.fn().mockImplementation(async () => {
    setProfileId("other");
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(voteWatchTogetherSuggestion("room", "room-proof", "a")).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("does not replay rejected votes or publish partial page results", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      response({ items: [row("a", 0)], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockRejectedValueOnce(new Error("lost page"));
  vi.stubGlobal("fetch", fetch);
  await expect(listWatchTogetherSuggestions("room", "room-proof")).rejects.toThrow();
  fetch.mockReset().mockResolvedValue(
    new Response(JSON.stringify({ status: 401, code: "authentication_required" }), {
      status: 401,
      headers: { "Content-Type": "application/problem+json" },
    }),
  );
  await expect(voteWatchTogetherSuggestion("room", "room-proof", "a")).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
