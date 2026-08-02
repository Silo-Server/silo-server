import { describe, expect, it } from "vitest";

import {
  buildAdminCollectionEditorPath,
  buildTMDBPresetSourceInput,
  parseTMDBPresetSourceConfig,
  toAdminCollectionBuilderValue,
  toAdminCollectionRequest,
} from "./adminCollectionsShared";

describe("AdminCollections helpers", () => {
  it("seeds builder state with multi-library scope", () => {
    const draft = toAdminCollectionBuilderValue(
      {
        id: "col-1",
        library_id: 1,
        library_ids: [1, 2],
        slug: "action",
        title: "Action",
        description: "",
        collection_type: "smart",
        visibility: "visible",
        sort_order: 0,
        group_id: null,
        featured: true,
        poster_url: "",
        backdrop_url: "",
        source_url: "",
        query_definition: {
          library_ids: [1, 2],
          match: "all",
          groups: [],
          sort: { field: "rating_imdb", order: "desc" },
        },
        sort_config: {},
        source_config: {},
        last_sync_status: "idle",
        last_sync_message: "",
        item_count: 0,
        created_at: "",
        updated_at: "",
      },
      null,
    );

    expect(draft.query_definition.library_ids).toEqual([1, 2]);
    expect(draft.collection_type).toBe("smart");
  });

  it("serializes manual admin drafts into the richer request shape", () => {
    const body = toAdminCollectionRequest(toAdminCollectionBuilderValue(null, 4));

    expect(body.library_ids).toEqual([4]);
    expect(body.collection_type).toBe("smart");
  });

  it("builds a create route that preserves the current library selection", () => {
    expect(buildAdminCollectionEditorPath("new", 7)).toBe("/admin/collections/new?libraryId=7");
  });

  it("builds an edit route for an existing collection", () => {
    expect(buildAdminCollectionEditorPath("col-9", 4)).toBe(
      "/admin/collections/col-9/edit?libraryId=4",
    );
  });

  it("parses a generic tmdb preset source config", () => {
    expect(
      parseTMDBPresetSourceConfig({
        id: "col-1",
        library_id: 1,
        library_ids: [1],
        slug: "popular-movies",
        title: "Popular Movies",
        description: "",
        collection_type: "tmdb",
        visibility: "visible",
        sort_order: 0,
        group_id: null,
        featured: true,
        poster_url: "",
        backdrop_url: "",
        source_url: "tmdb://popular/movie",
        query_definition: {
          library_ids: [1],
          match: "all",
          groups: [],
          sort: { field: "title", order: "asc" },
        },
        sort_config: {},
        source_config: {
          mode: "tmdb_preset",
          preset: "popular",
          media_type: "movie",
          limit: 35,
        },
        last_sync_status: "idle",
        last_sync_message: "",
        item_count: 0,
        created_at: "",
        updated_at: "",
      }),
    ).toEqual({
      collectionId: "",
      sourceKind: "preset",
      preset: "popular",
      mediaType: "movie",
      timeWindow: "day",
      limit: "35",
    });
  });

  it("builds a trending tmdb source input with time window in the config and URL", () => {
    expect(
      buildTMDBPresetSourceInput({
        preset: "trending",
        mediaType: "all",
        timeWindow: "week",
        limit: "50",
      }),
    ).toEqual({
      source_url: "tmdb://trending/all/week",
      source_config: {
        mode: "tmdb_preset",
        preset: "trending",
        media_type: "all",
        time_window: "week",
        limit: 50,
      },
    });
  });

  it("builds a movie-only tmdb source input without time window", () => {
    expect(
      buildTMDBPresetSourceInput({
        preset: "now_playing",
        mediaType: "movie",
        timeWindow: "day",
        limit: "",
      }),
    ).toEqual({
      source_url: "tmdb://now_playing/movie",
      source_config: {
        mode: "tmdb_preset",
        preset: "now_playing",
        media_type: "movie",
      },
    });
  });
});
