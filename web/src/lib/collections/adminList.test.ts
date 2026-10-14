import { describe, expect, it } from "vitest";

import type { LibraryCollection } from "@/api/types";

import {
  countByLibrary,
  filterAdminCollections,
  readAdminListState,
  rowTag,
  showOnTabsLabel,
  writeAdminListState,
  type AdminListState,
} from "./adminList";

function collection(title: string, overrides: Partial<LibraryCollection> = {}): LibraryCollection {
  return {
    id: title.toLowerCase().replace(/\s+/g, "-"),
    title,
    collection_type: "manual",
    library_id: 1,
    library_ids: [1],
    visibility: "visible",
    last_sync_status: "idle",
    item_count: 0,
    ...overrides,
  } as LibraryCollection;
}

const STATE: AdminListState = { view: "list", libraryId: null, kind: "all", q: "", failed: false };

describe("readAdminListState", () => {
  it("opens the list of every library by default", () => {
    expect(readAdminListState(new URLSearchParams())).toEqual(STATE);
  });

  it("reads the view, library, type, search and failed filter", () => {
    expect(
      readAdminListState(new URLSearchParams("view=list&libraryId=4&type=synced&q=best&failed=1")),
    ).toEqual({ view: "list", libraryId: 4, kind: "synced", q: "best", failed: true });
  });

  it("opens Arrange for a bare library link, the form older pages and editors use", () => {
    expect(readAdminListState(new URLSearchParams("libraryId=4")).view).toBe("arrange");
    expect(readAdminListState(new URLSearchParams("libraryId=4&view=list")).view).toBe("list");
  });

  it("ignores values it doesn't know", () => {
    expect(
      readAdminListState(new URLSearchParams("view=grid&libraryId=-2&type=trakt&failed=yes")),
    ).toEqual(STATE);
  });
});

describe("writeAdminListState", () => {
  it("round-trips every filter", () => {
    const state: AdminListState = {
      view: "list",
      libraryId: 7,
      kind: "smart",
      q: "kids",
      failed: true,
    };
    const params = writeAdminListState(new URLSearchParams(), state);
    expect(params.toString()).toBe("view=list&libraryId=7&type=smart&q=kids&failed=1");
    expect(readAdminListState(params)).toEqual(state);
  });

  it("names the List view whenever a library is set, so it never reads as an old Arrange link", () => {
    expect(writeAdminListState(new URLSearchParams(), STATE).toString()).toBe("");
    expect(writeAdminListState(new URLSearchParams(), { ...STATE, libraryId: 2 }).toString()).toBe(
      "view=list&libraryId=2",
    );
  });

  it("keeps parameters it doesn't own", () => {
    expect(
      writeAdminListState(new URLSearchParams("tab=x&q=old"), { ...STATE, view: "arrange" }).get(
        "tab",
      ),
    ).toBe("x");
  });
});

describe("filterAdminCollections", () => {
  const all = [
    collection("Studio Ghibli", { library_ids: [1, 2] }),
    collection("Best Picture Winners", { collection_type: "mdblist", library_ids: [1] }),
    collection("IMDb Top 250 Shows", {
      collection_type: "tmdb",
      library_ids: [3],
      last_sync_status: "failed",
    }),
    collection("A24 After Dark", { collection_type: "smart", library_ids: [1] }),
  ];
  const titles = (state: Partial<AdminListState>) =>
    filterAdminCollections(all, { ...STATE, ...state }).map((entry) => entry.title);

  it("lists every collection by name", () => {
    expect(titles({})).toEqual([
      "A24 After Dark",
      "Best Picture Winners",
      "IMDb Top 250 Shows",
      "Studio Ghibli",
    ]);
  });

  it("keeps the collections of one library", () => {
    expect(titles({ libraryId: 2 })).toEqual(["Studio Ghibli"]);
  });

  it("filters by type, with every list source counting as a synced list", () => {
    expect(titles({ kind: "synced" })).toEqual(["Best Picture Winners", "IMDb Top 250 Shows"]);
    expect(titles({ kind: "smart" })).toEqual(["A24 After Dark"]);
  });

  it("searches names without minding case", () => {
    expect(titles({ q: "  GHIB " })).toEqual(["Studio Ghibli"]);
  });

  it("keeps only lists whose last sync failed", () => {
    expect(titles({ failed: true })).toEqual(["IMDb Top 250 Shows"]);
  });
});

describe("countByLibrary", () => {
  it("counts a collection once in each of its libraries", () => {
    const counts = countByLibrary([
      collection("One", { library_ids: [1, 2] }),
      collection("Two", { library_ids: [], library_id: 2 }),
    ]);
    expect(counts.get(1)).toBe(1);
    expect(counts.get(2)).toBe(2);
  });
});

describe("rowTag", () => {
  it("shows at most one tag, and a hidden collection says so before On Home", () => {
    expect(rowTag(collection("A"))).toBeNull();
    expect(rowTag(collection("A", { home_row_count: 2 }))).toBe("home");
    expect(rowTag(collection("A", { visibility: "hidden" }))).toBe("hidden");
    expect(rowTag(collection("A", { visibility: "hidden", home_row_count: 1 }))).toBe("hidden");
  });
});

describe("showOnTabsLabel", () => {
  it("names the effect and every library's tab", () => {
    expect(showOnTabsLabel("Studio Ghibli", ["Movies", "Kids"])).toBe(
      "Show Studio Ghibli on the Movies and Kids Collections tabs",
    );
    expect(showOnTabsLabel("Studio Ghibli", ["Movies"])).toBe(
      "Show Studio Ghibli on the Movies Collections tab",
    );
    expect(showOnTabsLabel("Studio Ghibli", [])).toBe("Show Studio Ghibli on its Collections tabs");
  });
});
