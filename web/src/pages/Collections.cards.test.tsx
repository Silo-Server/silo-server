/**
 * The Collections page's card actions and sections:
 * each own card's ⋯ menu, the sharing switch in it, Sync now, and when
 * Shared with me and Server collections show.
 */
import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import getLibraryCollectionsOk from "../../../contracts/api/v2/fixtures/get_library_collections_ok.json";
import listCollectionsOk from "../../../contracts/api/v2/fixtures/list_collections_ok.json";
import { v2Problem } from "@/api/v2/problems.test-support";
import { personalCapabilities } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import { renderCollectionsPage } from "./collectionsPage.test-support";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
const account = vi.hoisted(() => ({
  profiles: [] as Array<{ id: string; name: string }>,
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: account.profiles }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" } }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium" } }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

const [rainyDays, familyNight] = listCollectionsOk.items;
const sharedRainyDays = { ...rainyDays, is_shared: true };
const letterboxd = {
  ...getCollectionOk,
  id: "c4",
  name: "Letterboxd watchlist",
  collection_type: "mdblist",
  last_sync_status: "failed",
  last_sync_message: "MDBList answered 503",
};
const oscarWinners = getLibraryCollectionsOk.ungrouped.collections[0]!;

function serverLibrary(id: string, name: string, collections: unknown[], total = 0) {
  return {
    library_id: id,
    library_name: name,
    total_count: total || collections.length,
    collections,
  };
}

const ghibli = { ...oscarWinners, id: "lc-ghibli", title: "Studio Ghibli" };

const heistNight = { ...oscarWinners, id: "lc-heist", title: "Heist night" };
const spaceOperas = { ...oscarWinners, id: "lc-space", title: "Space operas" };

/** A library's Collections tab answer whose whole list holds these collections. */
function libraryTab(id: string, collections: Array<{ id: string; title: string }>) {
  const [collection] = getLibraryCollectionsOk.collections;
  return {
    ...getLibraryCollectionsOk,
    library_id: id,
    collections: collections.map(({ id, title }) => ({ ...collection, id, title })),
  };
}

/** Server collections Movies (first card Oscar Winners of `moviesTotal`) and Kids (Studio Ghibli). */
function answerMoviesAndKids(moviesTotal: number) {
  v2Recorder.answer("GET /api/v2/collections/server", {
    libraries: [
      serverLibrary("1", "Movies", [oscarWinners], moviesTotal),
      serverLibrary("2", "Kids", [ghibli]),
    ],
  });
  v2Recorder.answer("GET /api/v2/library/{id}/collections", (call: { path: string }) =>
    call.path === "/api/v2/library/1/collections"
      ? libraryTab("1", [oscarWinners, heistNight, spaceOperas])
      : libraryTab("2", [ghibli]),
  );
}

/** The libraries whose whole Collections tab list the page read. */
function libraryReads() {
  return v2Recorder.callsOf("GET /api/v2/library/{id}/collections").map((call) => call.path);
}

/** The names of a region's collection cards, in order. */
function cardsIn(region: HTMLElement) {
  return within(region)
    .getAllByRole("link")
    .map(visibleText)
    .filter((name) => !name.startsWith("See all"));
}

/** An element's text without its aria-hidden parts (a captioned card's poster placeholder). */
function visibleText(element: HTMLElement) {
  const copy = element.cloneNode(true) as HTMLElement;
  copy.querySelectorAll("[aria-hidden='true']").forEach((node) => node.remove());
  return copy.textContent?.trim() ?? "";
}

/** The library a collection card opens in. */
function libraryOf(card: HTMLElement) {
  return new URL(card.getAttribute("href")!, "http://silo.test").searchParams.get("library_id");
}

beforeEach(() => {
  account.profiles = [
    { id: "p-owner", name: "Owner" },
    { id: "p-primary", name: "Maya" },
    { id: "p-kid", name: "Leo" },
  ];
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("GET /api/v2/collections", { items: [rainyDays, familyNight, letterboxd] });
  v2Recorder.answer("GET /api/v2/collections/server", { libraries: [] });
});

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

const show = () => renderCollectionsPage(<Where />);

async function openMenu(name: string) {
  await userEvent.click(await screen.findByRole("button", { name: `More for ${name}` }));
  return screen.findByRole("menu");
}

describe("Your collections card menu", () => {
  it("opens Add row on your Home from Add to my Home…, changing nothing yet", async () => {
    show();
    const menu = await openMenu("Rainy days");
    const add = within(menu).getByRole("menuitem", {
      name: "Add to my Home…",
      description: "A row on your Home",
    });
    await userEvent.click(add);
    expect(await screen.findByTestId("where")).toHaveTextContent(
      "/settings/home-screen?page=home&add=collection%3Auser%3Ac1",
    );
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("offers Sync now only on a synced list", async () => {
    v2Recorder.answer("POST /api/v2/collections/{id}/sync", {
      status: "success",
      message: "",
      items_matched: 3,
    });
    show();
    const menu = await openMenu("Letterboxd watchlist");
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    await vi.waitFor(() =>
      expect(v2Recorder.operations((call) => call.operation.startsWith("POST"))).toEqual([
        "POST /api/v2/collections/{id}/sync",
      ]),
    );
    expect(v2Recorder.callsOf("POST /api/v2/collections/{id}/sync")[0]?.path).toBe(
      "/api/v2/collections/c4/sync",
    );
  });

  it("shares at once when the switch turns on, with the collection's ETag", async () => {
    show();
    const menu = await openMenu("Rainy days");
    await userEvent.click(
      within(menu).getByRole("menuitemcheckbox", { name: "Show to other profiles" }),
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()).toEqual([
      {
        operation: "PATCH /api/v2/collections/{id}",
        path: "/api/v2/collections/c1",
        headers: { "If-Match": '"/api/v2/collections/c1#1"' },
        body: { is_shared: true },
      },
    ]);
  });

  it("asks before it stops sharing, naming who loses it, and saves only after Confirm", async () => {
    v2Recorder.answer("GET /api/v2/collections", { items: [sharedRainyDays, familyNight] });
    v2Recorder.answer("GET /api/v2/collections/{id}", sharedRainyDays);
    show();
    const menu = await openMenu("Rainy days");
    const toggle = within(menu).getByRole("menuitemcheckbox", { name: "Show to other profiles" });
    expect(toggle).toHaveAttribute("aria-checked", "true");
    await userEvent.click(toggle);

    const dialog = await screen.findByRole("alertdialog", { name: "Stop sharing Rainy days?" });
    expect(dialog).toHaveAccessibleDescription(
      "Maya and Leo lose it, including rows they made from it.",
    );
    expect(v2Recorder.writes()).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Stop sharing" }));

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toEqual({
      operation: "PATCH /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: { "If-Match": '"/api/v2/collections/c1#1"' },
      body: { is_shared: false },
    });
  });

  it("deletes after the confirm, with the collection's ETag", async () => {
    v2Recorder.answer("DELETE /api/v2/collections/{id}", undefined);
    show();
    const menu = await openMenu("Rainy days");
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: 'Delete "Rainy days"?' });
    expect(v2Recorder.writes()).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()[0]).toMatchObject({
      operation: "DELETE /api/v2/collections/{id}",
      path: "/api/v2/collections/c1",
      headers: { "If-Match": '"/api/v2/collections/c1#1"' },
    });
  });

  it("leaves the sharing switch out on a single-profile account", async () => {
    account.profiles = [{ id: "p-owner", name: "Owner" }];
    show();
    const menu = await openMenu("Rainy days");
    expect(within(menu).queryByRole("menuitemcheckbox")).toBeNull();
  });
});

describe("Your collections cards", () => {
  it("shows each card's type and titles, a Shared pill, and sync status only when it fails", async () => {
    v2Recorder.answer("GET /api/v2/collections", { items: [sharedRainyDays, letterboxd] });
    show();
    const own = await screen.findByRole("region", { name: "Your collections" });
    const rainy = (await within(own).findByRole("link", { name: "Rainy days" })).closest("li")!;
    expect(within(rainy).getByText("Manual · 4 titles")).toBeInTheDocument();
    expect(within(rainy).getByText("Shared")).toBeInTheDocument();
    const synced = within(own).getByRole("link", { name: "Letterboxd watchlist" }).closest("li")!;
    expect(within(synced).getByText(/Synced list · \d+ titles?/)).toBeInTheDocument();
    expect(within(synced).getByText("Sync failed")).toHaveAttribute(
      "title",
      "MDBList answered 503",
    );
    expect(within(synced).queryByText("Shared")).toBeNull();
  });
});

describe("Shared with me", () => {
  it("hides on a single-profile account", async () => {
    account.profiles = [{ id: "p-owner", name: "Owner" }];
    show();
    await screen.findByRole("region", { name: "Your collections" });
    expect(screen.queryByRole("region", { name: "Shared with me" })).toBeNull();
  });

  it("hides when no other profile shares a collection", async () => {
    v2Recorder.answer("GET /api/v2/collections", { items: [rainyDays] });
    show();
    await screen.findByRole("region", { name: "Your collections" });
    expect(screen.queryByRole("region", { name: "Shared with me" })).toBeNull();
  });
});

describe("Server collections", () => {
  it("hides when no library has any", async () => {
    show();
    await screen.findByRole("region", { name: "Your collections" });
    await vi.waitFor(() =>
      expect(v2Recorder.callsOf("GET /api/v2/collections/server")).toHaveLength(1),
    );
    expect(screen.queryByRole("region", { name: "Server collections" })).toBeNull();
  });

  it("opens All with each library's first collections and adds the rest after them", async () => {
    answerMoviesAndKids(3);
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    const pills = await within(server).findByRole("group", { name: "Library" });
    expect(within(pills).getByRole("button", { name: "All" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Studio Ghibli"]);
    // Opening the page reads no library's whole list.
    expect(libraryReads()).toEqual([]);

    await userEvent.click(within(server).getByRole("button", { name: "Load more" }));
    await within(server).findByRole("link", { name: "Space operas" });
    expect(cardsIn(server)).toEqual([
      oscarWinners.title,
      "Studio Ghibli",
      "Heist night",
      "Space operas",
    ]);
    // Kids sent all its collections already.
    expect(libraryReads()).toEqual(["/api/v2/library/1/collections"]);
    expect(within(server).queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("loads one library's rest each time the end of the grid is reached under All", async () => {
    const kiki = { ...oscarWinners, id: "lc-kiki", title: "Kiki's Delivery Service" };
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        serverLibrary("1", "Movies", [oscarWinners], 3),
        serverLibrary("2", "Kids", [ghibli], 2),
      ],
    });
    v2Recorder.answer("GET /api/v2/library/{id}/collections", (call: { path: string }) =>
      call.path === "/api/v2/library/1/collections"
        ? libraryTab("1", [oscarWinners, heistNight, spaceOperas])
        : libraryTab("2", [ghibli, kiki]),
    );
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    await userEvent.click(await within(server).findByRole("button", { name: "Load more" }));
    await within(server).findByRole("link", { name: "Space operas" });
    expect(libraryReads()).toEqual(["/api/v2/library/1/collections"]);
    expect(within(server).queryByRole("link", { name: kiki.title })).toBeNull();

    await userEvent.click(await within(server).findByRole("button", { name: "Load more" }));
    await within(server).findByRole("link", { name: kiki.title });
    expect(libraryReads()).toEqual([
      "/api/v2/library/1/collections",
      "/api/v2/library/2/collections",
    ]);
    expect(cardsIn(server)).toEqual([
      oscarWinners.title,
      "Studio Ghibli",
      "Heist night",
      "Space operas",
      kiki.title,
    ]);
    expect(within(server).queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("loads the rest under All once the end of the grid scrolls into view", async () => {
    const observed: Array<(entries: Array<{ isIntersecting: boolean }>) => void> = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(callback: (entries: Array<{ isIntersecting: boolean }>) => void) {
          observed.push(callback);
        }
        observe() {}
        unobserve() {}
        disconnect() {}
        takeRecords() {
          return [];
        }
      },
    );
    try {
      answerMoviesAndKids(3);
      show();
      const server = await screen.findByRole("region", { name: "Server collections" });
      await within(server).findByRole("link", { name: "Studio Ghibli" });
      expect(libraryReads()).toEqual([]);

      act(() => observed.at(-1)!([{ isIntersecting: true }]));
      expect(await within(server).findByRole("link", { name: "Space operas" })).toBeInTheDocument();
      expect(libraryReads()).toEqual(["/api/v2/library/1/collections"]);
      expect(within(server).queryByRole("button", { name: "Load more" })).toBeNull();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("loads a picked library's whole list straight away", async () => {
    answerMoviesAndKids(3);
    let release = () => {};
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    v2Recorder.answer("GET /api/v2/library/{id}/collections", async () => {
      await released;
      return libraryTab("1", [oscarWinners, heistNight, spaceOperas]);
    });
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    await userEvent.click(await within(server).findByRole("button", { name: "Movies" }));
    await vi.waitFor(() => expect(libraryReads()).toEqual(["/api/v2/library/1/collections"]));
    // A picked library has nothing to press while its list loads.
    expect(within(server).queryByRole("button", { name: "Load more" })).toBeNull();
    await act(async () => release());
    expect(libraryOf(await within(server).findByRole("link", { name: "Space operas" }))).toBe("1");
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Heist night", "Space operas"]);
    expect(within(server).queryByRole("button", { name: "Load more" })).toBeNull();

    await userEvent.click(within(server).getByRole("button", { name: "Kids" }));
    expect(cardsIn(server)).toEqual(["Studio Ghibli"]);
    expect(libraryReads()).toEqual(["/api/v2/library/1/collections"]);
  });

  it("keeps a library's first cards and links to its tab when the rest fail to load", async () => {
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        serverLibrary("1", "Movies", [oscarWinners], 30),
        serverLibrary("2", "Kids", [ghibli]),
      ],
    });
    v2Recorder.answer("GET /api/v2/library/{id}/collections", () => {
      throw v2Problem(500, "internal_error", "Failed to load collections");
    });
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    (await within(server).findByRole("button", { name: "Load more" })).focus();
    await userEvent.keyboard("{Enter}");
    const seeAll = await within(server).findByRole("link", { name: "See all 30 in Movies" });
    expect(seeAll).toHaveAttribute("href", "/library/1?tab=collections");
    // The button went with nothing added, so focus moves to the link.
    await vi.waitFor(() => expect(seeAll).toHaveFocus());
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Studio Ghibli"]);
    expect(within(server).queryByRole("link", { name: /in Kids$/ })).toBeNull();
  });

  it("keeps Load more focused while a library's rest loads, then focuses the first card it added", async () => {
    const kiki = { ...oscarWinners, id: "lc-kiki", title: "Kiki's Delivery Service" };
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        serverLibrary("1", "Movies", [oscarWinners], 3),
        serverLibrary("2", "Kids", [ghibli], 2),
      ],
    });
    let release = () => {};
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    v2Recorder.answer("GET /api/v2/library/{id}/collections", async (call: { path: string }) => {
      await released;
      return call.path === "/api/v2/library/1/collections"
        ? libraryTab("1", [oscarWinners, heistNight, spaceOperas])
        : libraryTab("2", [ghibli, kiki]);
    });
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    const loadMore = await within(server).findByRole("button", { name: "Load more" });
    loadMore.focus();
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() => expect(loadMore).toHaveAttribute("aria-disabled", "true"));
    expect(loadMore).toHaveFocus();
    // A press while Movies loads does not ask for Kids too.
    await userEvent.keyboard("{Enter}");
    expect(libraryReads()).toEqual(["/api/v2/library/1/collections"]);

    await act(async () => release());
    await vi.waitFor(() =>
      expect(within(server).getByRole("link", { name: "Heist night" })).toHaveFocus(),
    );
    expect(within(server).getByRole("button", { name: "Load more" })).not.toHaveAttribute(
      "aria-disabled",
    );

    // The last library's rest: the button goes, and focus still lands on its first card.
    within(server).getByRole("button", { name: "Load more" }).focus();
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() =>
      expect(within(server).getByRole("link", { name: kiki.title })).toHaveFocus(),
    );
    expect(within(server).queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("adds a cached library's rest under All only once it is asked for, after the earlier ones", async () => {
    const kiki = { ...oscarWinners, id: "lc-kiki", title: "Kiki's Delivery Service" };
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        serverLibrary("1", "Movies", [oscarWinners], 3),
        serverLibrary("2", "Kids", [ghibli], 2),
      ],
    });
    v2Recorder.answer("GET /api/v2/library/{id}/collections", (call: { path: string }) =>
      call.path === "/api/v2/library/1/collections"
        ? libraryTab("1", [oscarWinners, heistNight, spaceOperas])
        : libraryTab("2", [ghibli, kiki]),
    );
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    // Picking Kids caches its whole list.
    await userEvent.click(await within(server).findByRole("button", { name: "Kids" }));
    await within(server).findByRole("link", { name: kiki.title });
    await userEvent.click(within(server).getByRole("button", { name: "All" }));
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Studio Ghibli"]);

    within(server).getByRole("button", { name: "Load more" }).focus();
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() =>
      expect(within(server).getByRole("link", { name: "Heist night" })).toHaveFocus(),
    );
    expect(cardsIn(server)).toEqual([
      oscarWinners.title,
      "Studio Ghibli",
      "Heist night",
      "Space operas",
    ]);
    within(server).getByRole("button", { name: "Load more" }).focus();
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() =>
      expect(within(server).getByRole("link", { name: kiki.title })).toHaveFocus(),
    );
    expect(cardsIn(server)).toEqual([
      oscarWinners.title,
      "Studio Ghibli",
      "Heist night",
      "Space operas",
      kiki.title,
    ]);
  });

  it("keeps a library's rest on screen and offers Try again when refreshing it fails", async () => {
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [serverLibrary("1", "Movies", [oscarWinners], 3)],
    });
    let failing = false;
    v2Recorder.answer("GET /api/v2/library/{id}/collections", () => {
      if (failing) throw v2Problem(500, "internal_error", "Failed to load collections");
      return libraryTab("1", [oscarWinners, heistNight, spaceOperas]);
    });
    const client = show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    await userEvent.click(await within(server).findByRole("button", { name: "Load more" }));
    await within(server).findByRole("link", { name: "Space operas" });

    failing = true;
    await act(() => client.refetchQueries({ type: "active" }));
    expect(await within(server).findByRole("alert")).toHaveTextContent(
      "Couldn't refresh the collections in Movies.",
    );
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Heist night", "Space operas"]);

    failing = false;
    await userEvent.click(within(server).getByRole("button", { name: "Try again" }));
    await vi.waitFor(() => expect(within(server).queryByRole("alert")).toBeNull());
    expect(libraryReads()).toHaveLength(3);
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Heist night", "Space operas"]);
  });

  it("opens a collection in several libraries across all of them from All", async () => {
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        serverLibrary("1", "Movies", [oscarWinners]),
        serverLibrary("2", "Kids", [oscarWinners]),
      ],
    });
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    const card = () => within(server).getByRole("link", { name: oscarWinners.title });

    await within(server).findByRole("group", { name: "Library" });
    expect(within(server).getAllByRole("link", { name: oscarWinners.title })).toHaveLength(1);
    expect(libraryOf(card())).toBeNull();

    await userEvent.click(within(server).getByRole("button", { name: "Kids" }));
    expect(libraryOf(card())).toBe("2");
  });

  it("keeps the first collections in the server collections answer's order", async () => {
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [serverLibrary("1", "Movies", [oscarWinners, heistNight], 3)],
    });
    // A library list that disagrees with the first ones, as an older cached copy can.
    v2Recorder.answer(
      "GET /api/v2/library/{id}/collections",
      libraryTab("1", [heistNight, spaceOperas, oscarWinners]),
    );
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    await userEvent.click(await within(server).findByRole("button", { name: "Load more" }));
    await within(server).findByRole("link", { name: "Space operas" });
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Heist night", "Space operas"]);
  });

  it("finds a collection in several libraries among the rest of a library too", async () => {
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        serverLibrary("1", "Movies", [oscarWinners], 2),
        serverLibrary("2", "Kids", [ghibli]),
      ],
    });
    v2Recorder.answer(
      "GET /api/v2/library/{id}/collections",
      libraryTab("1", [oscarWinners, ghibli]),
    );
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    const ghibliCard = () => within(server).getByRole("link", { name: "Studio Ghibli" });
    expect(libraryOf(await within(server).findByRole("link", { name: "Studio Ghibli" }))).toBe("2");

    await userEvent.click(within(server).getByRole("button", { name: "Load more" }));
    await vi.waitFor(() => expect(libraryOf(ghibliCard())).toBeNull());
    expect(cardsIn(server)).toEqual([oscarWinners.title, "Studio Ghibli"]);
  });
});
