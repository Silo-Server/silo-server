import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
} from "@/api/client";
import { readAdminAutoscanConnections } from "./adminAutoscanConnections";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken(null);
  setProfileId("a");
});
afterEach(() => vi.unstubAllGlobals());
const row = { id: "connection", name: "Connection", kind: "radarr", has_api_key: true };
function response(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}
it("drains pages and preserves credential presence without introducing secret fields", async () => {
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(
      response({ items: [row], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockResolvedValueOnce(response({ items: [], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetchMock);
  const result = await readAdminAutoscanConnections(captureProfileRequestContext()!);
  expect(result).toEqual([row]);
  expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=next");
});
