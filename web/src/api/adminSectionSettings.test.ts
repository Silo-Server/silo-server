import { beforeEach, describe, expect, it, vi } from "vitest";
import { fetchAdminSectionSettings, updateAdminSectionSettings } from "./adminSectionSettings";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));

function tagged(body: unknown, etag: string | null) {
  return (_key: string, options: { onResponse?: (response: Response) => void }) => {
    options.onResponse?.(new Response(null, { headers: etag ? { ETag: etag } : {} }));
    return Promise.resolve(body);
  };
}

beforeEach(() => {
  request.mockReset();
});

describe("admin section settings", () => {
  it("reads whether profiles may add rule rows, with the version it read", async () => {
    request.mockImplementationOnce(tagged({ allow_profile_custom_sections: true }, '"v1"'));
    await expect(fetchAdminSectionSettings()).resolves.toEqual({
      allowProfileRuleRows: true,
      etag: '"v1"',
    });
    expect(request).toHaveBeenCalledWith(
      "GET /api/v2/admin/settings/sections",
      expect.objectContaining({ onResponse: expect.any(Function) }),
    );
  });

  it("refuses a read without a version, since a write could not be checked against it", async () => {
    request.mockImplementationOnce(tagged({ allow_profile_custom_sections: false }, null));
    await expect(fetchAdminSectionSettings()).rejects.toThrow(/version/);
  });

  it("always writes over the version it read, never a wildcard", async () => {
    request.mockImplementationOnce(tagged({ allow_profile_custom_sections: true }, '"v2"'));
    await expect(updateAdminSectionSettings(true, '"v1"')).resolves.toEqual({
      allowProfileRuleRows: true,
      etag: '"v2"',
    });
    expect(request).toHaveBeenCalledWith(
      "PUT /api/v2/admin/settings/sections",
      expect.objectContaining({
        headers: { "If-Match": '"v1"' },
        body: { allow_profile_custom_sections: true },
      }),
    );
    await expect(updateAdminSectionSettings(false, "*")).rejects.toThrow(/version/);
    await expect(updateAdminSectionSettings(false, "")).rejects.toThrow(/version/);
    expect(request).toHaveBeenCalledTimes(1);
  });
});
