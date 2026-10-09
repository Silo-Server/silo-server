/**
 * Arrange: one library's shelves. The board renders its shelves, My
 * collections and No heading with a viewer preview; keyboard and menu moves
 * send the existing order and group requests, guarded by an ETag; a move
 * shows at once and goes back when it fails; phones move cards from a sheet.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getAdminCollectionOk from "../../../../../contracts/api/v2/fixtures/get_admin_collection_ok.json";
import { adminCollectionFromV2 } from "@/api/adminCollections";
import type { LibraryCollection, LibraryCollectionGroup } from "@/api/types";
import { stubPhone } from "@/components/homeRows/phoneLayout.test-support";
import { MOVE_FAILED, ORDER_CHANGED, PIN_LABEL } from "@/lib/collections/copy";
import { adminCapabilities } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import { GroupsBoard } from "./GroupsBoard";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

function collection(
  id: string,
  title: string,
  visibility: LibraryCollection["visibility"] = "visible",
  featured = false,
): LibraryCollection {
  return adminCollectionFromV2({ ...getAdminCollectionOk, id, title, visibility, featured });
}

type Group = LibraryCollectionGroup & { collections: LibraryCollection[] };

const franchises = {
  id: "g1",
  library_id: 1,
  name: "Franchises",
  slug: "franchises",
  kind: "regular",
  default_sort_mode: "manual",
  sort_order: 0,
  collections: [collection("f1", "Alien")],
} as Group;

const mine = {
  id: "mine",
  library_id: 1,
  name: "My collections",
  slug: "my-collections",
  kind: "user_collections",
  default_sort_mode: "manual",
  sort_order: 1,
  collections: [],
} as Group;

const ungrouped = [
  collection("a", "Staff picks"),
  collection("b", "New this month"),
  collection("c", "Oscar winners", "hidden"),
];

const ORDERS: Record<string, string[]> = { g1: ["f1"], mine: [], ungrouped: ["a", "b", "c"] };

function groupBody(group: Group) {
  const { collections: _collections, ...rest } = group;
  return { ...rest, library_id: "1" };
}

// jsdom lays nothing out. Give each card a place in one column inside the
// viewport, and each shelf a box much taller than a card, so the keyboard
// sensor's collision detection sees the board the way a browser would.
const rects = new Map<Element, DOMRect>();
function place(element: Element, top: number, height: number) {
  rects.set(element, new DOMRect(0, top, 600, height));
}

beforeEach(() => {
  HTMLElement.prototype.scrollIntoView = () => {};
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    return rects.get(this) ?? new DOMRect(0, 0, 0, 0);
  });
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
  v2Recorder.answer("GET /api/v2/admin/libraries/{library_id}/collection-groups/order", {
    library_id: "1",
    group_id: "",
    ordered_ids: ["g1", "mine", "ungrouped"],
    has_more: false,
  });
  v2Recorder.answer(
    "PUT /api/v2/admin/libraries/{library_id}/collection-groups/order",
    ({ body }: { body: { ordered_ids: string[] } }) => ({
      library_id: "1",
      group_id: "",
      ordered_ids: body.ordered_ids,
      has_more: false,
    }),
  );
  v2Recorder.answer(
    "GET /api/v2/admin/collection-groups/{group_id}/collections/order",
    ({ path }: { path: string }) => {
      const id = path.split("/")[5]!;
      return { library_id: "1", group_id: id, ordered_ids: ORDERS[id], has_more: false };
    },
  );
  v2Recorder.answer(
    "PUT /api/v2/admin/collection-groups/{group_id}/collections/order",
    ({ path, body }: { path: string; body: { ordered_ids: string[] } }) => ({
      library_id: "1",
      group_id: path.split("/")[5],
      ordered_ids: body.ordered_ids,
      has_more: false,
    }),
  );
  v2Recorder.answer("GET /api/v2/admin/collection-groups/{id}", ({ path }: { path: string }) =>
    groupBody(path.endsWith("/mine") ? mine : franchises),
  );
  v2Recorder.answer(
    "PATCH /api/v2/admin/collection-groups/{id}",
    ({ body }: { body: Record<string, unknown> }) => ({ ...groupBody(franchises), ...body }),
  );
  v2Recorder.answer("DELETE /api/v2/admin/collection-groups/{id}", undefined);
});

afterEach(() => {
  cleanup();
  rects.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const handlers = {
  onEditCollection: vi.fn(),
  onVisibleChange: vi.fn(),
};

/** Renders the board without waiting for anything. */
function mountBoard(groups: Group[] = [franchises, mine], loose = ungrouped) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
      }
    >
      <GroupsBoard
        libraryID={1}
        libraryName="Movies"
        groups={groups}
        ungrouped={loose}
        ungroupedSortOrder={2}
        isVisible={(entry) => entry.visibility !== "hidden"}
        {...handlers}
      />
    </QueryClientProvider>,
  );
}

/** Renders the board; awaited, it returns once shelf changes are on (capabilities read). */
async function renderBoard(groups: Group[] = [franchises, mine], loose = ungrouped) {
  mountBoard(groups, loose);
  await vi.waitFor(() =>
    expect(screen.getByRole("combobox", { name: "Order of Franchises" })).toBeEnabled(),
  );
}

const noHeading = () => screen.getByRole("region", { name: "No heading" });
const cardTitles = (shelf: HTMLElement) =>
  within(shelf)
    .getAllByRole("listitem")
    .map((card) => within(card).getAllByRole("paragraph")[0]!.textContent);

function layOut() {
  const section = noHeading();
  place(section, 0, 2000);
  place(section.lastElementChild!, 10, 1990);
  within(section)
    .getAllByRole("button", { name: /^Move / })
    .slice(1)
    .forEach((grip, index) => place(grip.parentElement!, 100 + index * 50, 40));
  const shelf = screen.getByRole("region", { name: "Shelf Franchises" });
  place(shelf, 2100, 400);
  place(shelf.lastElementChild!, 2150, 350);
  place(within(shelf).getByRole("button", { name: "Move Alien" }).parentElement!, 2200, 40);
}

async function grabWithKeyboard(name: string) {
  const grip = screen.getByRole("button", { name });
  await vi.waitFor(() => expect(grip).not.toHaveAttribute("aria-disabled", "true"));
  layOut();
  grip.focus();
  await act(async () => {
    fireEvent.keyDown(grip, { code: "Space" });
    // The sensor starts listening for arrows on the next task.
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

async function press(code: string, times = 1) {
  for (let count = 0; count < times; count++) {
    await act(async () => {
      fireEvent.keyDown(document.activeElement!, { code });
    });
  }
}

/** Answers the shelf order reads as if another admin had saved `orders` since the board loaded. */
function someoneReorders(orders: Record<string, string[]>) {
  v2Recorder.answer(
    "GET /api/v2/admin/collection-groups/{group_id}/collections/order",
    ({ path }: { path: string }) => {
      const id = path.split("/")[5]!;
      return {
        library_id: "1",
        group_id: id,
        ordered_ids: orders[id] ?? ORDERS[id],
        has_more: false,
      };
    },
  );
  for (const id of Object.keys(orders))
    v2Recorder.bump(`/api/v2/admin/collection-groups/${id}/collections/order`);
}

async function openMenu(name: string) {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name }));
  return user;
}

describe("GroupsBoard", () => {
  it("shows each shelf, My collections and No heading top to bottom, with what viewers see", async () => {
    await renderBoard();
    expect(
      screen.getByRole("heading", { name: "Shelves on Movies › Collections" }),
    ).toBeInTheDocument();
    expect(
      screen.getAllByRole("region").map((region) => region.getAttribute("aria-label")),
    ).toEqual(["Shelf Franchises", "Shelf My collections", "No heading"]);
    expect(cardTitles(noHeading())).toEqual(["Staff picks", "New this month", "Oscar winners"]);
    const myShelf = screen.getByRole("region", { name: "Shelf My collections" });
    expect(myShelf).toHaveTextContent("Different for each viewer");
    expect(myShelf).toHaveTextContent("Each viewer's own collections land here");
    expect(screen.getByRole("combobox", { name: "Order of Franchises" })).toHaveDisplayValue(
      "Your order",
    );
  });

  it("dims a hidden collection on its shelf and leaves it out of the viewer preview", async () => {
    await renderBoard();
    const oscars = within(noHeading()).getAllByRole("listitem")[2]!;
    expect(oscars).toHaveTextContent("Hidden");
    const preview = screen.getByRole("complementary", { name: "What viewers see" });
    expect(preview).toHaveTextContent("Staff picks");
    expect(preview).toHaveTextContent("Each viewer's own");
    expect(preview).not.toHaveTextContent("Oscar winners");
  });

  it("moves a collection with the keyboard and saves the order the drag showed", async () => {
    await renderBoard();
    await grabWithKeyboard("Move Staff picks");
    // Each arrow press moves 25px; four land on the third card's center.
    await press("ArrowDown", 4);
    await press("Space");

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    // Changed on purpose from the earlier golden (b, a, c): the drop now
    // saves the order the drag preview showed instead of one slot short.
    expect(v2Recorder.writes()).toEqual([
      {
        operation: "PUT /api/v2/admin/collection-groups/{group_id}/collections/order",
        path: "/api/v2/admin/collection-groups/ungrouped/collections/order",
        query: { library_id: "1" },
        headers: { "If-Match": '"/api/v2/admin/collection-groups/ungrouped/collections/order#1"' },
        body: { ordered_ids: ["b", "c", "a"] },
      },
    ]);
  });

  it("shows My collections can't take a server collection while one is dragged", async () => {
    await renderBoard();
    const myShelf = screen.getByRole("region", { name: "Shelf My collections" });
    expect(myShelf).not.toHaveTextContent("Viewers' own collections only");
    await grabWithKeyboard("Move Staff picks");
    expect(myShelf).toHaveTextContent("Viewers' own collections only");
    await press("Escape");
    expect(myShelf).not.toHaveTextContent("Viewers' own collections only");
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("shows a move at once and puts it back with Try again when it fails", async () => {
    let fail: (error: Error) => void = () => {};
    v2Recorder.answer(
      "PUT /api/v2/admin/collection-groups/{group_id}/collections/order",
      () =>
        new Promise((_resolve, reject) => {
          fail = reject;
        }),
    );
    await renderBoard();
    await grabWithKeyboard("Move Staff picks");
    await press("ArrowDown", 4);
    await press("Space");

    await vi.waitFor(() =>
      expect(cardTitles(noHeading())).toEqual(["New this month", "Oscar winners", "Staff picks"]),
    );
    await act(async () => fail(new Error("offline")));
    await vi.waitFor(() =>
      expect(cardTitles(noHeading())).toEqual(["Staff picks", "New this month", "Oscar winners"]),
    );
    expect(toast.error).toHaveBeenCalledWith("Couldn't move it", {
      action: { label: "Try again", onClick: expect.any(Function) },
    });

    v2Recorder.answer(
      "PUT /api/v2/admin/collection-groups/{group_id}/collections/order",
      ({ body }: { body: { ordered_ids: string[] } }) => ({
        library_id: "1",
        group_id: "ungrouped",
        ordered_ids: body.ordered_ids,
        has_more: false,
      }),
    );
    const [, options] = vi.mocked(toast.error).mock.calls[0]!;
    await act(async () => {
      (options as unknown as { action: { onClick: () => void } }).action.onClick();
    });
    // Try again reads a fresh ETag and sends the same order.
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(2));
    expect(v2Recorder.writes()[1]).toMatchObject({
      path: "/api/v2/admin/collection-groups/ungrouped/collections/order",
      body: { ordered_ids: ["b", "c", "a"] },
    });
  });

  it("doesn't retry a move over an order someone else changed meanwhile", async () => {
    let fail: (error: Error) => void = () => {};
    v2Recorder.answer(
      "PUT /api/v2/admin/collection-groups/{group_id}/collections/order",
      () =>
        new Promise((_resolve, reject) => {
          fail = reject;
        }),
    );
    await renderBoard();
    await grabWithKeyboard("Move Staff picks");
    await press("ArrowDown", 4);
    await press("Space");
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));

    // Another admin reorders No heading, keeping the same collections.
    someoneReorders({ ungrouped: ["c", "a", "b"] });
    await act(async () => fail(new Error("precondition failed")));
    await vi.waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(MOVE_FAILED, expect.anything()),
    );
    const [, options] = vi.mocked(toast.error).mock.calls[0]!;
    await act(async () => {
      (options as unknown as { action: { onClick: () => void } }).action.onClick();
    });

    await vi.waitFor(() => expect(toast.error).toHaveBeenCalledWith(ORDER_CHANGED));
    expect(v2Recorder.writes()).toHaveLength(1);
  });

  it("won't move a collection from its ⋯ onto an order someone else changed", async () => {
    await renderBoard();
    someoneReorders({ ungrouped: ["b", "a", "c"] });
    const user = await openMenu("More for Alien");
    (await screen.findByRole("menuitem", { name: "Move to shelf" })).focus();
    await user.keyboard("{ArrowRight}");
    await user.click(await screen.findByRole("menuitemradio", { name: "No heading" }));

    await vi.waitFor(() => expect(toast.error).toHaveBeenCalledWith(ORDER_CHANGED));
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("won't move a shelf to the top over a shelf order someone else changed", async () => {
    await renderBoard();
    v2Recorder.answer("GET /api/v2/admin/libraries/{library_id}/collection-groups/order", {
      library_id: "1",
      group_id: "",
      ordered_ids: ["ungrouped", "g1", "mine"],
      has_more: false,
    });
    v2Recorder.bump("/api/v2/admin/libraries/1/collection-groups/order");
    const user = await openMenu("More for shelf My collections");
    await user.click(await screen.findByRole("menuitem", { name: "Move to top" }));

    await vi.waitFor(() => expect(toast.error).toHaveBeenCalledWith(ORDER_CHANGED));
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("says a server collection can't go on My collections when it's dropped there", async () => {
    await renderBoard();
    const myShelf = screen.getByRole("region", { name: "Shelf My collections" });
    // My collections sits just below the last card of No heading.
    place(within(myShelf).getByText(/Each viewer's own collections land here/), 250, 40);
    await grabWithKeyboard("Move Staff picks");
    await press("ArrowDown", 6);
    expect(
      await screen.findByText(
        "Staff picks can't go on My collections. It holds viewers' own collections.",
      ),
    ).toBeInTheDocument();
    await press("Space");
    expect(
      await screen.findByText("Staff picks can't go on My collections. Nothing moved."),
    ).toBeInTheDocument();
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("says a shelf sorted by name keeps its order when a card is dropped within it", async () => {
    const sorted = {
      ...franchises,
      default_sort_mode: "name_asc",
      collections: [collection("f2", "Blade Runner"), collection("f1", "Alien")],
    } as Group;
    v2Recorder.answer(
      "GET /api/v2/admin/collection-groups/{group_id}/collections/order",
      ({ path }: { path: string }) => {
        const id = path.split("/")[5]!;
        const ids = id === "g1" ? ["f2", "f1"] : ORDERS[id];
        return { library_id: "1", group_id: id, ordered_ids: ids, has_more: false };
      },
    );
    await renderBoard([sorted, mine]);
    const shelf = screen.getByRole("region", { name: "Shelf Franchises" });
    place(
      within(shelf).getByRole("button", { name: "Move Blade Runner" }).parentElement!,
      2250,
      40,
    );
    await grabWithKeyboard("Move Alien");
    await press("ArrowDown", 2);
    await press("Space");

    expect(
      await screen.findByText("Franchises sorts by name, so the order didn't change."),
    ).toBeInTheDocument();
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("moves a collection to another shelf from its ⋯ with a fresh ETag", async () => {
    await renderBoard();
    const user = await openMenu("More for Alien");
    (await screen.findByRole("menuitem", { name: "Move to shelf" })).focus();
    await user.keyboard("{ArrowRight}");
    expect(await screen.findByRole("menuitemradio", { name: "Franchises" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    // My collections holds viewers' own collections, so it isn't offered.
    expect(screen.queryByRole("menuitemradio", { name: "My collections" })).toBeNull();
    await user.click(screen.getByRole("menuitemradio", { name: "No heading" }));

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toEqual({
      operation: "PUT /api/v2/admin/collection-groups/{group_id}/collections/order",
      path: "/api/v2/admin/collection-groups/ungrouped/collections/order",
      query: { library_id: "1" },
      headers: { "If-Match": '"/api/v2/admin/collection-groups/ungrouped/collections/order#1"' },
      body: { ordered_ids: ["a", "b", "c", "f1"] },
    });
  });

  it("hides a collection from its ⋯ through the page", async () => {
    await renderBoard();
    const user = await openMenu("More for Staff picks");
    await user.click(await screen.findByRole("menuitem", { name: "Hide from Collections tab" }));
    expect(handlers.onVisibleChange).toHaveBeenCalledWith(
      expect.objectContaining({ id: "a" }),
      false,
    );
  });

  it("offers Show on the Collections tab for a hidden collection", async () => {
    await renderBoard();
    await openMenu("More for Oscar winners");
    expect(
      await screen.findByRole("menuitem", { name: "Show on the Collections tab" }),
    ).toBeInTheDocument();
  });

  it("renames a shelf with the group rename request", async () => {
    await renderBoard();
    const user = await openMenu("More for shelf Franchises");
    await user.click(await screen.findByRole("menuitem", { name: "Rename shelf" }));
    const dialog = await screen.findByRole("dialog", { name: "Rename shelf" });
    const name = within(dialog).getByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "Sagas");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toEqual({
      operation: "PATCH /api/v2/admin/collection-groups/{id}",
      path: "/api/v2/admin/collection-groups/g1",
      headers: { "If-Match": '"/api/v2/admin/collection-groups/g1#1"' },
      body: { name: "Sagas" },
    });
    await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("renames with the shelf as it was when Rename opened, so a rename meanwhile conflicts", async () => {
    await renderBoard();
    const user = await openMenu("More for shelf Franchises");
    await user.click(await screen.findByRole("menuitem", { name: "Rename shelf" }));
    const dialog = await screen.findByRole("dialog", { name: "Rename shelf" });
    // Another admin renames the shelf while this dialog is open.
    v2Recorder.bump("/api/v2/admin/collection-groups/g1");
    const name = within(dialog).getByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "Sagas");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]!.headers).toEqual({
      "If-Match": '"/api/v2/admin/collection-groups/g1#1"',
    });
    await vi.waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(screen.getByRole("dialog", { name: "Rename shelf" })).toBeInTheDocument();
  });

  it("saves a shelf's order for viewers when Order changes", async () => {
    await renderBoard();
    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: "Order of Franchises" }),
      "name_asc",
    );
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toMatchObject({
      operation: "PATCH /api/v2/admin/collection-groups/{id}",
      path: "/api/v2/admin/collection-groups/g1",
      body: { default_sort_mode: "name_asc" },
    });
  });

  it("says a deleted shelf's collections move to No heading, then deletes it", async () => {
    await renderBoard();
    const user = await openMenu("More for shelf Franchises");
    await user.click(await screen.findByRole("menuitem", { name: "Delete shelf…" }));
    const dialog = await screen.findByRole("alertdialog", {
      name: "Delete the Franchises shelf?",
    });
    expect(dialog).toHaveTextContent(
      "Its 1 collection moves to No heading on Movies › Collections. It isn't deleted. Only Movies changes",
    );
    await user.click(within(dialog).getByRole("button", { name: "Delete shelf" }));

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toEqual({
      operation: "DELETE /api/v2/admin/collection-groups/{id}",
      path: "/api/v2/admin/collection-groups/g1",
      headers: { "If-Match": '"/api/v2/admin/collection-groups/g1#1"' },
    });
  });

  it("never offers to delete My collections, and moves it to the top", async () => {
    await renderBoard();
    const user = await openMenu("More for shelf My collections");
    expect(await screen.findByRole("menuitem", { name: "Rename shelf" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Delete shelf…" })).toBeNull();
    await user.click(screen.getByRole("menuitem", { name: "Move to top" }));

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toEqual({
      operation: "PUT /api/v2/admin/libraries/{library_id}/collection-groups/order",
      path: "/api/v2/admin/libraries/1/collection-groups/order",
      headers: { "If-Match": '"/api/v2/admin/libraries/1/collection-groups/order#1"' },
      body: { ordered_ids: ["mine", "g1", "ungrouped"] },
    });
  });

  it("adds a shelf from New shelf", async () => {
    v2Recorder.answer("POST /api/v2/admin/libraries/{library_id}/collection-groups", {
      ...groupBody(franchises),
      id: "g2",
      name: "Studios",
    });
    await renderBoard();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "New shelf" }));
    const dialog = await screen.findByRole("dialog", { name: "New shelf" });
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "Studios");
    await user.click(within(dialog).getByRole("button", { name: "Add shelf" }));
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toEqual({
      operation: "POST /api/v2/admin/libraries/{library_id}/collection-groups",
      path: "/api/v2/admin/libraries/1/collection-groups",
      headers: {},
      body: { name: "Studios" },
    });
  });

  it("moves a card from a sheet on a phone, where nothing drags", async () => {
    stubPhone();
    await renderBoard();
    expect(screen.queryByRole("button", { name: "Move Staff picks" })).toBeNull();
    expect(screen.queryByRole("complementary", { name: "What viewers see" })).toBeNull();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "More for Staff picks" }));
    const sheet = await screen.findByRole("dialog", { name: "Move Staff picks" });
    expect(within(sheet).getByRole("radio", { name: /No heading/ })).toBeChecked();
    expect(within(sheet).queryByRole("radio", { name: /My collections/ })).toBeNull();
    const move = within(sheet).getByRole("button", { name: "Move" });
    expect(move).toBeDisabled();
    await user.click(within(sheet).getByRole("radio", { name: /Franchises/ }));
    await user.click(move);

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toMatchObject({
      path: "/api/v2/admin/collection-groups/g1/collections/order",
      body: { ordered_ids: ["f1", "a"] },
    });
  });

  describe("Pin to the start of its shelf", () => {
    /** New this month is pinned, so No heading shows it first: b | a, c. */
    const pinnedLoose = [
      collection("a", "Staff picks"),
      collection("b", "New this month", "visible", true),
      collection("c", "Oscar winners", "hidden"),
    ];
    const band = (shelf: HTMLElement) =>
      within(shelf).queryByRole("group", { name: "Pinned to the start" });

    beforeEach(() => {
      v2Recorder.answer("GET /api/v2/admin/collections/{id}", ({ path }: { path: string }) => ({
        ...getAdminCollectionOk,
        id: path.split("/").pop(),
      }));
    });

    it("pins from ⋯ with only collection_type and featured, and shows it first at once", async () => {
      let fail: (error: Error) => void = () => {};
      v2Recorder.answer(
        "PATCH /api/v2/admin/collections/{id}",
        () =>
          new Promise((_resolve, reject) => {
            fail = reject;
          }),
      );
      await renderBoard();
      expect(band(noHeading())).toBeNull();
      const user = await openMenu("More for New this month");
      const pin = await screen.findByRole("menuitem", { name: PIN_LABEL });
      expect(pin).toHaveAccessibleDescription(
        "Shows first on this shelf and in Server collections on the Collections page.",
      );
      await user.click(pin);

      await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
      expect(v2Recorder.writes()[0]).toEqual({
        operation: "PATCH /api/v2/admin/collections/{id}",
        path: "/api/v2/admin/collections/b",
        headers: { "If-Match": '"/api/v2/admin/collections/b#1"' },
        body: { collection_type: "manual", featured: true },
      });
      expect(cardTitles(noHeading())).toEqual(["New this month", "Staff picks", "Oscar winners"]);
      expect(within(band(noHeading())!).getByText("New this month")).toBeInTheDocument();

      // A failed pin goes back.
      await act(async () => fail(new Error("offline")));
      await vi.waitFor(() => expect(band(noHeading())).toBeNull());
      expect(cardTitles(noHeading())).toEqual(["Staff picks", "New this month", "Oscar winners"]);
      expect(toast.error).toHaveBeenCalled();
    });

    it("unpins a pinned collection, which says what it stops doing", async () => {
      await renderBoard([franchises, mine], pinnedLoose);
      const user = await openMenu("More for New this month");
      const unpin = await screen.findByRole("menuitem", { name: "Unpin" });
      expect(unpin).toHaveAccessibleDescription(
        "Stops showing first on this shelf and in Server collections on the Collections page.",
      );
      expect(screen.queryByRole("menuitem", { name: PIN_LABEL })).toBeNull();
      await user.click(unpin);

      await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
      expect(v2Recorder.writes()[0]).toMatchObject({
        path: "/api/v2/admin/collections/b",
        body: { collection_type: "manual", featured: false },
      });
    });

    it("keeps pinned collections in a band at the start, marked on the board and in the preview", async () => {
      await renderBoard([franchises, mine], pinnedLoose);
      expect(cardTitles(noHeading())).toEqual(["New this month", "Staff picks", "Oscar winners"]);
      const pinned = band(noHeading())!;
      expect(within(pinned).getAllByRole("listitem")).toHaveLength(1);
      expect(within(pinned).getByRole("img", { name: "Pinned" })).toBeInTheDocument();
      const preview = screen.getByRole("complementary", { name: "What viewers see" });
      // No heading in the preview: the pinned card first, with the pin; the hidden one left out.
      const loose = within(preview)
        .getAllByRole("listitem")
        .filter((card) => /New this month|Staff picks/.test(card.textContent ?? ""));
      expect(loose.map((card) => card.textContent)).toEqual(["New this month", "Staff picks"]);
      expect(within(loose[0]!).getByRole("img", { name: "Pinned" })).toBeInTheDocument();
      expect(within(preview).getAllByRole("img", { name: "Pinned" })).toHaveLength(1);
    });

    it("won't let a drag put a card above the band", async () => {
      await renderBoard([franchises, mine], pinnedLoose);
      await grabWithKeyboard("Move Oscar winners");
      // Up past Staff picks, onto the pinned card's place.
      await press("ArrowUp", 4);
      await press("Space");

      await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
      expect(v2Recorder.writes()[0]).toMatchObject({
        path: "/api/v2/admin/collection-groups/ungrouped/collections/order",
        body: { ordered_ids: ["b", "c", "a"] },
      });
      expect(
        await screen.findByText("Oscar winners dropped at position 2 of 3 on No heading."),
      ).toBeInTheDocument();
    });

    it("lets a pinned card from a shelf that sorts itself land inside another shelf's band", async () => {
      const sorted = {
        ...franchises,
        default_sort_mode: "name_asc",
        collections: [collection("f1", "Alien", "visible", true)],
      } as Group;
      await renderBoard([sorted, mine], pinnedLoose);
      // Franchises sorts itself, so Alien has no band there, but it is pinned.
      expect(band(screen.getByRole("region", { name: "Shelf Franchises" }))).toBeNull();
      await grabWithKeyboard("Move Alien");
      // Each arrow moves 25px: from Alien's middle (2220) to the pinned card's (120).
      await press("ArrowUp", 84);
      await press("Space");

      await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
      expect(v2Recorder.writes()[0]).toMatchObject({
        path: "/api/v2/admin/collection-groups/ungrouped/collections/order",
        body: { ordered_ids: ["f1", "b", "a", "c"] },
      });
      expect(
        await screen.findByText("Alien dropped at position 1 of 4 on No heading."),
      ).toBeInTheDocument();
    });

    it("says Pin reaches every library a collection is in", async () => {
      const shared = {
        ...franchises,
        collections: [{ ...collection("f1", "Alien", "visible", true), library_ids: [1, 2] }],
      } as Group;
      await renderBoard([shared, mine]);
      const user = await openMenu("More for Alien");
      expect(await screen.findByRole("menuitem", { name: "Unpin" })).toHaveAccessibleDescription(
        "Stops showing first on this shelf and in Server collections on the Collections page. This applies in every library it's in.",
      );
      await user.keyboard("{Escape}");
      await openMenu("More for Staff picks");
      // Staff picks is in Movies only.
      expect(await screen.findByRole("menuitem", { name: PIN_LABEL })).toHaveAccessibleDescription(
        "Shows first on this shelf and in Server collections on the Collections page.",
      );
    });

    it("stays on for a shelf sorted by name, and says it still leads Server collections", async () => {
      await renderBoard([{ ...franchises, default_sort_mode: "name_asc" }, mine]);
      await openMenu("More for Alien");
      const pin = await screen.findByRole("menuitem", { name: PIN_LABEL });
      expect(pin).not.toHaveAttribute("aria-disabled");
      expect(pin).toHaveAccessibleDescription(
        "Shows first in Server collections on the Collections page; this shelf sorts by name.",
      );
    });

    it("is off where the server can't arrange shelves, on the board and the phone sheet", async () => {
      v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
        ...adminCapabilities,
        groups: false,
      });
      mountBoard();
      await vi.waitFor(() =>
        expect(v2Recorder.callsOf("GET /api/v2/admin/collections/capabilities")).toHaveLength(1),
      );
      await openMenu("More for Staff picks");
      expect(await screen.findByRole("menuitem", { name: PIN_LABEL })).toHaveAttribute(
        "aria-disabled",
        "true",
      );
      cleanup();

      stubPhone();
      mountBoard();
      await vi.waitFor(() =>
        expect(v2Recorder.callsOf("GET /api/v2/admin/collections/capabilities")).toHaveLength(2),
      );
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "More for Staff picks" }));
      const sheet = await screen.findByRole("dialog", { name: "Move Staff picks" });
      expect(
        within(sheet).getByRole("switch", {
          name: "Pin Staff picks to the start of the collections with no heading",
        }),
      ).toBeDisabled();
    });

    it("pins from the phone sheet's switch", async () => {
      stubPhone();
      await renderBoard();
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "More for Staff picks" }));
      const sheet = await screen.findByRole("dialog", { name: "Move Staff picks" });
      const pin = within(sheet).getByRole("switch", {
        name: "Pin Staff picks to the start of the collections with no heading",
      });
      expect(pin).not.toBeChecked();
      expect(pin).toHaveAccessibleDescription(
        "Shows first on this shelf and in Server collections on the Collections page.",
      );
      await user.click(pin);

      await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
      expect(v2Recorder.writes()[0]).toMatchObject({
        path: "/api/v2/admin/collections/a",
        body: { collection_type: "manual", featured: true },
      });
    });
  });
});
