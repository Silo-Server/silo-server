import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/v2/schema";
import {
  fetchAdminBoardOrderSnapshot,
  adminCreateBody,
  adminUpdateBody,
  fetchAdminCollectionSnapshot,
  prepareAdminCollectionDeletes,
  saveAdminArtwork,
} from "./adminCollections";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
const collection: components["schemas"]["AdminCollection"] = {
  id: "saved",
  title: "Movies",
  description: "",
  collection_type: "manual",
  visibility: "visible",
  library_id: "7",
  library_ids: ["7"],
  group_id: null,
  slug: "movies",
  sort_order: 0,
  featured: false,
  poster_url: "",
  backdrop_url: "",
  source_url: "",
  query_definition: {},
  sort_config: {},
  source_config: {},
  management_mode: "manual",
  management_key: "",
  management_source: "",
  last_sync_status: "idle",
  last_sync_message: "",
  item_count: 0,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
};
beforeEach(() => {
  request.mockReset();
});
describe("admin collection adapter", () => {
  it("splits source artwork and converts IDs without clearing omitted settings", () => {
    expect(
      JSON.parse(
        JSON.stringify(
          adminCreateBody({
            title: "New",
            library_id: 7,
            library_ids: [7],
            poster_source_url: "https://example.test/poster",
          }),
        ),
      ),
    ).toEqual({ title: "New", library_id: "7", library_ids: ["7"] });
    expect(
      JSON.parse(
        JSON.stringify(
          adminUpdateBody({
            title: "Changed",
            group_id: null,
            library_id: 7,
            poster_source_url: "https://example.test/poster",
          }),
        ),
      ),
    ).toEqual({ title: "Changed", group_id: null });
  });
  it("keeps the created resource on artwork failure and still attempts the other image once", async () => {
    request
      .mockRejectedValueOnce(new Error("Storage unavailable"))
      .mockResolvedValueOnce(collection);
    const poster = new File(["poster"], "poster.png");
    const result = await saveAdminArtwork(
      collection,
      { backdrop_source_url: "https://example.test/backdrop" },
      poster,
    );
    expect(result.collection.id).toBe("saved");
    expect(result.artworkErrors).toEqual(["poster: Storage unavailable"]);
    expect(request.mock.calls).toEqual([
      [
        "PUT /api/v2/admin/collections/{id}/poster",
        { path: { id: "saved" }, form: { image: poster } },
      ],
      [
        "PUT /api/v2/admin/collections/{id}/backdrop",
        { path: { id: "saved" }, form: { source_url: "https://example.test/backdrop" } },
      ],
    ]);
  });
  it("retains the exact canonical validator and refuses missing validators", async () => {
    request.mockImplementationOnce(async (_operation, options) => {
      options.onResponse(new Response(null, { headers: { ETag: '"observed"' } }));
      return collection;
    });
    expect((await fetchAdminCollectionSnapshot("saved")).etag).toBe('"observed"');
    request.mockResolvedValueOnce(collection);
    await expect(fetchAdminCollectionSnapshot("saved")).rejects.toThrow("version is unavailable");
  });
  it("prepares bulk confirmation with at most four reads in flight and no mutations", async () => {
    let inFlight = 0,
      maximum = 0;
    request.mockImplementation(async (_operation, options) => {
      inFlight++;
      maximum = Math.max(maximum, inFlight);
      await Promise.resolve();
      options.onResponse(new Response(null, { headers: { ETag: `"${options.path.id}"` } }));
      inFlight--;
      return { ...collection, id: options.path.id };
    });
    const snapshots = await prepareAdminCollectionDeletes(["1", "2", "3", "4", "5", "1"]);
    expect(maximum).toBe(4);
    expect(snapshots).toHaveLength(5);
    expect(snapshots[4]).toEqual({ id: "5", etag: '"5"' });
    expect(
      request.mock.calls.every(([operation]) => operation === "GET /api/v2/admin/collections/{id}"),
    ).toBe(true);
  });
  it("rejects group-order reads that span a concurrent library move", async () => {
    let groupReads = 0;
    request.mockImplementation(async (operation, options) => {
      const groupOrder =
        operation === "GET /api/v2/admin/libraries/{library_id}/collection-groups/order";
      const etag = groupOrder ? `"revision-${++groupReads}"` : '"destination"';
      options.onResponse(new Response(null, { headers: { ETag: etag } }));
      return { ordered_ids: [], has_more: false };
    });
    await expect(fetchAdminBoardOrderSnapshot(7, ["group"])).rejects.toThrow(
      "changed while loading",
    );
    expect(request.mock.calls.every(([operation]) => operation.startsWith("GET"))).toBe(true);
  });
  it("uses a replacement instead of deleting artwork when both are supplied", async () => {
    request.mockResolvedValueOnce(collection);
    const replacement = new File(["new"], "poster.png");
    const result = await saveAdminArtwork(collection, {}, replacement, null, ["poster"]);
    expect(result.artworkErrors).toEqual([]);
    expect(request.mock.calls).toEqual([
      [
        "PUT /api/v2/admin/collections/{id}/poster",
        { path: { id: "saved" }, form: { image: replacement } },
      ],
    ]);
  });
});
