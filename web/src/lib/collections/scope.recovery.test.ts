import { describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../../contracts/api/v2/fixtures/get_collection_ok.json";
import { adminCollectionFromV2 } from "@/api/adminCollections";
import { collectionFromV2 } from "@/api/personalCollections";
import type { QueryDefinition } from "@/api/types";
import {
  adminSmartCollection,
  adminSyncedCollection,
  personalSmartCollection,
  personalSyncedCollection,
} from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import {
  PERSONAL_SCOPE,
  SERVER_SCOPE,
  type CollectionScope,
  type ManualOrSmartDraft,
  type WireCollection,
} from "./scope";

import { mergeDraft } from "./draft";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
installV2Recorder();
const writes = () => v2Recorder.writes();

function storedQuery(limit: number | undefined): QueryDefinition & Record<string, unknown> {
  return {
    library_ids: [1],
    match: "all",
    groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "Comedy" }] }],
    sort: { field: "added_at", order: "desc" },
    ...(limit === undefined ? {} : { limit }),
  };
}

async function loaded<Raw extends WireCollection>(scope: CollectionScope<Raw>, id = "c1") {
  const snapshot = await scope.fetchSnapshot(id);
  v2Recorder.calls = [];
  const draft = scope.toDraft(snapshot.view, { kind: "manual" });
  if (draft.kind === "synced") throw new Error("expected manual or smart draft");
  return { draft: draft as ManualOrSmartDraft, etag: snapshot.etag };
}

describe("legacy definition metadata recovery", () => {
  const legacyRules = {
    ...storedQuery(undefined),
    groups: Array.from({ length: 17 }, () => storedQuery(undefined).groups[0]!),
  };

  it("renames a personal smart collection without resubmitting unchanged admission fields", async () => {
    v2Recorder.answer("GET /api/v2/collections/{id}", personalSmartCollection(legacyRules));
    const { draft, etag } = await loaded(PERSONAL_SCOPE);
    expect(draft.rules?.groups).toHaveLength(17);
    await PERSONAL_SCOPE.update({ id: "c1", etag }, { ...draft, name: "Renamed" }, draft);
    const body = writes()[0]!.body as Record<string, unknown>;
    expect(body.name).toBe("Renamed");
    expect(body).not.toHaveProperty("query_definition");
    expect(body).not.toHaveProperty("display_query_definition");
    expect(body).not.toHaveProperty("library_ids");
    expect(draft.rules?.groups).toHaveLength(17);
  });

  it("renames a server smart collection without resubmitting unchanged admission fields", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminSmartCollection(legacyRules));
    const { draft, etag } = await loaded(SERVER_SCOPE);
    expect(draft.rules?.groups).toHaveLength(17);
    await SERVER_SCOPE.update({ id: "c1", etag }, { ...draft, name: "Renamed" }, draft);
    const body = writes()[0]!.body as Record<string, unknown>;
    expect(body.title).toBe("Renamed");
    expect(body).not.toHaveProperty("query_definition");
    expect(body).not.toHaveProperty("collection_type");
    expect(body).not.toHaveProperty("library_ids");
    expect(draft.rules?.groups).toHaveLength(17);
  });

  it("sends a replacement personal definition and changed library scope", async () => {
    v2Recorder.answer("GET /api/v2/collections/{id}", personalSmartCollection(legacyRules));
    const { draft, etag } = await loaded(PERSONAL_SCOPE);
    await PERSONAL_SCOPE.update(
      { id: "c1", etag },
      { ...draft, rules: storedQuery(undefined), libraryIds: [2] },
      draft,
    );
    const body = writes()[0]!.body as Record<string, unknown>;
    expect(body.query_definition).toMatchObject({
      groups: storedQuery(undefined).groups,
      library_ids: [2],
    });
    expect(draft.rules?.groups).toHaveLength(17);
  });

  it("sends a replacement server definition and changed library scope", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminSmartCollection(legacyRules));
    const { draft, etag } = await loaded(SERVER_SCOPE);
    await SERVER_SCOPE.update(
      { id: "c1", etag },
      { ...draft, rules: storedQuery(undefined), libraryIds: [2] },
      draft,
    );
    const body = writes()[0]!.body as Record<string, unknown>;
    expect(body.query_definition).toMatchObject({
      groups: storedQuery(undefined).groups,
      library_ids: [2],
    });
    expect(body.library_ids).toEqual(["2"]);
    expect(body).not.toHaveProperty("collection_type");
    expect(draft.rules?.groups).toHaveLength(17);
  });

  it("keeps unchanged legacy personal display filters out of a rename", async () => {
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...getCollectionOk,
      display_query_definition: legacyRules,
    });
    const { draft, etag } = await loaded(PERSONAL_SCOPE);
    await PERSONAL_SCOPE.update({ id: "c1", etag }, { ...draft, name: "Renamed" }, draft);
    expect(writes()[0]!.body).not.toHaveProperty("display_query_definition");
    expect(draft.showOnly?.groups).toHaveLength(17);
  });

  it("sends a changed personal display filter", async () => {
    const { draft, etag } = await loaded(PERSONAL_SCOPE);
    const showOnly = storedQuery(undefined);
    await PERSONAL_SCOPE.update({ id: "c1", etag }, { ...draft, showOnly }, draft);
    expect(writes()[0]!.body).toHaveProperty("display_query_definition", showOnly);
  });

  it.each([PERSONAL_SCOPE, SERVER_SCOPE] as const)(
    "sends a smart definition when only its library scope changes (%s)",
    async (scope) => {
      v2Recorder.answer(
        scope.kind === "personal"
          ? "GET /api/v2/collections/{id}"
          : "GET /api/v2/admin/collections/{id}",
        scope.kind === "personal"
          ? personalSmartCollection(storedQuery(undefined))
          : adminSmartCollection(storedQuery(undefined)),
      );
      const { draft, etag } = await loaded(scope as CollectionScope<WireCollection>);
      const smart = { ...draft, kind: "smart" as const, rules: storedQuery(undefined) };
      await scope.update({ id: "c1", etag }, { ...smart, libraryIds: [2] }, smart);
      expect(writes()[0]!.body).toHaveProperty("query_definition.library_ids", [2]);
    },
  );

  it.each([PERSONAL_SCOPE, SERVER_SCOPE] as const)(
    "retains the complete definition without a loaded base (%s)",
    async (scope) => {
      v2Recorder.answer(
        scope.kind === "personal"
          ? "GET /api/v2/collections/{id}"
          : "GET /api/v2/admin/collections/{id}",
        scope.kind === "personal"
          ? personalSmartCollection(legacyRules)
          : adminSmartCollection(legacyRules),
      );
      const { draft, etag } = await loaded(scope as CollectionScope<WireCollection>);
      await scope.update({ id: "c1", etag }, { ...draft, kind: "smart", rules: legacyRules });
      expect(writes()[0]!.body).toHaveProperty("query_definition.groups", legacyRules.groups);
    },
  );

  it("sends an intentionally changed legacy definition without trimming it", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminSmartCollection(legacyRules));
    const { draft, etag } = await loaded(SERVER_SCOPE);
    const rules = { ...legacyRules, match: "any" as const };
    await SERVER_SCOPE.update({ id: "c1", etag }, { ...draft, rules }, draft);
    expect(writes()[0]!.body).toHaveProperty("query_definition.groups", rules.groups);
    expect(writes()[0]!.body).toHaveProperty("query_definition.match", "any");
  });

  it("sends a changed server synced source type", async () => {
    const view = SERVER_SCOPE.toView(
      adminCollectionFromV2(
        adminSyncedCollection("mdblist", {
          source_url: "https://mdblist.com/lists/synthetic/list",
          source_config: { mode: "mdblist_json" },
        }) as never,
      ),
    );
    const draft = SERVER_SCOPE.toDraft(view, { kind: "synced" });
    if (draft.kind !== "synced" || !draft.list) throw new Error("missing synced draft");
    await SERVER_SCOPE.update(
      { id: "c1", etag: v2Recorder.etag("/api/v2/admin/collections/c1") },
      {
        ...draft,
        kind: "synced",
        list: { ...draft.list, source: "tmdb_list", link: "https://www.themoviedb.org/list/123" },
      },
      draft,
    );
    expect(writes()[0]!.body).toHaveProperty("collection_type", "tmdb");
    expect(writes()[0]!.body).not.toHaveProperty("library_ids");
  });

  it("preserves a personal synced library clear", async () => {
    const view = PERSONAL_SCOPE.toView(
      collectionFromV2({
        ...personalSyncedCollection("mdblist", {
          source_url: "https://mdblist.com/lists/synthetic/list",
          source_config: { mode: "mdblist_json", library_ids: [1] },
        }),
      } as never),
    );
    const draft = PERSONAL_SCOPE.toDraft(view, { kind: "synced" });
    if (draft.kind !== "synced" || !draft.list) throw new Error("missing synced draft");
    await PERSONAL_SCOPE.update(
      { id: "c1", etag: v2Recorder.etag("/api/v2/collections/c1") },
      { ...draft, kind: "synced", list: draft.list, libraryIds: [] },
      draft,
    );
    expect(writes()[0]!.body).toHaveProperty("library_ids", []);
  });

  it.each([PERSONAL_SCOPE, SERVER_SCOPE] as const)(
    "compares a retry against the new merged base (%s)",
    async (scope) => {
      const operation =
        scope.kind === "personal"
          ? "GET /api/v2/collections/{id}"
          : "GET /api/v2/admin/collections/{id}";
      const answer = scope.kind === "personal" ? personalSmartCollection : adminSmartCollection;
      v2Recorder.answer(operation, answer(legacyRules));
      const { draft: base, etag } = await loaded(scope as CollectionScope<WireCollection>);
      const mine = { ...base, name: "Renamed" };
      v2Recorder.bump(
        scope.kind === "personal" ? "/api/v2/collections/c1" : "/api/v2/admin/collections/c1",
      );
      await expect(scope.update({ id: "c1", etag }, mine, base)).rejects.toMatchObject({
        status: 412,
      });
      const newerRules = {
        ...legacyRules,
        groups: [...legacyRules.groups, legacyRules.groups[0]!],
      };
      v2Recorder.answer(operation, answer(newerRules));
      const fresh = await loaded(scope as CollectionScope<WireCollection>);
      const merged = mergeDraft(base, mine, fresh.draft);
      expect(merged.conflicts).toEqual([]);
      await scope.update(
        { id: "c1", etag: fresh.etag },
        { ...merged.draft, kind: "smart" },
        merged.base,
      );
      expect(writes()[0]!.body).not.toHaveProperty("query_definition");
      expect(merged.draft.rules?.groups).toHaveLength(18);
    },
  );
});
