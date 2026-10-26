import { beforeEach, describe, expect, it, vi } from "vitest";
import { isCapturedProfileAuthorityActive } from "@/api/client";
import { ADMIN_DOWNLOAD_REVOKE_MAX_IDS, revokeAdminDownloads } from "./adminDownloadStorage";
import { v2 } from "./request";

vi.mock("./request", async (original) => ({
  ...(await original<typeof import("./request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/client", async (original) => ({
  ...(await original<typeof import("@/api/client")>()),
  isCapturedProfileAuthorityActive: vi.fn(),
}));

const context = {
  accessToken: "test",
  authContextVersion: 1,
  serverOrigin: "",
  profileId: "owner",
  profileToken: null,
};

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(isCapturedProfileAuthorityActive).mockReturnValue(true);
});

describe("revokeAdminDownloads", () => {
  it("sends selected ids in batches the server accepts and adds up the results", async () => {
    vi.mocked(v2).mockImplementation((_op, options) => {
      const ids = (options as { body: { ids: string[] } }).body.ids;
      return Promise.resolve({
        revoked: ids.length,
        bytes: ids.length * 10,
        paused_monitors: 0,
        download_ids: ids,
      }) as never;
    });
    const ids = Array.from({ length: ADMIN_DOWNLOAD_REVOKE_MAX_IDS + 20 }, (_, i) => `dl-${i}`);

    const result = await revokeAdminDownloads(context, { ids }, "lost phone");

    const batches = vi
      .mocked(v2)
      .mock.calls.map(([, options]) => (options as { body: { ids: string[] } }).body.ids.length);
    expect(batches).toEqual([ADMIN_DOWNLOAD_REVOKE_MAX_IDS, 20]);
    expect(result.revoked).toBe(ids.length);
    expect(result.bytes).toBe(ids.length * 10);
    expect(result.download_ids).toEqual(ids);
  });
});
