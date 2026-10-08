/**
 * Named goldens for the collection editor tests: the writes today's pages
 * send, as `v2Recorder.writes()` records them (wire JSON, undefined members
 * absent). A later PR changes one of these only on purpose and says why.
 *
 * None of them reads a personal collection's `groups` or `group_id`.
 */
import type { RecordedCall } from "@/test/v2Recorder";

type Writes = RecordedCall[];
type ByLimit = Record<"no limit" | "the server's no-limit sentinel" | "a limit of 250", Writes>;

const personalPosterDelete: RecordedCall = {
  operation: "DELETE /api/v2/collections/{id}/image",
  path: "/api/v2/collections/c1/image",
  headers: {},
  query: {
    type: "poster",
  },
};

/** The default heroes for the Core pack on Movies (1) and TV Shows (2). */
const starterPackHeroes = {
  home: { library_id: "1", template_id: "tmdb_trending_movies_week" },
  libraries: { "1": "tmdb_trending_movies_week", "2": "tmdb_trending_tv_week" },
};

function starterPackDryRun(featured?: typeof starterPackHeroes): RecordedCall {
  return {
    operation: "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply",
    path: "/api/v2/admin/collections/template-bundles/core_defaults/apply",
    headers: {},
    body: { library_ids: ["1", "2"], dry_run: true, delete_existing: false, featured },
  };
}

export const goldens = {
  /** Admin manual create from the editor page: the POST, then the poster file, then the backdrop URL. */
  adminManualCreate: [
    {
      operation: "POST /api/v2/admin/collections",
      path: "/api/v2/admin/collections",
      headers: {},
      body: {
        title: "Staff picks",
        description: "",
        collection_type: "manual",
        visibility: "visible",
        featured: false,
        library_ids: ["1"],
      },
    },
    {
      operation: "PUT /api/v2/admin/collections/{id}/poster",
      path: "/api/v2/admin/collections/c1/poster",
      headers: {},
      form: {
        image: {
          file: "poster.png",
        },
      },
    },
    {
      operation: "PUT /api/v2/admin/collections/{id}/backdrop",
      path: "/api/v2/admin/collections/c1/backdrop",
      headers: {},
      form: {
        source_url: "https://images.example/backdrop.png",
      },
    },
  ] satisfies Writes,
  /** Admin smart create from the editor page: `query_definition` keeps numeric library ids; the top-level `library_ids` are strings. */
  adminSmartCreate: [
    {
      operation: "POST /api/v2/admin/collections",
      path: "/api/v2/admin/collections",
      headers: {},
      body: {
        title: "New this month",
        description: "",
        collection_type: "smart",
        visibility: "visible",
        featured: false,
        query_definition: {
          library_ids: [1],
          match: "all",
          groups: [],
          sort: {
            field: "added_at",
            order: "desc",
          },
        },
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin manual update from the editor page. The PATCH leaves out `featured`, so Pin set in Arrange stays. */
  adminManualUpdate: [
    {
      operation: "PATCH /api/v2/admin/collections/{id}",
      path: "/api/v2/admin/collections/c1",
      headers: {
        "If-Match": '"/api/v2/admin/collections/c1#1"',
      },
      body: {
        title: "Renamed",
        description: "",
        collection_type: "manual",
        visibility: "visible",
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin staged poster removal: nothing is sent until Save, then the DELETE follows the PATCH. */
  adminStagedPosterRemoval: [
    {
      operation: "PATCH /api/v2/admin/collections/{id}",
      path: "/api/v2/admin/collections/c1",
      headers: {
        "If-Match": '"/api/v2/admin/collections/c1#1"',
      },
      body: {
        title: "Original",
        description: "",
        collection_type: "manual",
        visibility: "visible",
        library_ids: ["1"],
      },
    },
    {
      operation: "DELETE /api/v2/admin/collections/{id}/image",
      path: "/api/v2/admin/collections/c1/image",
      headers: {},
      query: {
        type: "poster",
      },
    },
  ] satisfies Writes,
  /** Admin backdrop removed and then replaced by a file: the upload replaces it and no DELETE is sent. */
  adminBackdropReplacement: [
    {
      operation: "PATCH /api/v2/admin/collections/{id}",
      path: "/api/v2/admin/collections/c1",
      headers: {
        "If-Match": '"/api/v2/admin/collections/c1#1"',
      },
      body: {
        title: "Original",
        description: "",
        collection_type: "manual",
        visibility: "visible",
        library_ids: ["1"],
      },
    },
    {
      operation: "PUT /api/v2/admin/collections/{id}/backdrop",
      path: "/api/v2/admin/collections/c1/backdrop",
      headers: {},
      form: {
        image: {
          file: "backdrop.png",
        },
      },
    },
  ] satisfies Writes,
  /** A loaded admin smart collection saved unchanged from the editor page. The sentinel saves as no limit; the PATCH leaves out `featured`, so Pin set in Arrange stays. */
  adminSmartUnchanged: {
    "no limit": [
      {
        operation: "PATCH /api/v2/admin/collections/{id}",
        path: "/api/v2/admin/collections/c1",
        headers: {
          "If-Match": '"/api/v2/admin/collections/c1#1"',
        },
        body: {
          title: "Original",
          description: "",
          collection_type: "smart",
          visibility: "visible",
          query_definition: {
            library_ids: [1],
            match: "all",
            groups: [
              {
                match: "all",
                rules: [
                  {
                    field: "genre",
                    op: "is",
                    value: "Comedy",
                  },
                ],
              },
            ],
            sort: {
              field: "added_at",
              order: "desc",
            },
          },
          sort_config: {},
          library_ids: ["1"],
        },
      },
    ],
    "the server's no-limit sentinel": [
      {
        operation: "PATCH /api/v2/admin/collections/{id}",
        path: "/api/v2/admin/collections/c1",
        headers: {
          "If-Match": '"/api/v2/admin/collections/c1#1"',
        },
        body: {
          title: "Original",
          description: "",
          collection_type: "smart",
          visibility: "visible",
          query_definition: {
            library_ids: [1],
            match: "all",
            groups: [
              {
                match: "all",
                rules: [
                  {
                    field: "genre",
                    op: "is",
                    value: "Comedy",
                  },
                ],
              },
            ],
            sort: {
              field: "added_at",
              order: "desc",
            },
          },
          sort_config: {},
          library_ids: ["1"],
        },
      },
    ],
    "a limit of 250": [
      {
        operation: "PATCH /api/v2/admin/collections/{id}",
        path: "/api/v2/admin/collections/c1",
        headers: {
          "If-Match": '"/api/v2/admin/collections/c1#1"',
        },
        body: {
          title: "Original",
          description: "",
          collection_type: "smart",
          visibility: "visible",
          query_definition: {
            library_ids: [1],
            match: "all",
            groups: [
              {
                match: "all",
                rules: [
                  {
                    field: "genre",
                    op: "is",
                    value: "Comedy",
                  },
                ],
              },
            ],
            sort: {
              field: "added_at",
              order: "desc",
            },
            limit: 250,
          },
          sort_config: {},
          library_ids: ["1"],
        },
      },
    ],
  } satisfies ByLimit,
  /** A loaded personal smart collection saved unchanged from the editor page, with `description`. */
  personalSmartUnchanged: {
    "no limit": [
      {
        operation: "PATCH /api/v2/collections/{id}",
        path: "/api/v2/collections/c1",
        headers: {
          "If-Match": '"/api/v2/collections/c1#1"',
        },
        body: {
          name: "Rainy days",
          description: "",
          is_shared: false,
          query_definition: {
            library_ids: [1],
            match: "all",
            groups: [
              {
                match: "all",
                rules: [
                  {
                    field: "genre",
                    op: "is",
                    value: "Comedy",
                  },
                ],
              },
            ],
            sort: {
              field: "added_at",
              order: "desc",
            },
          },
          sort_config: {},
          include_in_server_collections: false,
        },
      },
    ],
    "the server's no-limit sentinel": [
      {
        operation: "PATCH /api/v2/collections/{id}",
        path: "/api/v2/collections/c1",
        headers: {
          "If-Match": '"/api/v2/collections/c1#1"',
        },
        body: {
          name: "Rainy days",
          description: "",
          is_shared: false,
          query_definition: {
            library_ids: [1],
            match: "all",
            groups: [
              {
                match: "all",
                rules: [
                  {
                    field: "genre",
                    op: "is",
                    value: "Comedy",
                  },
                ],
              },
            ],
            sort: {
              field: "added_at",
              order: "desc",
            },
          },
          sort_config: {},
          include_in_server_collections: false,
        },
      },
    ],
    "a limit of 250": [
      {
        operation: "PATCH /api/v2/collections/{id}",
        path: "/api/v2/collections/c1",
        headers: {
          "If-Match": '"/api/v2/collections/c1#1"',
        },
        body: {
          name: "Rainy days",
          description: "",
          is_shared: false,
          query_definition: {
            library_ids: [1],
            match: "all",
            groups: [
              {
                match: "all",
                rules: [
                  {
                    field: "genre",
                    op: "is",
                    value: "Comedy",
                  },
                ],
              },
            ],
            sort: {
              field: "added_at",
              order: "desc",
            },
            limit: 250,
          },
          sort_config: {},
          include_in_server_collections: false,
        },
      },
    ],
  } satisfies ByLimit,
  /** Personal smart create from the editor page, with `description`; the poster file uploads after the POST. */
  personalSmartCreate: [
    {
      operation: "POST /api/v2/collections",
      path: "/api/v2/collections",
      headers: {},
      body: {
        name: "Comfort",
        description: "",
        collection_type: "smart",
        is_shared: false,
        query_definition: {
          library_ids: [],
          match: "all",
          groups: [],
          sort: {
            field: "added_at",
            order: "desc",
          },
        },
        sort_config: {},
        include_in_server_collections: false,
      },
    },
    {
      operation: "PUT /api/v2/collections/{id}/poster",
      path: "/api/v2/collections/c1/poster",
      headers: {},
      form: {
        poster: {
          file: "poster.png",
        },
      },
    },
  ] satisfies Writes,
  /** Personal manual create from the editor page: a pasted poster URL rides in the POST body, with `description`. */
  personalManualCreate: [
    {
      operation: "POST /api/v2/collections",
      path: "/api/v2/collections",
      headers: {},
      body: {
        name: "Rainy days",
        description: "",
        collection_type: "manual",
        is_shared: false,
        include_in_server_collections: false,
        poster_source_url: "https://images.example/poster.png",
      },
    },
  ] satisfies Writes,
  /** Personal manual update from the editor page, with `description`. */
  personalManualUpdate: [
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#1"',
      },
      body: {
        name: "Renamed",
        description: "",
        is_shared: false,
        include_in_server_collections: false,
      },
    },
  ] satisfies Writes,
  /** Personal manual page, poster removed: nothing is sent until Save, then the DELETE follows the PATCH. */
  personalStagedPosterRemoval: [
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#1"',
      },
      body: {
        name: "Rainy days",
        description: "",
        is_shared: false,
        include_in_server_collections: false,
      },
    },
    personalPosterDelete,
  ] satisfies Writes,
  /** Personal manual page, poster removed and then replaced by a file: the upload replaces it and no DELETE is sent. */
  personalPosterReplacement: [
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#1"',
      },
      body: {
        name: "Rainy days",
        description: "",
        is_shared: false,
        include_in_server_collections: false,
      },
    },
    {
      operation: "PUT /api/v2/collections/{id}/poster",
      path: "/api/v2/collections/c1/poster",
      headers: {},
      form: {
        poster: {
          file: "poster.png",
        },
      },
    },
  ] satisfies Writes,
  /** Personal smart editor page, poster removed: the DELETE follows the PATCH on Save. */
  personalSmartStagedPosterRemoval: [
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#1"',
      },
      body: {
        name: "Rainy days",
        description: "",
        is_shared: false,
        query_definition: {
          library_ids: [1],
          match: "all",
          groups: [
            {
              match: "all",
              rules: [
                {
                  field: "genre",
                  op: "is",
                  value: "Comedy",
                },
              ],
            },
          ],
          sort: {
            field: "added_at",
            order: "desc",
          },
        },
        sort_config: {},
        include_in_server_collections: false,
      },
    },
    personalPosterDelete,
  ] satisfies Writes,
  /** Personal manual editor: a title added after the one already there goes to the end, and the rename's PATCH sends the ETag the add left behind. */
  personalAddThenRename: [
    {
      operation: "PUT /api/v2/collections/{id}/items/{item_id}",
      path: "/api/v2/collections/c1/items/movie:alien-1979",
      headers: {},
      body: {
        position: 1,
      },
    },
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#2"',
      },
      body: {
        name: "Renamed",
        description: "",
        is_shared: false,
        include_in_server_collections: false,
      },
    },
  ] satisfies Writes,
  /** Admin MDBList import from a pasted link: imported unpinned (`featured: false`). */
  adminImportMDBList: [
    {
      operation: "POST /api/v2/admin/collections/import/mdblist",
      path: "/api/v2/admin/collections/import/mdblist",
      headers: {},
      body: {
        title: "Top Watched",
        description: "",
        url: "https://mdblist.com/lists/user/top-watched/json",
        featured: false,
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin TMDB chart import: Trending starts on Both, today, and is named for it. */
  adminImportTMDBChart: [
    {
      operation: "POST /api/v2/admin/collections/import/tmdb",
      path: "/api/v2/admin/collections/import/tmdb",
      headers: {},
      body: {
        title: "Trending Today",
        description: "",
        preset: "trending",
        time_window: "day",
        media_type: "all",
        featured: false,
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin TMDB list import; the URL is sent as typed. */
  adminImportTMDBList: [
    {
      operation: "POST /api/v2/admin/collections/import/tmdb-list",
      path: "/api/v2/admin/collections/import/tmdb-list",
      headers: {},
      body: {
        title: "Festival Picks",
        description: "",
        url: "https://www.themoviedb.org/list/310-festival-picks",
        featured: false,
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin template pick: the template's server poster is sent as `poster_url`; imported unpinned. */
  adminTemplateTMDB: [
    {
      operation: "POST /api/v2/admin/collections/import/tmdb",
      path: "/api/v2/admin/collections/import/tmdb",
      headers: {},
      body: {
        title: "Trending Movies This Week",
        description: "Top trending movies on TMDB.",
        featured: false,
        sync_schedule: "0 4 * * *",
        limit: 50,
        sort_config: {},
        poster_url: "https://images.example/templates/trending-movies.jpg",
        preset: "trending",
        media_type: "movie",
        time_window: "week",
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin pick from MDBList search: the list's JSON URL, name and own description. */
  adminTemplateMDBListPick: [
    {
      operation: "POST /api/v2/admin/collections/import/mdblist",
      path: "/api/v2/admin/collections/import/mdblist",
      headers: {},
      body: {
        title: "Oscar Winners",
        description: "Best Picture winners.",
        featured: false,
        sort_config: {},
        url: "https://mdblist.com/lists/cinephile/oscar-winners/json",
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Personal template pick: the cron default maps to a named schedule; the server poster is `poster_url`. */
  personalTemplateTMDB: [
    {
      operation: "POST /api/v2/collections/import/tmdb",
      path: "/api/v2/collections/import/tmdb",
      headers: {},
      body: {
        title: "Trending Movies This Week",
        description: "Top trending movies on TMDB.",
        sync_schedule: "daily",
        limit: 50,
        is_shared: false,
        poster_url: "https://images.example/templates/trending-movies.jpg",
        sort_config: {},
        preset: "trending",
        media_type: "movie",
        time_window: "week",
      },
    },
  ] satisfies Writes,
  /** Admin Synced list editor, MDBList: the whole `source_config` is rebuilt and sent; never `featured`. */
  adminEditMDBList: [
    {
      operation: "PATCH /api/v2/admin/collections/{id}",
      path: "/api/v2/admin/collections/c1",
      headers: {
        "If-Match": '"/api/v2/admin/collections/c1#1"',
      },
      body: {
        title: "Original",
        description: "",
        visibility: "visible",
        collection_type: "mdblist",
        source_url: "https://mdblist.com/lists/user/top-watched/json",
        source_config: {
          mode: "mdblist_json",
          url: "https://mdblist.com/lists/user/top-watched/json",
          limit: 100,
        },
        sync_schedule: "",
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin Synced list editor, TMDB chart: the whole `source_config` is rebuilt and sent; never `featured`. */
  adminEditTMDBChart: [
    {
      operation: "PATCH /api/v2/admin/collections/{id}",
      path: "/api/v2/admin/collections/c1",
      headers: {
        "If-Match": '"/api/v2/admin/collections/c1#1"',
      },
      body: {
        title: "Trending This Week",
        description: "",
        visibility: "visible",
        collection_type: "tmdb",
        source_url: "tmdb://trending/movie/week",
        source_config: {
          mode: "tmdb_preset",
          preset: "trending",
          media_type: "movie",
          time_window: "week",
          limit: 40,
        },
        sync_schedule: "",
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin Synced list editor, TMDB list: the whole `source_config` is rebuilt and sent; never `featured`. */
  adminEditTMDBList: [
    {
      operation: "PATCH /api/v2/admin/collections/{id}",
      path: "/api/v2/admin/collections/c1",
      headers: {
        "If-Match": '"/api/v2/admin/collections/c1#1"',
      },
      body: {
        title: "Festival Picks",
        description: "",
        visibility: "visible",
        collection_type: "tmdb",
        source_url: "https://www.themoviedb.org/list/310",
        source_config: {
          mode: "tmdb_list",
          url: "https://www.themoviedb.org/list/310",
        },
        sync_schedule: "",
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Admin Synced list editor, legacy Trakt: the source can't change, so neither `source_url` nor `source_config` is sent. */
  adminEditTrakt: [
    {
      operation: "PATCH /api/v2/admin/collections/{id}",
      path: "/api/v2/admin/collections/c1",
      headers: {
        "If-Match": '"/api/v2/admin/collections/c1#1"',
      },
      body: {
        title: "For you",
        description: "",
        visibility: "visible",
        collection_type: "trakt",
        sync_schedule: "",
        sort_config: {},
        library_ids: ["1"],
      },
    },
  ] satisfies Writes,
  /** Personal Synced list editor: optional fields go only when changed; name, sharing, libraries and the tab switch always go. */
  personalSyncedRename: [
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#1"',
      },
      body: {
        name: "Top Watched",
        is_shared: false,
        library_ids: ["1"],
        include_in_server_collections: false,
      },
    },
  ] satisfies Writes,
  /** Personal Synced list editor, poster removed: the DELETE follows the PATCH on Save. */
  personalSyncedStagedPosterRemoval: [
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#1"',
      },
      body: {
        name: "Rainy days",
        is_shared: false,
        library_ids: ["1"],
        include_in_server_collections: false,
      },
    },
    personalPosterDelete,
  ] satisfies Writes,
  /** Personal Synced list editor: clearing Max titles sends `max_items: 0`. */
  personalSyncedClearLimit: [
    {
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: {
        "If-Match": '"/api/v2/collections/c1#1"',
      },
      body: {
        name: "Rainy days",
        is_shared: false,
        library_ids: ["1"],
        include_in_server_collections: false,
        max_items: 0,
      },
    },
  ] satisfies Writes,
  /**
   * Starter packs, hero switch off: the dry run when the pack opens, the same
   * dry run again right before Add, the job, and one more dry run once the job
   * ends so the table shows what is there now. No `featured` member, so the
   * server never touches existing hero rows; `delete_existing` is always false.
   */
  starterPackApply: [
    starterPackDryRun(),
    starterPackDryRun(),
    {
      operation: "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply-job",
      path: "/api/v2/admin/collections/template-bundles/core_defaults/apply-job",
      headers: {},
      body: { library_ids: ["1", "2"], delete_existing: false },
    },
    starterPackDryRun(),
  ] satisfies Writes,
  /**
   * Starter packs, hero switch turned on with the default heroes: the first
   * dry run has no heroes; the rest, the job included, carry the same `featured`.
   * When the job ends the switch turns back off, and only the check without
   * heroes runs again: the one with heroes the switch left isn't refetched.
   */
  starterPackApplyWithHeroes: [
    starterPackDryRun(),
    starterPackDryRun(starterPackHeroes),
    starterPackDryRun(starterPackHeroes),
    {
      operation: "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply-job",
      path: "/api/v2/admin/collections/template-bundles/core_defaults/apply-job",
      headers: {},
      body: { library_ids: ["1", "2"], delete_existing: false, featured: starterPackHeroes },
    },
    starterPackDryRun(),
  ] satisfies Writes,
  /** Add to collection, own manual collection: the personal item route. */
  addToPersonalCollection: [
    {
      operation: "PUT /api/v2/collections/{id}/items/{item_id}",
      path: "/api/v2/collections/c1/items/movie:heat-1995",
      headers: {},
      body: {
        position: 0,
      },
    },
  ] satisfies Writes,
  /**
   * What Add to collection lists: the profile's own manual collections, for
   * an acting admin too. Server collections are no longer offered here;
   * admins add titles to them from the collection's editor.
   */
  addToCollectionChoices: ["Rainy days"],
  /** What Add to collection reads to fill its list: one list, marked for the title. */
  addToCollectionReads: [
    {
      operation: "GET /api/v2/collections",
      path: "/api/v2/collections",
      headers: {},
      query: { contains_item: "movie:heat-1995" },
    },
  ] satisfies RecordedCall[],
  /** Add to collection's inline create: the personal manual collection, then the title. */
  addToNewCollection: [
    {
      operation: "POST /api/v2/collections",
      path: "/api/v2/collections",
      headers: {},
      body: {
        name: "Night in",
        description: "",
        is_shared: false,
        include_in_server_collections: false,
        collection_type: "manual",
      },
    },
    {
      operation: "PUT /api/v2/collections/{id}/items/{item_id}",
      path: "/api/v2/collections/c1/items/movie:heat-1995",
      headers: {},
      body: {
        position: 0,
      },
    },
  ] satisfies Writes,
};
