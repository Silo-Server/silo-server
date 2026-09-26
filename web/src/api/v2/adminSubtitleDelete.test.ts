import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "./adminSubtitles";
import { prepareAdminSubtitleDeletion } from "./adminSubtitleDelete";
const row = {
  id: "9007199254740993",
  media_file_id: "42",
  release_name: "Original",
  language: "en",
} as AdminStoredSubtitle;
const canonical = () =>
  new Response(JSON.stringify(row), {
    headers: { "Content-Type": "application/json", ETag: '"captured"' },
  });
beforeEach(() => {
  setAccessToken("admin");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("uses one captured strong validator and consumes the intent before the DELETE awaits", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(canonical())
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
  vi.stubGlobal("fetch", fetch);
  const intent = await prepareAdminSubtitleDeletion(row, adminSubtitleListScope());
  const pending = intent.confirm();
  await expect(intent.confirm()).rejects.toThrow("already attempted");
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls[1]![0]).toBe(`/api/v2/admin/subtitles/${row.id}`);
  expect(fetch.mock.calls[1]![1].method).toBe("DELETE");
  expect(new Headers(fetch.mock.calls[1]![1].headers).get("If-Match")).toBe('"captured"');
  finish(new Response(null, { status: 204 }));
  await pending;
});
it.each([401, 412, 500, 503])(
  "does not reread, rebase, replay or reuse an intent after %s",
  async (status) => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(canonical())
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ status, title: "Not confirmed", code: "internal_error" }), {
          status,
          headers: { "Content-Type": "application/problem+json", ETag: '"new"' },
        }),
      );
    vi.stubGlobal("fetch", fetch);
    const intent = await prepareAdminSubtitleDeletion(row, adminSubtitleListScope());
    await expect(intent.confirm()).rejects.toThrow();
    await expect(intent.confirm()).rejects.toThrow("already attempted");
    expect(fetch).toHaveBeenCalledTimes(2);
  },
);
it("refuses old authority before DELETE", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(canonical());
  vi.stubGlobal("fetch", fetch);
  const intent = await prepareAdminSubtitleDeletion(row, adminSubtitleListScope());
  setProfileToken("new-pin");
  await expect(intent.confirm()).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("refuses late success after the acting authority changes", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(canonical())
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
  vi.stubGlobal("fetch", fetch);
  const intent = await prepareAdminSubtitleDeletion(row, adminSubtitleListScope());
  const pending = intent.confirm();
  setProfileId("other");
  finish(new Response(null, { status: 204 }));
  await expect(pending).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});
