import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import getAdminCollectionOk from "../../../../contracts/api/v2/fixtures/get_admin_collection_ok.json";
import getCollectionOk from "../../../../contracts/api/v2/fixtures/get_collection_ok.json";
import { adminCollectionFromV2 } from "@/api/adminCollections";
import { collectionFromV2 } from "@/api/personalCollections";
import type { Collection, LibraryCollection } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { adminKeys, collectionKeys, libraryCollectionKeys } from "@/hooks/queries/keys";
import {
  adminCollection,
  adminSmartCollection,
  adminSyncedCollection,
  emptyPreview,
  personalSmartCollection,
  personalSyncedCollection,
} from "@/test/fixtures/collectionAnswers";
import { goldens } from "@/test/fixtures/collectionBodies";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import {
  collectionsInAdminScope,
  PERSONAL_SCOPE,
  SERVER_SCOPE,
  type CollectionScope,
  type ManualOrSmartDraft,
  type WireCollection,
} from "./scope";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());

installV2Recorder();

const writes = () => v2Recorder.writes();
const png = (name: string) => new File(["image"], name, { type: "image/png" });

function storedQuery(limit: number | undefined) {
  return {
    library_ids: [1],
    match: "all",
    groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "Comedy" }] }],
    sort: { field: "added_at", order: "desc" },
    ...(limit === undefined ? {} : { limit }),
  };
}

function savable<T extends { kind: string }>(draft: T) {
  if (draft.kind === "synced") throw new Error("only Manual and Smart drafts here");
  return draft as T & ManualOrSmartDraft;
}

/** Load through the scope, as an editor does, and return the draft it would start from. */
async function loaded<Raw extends WireCollection>(scope: CollectionScope<Raw>, id = "c1") {
  const snapshot = await scope.fetchSnapshot(id);
  v2Recorder.calls = [];
  return { draft: savable(scope.toDraft(snapshot.view, { kind: "manual" })), etag: snapshot.etag };
}

describe("keys", () => {
  it("are the keys the existing hooks use", () => {
    expect(SERVER_SCOPE.keys.root).toEqual(["admin", "collections"]);
    expect(SERVER_SCOPE.keys.list).toEqual(["admin", "collections", undefined]);
    expect(SERVER_SCOPE.keys.capabilities).toEqual(["admin", "collections", "capabilities"]);
    expect(SERVER_SCOPE.keys.snapshot("c1")).toEqual(["admin", "collections", "edit", "c1"]);
    expect(SERVER_SCOPE.keys.itemOrder("c1")).toEqual([
      "libraryCollections",
      "items",
      "c1",
      "order",
    ]);
    expect(SERVER_SCOPE.keys.preview("fp")).toEqual(["collections", "preview", "admin", "fp"]);

    expect(PERSONAL_SCOPE.keys.root).toEqual(["collections"]);
    expect(PERSONAL_SCOPE.keys.list).toEqual(["collections", "list"]);
    expect(PERSONAL_SCOPE.keys.capabilities).toEqual(["collections", "capabilities"]);
    expect(PERSONAL_SCOPE.keys.snapshot("c1")).toEqual(["collections", "edit", "c1"]);
    expect(PERSONAL_SCOPE.keys.itemOrder("c1")).toEqual(["collections", "items", "c1", "order"]);
    expect(PERSONAL_SCOPE.keys.preview("fp")).toEqual(["collections", "preview", "user", "fp"]);
  });
});

describe("paths", () => {
  it("lead to today's server pages", () => {
    expect(SERVER_SCOPE.paths.list()).toBe("/admin/collections");
    expect(SERVER_SCOPE.paths.list({ libraryId: 3 })).toBe(
      "/admin/collections?libraryId=3&view=list",
    );
    expect(SERVER_SCOPE.paths.list({ libraryId: 3, view: "arrange" })).toBe(
      "/admin/collections?libraryId=3&view=arrange",
    );
    expect(SERVER_SCOPE.paths.list({ libraryId: null })).toBe("/admin/collections");
    expect(SERVER_SCOPE.paths.create()).toBe("/admin/collections/new");
    expect(SERVER_SCOPE.paths.create({ type: "synced", source: "mdblist", libraryId: 2 })).toBe(
      "/admin/collections/new?type=synced&source=mdblist&libraryId=2",
    );
    expect(SERVER_SCOPE.paths.edit("c1")).toBe("/admin/collections/c1/edit");
    expect(SERVER_SCOPE.paths.edit("c1", { libraryId: 2, focus: "titles" })).toBe(
      "/admin/collections/c1/edit?libraryId=2&focus=titles",
    );
    const view = SERVER_SCOPE.toView(adminCollectionFromV2(adminCollection as never));
    expect(SERVER_SCOPE.paths.browse(view)).toBe(
      "/catalog?source=library_collection&collection_id=c1&title=Original",
    );
  });

  it("lead to today's personal pages", () => {
    expect(PERSONAL_SCOPE.paths.list()).toBe("/collections");
    expect(PERSONAL_SCOPE.paths.create()).toBe("/collections/new");
    expect(PERSONAL_SCOPE.paths.create({ type: "smart" })).toBe("/collections/new?type=smart");
    expect(PERSONAL_SCOPE.paths.edit("col-3")).toBe("/collections/col-3/edit");
    expect(PERSONAL_SCOPE.paths.edit("col-3", { focus: "titles" })).toBe(
      "/collections/col-3/edit?focus=titles",
    );
    const view = PERSONAL_SCOPE.toView(collectionFromV2(getCollectionOk as never));
    expect(PERSONAL_SCOPE.paths.browse(view)).toBe(
      "/catalog?source=user_collection&collection_id=c1&title=Rainy+days",
    );
  });
});

describe("toView", () => {
  it("reads a server collection", () => {
    const view = SERVER_SCOPE.toView(
      adminCollectionFromV2({
        ...adminCollection,
        featured: true,
        poster_url: "https://images.example/p.png",
        item_count: 7,
      } as never),
    );
    expect(view).toMatchObject({
      id: "c1",
      scope: "server",
      kind: "manual",
      source: null,
      name: "Original",
      description: "",
      posterUrl: "https://images.example/p.png",
      backdropUrl: undefined,
      itemCount: 7,
      libraryIds: [1],
      server: { visibility: "visible", pinFirst: true },
    });
    expect(view.sync).toBeUndefined();
    expect(view.ownerProfileId).toBeUndefined();
  });

  it("reads a server synced list's source and sync state", () => {
    const view = SERVER_SCOPE.toView(
      adminCollectionFromV2({
        ...adminSyncedCollection("tmdb", {
          source_url: "https://www.themoviedb.org/list/310",
          source_config: { mode: "tmdb_list", url: "https://www.themoviedb.org/list/310" },
        }),
        last_sync_status: "warning",
        last_sync_message: "2 unmatched",
        sync_schedule: "0 4 * * *",
      } as never),
    );
    expect(view).toMatchObject({
      kind: "synced",
      source: "tmdb_list",
      sync: { status: "warning", message: "2 unmatched", schedule: "0 4 * * *" },
    });
  });

  it("reads a personal collection with its owner and sharing", () => {
    const view = PERSONAL_SCOPE.toView(
      collectionFromV2({
        ...getCollectionOk,
        is_shared: true,
        include_in_server_collections: true,
      } as never),
    );
    expect(view).toMatchObject({
      id: "c1",
      scope: "personal",
      kind: "manual",
      name: "Rainy days",
      itemCount: 4,
      libraryIds: [],
      ownerProfileId: "p-owner",
      personal: { shared: true, inLibraryTabs: true },
    });
    expect(view.server).toBeUndefined();
  });

  it("reads the libraries a personal smart collection or synced list is limited to", () => {
    const smart = PERSONAL_SCOPE.toView(
      collectionFromV2(personalSmartCollection(storedQuery(undefined)) as never),
    );
    expect(smart.libraryIds).toEqual([1]);
    const synced = PERSONAL_SCOPE.toView(
      collectionFromV2(
        personalSyncedCollection("mdblist", {
          source_url: "https://mdblist.com/lists/user/top-watched/json",
          source_config: { library_ids: [2, 2, -1, 3] },
        }) as never,
      ),
    );
    expect(synced).toMatchObject({ kind: "synced", source: "mdblist", libraryIds: [2, 3] });
  });
});

describe("toDraft", () => {
  it("starts a new server collection in the library it was opened from", () => {
    expect(SERVER_SCOPE.toDraft(null, { kind: "smart", libraryId: 4 })).toEqual({
      kind: "smart",
      name: "",
      description: "",
      libraryIds: [4],
      rules: {
        library_ids: [4],
        match: "all",
        groups: [],
        sort: { field: "added_at", order: "desc" },
      },
      rawSortConfig: {},
      artwork: {},
      server: { visibility: "visible" },
    });
    expect(SERVER_SCOPE.toDraft(null, { kind: "manual" })).toMatchObject({
      kind: "manual",
      libraryIds: [],
      rules: undefined,
    });
  });

  it("starts a new personal collection unshared, in every library", () => {
    expect(PERSONAL_SCOPE.toDraft(null, { kind: "manual" })).toEqual({
      kind: "manual",
      name: "",
      description: "",
      libraryIds: [],
      rules: undefined,
      rawSortConfig: {},
      showOnly: undefined,
      artwork: {},
      personal: { shared: false, inLibraryTabs: false },
    });
  });

  it.each([
    [undefined, undefined],
    [10_000_000, undefined],
    [250, 250],
  ])("clears a smart limit of %j to %j", (stored, limit) => {
    const view = SERVER_SCOPE.toView(
      adminCollectionFromV2(adminSmartCollection(storedQuery(stored)) as never),
    );
    expect(SERVER_SCOPE.toDraft(view, { kind: "manual" }).rules?.limit).toBe(limit);
  });

  it("keeps the stored kind over the requested one", () => {
    const view = PERSONAL_SCOPE.toView(
      collectionFromV2(personalSmartCollection(storedQuery(undefined)) as never),
    );
    expect(PERSONAL_SCOPE.toDraft(view, { kind: "manual" }).kind).toBe("smart");
  });
});

describe("isReadOnly", () => {
  const view = PERSONAL_SCOPE.toView(getCollectionOk as unknown as Collection);

  it.each([
    ["an unknown profile", undefined],
    ["no profile", null],
    ["another profile", "p-other"],
  ])("locks a personal collection for %s", (_label, profileId) => {
    expect(PERSONAL_SCOPE.isReadOnly(view, profileId)).toBe(true);
  });

  it("lets its creator change a personal collection", () => {
    expect(PERSONAL_SCOPE.isReadOnly(view, "p-owner")).toBe(false);
  });

  it("never locks a server collection; the admin routes decide who may change it", () => {
    const server = SERVER_SCOPE.toView(adminCollectionFromV2(getAdminCollectionOk as never));
    expect(SERVER_SCOPE.isReadOnly(server, undefined)).toBe(false);
  });
});

describe("create and update send the editor's bodies", () => {
  it("creates a server manual collection, then its poster and backdrop", async () => {
    const draft = savable(SERVER_SCOPE.toDraft(null, { kind: "manual", libraryId: 1 }));
    const result = await SERVER_SCOPE.create({
      ...draft,
      name: "Staff picks",
      artwork: {
        poster: { file: png("poster.png") },
        backdrop: { sourceUrl: " https://images.example/backdrop.png " },
      },
    });
    expect(result).toEqual({ id: "c1", warnings: [], failedArtwork: [] });
    expect(writes()).toEqual(goldens.adminManualCreate);
  });

  it("creates a server smart collection", async () => {
    const draft = savable(SERVER_SCOPE.toDraft(null, { kind: "smart", libraryId: 1 }));
    await SERVER_SCOPE.create({ ...draft, name: "New this month" });
    expect(writes()).toEqual(goldens.adminSmartCreate);
  });

  it("updates a server manual collection with the snapshot's ETag", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminCollection);
    const { draft, etag } = await loaded(SERVER_SCOPE);
    await expect(
      SERVER_SCOPE.update({ id: "c1", etag }, { ...draft, name: "Renamed" }),
    ).resolves.toEqual({ warnings: [], failedArtwork: [] });
    expect(writes()).toEqual(goldens.adminManualUpdate);
  });

  it.each([
    ["no limit", undefined],
    ["the server's no-limit sentinel", 10_000_000],
    ["a limit of 250", 250],
  ] as const)("saves a loaded server smart collection unchanged with %s", async (label, limit) => {
    v2Recorder.answer(
      "GET /api/v2/admin/collections/{id}",
      adminSmartCollection(storedQuery(limit)),
    );
    const { draft, etag } = await loaded(SERVER_SCOPE);
    await SERVER_SCOPE.update({ id: "c1", etag }, draft);
    expect(writes()).toEqual(goldens.adminSmartUnchanged[label]);
  });

  it.each([
    ["no limit", undefined],
    ["the server's no-limit sentinel", 10_000_000],
    ["a limit of 250", 250],
  ] as const)(
    "saves a loaded personal smart collection unchanged with %s",
    async (label, limit) => {
      v2Recorder.answer(
        "GET /api/v2/collections/{id}",
        personalSmartCollection(storedQuery(limit)),
      );
      const { draft, etag } = await loaded(PERSONAL_SCOPE);
      await PERSONAL_SCOPE.update({ id: "c1", etag }, draft);
      expect(writes()).toEqual(goldens.personalSmartUnchanged[label]);
    },
  );

  it("creates a personal smart collection, then uploads its poster", async () => {
    const draft = savable(PERSONAL_SCOPE.toDraft(null, { kind: "smart" }));
    const result = await PERSONAL_SCOPE.create({
      ...draft,
      name: "Comfort",
      artwork: { poster: { file: png("poster.png") } },
    });
    expect(result).toEqual({ id: "c1", warnings: [], failedArtwork: [] });
    expect(writes()).toEqual(goldens.personalSmartCreate);
  });

  it("creates a personal manual collection with a pasted poster URL in the POST body", async () => {
    const draft = savable(PERSONAL_SCOPE.toDraft(null, { kind: "manual" }));
    await PERSONAL_SCOPE.create({
      ...draft,
      name: "Rainy days",
      artwork: { poster: { sourceUrl: "https://images.example/poster.png" } },
    });
    expect(writes()).toEqual(goldens.personalManualCreate);
  });

  it("sends a personal manual collection's Show only filter, and never one for smart", async () => {
    const showOnly = {
      match: "all" as const,
      groups: [
        {
          match: "all" as const,
          rules: [
            { field: "watched", op: "is", value: false },
            { field: "type", op: "is", value: "movie" },
          ],
        },
      ],
    };
    const manual = savable(PERSONAL_SCOPE.toDraft(null, { kind: "manual" }));
    const smart = savable(PERSONAL_SCOPE.toDraft(null, { kind: "smart" }));
    await PERSONAL_SCOPE.create({ ...manual, name: "Unwatched movies", showOnly });
    await PERSONAL_SCOPE.create({ ...manual, name: "Everything" });
    await PERSONAL_SCOPE.create({ ...smart, name: "Comfort", showOnly });
    const [filtered, unfiltered, rules] = writes().map(
      (call) => call.body as Record<string, unknown>,
    );
    expect(filtered!.display_query_definition).toEqual(showOnly);
    expect(filtered).not.toHaveProperty("watch_filter");
    expect(unfiltered).not.toHaveProperty("display_query_definition");
    expect(rules).not.toHaveProperty("display_query_definition");
  });

  it("clears a saved Show only filter when it is set back to All", async () => {
    const showOnly = {
      match: "all" as const,
      groups: [{ match: "all" as const, rules: [{ field: "watched", op: "is", value: false }] }],
    };
    const first = await loaded(PERSONAL_SCOPE);
    const base = { ...first.draft, showOnly };
    await PERSONAL_SCOPE.update(
      { id: "c1", etag: first.etag },
      { ...base, showOnly: undefined },
      base,
    );
    const [cleared] = writes().map((call) => call.body as Record<string, unknown>);
    expect(cleared!.display_query_definition).toEqual({ match: "all", groups: [] });

    const second = await loaded(PERSONAL_SCOPE);
    await PERSONAL_SCOPE.update(
      { id: "c1", etag: second.etag },
      { ...second.draft, name: "Renamed" },
      second.draft,
    );
    const [untouched] = writes().map((call) => call.body as Record<string, unknown>);
    expect(untouched).not.toHaveProperty("display_query_definition");
  });

  it("updates a personal manual collection", async () => {
    const { draft, etag } = await loaded(PERSONAL_SCOPE);
    await PERSONAL_SCOPE.update({ id: "c1", etag }, { ...draft, name: "Renamed" });
    expect(writes()).toEqual(goldens.personalManualUpdate);
  });

  it("creates a server collection unpinned and never sends featured on a save", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", { ...adminCollection, featured: true });
    const { draft, etag } = await loaded(SERVER_SCOPE);
    await SERVER_SCOPE.update({ id: "c1", etag }, draft);
    await SERVER_SCOPE.create({ ...draft, name: "Copy" });
    const [patch, post] = writes();
    expect(patch!.body).not.toHaveProperty("featured");
    expect(post!.body).toHaveProperty("featured", false);
  });

  it("removes staged artwork after the save, and a new file in the slot wins", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminCollection);
    const { draft, etag } = await loaded(SERVER_SCOPE);
    await SERVER_SCOPE.update(
      { id: "c1", etag },
      {
        ...draft,
        artwork: { poster: { remove: true }, backdrop: { remove: true, file: png("b.png") } },
      },
    );
    expect(writes().map((call) => `${call.operation} ${JSON.stringify(call.query ?? {})}`)).toEqual(
      [
        "PATCH /api/v2/admin/collections/{id} {}",
        'DELETE /api/v2/admin/collections/{id}/image {"type":"poster"}',
        "PUT /api/v2/admin/collections/{id}/backdrop {}",
      ],
    );
  });

  it("removes a staged personal poster after the PATCH", async () => {
    const { draft, etag } = await loaded(PERSONAL_SCOPE);
    await PERSONAL_SCOPE.update(
      { id: "c1", etag },
      { ...draft, artwork: { poster: { remove: true } } },
    );
    expect(writes()).toEqual(goldens.personalStagedPosterRemoval);
  });

  it("reports a poster that failed after the collection saved as a warning", async () => {
    v2Recorder.answer("PUT /api/v2/collections/{id}/poster", () => {
      throw new Error("too large");
    });
    const draft = savable(PERSONAL_SCOPE.toDraft(null, { kind: "manual" }));
    await expect(
      PERSONAL_SCOPE.create({ ...draft, name: "X", artwork: { poster: { file: png("p.png") } } }),
    ).resolves.toEqual({ id: "c1", warnings: ["too large"], failedArtwork: ["poster"] });
  });

  it("answers a stale ETag with the precondition failure", async () => {
    const { draft, etag } = await loaded(PERSONAL_SCOPE);
    v2Recorder.bump("/api/v2/collections/c1");
    const error = await PERSONAL_SCOPE.update({ id: "c1", etag }, draft).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(V2ProblemError);
    expect(PERSONAL_SCOPE.errorMessage(error, "Failed")).toMatch(/changed while you were editing/);
    expect(SERVER_SCOPE.errorMessage(error, "Failed")).toMatch(/This collection changed/);
    expect(PERSONAL_SCOPE.errorMessage(new Error("boom"), "Failed")).toBe("boom");
    expect(PERSONAL_SCOPE.errorMessage("??", "Failed")).toBe("Failed");
  });
});

describe("remove, sync and preview", () => {
  it.each([
    [SERVER_SCOPE, "DELETE /api/v2/admin/collections/{id}", "/api/v2/admin/collections/c1"],
    [PERSONAL_SCOPE, "DELETE /api/v2/collections/{id}", "/api/v2/collections/c1"],
  ] as const)("removes with If-Match (%#)", async (scope, operation, path) => {
    v2Recorder.answer(operation, undefined);
    const etag = v2Recorder.etag(path);
    await scope.remove({ id: "c1", etag });
    expect(writes()).toEqual([{ operation, path, headers: { "If-Match": etag } }]);
  });

  it("refuses to remove without a usable ETag", async () => {
    await expect(PERSONAL_SCOPE.remove({ id: "c1", etag: "" })).rejects.toThrow(
      "version is unavailable",
    );
    expect(writes()).toEqual([]);
  });

  it.each([
    [SERVER_SCOPE, "POST /api/v2/admin/collections/{id}/sync"],
    [PERSONAL_SCOPE, "POST /api/v2/collections/{id}/sync"],
  ] as const)("syncs and reports how many titles matched (%#)", async (scope, operation) => {
    v2Recorder.answer(operation, {
      id: "run-1",
      collection_id: "c1",
      status: "warning",
      message: "1 unmatched",
      items_matched: 9,
      items_unmatched: 1,
      items_added: 0,
      items_removed: 0,
      created_at: "2026-01-02T03:04:05Z",
      started_at: "2026-01-02T03:04:05Z",
      completed_at: "2026-01-02T03:04:06Z",
    });
    await expect(scope.sync("c1")).resolves.toEqual({
      status: "warning",
      message: "1 unmatched",
      itemsMatched: 9,
      itemsUnmatched: 1,
    });
  });

  it.each([
    [SERVER_SCOPE, "POST /api/v2/admin/collections/preview"],
    [PERSONAL_SCOPE, "POST /api/v2/collections/preview"],
  ] as const)("previews rules with a limit (%#)", async (scope, operation) => {
    v2Recorder.answer(operation, {
      ...emptyPreview,
      items: [{ content_id: "movie:heat-1995", title: "Heat", type: "movie" }],
      total: 1,
    });
    const rules = storedQuery(undefined) as never;
    await expect(scope.preview(rules, 12)).resolves.toEqual({
      items: [{ content_id: "movie:heat-1995", title: "Heat", type: "movie" }],
      total: 1,
    });
    expect(v2Recorder.callsOf(operation)[0]!.body).toEqual({ query_definition: rules, limit: 12 });
  });
});

describe("invalidate", () => {
  it("refreshes a personal collection's own keys, the catalog and library tabs", async () => {
    const client = new QueryClient();
    const spy = vi.spyOn(client, "invalidateQueries");
    await PERSONAL_SCOPE.invalidate(client, "c1");
    expect(spy.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      ["collections"],
      ["catalog"],
      ["collections", "items", "c1"],
      ["libraryCollections"],
    ]);
  });

  it("refreshes the server collection keys, library tabs and the catalog", async () => {
    const client = new QueryClient();
    const spy = vi.spyOn(client, "invalidateQueries");
    await SERVER_SCOPE.invalidate(client);
    expect(spy.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      ["libraryCollections"],
      ["catalog"],
      ["admin", "collections"],
      ["admin", "collectionGroups"],
    ]);
  });

  // Add to Home follows a save in the editor; the Home rows pages must not
  // offer collections from before it.
  it("refreshes the collection options of both Home rows pages", async () => {
    const adminOptions = adminKeys.collections(undefined);
    const profileOwn = collectionKeys.list();
    const profileServer = libraryCollectionKeys.list(7);
    const stale = async (scope: CollectionScope<never>) => {
      const client = new QueryClient();
      for (const key of [adminOptions, profileOwn, profileServer]) client.setQueryData(key, {});
      await scope.invalidate(client, "c1");
      const invalidated = (key: readonly unknown[]) =>
        client.getQueryState(key)?.isInvalidated ?? false;
      return [invalidated(adminOptions), invalidated(profileOwn), invalidated(profileServer)];
    };
    // A profile reads server collections from the library tabs; admin rows offer no personal ones.
    expect(await stale(SERVER_SCOPE as CollectionScope<never>)).toEqual([true, false, true]);
    expect(await stale(PERSONAL_SCOPE as CollectionScope<never>)).toEqual([false, true, true]);
  });
});

describe("saved Synced lists", () => {
  it("reads what a server list follows into the draft", () => {
    const chart = SERVER_SCOPE.toView(
      adminCollectionFromV2({
        ...adminSyncedCollection("tmdb", {
          source_url: "tmdb://trending/movie/week",
          source_config: {
            mode: "tmdb_preset",
            preset: "trending",
            media_type: "movie",
            time_window: "week",
            limit: 40,
          },
        }),
        sync_schedule: "0 3 * * *",
      } as never),
    );
    expect(SERVER_SCOPE.toDraft(chart, { kind: "manual" }).list).toMatchObject({
      source: "tmdb_chart",
      link: "",
      chart: { preset: "trending", mediaType: "movie", timeWindow: "week" },
      limit: 40,
      schedule: "0 3 * * *",
    });
    const franchise = SERVER_SCOPE.toView(
      adminCollectionFromV2(
        adminSyncedCollection("tmdb", {
          source_url: "tmdb://collection/119",
          source_config: { mode: "tmdb_collection", collection_id: 119 },
        }) as never,
      ),
    );
    expect(SERVER_SCOPE.toDraft(franchise, { kind: "manual" }).list).toMatchObject({
      source: "tmdb_franchise",
      franchiseId: "119",
      limit: undefined,
      schedule: "",
    });
  });

  it("reads a personal list's schedule by its cadence name", () => {
    const view = PERSONAL_SCOPE.toView(
      collectionFromV2({
        ...personalSyncedCollection("mdblist", {
          source_url: "https://mdblist.com/lists/user/top-watched",
          source_config: { limit: 50 },
        }),
        sync_schedule: "17 4 * * 1",
        sync_cadence: "weekly",
      } as never),
    );
    expect(PERSONAL_SCOPE.toDraft(view, { kind: "manual" }).list).toMatchObject({
      source: "mdblist",
      link: "https://mdblist.com/lists/user/top-watched",
      limit: 50,
      schedule: "weekly",
    });
  });

  it("starts manual and smart drafts with no list", () => {
    expect(SERVER_SCOPE.toDraft(null, { kind: "manual", libraryId: 1 }).list).toBeUndefined();
  });
});

describe("collectionsInAdminScope", () => {
  it("uses the rendered board as the destructive scope for one library", () => {
    const allCollections = [
      { id: "all-only" },
      { id: "ungrouped" },
      { id: "grouped" },
    ] as LibraryCollection[];
    const grouped = { id: "grouped" } as LibraryCollection;
    const ungrouped = { id: "ungrouped" } as LibraryCollection;

    expect(
      collectionsInAdminScope(
        allCollections,
        {
          groups: [{ collections: [grouped] }, { collections: [grouped] }],
          ungrouped: [ungrouped],
        },
        7,
      ).map((collection) => collection.id),
    ).toEqual(["ungrouped", "grouped"]);
  });

  it("uses the unscoped collection list when all libraries are selected", () => {
    const allCollections = [{ id: "one" }, { id: "two" }] as LibraryCollection[];

    expect(collectionsInAdminScope(allCollections, undefined, null)).toEqual(allCollections);
  });
});
