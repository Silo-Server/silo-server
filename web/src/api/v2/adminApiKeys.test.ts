import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  adminApiKeyScope,
  captureAdminApiKeyAuthority,
  createAdminApiKey,
  deleteAdminApiKey,
  getAdminApiKey,
  listAdminApiKeysPage,
  updateAdminApiKeyTier,
} from "./adminApiKeys";
const metadata = {
  id: "9007199254740993",
  user_id: "2",
  label: "Automation",
  key_prefix: "sa_12345678",
  rate_tier: "standard",
  scopes: [],
  created_at: "2026-01-02T03:04:05.000Z",
};
function json(body: unknown, status = 200, etag?: string) {
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
  setAccessToken("account-a");
  setRefreshToken("refresh-a");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
describe("admin API key fetch boundary", () => {
  it("uses frozen exact tags and string IDs without read-on-save", async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(json(metadata, 200, '"original"'))
      .mockResolvedValueOnce(json({ ...metadata, rate_tier: "elevated" }, 200, '"saved"'))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetch);
    const editor = await getAdminApiKey(metadata.id);
    const saved = await updateAdminApiKeyTier(editor, "elevated");
    await deleteAdminApiKey(saved);
    expect(fetch.mock.calls.map(([url]) => String(url))).toEqual([
      `/api/v2/admin/api-keys/${metadata.id}`,
      `/api/v2/admin/api-keys/${metadata.id}/tier`,
      `/api/v2/admin/api-keys/${metadata.id}`,
    ]);
    expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("If-Match")).toBe('"original"');
    expect(new Headers(fetch.mock.calls[2]![1]?.headers).get("If-Match")).toBe('"saved"');
    expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({ rate_tier: "elevated" });
    expect(editor.etag).toBe('"original"');
  });
  it("does not refresh or replay any mutation on401", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(json(metadata, 200, '"tag"'));
    vi.stubGlobal("fetch", fetch);
    const editor = await getAdminApiKey(metadata.id);
    for (const run of [
      () => createAdminApiKey({ label: "Tool" }),
      () => updateAdminApiKeyTier(editor, "elevated"),
      () => deleteAdminApiKey(editor),
    ]) {
      fetch.mockClear();
      fetch.mockResolvedValue(
        json(
          {
            type: "https://silo.example/problems/authentication_required",
            title: "Unauthorized",
            status: 401,
            detail: "Sign in again",
          },
          401,
        ),
      );
      await expect(run()).rejects.toMatchObject({ status: 401 });
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(String(fetch.mock.calls[0]![0])).not.toContain("refresh");
    }
  });
  it("preserves observed tags after412 and rejects nonstrong tags", async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(json(metadata, 200, '"old"'))
      .mockResolvedValueOnce(
        json(
          {
            type: "https://silo.example/problems/precondition_failed",
            title: "Changed",
            status: 412,
            detail: "Reload",
          },
          412,
          '"current"',
        ),
      );
    vi.stubGlobal("fetch", fetch);
    const editor = await getAdminApiKey(metadata.id);
    await expect(updateAdminApiKeyTier(editor, "elevated")).rejects.toMatchObject({ status: 412 });
    expect(editor.etag).toBe('"old"');
    expect(fetch).toHaveBeenCalledTimes(2);
    for (const tag of [undefined, 'W/"weak"', "*"]) {
      fetch.mockResolvedValue(json(metadata, 200, tag));
      await expect(getAdminApiKey(metadata.id)).rejects.toThrow("strong ETag");
    }
    fetch.mockClear();
    await expect(deleteAdminApiKey({ ...editor, etag: "*" })).rejects.toThrow("strong ETag");
    expect(fetch).not.toHaveBeenCalled();
  });
  it("rejects authority changes before requests and after response", async () => {
    const captured = captureAdminApiKeyAuthority();
    const scope = adminApiKeyScope(captured);
    setProfileId("child");
    expect(adminApiKeyScope()).not.toBe(scope);
    const fetch = vi.fn<typeof globalThis.fetch>();
    vi.stubGlobal("fetch", fetch);
    await expect(createAdminApiKey({ label: "Tool" }, captured)).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
    setProfileId("owner");
    fetch.mockImplementation(async () => {
      setProfileId("child");
      return json(metadata, 200, '"tag"');
    });
    await expect(getAdminApiKey(metadata.id)).rejects.toThrow();
  });
  it("requires coherent bounded pagination and rejects repeated cursors", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>();
    vi.stubGlobal("fetch", fetch);
    for (const page of [
      undefined,
      { has_more: true },
      { has_more: true, next_cursor: "cursor" },
      { has_more: false, next_cursor: "unexpected" },
    ]) {
      fetch.mockResolvedValue(json({ items: [metadata], page }));
      await expect(listAdminApiKeysPage("cursor")).rejects.toThrow("pagination");
    }
    fetch.mockResolvedValue(
      json({ items: [metadata], page: { has_more: true, next_cursor: "next" } }),
    );
    expect((await listAdminApiKeysPage()).items[0]?.id).toBe(metadata.id);
    expect(String(fetch.mock.calls[fetch.mock.calls.length - 1]![0])).toBe(
      "/api/v2/admin/api-keys?limit=50",
    );
  });
  it("returns the creation secret once without subsequent reads", async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(json({ ...metadata, key: "one-time-secret" }, 201));
    vi.stubGlobal("fetch", fetch);
    expect(
      (await createAdminApiKey({ label: "Tool", user_id: "9007199254740993", scopes: [] })).key,
    ).toBe("one-time-secret");
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({
      label: "Tool",
      user_id: "9007199254740993",
      scopes: [],
    });
  });
});
