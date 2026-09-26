import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/v2/schema";
import { V2ProblemError } from "@/api/v2/request";
import {
  fetchAdminSections,
  fetchAdminSectionSnapshot,
  fetchAdminSectionDeleteTargets,
  createAdminSection,
  updateAdminSection,
  deleteAdminSection,
  reorderAdminSections,
  restoreAdminSections,
  bulkCreateAdminSections,
} from "./adminSections";
import { previewSection } from "@/lib/recipes";
const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
const row: components["schemas"]["AdminSection"] = {
  id: "s1",
  scope: "library",
  library_id: "7",
  position: 0,
  section_type: "recently_added",
  title: "Saved",
  enabled: false,
  featured: false,
  item_limit: 20,
  config: {
    library_ids: [7],
    groups: [{ match: "all", rules: [{ field: "year", op: "between", value: [2000, 2020] }] }],
  },
  created_at: "2026-09-05T00:00:00.000Z",
  updated_at: "2026-09-05T00:00:00.000Z",
};
function tagged(body: unknown, etag = '"saved"') {
  return (_key: string, options: { onResponse?: (response: Response) => void }) => {
    options.onResponse?.(new Response(null, { headers: { ETag: etag } }));
    return Promise.resolve(body);
  };
}
beforeEach(() => {
  request.mockReset();
});
describe("admin sections adapter", () => {
  it("brackets disabled definitions with canonical order and maps only top-level library IDs", async () => {
    const order = { scope: "library", library_id: "7", ordered_ids: ["s2", "s1"] };
    request
      .mockImplementationOnce(tagged(order))
      .mockResolvedValueOnce({ items: [row, { ...row, id: "s2" }] })
      .mockImplementationOnce(tagged(order));
    const board = await fetchAdminSections("library", 7);
    expect(board.sections.map((section) => section.id)).toEqual(["s2", "s1"]);
    expect(board.sections[1]).toEqual({ ...row, library_id: 7 });
    expect(board.etag).toBe('"saved"');
    expect(request.mock.calls.map(([key]) => key)).toEqual([
      "GET /api/v2/admin/sections/order",
      "GET /api/v2/admin/sections",
      "GET /api/v2/admin/sections/order",
    ]);
  });
  it.each(["version", "membership"])("rejects a torn %s board", async (reason) => {
    const order = { scope: "home", library_id: null, ordered_ids: ["s1"] };
    request
      .mockImplementationOnce(tagged(order))
      .mockResolvedValueOnce({ items: reason === "membership" ? [] : [row] })
      .mockImplementationOnce(tagged(order, reason === "version" ? '"new"' : '"saved"'));
    await expect(fetchAdminSections("home")).rejects.toThrow("changed while loading");
  });
  it("captures exact row versions and rejects missing validators", async () => {
    request.mockImplementationOnce(tagged(row));
    expect(await fetchAdminSectionSnapshot("s1")).toEqual({
      section: { ...row, library_id: 7 },
      etag: '"saved"',
    });
    request.mockResolvedValueOnce(row);
    await expect(fetchAdminSectionSnapshot("s1")).rejects.toThrow("version is unavailable");
  });
  it("sends explicit captured guards without preflight, retry, or latest tag substitution", async () => {
    const stale = new V2ProblemError(
      "updateAdminSection",
      {
        type: "stale_version",
        instance: "/api/v2/admin/sections/s1",
        title: "Stale",
        detail: "Changed",
        status: 412,
      },
      null,
      '"new"',
    );
    request.mockRejectedValueOnce(stale);
    await expect(
      updateAdminSection({
        id: "s1",
        etag: '"old"',
        title: "Draft",
        scope: "library",
        library_id: 7,
      }),
    ).rejects.toBe(stale);
    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0]).toMatchObject([
      "PATCH /api/v2/admin/sections/{id}",
      { path: { id: "s1" }, headers: { "If-Match": '"old"' }, body: { title: "Draft" } },
    ]);
    expect(request.mock.calls[0]![1].body).not.toHaveProperty("scope");
    expect(() => deleteAdminSection({ id: "s1", etag: "*" })).toThrow();
    await reorderAdminSections({
      scope: "library",
      library_id: 7,
      etag: '"order"',
      ordered_ids: ["s1"],
    });
    await restoreAdminSections({
      scope: "library",
      library_id: 7,
      etag: '"restore"',
      reset_profiles: true,
    });
    expect(request.mock.calls[1]![1]).toEqual({
      query: { scope: "library", library_id: "7" },
      headers: { "If-Match": '"order"' },
      body: { ordered_ids: ["s1"] },
    });
    expect(request.mock.calls[2]![1].headers).toEqual({ "If-Match": '"restore"' });
  });
  it("bounds deduplicated delete snapshot reads to four and retains absent targets", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let active = 0,
      maximum = 0;
    request.mockImplementation(
      async (
        _key: string,
        options: { path: { id: string }; onResponse: (response: Response) => void },
      ) => {
        active++;
        maximum = Math.max(active, maximum);
        await gate;
        active--;
        if (options.path.id === "gone")
          throw new V2ProblemError("getAdminSection", {
            type: "not_found",
            instance: "/api/v2/admin/sections/s1",
            title: "Gone",
            detail: "Gone",
            status: 404,
          });
        options.onResponse(new Response(null, { headers: { ETag: `"${options.path.id}"` } }));
        return { ...row, id: options.path.id };
      },
    );
    const pending = fetchAdminSectionDeleteTargets(["s1", "s2", "s3", "s4", "gone", "s1"]);
    expect(request).toHaveBeenCalledTimes(4);
    release();
    const targets = await pending;
    expect(maximum).toBe(4);
    expect(targets).toHaveLength(5);
    expect(targets[4]).toEqual({ id: "gone", etag: null });
  });
  it("preserves config and false fields on create and caps bulk to 100 requests targets", async () => {
    request.mockResolvedValue(row);
    await createAdminSection({ ...row, library_id: 7 });
    expect(request.mock.calls[0]![1].body).toMatchObject({
      library_id: "7",
      config: row.config,
      enabled: false,
    });
    expect(() =>
      bulkCreateAdminSections({
        scope: "library",
        title: "New",
        section_type: "recently_added",
        library_ids: Array.from({ length: 101 }, (_, i) => i + 1),
      }),
    ).toThrow("100");
    expect(request).toHaveBeenCalledTimes(1);
    await bulkCreateAdminSections({
      scope: "library",
      title: "New",
      section_type: "recently_added",
      library_ids: [7],
    });
    expect(request.mock.calls[1]![1].body.library_ids).toEqual(["7"]);
  });
  it("adapts native preview items and top-level scope IDs without rewriting recipe config", async () => {
    request.mockResolvedValue({
      items: [{ content_id: "media", title: "Title", poster_url: "https://example.test/poster" }],
      total_count: 1,
    });
    expect(
      await previewSection({
        section_type: "query",
        config: row.config,
        library_id: 7,
        library_ids: [7],
      }),
    ).toEqual({
      items: [{ content_id: "media", title: "Title", poster_path: "https://example.test/poster" }],
      total_count: 1,
    });
    expect(request.mock.calls[0]).toEqual([
      "POST /api/v2/admin/sections/preview",
      { body: { section_type: "query", config: row.config, library_id: "7", library_ids: ["7"] } },
    ]);
  });
});
