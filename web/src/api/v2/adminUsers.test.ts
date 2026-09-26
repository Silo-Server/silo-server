import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  getAdminUser,
  updateAdminUser,
  deleteAdminUser,
  createAdminUser,
  listAdminUsers,
  impersonateAdminUser,
} from "./adminUsers";
function v2User(id: string, username: string) {
  return {
    id,
    username,
    email: `${username}@example.test`,
    role: "user",
    permissions: [],
    enabled: true,
    library_ids: null,
    max_playback_quality: null,
    max_streams: null,
    max_transcodes: null,
    transcode_allowed: null,
    audio_transcode_allowed: null,
    max_profiles: 5,
    download_allowed: null,
    download_transcode_allowed: null,
    requests_allowed: null,
    access_group_id: null,
    effective_policy: {
      library_ids: [],
      max_playback_quality: "",
      max_streams: 0,
      max_transcodes: 0,
      transcode_allowed: true,
      audio_transcode_allowed: false,
      download_allowed: true,
      download_transcode_allowed: false,
      requests_allowed: false,
      permissions: [],
    },
    created_at: "2026-01-02T03:04:05.678Z",
    updated_at: "2026-01-02T03:04:05.678Z",
    last_active_at: null,
  };
}

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
it.each([
  { name: "unrestricted", wire: null, expected: null },
  { name: "deny all", wire: [], expected: [] },
  { name: "restricted", wire: ["3", "7"], expected: [3, 7] },
])("preserves $name effective library access", async ({ wire, expected }) => {
  const user = v2User("7", "Initial");
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    response(
      {
        ...user,
        effective_policy: { ...user.effective_policy, library_ids: wire },
      },
      200,
      '"current"',
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const editor = await getAdminUser(7);
  expect(editor.user.effective_policy.library_ids).toEqual(expected);
});
it("uses canonical exact tags and explicit policy null/empty/false without reading on mutation", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(response(v2User("7", "Initial"), 200, '"old"'))
    .mockResolvedValueOnce(new Response(null, { status: 204 }))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const editor = await getAdminUser(7);
  await updateAdminUser(editor, {
    library_ids: [],
    access_group_id: null,
    download_allowed: false,
    max_streams: null,
  });
  await deleteAdminUser(editor);
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({
    library_ids: [],
    access_group_id: null,
    download_allowed: false,
    max_streams: null,
  });
  expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("If-Match")).toBe('"old"');
  expect(new Headers(fetch.mock.calls[2]![1]?.headers).get("If-Match")).toBe('"old"');
});
it("does not replay any mutation or impersonation on 401", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(response(v2User("7", "Initial"), 200, '"old"'));
  vi.stubGlobal("fetch", fetch);
  const editor = await getAdminUser(7);
  for (const action of [
    () =>
      createAdminUser({
        username: "new",
        email: "new@example.invalid",
        password: "test-password",
        role: "user",
        create_default_profile: false,
      }),
    () => updateAdminUser(editor, { enabled: false }),
    () => deleteAdminUser(editor),
    () => impersonateAdminUser(7),
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
it("rejects incomplete cursor pages and authority changes during traversal", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () =>
    response({
      items: [v2User("7", "Initial")],
      page: { has_more: true, next_cursor: "repeat" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  await expect(listAdminUsers()).rejects.toThrow("pagination");
  expect(fetch).toHaveBeenCalledTimes(2);
  fetch.mockClear();
  fetch.mockImplementation(async () => {
    setProfileId("other");
    return response({ items: [v2User("7", "Initial")], page: { has_more: false } });
  });
  await expect(listAdminUsers()).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("retains the observed tag after conflict and rejects stale authority before send", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(response(v2User("7", "Initial"), 200, '"old"'))
    .mockResolvedValueOnce(
      response(
        {
          type: "https://silo.example/problems/precondition_failed",
          title: "Changed",
          status: 412,
        },
        412,
        '"fresh"',
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const editor = await getAdminUser(7);
  await expect(updateAdminUser(editor, { username: "draft" })).rejects.toMatchObject({
    status: 412,
  });
  expect(editor.etag).toBe('"old"');
  setProfileId("other");
  await expect(deleteAdminUser(editor)).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});

it("discards an impersonation response when the acting profile changes in flight", async () => {
  let finish!: (response: Response) => void;
  const pending = new Promise<Response>((resolve) => {
    finish = resolve;
  });
  const fetch = vi.fn<typeof globalThis.fetch>().mockReturnValue(pending);
  vi.stubGlobal("fetch", fetch);
  const result = impersonateAdminUser(7);
  setProfileId("other");
  finish(response({}));
  await expect(result).rejects.toThrow("changed");
  expect(fetch).toHaveBeenCalledTimes(1);
});
