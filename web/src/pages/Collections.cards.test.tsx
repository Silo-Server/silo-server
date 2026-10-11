/**
 * The Collections page's card actions and sections:
 * each own card's ⋯ menu, the sharing switch in it, Sync now, and when
 * Shared with me and Server collections show.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import getLibraryCollectionsOk from "../../../contracts/api/v2/fixtures/get_library_collections_ok.json";
import listCollectionsOk from "../../../contracts/api/v2/fixtures/list_collections_ok.json";
import { personalCapabilities } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import Collections from "./Collections";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
const account = vi.hoisted(() => ({
  profiles: [] as Array<{ id: string; name: string }>,
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: account.profiles }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" } }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium", caption: "title" } }),
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

function show() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
      }
    >
      <MemoryRouter initialEntries={["/collections"]}>
        <Routes>
          <Route path="/collections" element={<Collections />} />
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function openMenu(name: string) {
  await userEvent.click(await screen.findByRole("button", { name: `More for ${name}` }));
  return screen.findByRole("menu");
}

describe("Your collections card menu", () => {
  it("offers Edit, the sharing switch and Delete on a manual collection, without Sync now", async () => {
    show();
    const menu = await openMenu("Rainy days");
    const share = within(menu).getByRole("menuitemcheckbox", {
      name: "Show to other profiles",
      description: "Every profile on this account sees it",
    });
    expect(share).toHaveAttribute("aria-checked", "false");
    expect([...menu.querySelectorAll('[role^="menuitem"]')]).toEqual([
      within(menu).getByRole("menuitem", { name: "Edit collection" }),
      within(menu).getByRole("menuitem", { name: "Add to my Home…" }),
      share,
      within(menu).getByRole("menuitem", { name: "Delete…" }),
    ]);
  });

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

  it("opens the editor from Edit collection", async () => {
    show();
    const menu = await openMenu("Rainy days");
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Edit collection" }));
    expect(await screen.findByTestId("where")).toHaveTextContent("/collections/c1/edit");
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

  it("sends nothing when stopping sharing is cancelled", async () => {
    v2Recorder.answer("GET /api/v2/collections", { items: [sharedRainyDays, familyNight] });
    show();
    const menu = await openMenu("Rainy days");
    await userEvent.click(
      within(menu).getByRole("menuitemcheckbox", { name: "Show to other profiles" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(v2Recorder.calls.filter((call) => call.path === "/api/v2/collections/c1")).toEqual([]);
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

  it("offers one dashed New collection card when the profile has none", async () => {
    v2Recorder.answer("GET /api/v2/collections", { items: [familyNight] });
    show();
    const own = await screen.findByRole("region", { name: "Your collections" });
    const card = await within(own).findByRole("button", { name: "New collection" });
    expect(within(own).getAllByRole("button")).toEqual([card]);
    await userEvent.click(card);
    expect(await screen.findByRole("dialog", { name: "New collection" })).toBeInTheDocument();
  });
});

describe("Shared with me", () => {
  it("groups cards by owner, labels them read-only, and gives them no buttons", async () => {
    show();
    const shared = await screen.findByRole("region", { name: "Shared with me" });
    expect(
      within(shared).getByText("Read-only. Other profiles on this account made these."),
    ).toBeInTheDocument();
    expect(within(shared).getByRole("heading", { name: "by Maya" })).toBeInTheDocument();
    expect(within(shared).getByRole("link", { name: "Family night" })).toBeInTheDocument();
    expect(within(shared).queryAllByRole("button")).toEqual([]);
  });

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

  it("switches libraries with the pills and links See all to the library's tab", async () => {
    const ghibli = { ...oscarWinners, id: "lc-ghibli", title: "Studio Ghibli" };
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        serverLibrary("1", "Movies", [oscarWinners], 12),
        serverLibrary("2", "Kids", [ghibli]),
      ],
    });
    show();
    const server = await screen.findByRole("region", { name: "Server collections" });
    const pills = await within(server).findByRole("group", { name: "Library" });
    expect(within(pills).getByRole("button", { name: "All" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(within(server).getByRole("link", { name: oscarWinners.title })).toBeInTheDocument();
    expect(within(server).getByRole("link", { name: "Studio Ghibli" })).toBeInTheDocument();

    await userEvent.click(within(pills).getByRole("button", { name: "Kids" }));
    expect(within(server).queryByRole("link", { name: oscarWinners.title })).toBeNull();
    expect(within(server).getByRole("link", { name: "Studio Ghibli" })).toBeInTheDocument();

    await userEvent.click(within(pills).getByRole("button", { name: "Movies" }));
    expect(
      within(server).getByRole("link", { name: "See all 12 Movies collections" }),
    ).toHaveAttribute("href", "/library/1?tab=collections");
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
    const libraryOf = () =>
      new URL(
        within(server).getByRole("link", { name: oscarWinners.title }).getAttribute("href")!,
        "http://silo.test",
      ).searchParams.get("library_id");

    await within(server).findByRole("group", { name: "Library" });
    expect(within(server).getAllByRole("link", { name: oscarWinners.title })).toHaveLength(1);
    expect(libraryOf()).toBeNull();

    await userEvent.click(within(server).getByRole("button", { name: "Kids" }));
    expect(libraryOf()).toBe("2");
  });
});
