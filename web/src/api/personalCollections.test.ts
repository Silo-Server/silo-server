import { describe, expect, it, vi } from "vitest";
import type { components } from "@/api/v2/schema";
import {
  collectionsFromV2,
  collectionUpdateToV2,
  discoveryFromV2,
  importBodyToV2,
  saveCollectionPoster,
} from "./personalCollections";
import { v2Fixture } from "@/api/v2/testing";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", () => ({ v2: request }));

const collection: components["schemas"]["PersonalCollection"] = {
  id: "saved",
  profile_id: "owner",
  creator_profile_id: "owner",
  name: "Films",
  description: "",
  collection_type: "manual",
  is_shared: false,
  query_definition: {},
  sort_config: {},
  sort_order: 0,
  group_id: null,
  source_url: "",
  sync_schedule: "",
  sync_cadence: "",
  next_sync_at: null,
  last_sync_at: null,
  last_sync_status: "",
  last_sync_message: "",
  item_count: 0,
  include_in_server_collections: false,
  poster_url: "",
  poster_thumbhash: "",
  poster_is_collage: false,
  created_at: "2026-09-05T00:00:00.000Z",
  updated_at: "2026-09-05T00:00:00.000Z",
};

describe("personal collection v2 adapter", () => {
  it("lists own and shared collections without personal collection groups", () => {
    const shared = { ...collection, id: "theirs", creator_profile_id: "parent", is_shared: true };
    const result = collectionsFromV2(
      v2Fixture<"GET /api/v2/collections">({ items: [collection, shared], groups: [] }),
    );
    expect(result).toEqual({
      collections: [
        expect.objectContaining({ id: "saved", creator_profile_id: "owner" }),
        expect.objectContaining({ id: "theirs", creator_profile_id: "parent", is_shared: true }),
      ],
    });
  });

  it("sends the sharing switch and converts library IDs without clearing omitted settings", () => {
    expect(collectionUpdateToV2({ is_shared: true, library_ids: [7], max_items: 0 })).toEqual({
      is_shared: true,
      library_ids: ["7"],
      max_items: 0,
    });
    expect(JSON.parse(JSON.stringify(collectionUpdateToV2({ name: "Renamed" })))).toEqual({
      name: "Renamed",
    });
    expect(importBodyToV2({ title: "Imported", library_ids: [7, 9] }).library_ids).toEqual([
      "7",
      "9",
    ]);
  });

  it("adapts the discovery envelope and media kind without leaking numeric IDs onto v2 requests", () => {
    const result = discoveryFromV2(
      v2Fixture<"GET /api/v2/collections/import/mdblist/top">({
        configured: true,
        items: [
          {
            id: "12",
            user_id: "4",
            user_name: "curator",
            name: "Films",
            slug: "films",
            description: "",
            media_type: "movie",
            items: 3,
            likes: 2,
            url: "https://mdblist.com/lists/curator/films",
          },
        ],
      }),
    );
    expect(result.lists[0]).toMatchObject({ id: 12, user_id: 4, mediatype: "movie" });
  });

  it("returns the saved resource after a poster failure so a caller cannot repeat creation", async () => {
    request.mockRejectedValueOnce(new Error("Storage unavailable"));
    const poster = new File(["image"], "poster.png", { type: "image/png" });
    const result = await saveCollectionPoster(collection, poster);
    expect(result.collection.id).toBe("saved");
    expect(result.posterError).toBe("Storage unavailable");
    expect(request).toHaveBeenCalledWith("PUT /api/v2/collections/{id}/poster", {
      path: { id: "saved" },
      form: { poster },
    });
    expect(request).toHaveBeenCalledTimes(1);
  });
});

describe("saving a staged poster removal", () => {
  const withPoster = { ...collection, poster_url: "https://images.example/p.webp" };

  it("deletes the poster and returns the collection without it", async () => {
    request.mockReset().mockResolvedValueOnce(undefined);
    const result = await saveCollectionPoster(withPoster, null, undefined, true);
    expect(request.mock.calls).toEqual([
      [
        "DELETE /api/v2/collections/{id}/image",
        { path: { id: "saved" }, query: { type: "poster" } },
      ],
    ]);
    expect(result.collection.poster_url).toBe("");
    expect(result.posterError).toBeUndefined();
  });

  it("uploads a replacement file instead of deleting, so the new poster survives", async () => {
    request.mockReset().mockResolvedValueOnce(withPoster);
    const poster = new File(["image"], "poster.png", { type: "image/png" });
    await saveCollectionPoster(withPoster, poster, undefined, true);
    expect(request.mock.calls).toEqual([
      ["PUT /api/v2/collections/{id}/poster", { path: { id: "saved" }, form: { poster } }],
    ]);
  });

  it("uploads a replacement URL instead of deleting", async () => {
    request.mockReset().mockResolvedValueOnce(withPoster);
    await saveCollectionPoster(withPoster, null, "https://images.example/new.png", true);
    expect(request.mock.calls).toEqual([
      [
        "PUT /api/v2/collections/{id}/poster",
        { path: { id: "saved" }, form: { source_url: "https://images.example/new.png" } },
      ],
    ]);
  });

  it("keeps the saved collection and reports a failed removal", async () => {
    request.mockReset().mockRejectedValueOnce(new Error("Storage unavailable"));
    const result = await saveCollectionPoster(withPoster, null, undefined, true);
    expect(result.collection.poster_url).toBe("https://images.example/p.webp");
    expect(result.posterError).toBe("Storage unavailable");
  });

  it("sends nothing when no poster change is staged", async () => {
    request.mockReset();
    await saveCollectionPoster(withPoster);
    expect(request).not.toHaveBeenCalled();
  });
});
