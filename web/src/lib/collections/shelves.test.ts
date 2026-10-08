import { describe, expect, it } from "vitest";
import type { LibraryCollection, LibraryCollectionGroup } from "@/api/types";

import {
  UNGROUPED,
  applyCollectionMove,
  applyPin,
  applyShelfMove,
  boardShelves,
  planCollectionMove,
  planShelfMove,
  pinnedBand,
  shelfCountLine,
  shownCollections,
  type Shelf,
} from "./shelves";

function collection(id: string, extra: Partial<LibraryCollection> = {}): LibraryCollection {
  return {
    id,
    title: id,
    item_count: 0,
    updated_at: "2026-01-01T00:00:00Z",
    visibility: "visible",
    ...extra,
  } as LibraryCollection;
}

function group(
  id: string,
  sort_order: number,
  collections: LibraryCollection[],
  extra: Partial<LibraryCollectionGroup> = {},
) {
  return {
    id,
    library_id: 1,
    name: id,
    slug: id,
    kind: "regular",
    default_sort_mode: "manual",
    sort_order,
    collections,
    ...extra,
  } as LibraryCollectionGroup & { collections: LibraryCollection[] };
}

const board: Shelf[] = boardShelves(
  [
    group("studios", 0, [collection("a24"), collection("ghibli"), collection("criterion")]),
    group("mine", 2, [], { kind: "user_collections", name: "My collections" }),
    group("awards", 1, [collection("oscars")], { default_sort_mode: "name_asc" }),
  ],
  [collection("xmas"), collection("staff")],
  3,
);

describe("boardShelves", () => {
  it("lists shelves and No heading top to bottom, the way viewers see them", () => {
    expect(board.map((shelf) => [shelf.id, shelf.kind, shelf.name])).toEqual([
      ["studios", "regular", "studios"],
      ["awards", "regular", "awards"],
      ["mine", "user_collections", "My collections"],
      [UNGROUPED, "ungrouped", "No heading"],
    ]);
  });

  it("breaks a position tie by id, with No heading's sentinel among them", () => {
    const tied = boardShelves([group("b", 0, []), group("z", 0, [])], [], 0);
    expect(tied.map((shelf) => shelf.id)).toEqual(["b", UNGROUPED, "z"]);
  });
});

describe("shownCollections", () => {
  const shelf = (sortMode: Shelf["sortMode"]): Shelf => ({
    id: "s",
    kind: "regular",
    name: "s",
    sortMode,
    collections: [
      collection("b", { title: "Beta", item_count: 3, updated_at: "2026-03-01T00:00:00Z" }),
      collection("a", { title: "Alpha", item_count: 9, updated_at: "2026-01-01T00:00:00Z" }),
      collection("c", { title: "Gamma", item_count: 1, updated_at: "2026-05-01T00:00:00Z" }),
    ],
  });
  it.each([
    ["manual", ["b", "a", "c"]],
    ["name_asc", ["a", "b", "c"]],
    ["name_desc", ["c", "b", "a"]],
    ["recent", ["c", "b", "a"]],
    ["most_items", ["a", "b", "c"]],
  ] as const)("orders a %s shelf the way viewers see it", (mode, ids) => {
    expect(shownCollections(shelf(mode)).map((entry) => entry.id)).toEqual(ids);
  });

  it("orders names the way the server does, uppercase before lowercase", () => {
    const mixed: Shelf = {
      ...shelf("name_asc"),
      collections: [
        collection("t", { title: "tick, tick... BOOM!" }),
        collection("e", { title: "Élite" }),
        collection("z", { title: "Zodiac" }),
      ],
    };
    expect(shownCollections(mixed).map((entry) => entry.id)).toEqual(["z", "t", "e"]);
    expect(shownCollections({ ...mixed, sortMode: "name_desc" }).map((entry) => entry.id)).toEqual([
      "e",
      "t",
      "z",
    ]);
  });
});

describe("pinned collections", () => {
  const pinned = (id: string) => collection(id, { featured: true });
  const shelf = (sortMode: Shelf["sortMode"]): Shelf => ({
    id: "s",
    kind: "regular",
    name: "s",
    sortMode,
    collections: [collection("a"), pinned("p1"), collection("b"), pinned("p2")],
  });

  it("lead a Your order shelf in a band, each part in its stored order", () => {
    expect(shownCollections(shelf("manual")).map((entry) => entry.id)).toEqual([
      "p1",
      "p2",
      "a",
      "b",
    ]);
    expect(pinnedBand(shelf("manual")).map((entry) => entry.id)).toEqual(["p1", "p2"]);
  });

  it("lead No heading too, which viewers see in your order", () => {
    const loose = boardShelves([], [collection("x"), pinned("p")], 0)[0]!;
    expect(shownCollections(loose).map((entry) => entry.id)).toEqual(["p", "x"]);
  });

  it("change nothing on a shelf that sorts itself, which has no band", () => {
    expect(shownCollections(shelf("name_asc")).map((entry) => entry.id)).toEqual([
      "a",
      "b",
      "p1",
      "p2",
    ]);
    expect(pinnedBand(shelf("name_asc"))).toEqual([]);
  });

  it("show a pin or unpin at once, wherever the collection is", () => {
    const next = applyPin([shelf("manual")], "a", true);
    expect(next[0]!.collections.find((entry) => entry.id === "a")?.featured).toBe(true);
    expect(pinnedBand(next[0]!).map((entry) => entry.id)).toEqual(["a", "p1", "p2"]);
    expect(pinnedBand(applyPin(next, "p1", false)[0]!).map((entry) => entry.id)).toEqual([
      "a",
      "p2",
    ]);
  });
});

describe("shelfCountLine", () => {
  it("counts the collections and names an automatic order", () => {
    expect(shelfCountLine(board[0]!)).toBe("3 collections");
    expect(shelfCountLine(board[1]!)).toBe("1 collection, sorted by name");
    expect(shelfCountLine({ ...board[1]!, sortMode: "most_items" })).toBe(
      "1 collection, sorted by most titles",
    );
    expect(shelfCountLine({ ...board[1]!, sortMode: "recent" })).toBe(
      "1 collection, sorted by recently updated",
    );
  });
});

describe("planCollectionMove", () => {
  it("drops a collection on the card it lands on, in its own shelf, like the drag showed", () => {
    expect(planCollectionMove(board, "a24", "studios", "criterion")).toEqual({
      shelfId: "studios",
      orderedIds: ["ghibli", "criterion", "a24"],
    });
    expect(planCollectionMove(board, "criterion", "studios", "a24")).toEqual({
      shelfId: "studios",
      orderedIds: ["criterion", "a24", "ghibli"],
    });
  });

  it("puts a collection from another shelf before the card it lands on", () => {
    expect(planCollectionMove(board, "xmas", "studios", "ghibli")).toEqual({
      shelfId: "studios",
      orderedIds: ["a24", "xmas", "ghibli", "criterion"],
    });
  });

  it("adds a collection to the end of a shelf dropped on or picked from Move to shelf", () => {
    expect(planCollectionMove(board, "oscars", UNGROUPED, null)).toEqual({
      shelfId: UNGROUPED,
      orderedIds: ["xmas", "staff", "oscars"],
    });
  });

  it("changes nothing for a move to its own shelf, or within a shelf that sorts itself", () => {
    expect(planCollectionMove(board, "a24", "studios", null)).toBeNull();
    expect(planCollectionMove(board, "a24", "studios", "a24")).toBeNull();
    const sorted = boardShelves(
      [group("awards", 0, [collection("x"), collection("y")], { default_sort_mode: "name_asc" })],
      [],
      1,
    );
    expect(planCollectionMove(sorted, "x", "awards", "y")).toBeNull();
  });

  it("never puts a server collection on My collections", () => {
    expect(planCollectionMove(board, "xmas", "mine", null)).toBeNull();
  });
});

describe("planCollectionMove with a pinned band", () => {
  // Shown as p1, p2 | a, b: the band leads the shelf.
  const banded = boardShelves(
    [
      group("studios", 0, [
        collection("a"),
        collection("p1", { featured: true }),
        collection("b"),
        collection("p2", { featured: true }),
      ]),
      group("awards", 1, [collection("x"), collection("q", { featured: true })]),
      group("sorted", 2, [collection("n", { featured: true })], { default_sort_mode: "name_asc" }),
    ],
    [],
    3,
  );

  it("keeps a card dropped on the band right after it, and saves the order shown", () => {
    expect(planCollectionMove(banded, "b", "studios", "p1")).toEqual({
      shelfId: "studios",
      orderedIds: ["p1", "p2", "b", "a"],
    });
  });

  it("keeps a pinned card in the band, at its end when dropped below it", () => {
    expect(planCollectionMove(banded, "p1", "studios", "b")).toEqual({
      shelfId: "studios",
      orderedIds: ["p2", "p1", "a", "b"],
    });
    expect(planCollectionMove(banded, "p2", "studios", "p1")).toEqual({
      shelfId: "studios",
      orderedIds: ["p2", "p1", "a", "b"],
    });
  });

  it("changes nothing when the band puts a card back where it was", () => {
    expect(planCollectionMove(banded, "a", "studios", "p2")).toBeNull();
  });

  it("puts a card from another shelf after the band, and a pinned one at the band's end", () => {
    expect(planCollectionMove(banded, "x", "studios", "p1")).toEqual({
      shelfId: "studios",
      orderedIds: ["p1", "p2", "x", "a", "b"],
    });
    expect(planCollectionMove(banded, "q", "studios", "a")).toEqual({
      shelfId: "studios",
      orderedIds: ["p1", "p2", "q", "a", "b"],
    });
    expect(planCollectionMove(banded, "n", "studios", null)).toEqual({
      shelfId: "studios",
      orderedIds: ["p1", "p2", "n", "a", "b"],
    });
  });

  it("leaves a shelf that sorts itself in its stored order", () => {
    expect(planCollectionMove(banded, "x", "sorted", null)).toEqual({
      shelfId: "sorted",
      orderedIds: ["n", "x"],
    });
  });
});

describe("applyCollectionMove", () => {
  it("shows the board as it will be once the move saves", () => {
    const next = applyCollectionMove(board, "xmas", {
      shelfId: "studios",
      orderedIds: ["a24", "xmas", "ghibli", "criterion"],
    });
    expect(next.map((shelf) => shelf.collections.map((entry) => entry.id))).toEqual([
      ["a24", "xmas", "ghibli", "criterion"],
      ["oscars"],
      [],
      ["staff"],
    ]);
  });
});

describe("planShelfMove", () => {
  it("moves a shelf to the top or the bottom, No heading included", () => {
    expect(planShelfMove(board, "mine", 0)).toEqual(["mine", "studios", "awards", UNGROUPED]);
    expect(planShelfMove(board, "studios", Infinity)).toEqual([
      "awards",
      "mine",
      UNGROUPED,
      "studios",
    ]);
    expect(planShelfMove(board, "studios", 0)).toBeNull();
  });

  it("reorders the board's shelves", () => {
    expect(
      applyShelfMove(board, ["mine", "studios", "awards", UNGROUPED]).map((s) => s.id),
    ).toEqual(["mine", "studios", "awards", UNGROUPED]);
  });
});
