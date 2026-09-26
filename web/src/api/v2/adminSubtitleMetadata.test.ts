import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "./adminSubtitles";
import {
  captureAdminSubtitleEditIntent,
  getAdminSubtitleEditor,
  updateAdminSubtitleMetadata,
} from "./adminSubtitleMetadata";
const row = {
  id: "9007199254740993",
  media_file_id: "42",
  release_name: "Original",
  language: "en",
  hearing_impaired: true,
} as AdminStoredSubtitle;
const json = (body: unknown, tag = '"v4"', status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status === 200 ? "application/json" : "application/problem+json",
      ETag: tag,
    },
  });
beforeEach(() => {
  setAccessToken("admin");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("reads canonical state once and sends only the patch with the original strong validator", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json(row))
    .mockResolvedValueOnce(json({ ...row, release_name: "", hearing_impaired: false }, '"v5"'));
  vi.stubGlobal("fetch", fetch);
  const intent = captureAdminSubtitleEditIntent(row, adminSubtitleListScope());
  const editor = await getAdminSubtitleEditor(intent);
  const saved = await updateAdminSubtitleMetadata(editor, {
    release_name: "",
    hearing_impaired: false,
  });
  expect(fetch.mock.calls).toHaveLength(2);
  expect(fetch.mock.calls[1]![0]).toBe(`/api/v2/admin/subtitles/${row.id}`);
  expect(fetch.mock.calls[1]![1]?.method).toBe("PATCH");
  expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("If-Match")).toBe('"v4"');
  expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({
    release_name: "",
    hearing_impaired: false,
  });
  expect(editor.etag).toBe('"v4"');
  expect(saved.etag).toBe('"v5"');
});
it.each([401, 412, 503])(
  "does not reread, replay or adopt an error validator after %s",
  async (status) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(json(row))
      .mockResolvedValueOnce(
        json(
          { type: "about:blank", title: "Not saved", status, code: "precondition_failed" },
          '"newer"',
          status,
        ),
      );
    vi.stubGlobal("fetch", fetch);
    const editor = await getAdminSubtitleEditor(
      captureAdminSubtitleEditIntent(row, adminSubtitleListScope()),
    );
    await expect(updateAdminSubtitleMetadata(editor, { release_name: "Draft" })).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(editor.etag).toBe('"v4"');
  },
);
it("refuses stale click authority and a PIN change while canonical state is in flight", async () => {
  const scope = adminSubtitleListScope();
  const intent = captureAdminSubtitleEditIntent(row, scope);
  let finish!: (r: Response) => void;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const request = getAdminSubtitleEditor(intent);
  setProfileToken("changed");
  finish(json(row));
  await expect(request).rejects.toThrow();
  expect(() => captureAdminSubtitleEditIntent(row, scope)).toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it.each([
  ['W/"weak"', row],
  ['"v4"', { ...row, media_file_id: "43" }],
])("rejects unusable canonical metadata", async (etag, body) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(body, etag as string)));
  await expect(
    getAdminSubtitleEditor(captureAdminSubtitleEditIntent(row, adminSubtitleListScope())),
  ).rejects.toThrow();
});
it("refuses a late successful save after authority changes", async () => {
  let finish!: (r: Response) => void;
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json(row))
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
  vi.stubGlobal("fetch", fetch);
  const editor = await getAdminSubtitleEditor(
    captureAdminSubtitleEditIntent(row, adminSubtitleListScope()),
  );
  const saving = updateAdminSubtitleMetadata(editor, { release_name: "Draft" });
  setProfileId("other");
  finish(json({ ...row, release_name: "Draft" }, '"saved"'));
  await expect(saving).rejects.toThrow();
  expect(editor.etag).toBe('"v4"');
  expect(fetch).toHaveBeenCalledTimes(2);
});
