import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope } from "./adminSubtitles";
import {
  captureProviderEditIntent,
  getProviderEditor,
  saveProviderConfiguration,
  providerSaveMessage,
} from "./adminSubtitleProviderConfiguration";
const row = { provider_name: "subdl", enabled: true, has_api_key: true, has_credentials: false };
const json = (body: unknown, tag = '"v4"', status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status === 200 ? "application/json" : "application/problem+json",
      ETag: tag,
    },
  });
const saved = {
  saved_revision: "9007199254740993",
  local_apply: "applied",
  local_applied_revision: "9007199254740995",
};
beforeEach(() => {
  setAccessToken("admin");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
const intent = () => captureProviderEditIntent("subdl", adminSubtitleListScope());
it("sends one guarded PUT with original blank/clear fields and distinguishes later local revision", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json(row))
    .mockResolvedValueOnce(json(saved, '"v5"'));
  vi.stubGlobal("fetch", fetch);
  const editor = await getProviderEditor(intent());
  const result = await saveProviderConfiguration(editor, {
    enabled: false,
    api_key: "",
    clear_credentials: true,
  });
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls[1]![0]).toBe("/api/v2/admin/subtitle-providers/subdl");
  expect(fetch.mock.calls[1]![1]?.method).toBe("PUT");
  expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("If-Match")).toBe('"v4"');
  expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({
    enabled: false,
    api_key: "",
    clear_credentials: true,
  });
  expect(editor.etag).toBe('"v4"');
  expect(result).toEqual(saved);
  expect(providerSaveMessage(result)).toContain("different saved revision");
});
it.each([401, 412, 500, 503])("does not replay, reread or rebase after %s", async (status) => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json(row))
    .mockResolvedValueOnce(
      json(
        { type: "about:blank", title: "Not confirmed", status, code: "precondition_failed" },
        '"new"',
        status,
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const editor = await getProviderEditor(intent());
  await expect(saveProviderConfiguration(editor, { enabled: false })).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(editor.etag).toBe('"v4"');
});
it("preserves uncertainty after network failure without replay", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json(row))
    .mockRejectedValueOnce(new TypeError("lost response"));
  vi.stubGlobal("fetch", fetch);
  const editor = await getProviderEditor(intent());
  await expect(saveProviderConfiguration(editor, { enabled: false })).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});
it.each(['W/"v4"', "", "*"])("refuses unusable canonical validator %s", async (tag) => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(json(row, tag));
  vi.stubGlobal("fetch", fetch);
  await expect(getProviderEditor(intent())).rejects.toThrow("strong edit validator");
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("refuses changed authority before save and after canonical read", async () => {
  let finish!: (response: Response) => void;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const pending = getProviderEditor(intent());
  setProfileToken("new-pin");
  finish(json(row));
  await expect(pending).rejects.toThrow();
  fetch.mockResolvedValue(json(row));
  const editor = await getProviderEditor(intent());
  setProfileId("other");
  await expect(saveProviderConfiguration(editor, { enabled: false })).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});
it("rejects a result delivered after authority changes", async () => {
  let finish!: (response: Response) => void;
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
  const editor = await getProviderEditor(intent());
  const pending = saveProviderConfiguration(editor, { enabled: false });
  setAccessToken("other-account");
  finish(json(saved));
  await expect(pending).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});
it("reports confirmed save with local failure truthfully", () => {
  expect(providerSaveMessage({ saved_revision: "5", local_apply: "failed" })).toContain(
    "Settings saved, but not applied",
  );
});

it.each([
  { ...saved, saved_revision: 9007199254740992 },
  { ...saved, local_applied_revision: 9 },
  { ...saved, local_apply: "unknown" },
])("rejects malformed saved outcome without replay", async (invalid) => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(json(row))
    .mockResolvedValueOnce(json(invalid));
  vi.stubGlobal("fetch", fetch);
  const editor = await getProviderEditor(intent());
  await expect(saveProviderConfiguration(editor, { enabled: false })).rejects.toThrow(
    "Unable to confirm",
  );
  expect(fetch).toHaveBeenCalledTimes(2);
});
