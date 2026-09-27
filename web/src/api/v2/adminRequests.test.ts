import { afterEach, describe, expect, it, vi } from "vitest";
import { captureProfileRequestContext, isProfileRequestContextCurrent } from "@/api/client";
import { v2, V2ProblemError } from "./request";
import {
  getAdminRequestIntegrationV2,
  listAdminMediaRequestsV2,
  listAdminRequestIntegrationsV2,
  putAdminRequestSettingsV2,
  putAdminRequestUserLimitV2,
  requestValidationErrors,
  saveAdminRequestIntegrationV2,
  loadAdminRequestIntegrationOptionsV2,
} from "./adminRequests";
vi.mock("./request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  captureProfileRequestContext: vi.fn(),
  isProfileRequestContextCurrent: vi.fn(),
}));
const authority = {
  accessToken: "test",
  authContextVersion: 1,
  serverOrigin: "",
  profileId: "profile",
  profileToken: null,
};
function response(options: unknown, body: unknown) {
  (options as { onResponse?: (r: Response) => void })?.onResponse?.(
    new Response(null, { headers: { ETag: '"saved"' } }),
  );
  return Promise.resolve(body) as never;
}
afterEach(() => vi.resetAllMocks());
describe("admin request v2 adapter", () => {
  it("sends only writable integration fields, string IDs, and the editor validator", async () => {
    vi.mocked(v2).mockImplementation((_op, options) =>
      response(options, { id: "row", installation_id: "7" }),
    );
    await saveAdminRequestIntegrationV2({
      id: "row",
      name: "Edited",
      enabled: true,
      base_url: "https://example.invalid",
      installation_id: 7,
      capability_id: "arr",
      has_api_key: true,
      updated_at: "old",
      last_check_status: "ok",
      etag: '"editor"',
    });
    const [op, options] = vi.mocked(v2).mock.calls[0]!;
    expect(op).toBe("PUT /api/v2/admin/request-integrations/{id}");
    expect(options).toMatchObject({
      headers: { "If-Match": '"editor"' },
      body: { installation_id: "7" },
    });
    const body = (options as { body: Record<string, unknown> }).body;
    expect(body).not.toHaveProperty("id");
    expect(body).not.toHaveProperty("has_api_key");
    expect(body).not.toHaveProperty("etag");
    expect(body).not.toHaveProperty("updated_at");
    expect(body).not.toHaveProperty("last_check_status");
    await expect(
      saveAdminRequestIntegrationV2({ id: "row", name: "Edited", enabled: true, base_url: "" }),
    ).rejects.toThrow("Reload");
    expect(v2).toHaveBeenCalledTimes(1);
  });
  it("omits response metadata from settings and account-limit PUT bodies", async () => {
    vi.mocked(v2).mockImplementation((_op, options) => response(options, { user_id: "8" }));
    await putAdminRequestSettingsV2({
      requests_enabled: true,
      global_max_requests: 5,
      global_window_days: 7,
      global_auto_approval_enabled: false,
      force_dual_quality: false,
      updated_at: "old",
      etag: '"settings"',
    });
    expect((vi.mocked(v2).mock.calls[0]![1]! as { body: object }).body).not.toHaveProperty(
      "updated_at",
    );
    await putAdminRequestUserLimitV2(8, {
      user_id: 8,
      limit_mode: "inherit",
      approval_mode: "manual",
      updated_at: "old",
      etag: '"limit"',
    });
    expect(vi.mocked(v2).mock.calls[1]![1]!).toMatchObject({
      path: { user_id: "8" },
      headers: { "If-Match": '"limit"' },
      body: { max_requests: null, window_days: null },
    });
    expect((vi.mocked(v2).mock.calls[1]![1]! as { body: object }).body).not.toHaveProperty(
      "user_id",
    );
  });
  it("loads canonical integration representation instead of pairing list data with a fresh tag", async () => {
    vi.mocked(captureProfileRequestContext).mockReturnValue(authority);
    vi.mocked(isProfileRequestContextCurrent).mockReturnValue(true);
    vi.mocked(v2).mockImplementation((op, options) =>
      response(
        options,
        op === "GET /api/v2/admin/request-integrations"
          ? { items: [{ id: "a", name: "Old list name" }], page: { has_more: false } }
          : { id: "a", name: "Canonical", installation_id: "7" },
      ),
    );
    expect(await listAdminRequestIntegrationsV2()).toEqual([
      { id: "a", name: "Canonical", installation_id: 7, etag: '"saved"' },
    ]);
    expect(
      vi
        .mocked(v2)
        .mock.calls.every(
          ([, options]) => (options as { profileContext: unknown }).profileContext === authority,
        ),
    ).toBe(true);
  });
  it("rejects malformed continuation and authority changes without returning partial results", async () => {
    vi.mocked(captureProfileRequestContext).mockReturnValue(authority);
    vi.mocked(isProfileRequestContextCurrent).mockReturnValue(true);
    vi.mocked(v2).mockResolvedValue({ items: [], page: { has_more: true } } as never);
    await expect(listAdminMediaRequestsV2({ limit: 100 })).rejects.toThrow(
      "Incomplete request page",
    );
    vi.mocked(v2).mockResolvedValue({
      items: [],
      page: { has_more: true, next_cursor: "repeat" },
    } as never);
    await expect(listAdminRequestIntegrationsV2()).rejects.toThrow("Incomplete integration page");
    vi.mocked(isProfileRequestContextCurrent).mockReturnValue(false);
    await expect(listAdminMediaRequestsV2()).rejects.toThrow("account or server changed");
  });
  it("maps problem field details inline and unwraps options with string installation IDs", async () => {
    const error = new V2ProblemError("save", {
      type: "https://example.invalid/problems/validation_failed",
      title: "Invalid",
      status: 422,
      detail: "Choose a folder",
      instance: "test",
      errors: [{ code: "invalid", location: "body.root_folder", detail: "Folder missing" }],
    });
    expect(requestValidationErrors(error)).toEqual({
      fields: { root_folder: "Folder missing" },
      message: "Choose a folder",
    });
    vi.mocked(v2).mockResolvedValue({ options: { folder: [{ value: "a", label: "A" }] } } as never);
    expect(
      await loadAdminRequestIntegrationOptionsV2("new", {
        base_url: "https://example.invalid",
        installation_id: 7,
      }),
    ).toEqual({ folder: [{ value: "a", label: "A" }] });
    expect(vi.mocked(v2).mock.calls[0]![1]!).toMatchObject({ body: { installation_id: "7" } });
  });
  it("requires the canonical GET validator", async () => {
    vi.mocked(v2).mockResolvedValue({ id: "a" } as never);
    await expect(getAdminRequestIntegrationV2("a")).rejects.toThrow("Reload");
  });
});
