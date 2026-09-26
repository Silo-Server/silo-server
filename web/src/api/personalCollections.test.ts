import { describe, expect, it, vi } from "vitest";
import type { components } from "@/api/v2/schema";
import {
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
  allowed_profile_ids: ["owner"],
  query_definition: {},
  sort_config: {},
  sort_order: 0,
  group_id: null,
  source_url: "",
  sync_schedule: "",
  next_sync_at: null,
  last_sync_at: null,
  last_sync_status: "",
  last_sync_message: "",
  item_count: 0,
  include_in_server_collections: false,
  poster_url: "",
  poster_thumbhash: "",
  created_at: "2026-09-05T00:00:00.000Z",
  updated_at: "2026-09-05T00:00:00.000Z",
};

describe("personal collection v2 adapter", () => {
  it("preserves group removal and converts library IDs without clearing omitted settings", () => {
    expect(collectionUpdateToV2({ group_id: null, library_ids: [7], max_items: 0 })).toEqual({
      group_id: null,
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
