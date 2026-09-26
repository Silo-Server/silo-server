import { beforeEach, describe, expect, it, vi } from "vitest";
import { v2 } from "./request";
import {
  getAdminImportRun,
  createAdminImportRun,
  updateAdminImportSource,
  clearAdminImportToken,
  updateAdminImportMapping,
  listAdminImportSources,
  listAdminImportMappings,
  bulkAdminImportRuns,
} from "./adminHistoryImports";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
vi.mock("./request", async (original) => ({
  ...(await original<typeof import("./request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/client", async (original) => ({
  ...(await original<typeof import("@/api/client")>()),
  captureProfileRequestContext: vi.fn(),
  isCapturedProfileAuthorityActive: vi.fn(),
}));
const context = {
  accessToken: "test",
  authContextVersion: 1,
  serverOrigin: "",
  profileId: "owner",
  profileToken: null,
};
function reply(
  options: unknown,
  body: unknown,
  headers: Record<string, string> = { ETag: '"new"' },
) {
  (options as { onResponse?: (r: Response) => void })?.onResponse?.(
    new Response(null, { headers }),
  );
  return Promise.resolve(body) as never;
}
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(captureProfileRequestContext).mockReturnValue(context);
  vi.mocked(isCapturedProfileAuthorityActive).mockReturnValue(true);
});
describe("admin history import wire adapter", () => {
  it("writes string IDs and original editor tags without response-only fields", async () => {
    vi.mocked(v2).mockImplementation((_op, options) =>
      reply(options, { id: "3", source_id: "1", silo_user_id: "9" }),
    );
    await updateAdminImportSource(
      1,
      { base_url: "https://other.invalid", admin_token: "" },
      '"source"',
    );
    expect(vi.mocked(v2).mock.calls[0]![1]).toMatchObject({
      path: { id: "1" },
      headers: { "If-Match": '"source"' },
      body: { base_url: "https://other.invalid", admin_token: "" },
    });
    await updateAdminImportMapping(3, { silo_user_id: 9, silo_profile_id: "target" }, '"mapping"');
    expect(vi.mocked(v2).mock.calls[1]![1]).toMatchObject({
      path: { id: "3" },
      headers: { "If-Match": '"mapping"' },
      body: { silo_user_id: "9", silo_profile_id: "target" },
    });
    await expect(updateAdminImportSource(1, {})).rejects.toThrow("Reload");
    expect(v2).toHaveBeenCalledTimes(2);
  });
  it("clears credentials with204 then reads fresh source under the same authority", async () => {
    vi.mocked(v2).mockImplementation((op, options) =>
      op.startsWith("DELETE")
        ? (Promise.resolve(undefined) as never)
        : reply(options, { id: "1", has_admin_token: false }),
    );
    expect(await clearAdminImportToken(1, '"old"')).toMatchObject({
      etag: '"new"',
      has_admin_token: false,
    });
    expect(vi.mocked(v2).mock.calls.map(([op]) => op)).toEqual([
      "DELETE /api/v2/admin/history-imports/sources/{id}/token",
      "GET /api/v2/admin/history-import-sources/{id}",
    ]);
    expect(
      vi
        .mocked(v2)
        .mock.calls.every(
          ([, options]) => (options as { profileContext: unknown }).profileContext === context,
        ),
    ).toBe(true);
  });
  it("hydrates only canonical source data and rejects malformed mapping pagination", async () => {
    vi.mocked(v2).mockImplementation((op, options) =>
      reply(
        options,
        op === "GET /api/v2/admin/history-import-sources"
          ? { items: [{ id: "1", name: "Stale list" }], page: { has_more: false } }
          : { id: "1", name: "Canonical" },
      ),
    );
    expect(await listAdminImportSources()).toMatchObject([
      { id: 1, name: "Canonical", etag: '"new"' },
    ]);
    vi.mocked(v2).mockResolvedValue({
      items: [],
      page: { has_more: true, next_cursor: "repeat" },
    } as never);
    await expect(listAdminImportMappings(1)).rejects.toThrow("Incomplete history import page");
    vi.mocked(isCapturedProfileAuthorityActive).mockReturnValue(false);
    await expect(listAdminImportSources()).rejects.toThrow("account or server changed");
  });
  it("captures accepted Location and RetryAfter and refuses unexpected poll destinations", async () => {
    vi.mocked(v2).mockImplementation((_op, options) =>
      reply(
        options,
        { id: "run", user_id: "9", status: "queued", terminal: false, cancelable: true },
        { Location: "/api/v2/admin/history-imports/runs/run", "Retry-After": "2" },
      ),
    );
    const run = await createAdminImportRun(3);
    expect(run).toMatchObject({
      id: "run",
      location: "/api/v2/admin/history-imports/runs/run",
      retryAfterMs: 2000,
    });
    await getAdminImportRun(run.id, run.location);
    expect(vi.mocked(v2).mock.calls[1]![0]).toBe("GET /api/v2/admin/history-imports/runs/{id}");
    await expect(getAdminImportRun(run.id, "https://untrusted.invalid/run")).rejects.toThrow(
      "Unexpected import status location",
    );
    expect(v2).toHaveBeenCalledTimes(2);
  });
  it("does not turn missing acceptance metadata into an automatic second POST", async () => {
    vi.mocked(v2).mockResolvedValue({
      id: "run",
      user_id: "9",
      status: "queued",
      terminal: false,
      cancelable: true,
    } as never);
    await expect(createAdminImportRun(3)).rejects.toThrow("Refresh runs before trying again");
    expect(v2).toHaveBeenCalledTimes(1);
  });
  it("keeps active and failed bulk outcomes instead of reporting all as created", async () => {
    vi.mocked(v2).mockResolvedValue({
      accepted: 1,
      active: 1,
      failed: 1,
      outcomes: [
        {
          mapping_id: "1",
          status: "accepted",
          run: { id: "a", user_id: "9", status: "queued", terminal: false, cancelable: true },
          location: "/api/v2/admin/history-imports/runs/a",
        },
        {
          mapping_id: "2",
          status: "active",
          run: { id: "b", user_id: "9", status: "running", terminal: false, cancelable: true },
        },
        { mapping_id: "3", status: "failed", error: "Unavailable source" },
      ],
    } as never);
    expect(await bulkAdminImportRuns(4)).toMatchObject({
      accepted: 1,
      active: 1,
      failed: 1,
      outcomes: [
        {
          mapping_id: 1,
          status: "accepted",
          run: { location: "/api/v2/admin/history-imports/runs/a" },
        },
        { mapping_id: 2, status: "active" },
        { mapping_id: 3, status: "failed", error: "Unavailable source" },
      ],
    });
  });
});
