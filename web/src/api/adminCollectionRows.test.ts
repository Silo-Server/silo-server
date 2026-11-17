import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/v2/schema";
import type { CollectionRow } from "@/lib/collections/rows";
import { deleteAdminCollectionRows } from "./adminCollectionRows";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));

// Writing a row moves the collection's revision; the run reads its new ETag.
const collectionReads = vi.hoisted(() => vi.fn());
vi.mock("@/api/adminCollections", () => ({ fetchAdminCollectionSnapshot: collectionReads }));
const read = (etag: string, title = "Marvel") =>
  Promise.resolve({ etag, collection: { id: "marvel", title, poster_url: `/signed?${etag}` } });

function section(
  id: string,
  sectionType: string,
  config: Record<string, unknown>,
): components["schemas"]["AdminSection"] {
  return {
    id,
    scope: "home",
    library_id: null,
    position: 0,
    section_type: sectionType,
    title: id,
    enabled: true,
    featured: false,
    web_only: false,
    item_limit: 20,
    config,
    created_at: "2026-11-16T00:00:00.000Z",
    updated_at: "2026-11-16T00:00:00.000Z",
  };
}

function row(id: string, listCount?: number): CollectionRow {
  return {
    id,
    page: { kind: "home" },
    title: id,
    enabled: true,
    position: 1,
    pageRowCount: 3,
    ...(listCount ? { listCount } : {}),
  };
}

const list = (...ids: string[]) => ({
  collections: ids.map((id) => ({ library_collection_id: id })),
});

beforeEach(() => {
  request.mockReset();
  collectionReads.mockReset();
  collectionReads.mockReturnValueOnce(read('"before"')).mockReturnValueOnce(read('"after-rows"'));
});

describe("deleteAdminCollectionRows", () => {
  it("takes the collection out of a row of collections that lists others and deletes the rest", async () => {
    const stored: Record<string, components["schemas"]["AdminSection"]> = {
      single: section("single", "collection", { library_collection_id: "marvel" }),
      shelf: section("shelf", "collection_posters", list("pixar", "marvel", "ghibli")),
      only: section("only", "collection_posters", list("marvel")),
    };
    request.mockImplementation(
      (
        route: string,
        options: { path: { id: string }; onResponse?: (response: Response) => void },
      ) => {
        if (route === "GET /api/v2/admin/sections/{id}") {
          options.onResponse?.(new Response(null, { headers: { ETag: `"${options.path.id}"` } }));
          return Promise.resolve(stored[options.path.id]);
        }
        return Promise.resolve(stored[options.path.id]);
      },
    );

    const result = await deleteAdminCollectionRows(
      "marvel",
      [row("single"), row("shelf", 3), row("only", 1)],
      '"before"',
    );

    expect(result.remaining).toEqual([]);
    expect(result.etag).toBe('"after-rows"');
    const writes = request.mock.calls
      .filter(([route]) => !String(route).startsWith("GET"))
      .map(([route, options]) => [route, options.path.id, options.body?.config]);
    expect(writes).toEqual([
      ["DELETE /api/v2/admin/sections/{id}", "single", undefined],
      ["PATCH /api/v2/admin/sections/{id}", "shelf", list("pixar", "ghibli")],
      ["DELETE /api/v2/admin/sections/{id}", "only", undefined],
    ]);
  });

  it("keeps the old ETag when the collection changed during the run", async () => {
    const shelf = section("shelf", "collection_posters", list("pixar", "marvel"));
    request.mockImplementation(
      (_route: string, options: { onResponse?: (response: Response) => void }) => {
        options.onResponse?.(new Response(null, { headers: { ETag: '"shelf"' } }));
        return Promise.resolve(shelf);
      },
    );
    collectionReads.mockReset();
    collectionReads
      .mockReturnValueOnce(read('"before"'))
      .mockReturnValueOnce(read('"after-rows"', "Marvel, renamed"));

    const result = await deleteAdminCollectionRows("marvel", [row("shelf", 2)], '"before"');

    expect(result).toEqual({ remaining: [] });
  });

  it("writes no row when the collection changed before the run", async () => {
    collectionReads.mockReset();
    collectionReads.mockReturnValueOnce(read('"edited"'));
    const rows = [row("shelf", 2)];

    const result = await deleteAdminCollectionRows("marvel", rows, '"before"');

    expect(result.remaining).toEqual(rows);
    expect(request).not.toHaveBeenCalled();
  });
});
