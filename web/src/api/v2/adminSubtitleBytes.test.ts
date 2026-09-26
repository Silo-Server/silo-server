// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "./adminSubtitles";
import { downloadAdminSubtitle } from "./adminSubtitleBytes";
const row = { id: "9007199254740993", format: "srt" } as AdminStoredSubtitle;
let click: ReturnType<typeof vi.spyOn>;
const create = vi.fn(() => "blob:subtitle");
const revoke = vi.fn();
beforeEach(() => {
  setAccessToken("admin");
  setProfileId("owner");
  setProfileToken(null);
  create.mockClear();
  revoke.mockClear();
  URL.createObjectURL = create;
  URL.revokeObjectURL = revoke;
  click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const bytes = () =>
  new Response(new Uint8Array([0, 1, 255]), {
    headers: { "Content-Type": "application/x-subrip; charset=utf-8" },
  });
it("uses exact string ID and captured bearer authority then saves complete bytes", async () => {
  const fetch = vi.fn().mockResolvedValue(bytes());
  vi.stubGlobal("fetch", fetch);
  await downloadAdminSubtitle(row, adminSubtitleListScope());
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]![0]).toBe(`/api/v2/admin/subtitles/${row.id}/download`);
  expect(new Headers(fetch.mock.calls[0]![1].headers).get("Authorization")).toBe("Bearer admin");
  expect(create).toHaveBeenCalledTimes(1);
  expect(click).toHaveBeenCalledTimes(1);
  expect(revoke).toHaveBeenCalledWith("blob:subtitle");
});
it.each([401, 403, 404, 500, 503])("does not save or replay after %s", async (status) => {
  const fetch = vi.fn().mockResolvedValue(new Response("private error", { status }));
  vi.stubGlobal("fetch", fetch);
  await expect(downloadAdminSubtitle(row, adminSubtitleListScope())).rejects.toThrow(`(${status})`);
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(create).not.toHaveBeenCalled();
});
it("refuses old list authority before fetch", async () => {
  const scope = adminSubtitleListScope();
  setProfileToken("new-pin");
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(downloadAdminSubtitle(row, scope)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
it("refuses an authority change during body consumption", async () => {
  let finish!: (blob: Blob) => void;
  const response = bytes();
  vi.spyOn(response, "blob").mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const pending = downloadAdminSubtitle(row, adminSubtitleListScope());
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
  setProfileId("other");
  finish(new Blob(["subtitle"]));
  await expect(pending).rejects.toThrow();
  expect(create).not.toHaveBeenCalled();
});
it("does not save an HTML or JSON success response", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response("login", { headers: { "Content-Type": "text/html" } })),
  );
  await expect(downloadAdminSubtitle(row, adminSubtitleListScope())).rejects.toThrow("Unexpected");
  expect(click).not.toHaveBeenCalled();
});
