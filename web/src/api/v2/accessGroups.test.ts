import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  getAccessGroup,
  updateAccessGroup,
  deleteAccessGroup,
  createAccessGroup,
  listAccessGroups,
} from "./accessGroups";
const group = {
  id: "7",
  name: "Guests",
  description: "",
  library_ids: null,
  max_playback_quality: "source",
  download_allowed: true,
  download_transcode_allowed: true,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 0,
  max_transcodes: 0,
  allowed_permissions: null,
  requests_allowed: true,
  is_default: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};
function response(body: unknown, status = 200, etag?: string) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
      ...(etag ? { ETag: etag } : {}),
    },
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
it("captures canonical tags, preserves false and empty arrays, and never reads on save", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(response(group, 200, '"old"'))
    .mockResolvedValueOnce(response(group, 200, '"new"'))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const editor = await getAccessGroup(7);
  const saved = await updateAccessGroup(editor, {
    library_ids: [],
    allowed_permissions: [],
    download_allowed: false,
  });
  await deleteAccessGroup(saved);
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({
    library_ids: [],
    allowed_permissions: [],
    download_allowed: false,
  });
  expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("If-Match")).toBe('"old"');
  expect(new Headers(fetch.mock.calls[2]![1]?.headers).get("If-Match")).toBe('"new"');
  expect(editor.etag).toBe('"old"');
});
it("never refreshes or replays mutations on 401", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(response(group, 200, '"old"'));
  vi.stubGlobal("fetch", fetch);
  const editor = await getAccessGroup(7);
  for (const action of [
    () => createAccessGroup({ name: "New" }),
    () => updateAccessGroup(editor, { name: "New" }),
    () => deleteAccessGroup(editor),
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
it("rejects incomplete or cyclic list traversals and preserves picker ordering", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      response({
        items: [{ ...group, name: "Z", member_count: 2 }],
        page: { has_more: true, next_cursor: "next" },
      }),
    )
    .mockResolvedValueOnce(
      response({
        items: [{ ...group, id: "8", name: "A", member_count: 1 }],
        page: { has_more: false },
      }),
    );
  vi.stubGlobal("fetch", fetch);
  expect((await listAccessGroups()).map((g) => g.id)).toEqual([8, 7]);
  expect(String(fetch.mock.calls[1]![0])).toContain("cursor=next");
  fetch.mockImplementation(async () =>
    response({ items: [group], page: { has_more: true, next_cursor: "cycle" } }),
  );
  await expect(listAccessGroups()).rejects.toThrow("pagination");
  fetch.mockResolvedValue(response({ items: [], page: { has_more: true, next_cursor: 7 } }));
  await expect(listAccessGroups()).rejects.toThrow("pagination");
});
it("rejects stale authority and retains old validator on conflict", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(response(group, 200, '"old"'))
    .mockResolvedValueOnce(
      response(
        {
          type: "https://silo.example/problems/precondition_failed",
          title: "Changed",
          status: 412,
        },
        412,
        '"current"',
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const editor = await getAccessGroup(7);
  await expect(updateAccessGroup(editor, { name: "draft" })).rejects.toMatchObject({ status: 412 });
  expect(editor.etag).toBe('"old"');
  setProfileId("other");
  await expect(deleteAccessGroup(editor)).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});
