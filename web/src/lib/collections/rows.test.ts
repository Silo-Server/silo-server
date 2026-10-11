import { describe, expect, it } from "vitest";

import { readRowLinks, safeReturnPath } from "@/lib/homeRows/rowLinks";

import { PERSONAL_SCOPE, SERVER_SCOPE } from "./scope";
import {
  addRowPath,
  addToMyHomePath,
  matchedLibraries,
  onRowsLine,
  openRowPath,
  profileCollectionRows,
  rowMeta,
  rowPages,
  rowPlaceInSentence,
  rowPlaces,
  type CollectionRow,
} from "./rows";

const NAMES = new Map([
  [1, "Movies"],
  [2, "Kids"],
  [3, "4K Movies"],
]);

function row(overrides: Partial<CollectionRow>): CollectionRow {
  return {
    id: "s1",
    page: { kind: "home" },
    title: "Studio Ghibli",
    enabled: true,
    position: 6,
    pageRowCount: 9,
    ...overrides,
  };
}

function paramsOf(path: string) {
  return new URL(path, "https://silo.test").searchParams;
}

describe("collection rows", () => {
  it("says where each row is and its place on the page", () => {
    expect(rowMeta(row({}), NAMES)).toBe("Home · row 6 of 9");
    expect(
      rowMeta(
        row({ page: { kind: "library", libraryId: 2 }, position: 3, pageRowCount: 7 }),
        NAMES,
      ),
    ).toBe("Kids page · row 3 of 7");
    expect(rowMeta(row({ enabled: false }), NAMES)).toBe("Home · row 6 of 9 · Turned off");
  });

  it("names the pages viewers see it on, leaving turned-off rows out", () => {
    const rows = [
      row({ id: "a" }),
      row({ id: "b", page: { kind: "library", libraryId: 2 } }),
      row({ id: "c", page: { kind: "library", libraryId: 3 }, enabled: false }),
    ];
    expect(onRowsLine(rows, NAMES)).toBe("On Home and the Kids page");
    expect(rowPlaces(rows, NAMES)).toBe("Home and the Kids and 4K Movies pages");
    expect(onRowsLine([row({ enabled: false })], NAMES)).toBeNull();
    expect(onRowsLine([], NAMES)).toBeNull();
  });

  it("offers the collection's own libraries first, then the others", () => {
    const all = [
      { id: 1, name: "Movies" },
      { id: 2, name: "Kids" },
      { id: 3, name: "4K Movies" },
    ];
    expect(rowPages([{ id: 2, name: "Kids" }], all)).toEqual({
      bound: [{ id: 2, name: "Kids" }],
      others: [
        { id: 1, name: "Movies" },
        { id: 3, name: "4K Movies" },
      ],
    });
  });

  it("links into admin Home rows on the right page, with a return the Home rows page accepts", () => {
    for (const libraryId of [null, 7]) {
      const editor = SERVER_SCOPE.paths.edit("c 1/x", { libraryId });
      const path = addRowPath("c 1/x", { kind: "library", libraryId: 2 }, editor);
      expect(path.startsWith("/admin/sections?")).toBe(true);
      const params = paramsOf(path);
      expect(params.get("page")).toBe("2");
      expect(params.get("add")).toBe("collection:library:c 1/x");
      expect(safeReturnPath(params.get("return"))).toBe(editor);
      expect(readRowLinks(params, "admin")).toEqual({
        add: { source: "library", id: "c 1/x" },
        edit: null,
        returnTo: editor,
      });
    }
    const home = paramsOf(addRowPath("c1", { kind: "home" }));
    expect(home.get("page")).toBe("home");
    expect(home.has("return")).toBe(false);
  });

  it("opens a row in Edit row on its page", () => {
    const params = paramsOf(openRowPath({ id: "s9", page: { kind: "library", libraryId: 2 } }));
    expect(params.get("page")).toBe("2");
    expect(params.get("edit")).toBe("s9");
  });

  it("names this profile's own pages as yours", () => {
    const mine = (overrides: Partial<CollectionRow>) => row({ surface: "profile", ...overrides });
    expect(rowMeta(mine({ position: 4, pageRowCount: 8 }), NAMES)).toBe("My Home · row 4 of 8");
    expect(rowMeta(mine({ page: { kind: "library", libraryId: 2 }, enabled: false }), NAMES)).toBe(
      "My Kids page · row 6 of 9 · Hidden",
    );
    expect(
      onRowsLine(
        [mine({ id: "a" }), mine({ id: "b", page: { kind: "library", libraryId: 2 } })],
        NAMES,
      ),
    ).toBe("On my Home and my Kids page");
    expect(
      rowPlaces(
        [
          mine({ page: { kind: "library", libraryId: 1 } }),
          mine({ page: { kind: "library", libraryId: 2 } }),
        ],
        NAMES,
      ),
    ).toBe("my Movies and Kids pages");
    expect(rowPlaceInSentence({ kind: "home" }, NAMES, "profile")).toBe("my Home");
    expect(rowPlaceInSentence({ kind: "library", libraryId: 2 }, NAMES)).toBe("the Kids page");
  });

  it("finds a personal collection's rows on each page, placed by rank with hidden rows counted", () => {
    const section = (id: string, config?: Record<string, unknown>, hidden = false) => ({
      id,
      section_type: config ? "collection" : "continue_watching",
      title: id,
      featured: false,
      item_limit: 20,
      hidden,
      is_custom: Boolean(config),
      customized: false,
      position: 99,
      config,
    });
    const rows = profileCollectionRows("c1", [
      {
        page: { kind: "home" },
        sections: [
          section("s1"),
          section("other", { user_collection_id: "c2" }),
          section("server", { library_collection_id: "c1" }),
          section("mine", { user_collection_id: "c1" }),
        ],
      },
      {
        page: { kind: "library", libraryId: 2 },
        sections: [section("kids", { user_collection_id: "c1" }, true), section("s2")],
      },
    ]);
    expect(rows).toEqual([
      {
        id: "mine",
        page: { kind: "home" },
        surface: "profile",
        title: "mine",
        enabled: true,
        position: 4,
        pageRowCount: 4,
      },
      {
        id: "kids",
        page: { kind: "library", libraryId: 2 },
        surface: "profile",
        title: "kids",
        enabled: false,
        position: 1,
        pageRowCount: 2,
      },
    ]);
  });

  it("links into your Home Screen on the right page, with a return the page accepts", () => {
    const editor = PERSONAL_SCOPE.paths.edit("c 1/x");
    const path = addToMyHomePath({ source: "user", id: "c 1/x" }, { kind: "home" }, editor);
    expect(path.startsWith("/settings/home-screen?")).toBe(true);
    const params = paramsOf(path);
    expect(params.get("page")).toBe("home");
    expect(safeReturnPath(params.get("return"))).toBe(editor);
    expect(readRowLinks(params, "profile")).toEqual({
      add: { source: "user", id: "c 1/x" },
      edit: null,
      returnTo: editor,
    });
    const server = paramsOf(
      addToMyHomePath({ source: "library", id: "c1" }, { kind: "library", libraryId: 2 }),
    );
    expect(server.get("add")).toBe("collection:library:c1");
    expect(server.get("page")).toBe("2");
    expect(server.has("return")).toBe(false);
    expect(
      paramsOf(openRowPath({ id: "u9", page: { kind: "home" }, surface: "profile" })).get("edit"),
    ).toBe("u9");
    expect(
      openRowPath({ id: "u9", page: { kind: "home" }, surface: "profile" }).startsWith(
        "/settings/home-screen?",
      ),
    ).toBe(true);
  });

  it("offers a personal collection the libraries it matches, or every library when it names none", () => {
    const all = [
      { id: 1, name: "Movies" },
      { id: 2, name: "Kids" },
    ];
    expect(matchedLibraries(all, [2, 9])).toEqual([{ id: 2, name: "Kids" }]);
    expect(matchedLibraries(all, [])).toEqual(all);
  });
});
