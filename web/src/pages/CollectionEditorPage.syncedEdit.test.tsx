/**
 * Editing a saved Synced list on the collection editor page, both scopes: the
 * list it follows (a link, a chart, a TMDB collection ID, or a locked summary
 * for Discover and legacy Trakt lists), its sync status and Sync now, and the
 * schedule a profile picks by name.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import {
  adminCapabilities,
  adminCollectionList,
  adminSyncedCollection,
  personalCapabilities,
  personalSyncedCollection,
} from "@/test/fixtures/collectionAnswers";
import { chooseArtwork } from "@/test/collectionArtwork";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner", name: "Sam" }, isLoading: false }),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p-owner", name: "Sam" }] }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: LIBRARIES }),
}));
vi.mock("@/hooks/queries/libraries", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  )),
  useUserLibraries: () => ({ data: LIBRARIES }),
}));

const LIBRARIES = [
  { id: 1, name: "Movies", type: "movies" },
  { id: 2, name: "TV Shows", type: "series" },
];

const HOUR = 60 * 60 * 1000;

installV2Recorder();
beforeAll(() => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
});

type Source = { source_url: string; source_config: Record<string, unknown> };

/** A saved server Synced list, as the editor reads it and as the list shows it. */
function serverList(
  type: "mdblist" | "tmdb" | "trakt",
  source: Source,
  extra: Record<string, unknown> = {},
) {
  const collection = { ...adminSyncedCollection(type, source), ...extra };
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", collection);
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(collection));
  return collection;
}

function personalList(
  type: "mdblist" | "tmdb" | "trakt",
  source: Source,
  extra: Record<string, unknown>,
) {
  v2Recorder.answer("GET /api/v2/collections/{id}", {
    ...personalSyncedCollection(type, source),
    ...extra,
  });
}

const MDBLIST: Source = {
  source_url: "https://mdblist.com/lists/garycrawfordgc/netflix-originals/json",
  source_config: {
    mode: "mdblist_json",
    url: "https://mdblist.com/lists/garycrawfordgc/netflix-originals/json",
    limit: 50,
  },
};

const syncRun = (overrides: Record<string, unknown> = {}) => ({
  id: "run-1",
  collection_id: "c1",
  status: "success",
  message: "",
  items_added: 3,
  items_removed: 1,
  items_matched: 141,
  items_unmatched: 41,
  created_at: "2026-01-02T03:04:05Z",
  started_at: "2026-01-02T03:04:05Z",
  completed_at: "2026-01-02T03:04:06Z",
  ...overrides,
});

function showPage(url: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const router = createMemoryRouter(
    [
      { path: "/admin/collections", element: <p>Admin collections page</p> },
      {
        element: <CollectionEditorPage scope="server" />,
        children: [{ path: "/admin/collections/new" }, { path: "/admin/collections/:id/edit" }],
      },
      { path: "/collections", element: <p>Collections page</p> },
      {
        element: <CollectionEditorPage scope="personal" />,
        children: [{ path: "/collections/new" }, { path: "/collections/:id/edit" }],
      },
    ],
    { initialEntries: [url] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const SERVER_EDIT = "/admin/collections/c1/edit?libraryId=1";
const PERSONAL_EDIT = "/collections/c1/edit";
const ADMIN_PATCH = "PATCH /api/v2/admin/collections/{id}";
const PERSONAL_PATCH = "PATCH /api/v2/collections/{id}";

const patches = (operation: string) =>
  v2Recorder.callsOf(operation).map((call) => call.body as Record<string, unknown>);

async function nameField() {
  return screen.findByRole("textbox", { name: "Name" });
}

async function rename(name: string) {
  fireEvent.change(await nameField(), { target: { value: name } });
}

function saveBar() {
  return screen.getByRole("region", { name: "Unsaved changes" });
}

function saveButton() {
  return within(saveBar()).getByRole("button", { name: "Save" });
}

async function save(operation: string, count = 1) {
  fireEvent.click(saveButton());
  await vi.waitFor(() => expect(v2Recorder.callsOf(operation)).toHaveLength(count));
}

function choose(combobox: HTMLElement, option: string) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  fireEvent.click(screen.getByRole("option", { name: option }));
}

async function setMaxTitles(value: string) {
  const input = await screen.findByRole("spinbutton", { name: "Max titles" });
  fireEvent.change(input, { target: { value } });
  fireEvent.blur(input);
  return input;
}

async function openMoreActions() {
  const trigger = await screen.findByRole("button", { name: "More actions" });
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: "mouse" });
  return screen.findByRole("menu");
}

function statusStrip() {
  return screen.getByRole("group", { name: "Sync status" });
}

describe("server Synced list editor", () => {
  it("opens a saved list in the editor page, with its source, libraries and schedule", async () => {
    serverList("mdblist", MDBLIST, { sync_schedule: "0 3 * * *" });
    showPage(SERVER_EDIT);
    expect(await nameField()).toHaveValue("Original");
    expect(screen.getByRole("heading", { name: "The list it follows" })).toBeInTheDocument();
    expect(screen.getByText("mdblist.com/lists/garycrawfordgc/netflix-originals")).toBeVisible();
    expect(screen.getByRole("button", { name: "Change link" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Sync schedule" })).toHaveTextContent(
      "Every day at 3:00 AM",
    );
    expect(screen.getByRole("spinbutton", { name: "Max titles" })).toHaveValue(50);
    expect(screen.queryByRole("button", { name: "Save Collection" })).toBeNull();
  });

  it("shows when it last synced, when it syncs next, and why titles are skipped", async () => {
    serverList("mdblist", MDBLIST, {
      last_sync_status: "success",
      last_sync_at: new Date(Date.now() - 3 * HOUR).toISOString(),
      next_sync_at: new Date(Date.now() + 5 * HOUR).toISOString(),
      sync_schedule: "0 3 * * *",
      item_count: 182,
    });
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", syncRun());
    showPage(SERVER_EDIT);
    await nameField();
    const strip = statusStrip();
    expect(within(strip).getByText("Last sync")).toBeInTheDocument();
    expect(within(strip).getByText("3 hours ago")).toBeInTheDocument();
    expect(within(strip).getByText("Next sync")).toBeInTheDocument();
    expect(within(strip).getByText("Not in your libraries")).toBeInTheDocument();
    expect(within(strip).getByText("Not counted yet")).toBeInTheDocument();

    const menu = await openMoreActions();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    expect(await within(statusStrip()).findByText("41 titles skipped")).toBeInTheDocument();

    const why = within(statusStrip()).getByRole("button", { name: "Why titles are skipped" });
    expect(why).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(why);
    expect(why).toHaveAttribute("aria-expanded", "true");
    expect(
      screen.getByText(
        "41 titles on the list aren't in Movies, so they're skipped. Add them to one of those libraries and they join at the next sync.",
      ),
    ).toBeVisible();
  });

  it("says when a list has no schedule", async () => {
    serverList("mdblist", MDBLIST);
    showPage(SERVER_EDIT);
    await nameField();
    expect(within(statusStrip()).getByText("Not scheduled")).toBeInTheDocument();
    expect(within(statusStrip()).getByText("Not yet")).toBeInTheDocument();
  });

  it("disables Sync now while a sync runs, then saves with the token after the sync", async () => {
    serverList("mdblist", MDBLIST);
    let finish: (value: unknown) => void = () => {};
    v2Recorder.answer(
      "POST /api/v2/admin/collections/{id}/sync",
      () => new Promise((resolve) => (finish = resolve)),
    );
    showPage(SERVER_EDIT);
    await rename("Netflix Originals");

    let menu = await openMoreActions();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    expect(await within(statusStrip()).findByText("Syncing now…")).toBeInTheDocument();
    menu = await openMoreActions();
    expect(within(menu).getByRole("menuitem", { name: "Syncing now…" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    fireEvent.keyDown(menu, { key: "Escape" });

    await act(async () => finish(syncRun()));
    await vi.waitFor(() => expect(within(statusStrip()).queryByText("Syncing now…")).toBeNull());
    // The sync moved the collection's revision; the editor read it again.
    await vi.waitFor(() =>
      expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}").length).toBeGreaterThan(1),
    );
    const afterSync = v2Recorder.etag("/api/v2/admin/collections/c1");
    await save(ADMIN_PATCH);
    // One PATCH, with the token the sync left: no 412 and no retry.
    const [patch] = v2Recorder.callsOf(ADMIN_PATCH) as [RecordedCall];
    expect(v2Recorder.callsOf(ADMIN_PATCH)).toHaveLength(1);
    expect(patch.headers["If-Match"]).toBe(afterSync);
    expect(patch.body).toMatchObject({ title: "Netflix Originals" });
  });

  it("keeps Sync now and Delete waiting until the list is read again after a sync", async () => {
    const saved = serverList("mdblist", MDBLIST);
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", syncRun());
    showPage(SERVER_EDIT);
    await nameField();
    // Hold the read after the sync: until it answers, the editor has the old token.
    let release: () => void = () => {};
    v2Recorder.answer(
      "GET /api/v2/admin/collections/{id}",
      () => new Promise((resolve) => (release = () => resolve(saved))),
    );
    let menu = await openMoreActions();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    await vi.waitFor(() =>
      expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}")).toHaveLength(2),
    );
    menu = await openMoreActions();
    expect(within(menu).getByRole("menuitem", { name: "Syncing now…" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(within(menu).getByRole("menuitem", { name: "Delete…" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    fireEvent.keyDown(menu, { key: "Escape" });

    await act(async () => release());
    await vi.waitFor(() => expect(within(statusStrip()).queryByText("Syncing now…")).toBeNull());
    menu = await openMoreActions();
    expect(within(menu).getByRole("menuitem", { name: "Delete…" })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  it("reads the list again when Delete finds it changed, so the next Delete goes through", async () => {
    serverList("mdblist", MDBLIST);
    v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", undefined);
    showPage(SERVER_EDIT);
    await nameField();
    // A scheduled sync moves the list's revision while the editor is open.
    v2Recorder.bump("/api/v2/admin/collections/c1");
    const deleteOnce = async () => {
      const menu = await openMoreActions();
      fireEvent.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
      const dialog = await screen.findByRole("alertdialog");
      fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    };
    await deleteOnce();
    await vi.waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    await deleteOnce();
    await vi.waitFor(() =>
      expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(2),
    );
    const removes = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(removes[1]!.headers["If-Match"]).toBe(v2Recorder.etag("/api/v2/admin/collections/c1"));
  });

  it("hides the skipped count while what the sync read is changed", async () => {
    serverList("mdblist", MDBLIST);
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", syncRun());
    showPage(SERVER_EDIT);
    await nameField();
    const menu = await openMoreActions();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    expect(await within(statusStrip()).findByText("41 titles skipped")).toBeInTheDocument();

    // The 41 came from the saved Max titles, not this one.
    await setMaxTitles("20");
    expect(within(statusStrip()).getByText("Not counted yet")).toBeInTheDocument();
    fireEvent.click(within(saveBar()).getByRole("button", { name: "Discard" }));
    expect(await within(statusStrip()).findByText("41 titles skipped")).toBeInTheDocument();
  });

  it("drops the skipped count when a later Sync now fails", async () => {
    serverList("mdblist", MDBLIST);
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", syncRun());
    showPage(SERVER_EDIT);
    await nameField();
    let menu = await openMoreActions();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    expect(await within(statusStrip()).findByText("41 titles skipped")).toBeInTheDocument();

    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", () => {
      throw new Error("MDBList didn't answer.");
    });
    await vi.waitFor(() => expect(within(statusStrip()).queryByText("Syncing now…")).toBeNull());
    menu = await openMoreActions();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    // The 41 belonged to the run before; the failed one counted nothing.
    expect(await within(statusStrip()).findByText("Not counted yet")).toBeInTheDocument();
    expect(within(statusStrip()).queryByText("41 titles skipped")).toBeNull();
  });

  it("reads the list again after a failed Sync now, for its reason and token", async () => {
    const saved = serverList("mdblist", MDBLIST);
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", () => {
      // The server records the failed run on the collection, then answers an error.
      v2Recorder.bump("/api/v2/admin/collections/c1");
      v2Recorder.answer("GET /api/v2/admin/collections/{id}", {
        ...saved,
        last_sync_status: "failed",
        last_sync_message: "MDBList didn't answer.",
        last_sync_at: new Date().toISOString(),
      });
      throw new Error("MDBList didn't answer.");
    });
    showPage(SERVER_EDIT);
    await rename("Netflix Originals");

    const menu = await openMoreActions();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Sync now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("MDBList didn't answer.");

    const afterSync = v2Recorder.etag("/api/v2/admin/collections/c1");
    await save(ADMIN_PATCH);
    const [patch] = v2Recorder.callsOf(ADMIN_PATCH) as [RecordedCall];
    expect(v2Recorder.callsOf(ADMIN_PATCH)).toHaveLength(1);
    expect(patch.headers["If-Match"]).toBe(afterSync);
  });

  it("offers Sync now on a list whose stored status says running", async () => {
    // The server writes a sync's status when the run ends, so a stored
    // "running" is stale: it must not lock the editor.
    serverList("mdblist", MDBLIST, { last_sync_status: "running" });
    showPage(SERVER_EDIT);
    await nameField();
    expect(within(statusStrip()).queryByText("Syncing now…")).toBeNull();
    const menu = await openMoreActions();
    expect(within(menu).getByRole("menuitem", { name: "Sync now" })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  it("holds Sync now until changes to what the sync reads are saved", async () => {
    serverList("mdblist", MDBLIST, {
      last_sync_status: "failed",
      last_sync_message: "MDBList didn't answer.",
      last_sync_at: new Date(Date.now() - 6 * HOUR).toISOString(),
    });
    showPage(SERVER_EDIT);
    await rename("Netflix Originals");
    // A new name doesn't change what the sync reads.
    let menu = await openMoreActions();
    expect(within(menu).getByRole("menuitem", { name: /Sync now/ })).not.toHaveAttribute(
      "aria-disabled",
    );
    fireEvent.keyDown(menu, { key: "Escape" });

    // Sync now runs the saved list, not these unsaved Max titles.
    await setMaxTitles("20");
    menu = await openMoreActions();
    const item = within(menu).getByRole("menuitem", { name: /Sync now/ });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(item).toHaveTextContent("Save your changes first");
    fireEvent.keyDown(menu, { key: "Escape" });
    const alert = screen.getByRole("alert");
    expect(within(alert).getByRole("button", { name: "Sync now" })).toBeDisabled();
    expect(alert).toHaveTextContent("Save your changes first");
  });

  it("offers no Sync now when the server can't import lists", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
      ...adminCapabilities,
      imports: false,
    });
    serverList("mdblist", MDBLIST, {
      last_sync_status: "failed",
      last_sync_message: "MDBList didn't answer.",
      last_sync_at: new Date(Date.now() - 6 * HOUR).toISOString(),
    });
    showPage(SERVER_EDIT);
    const alert = await screen.findByRole("alert");
    // Sync answers 501 without import storage.
    expect(within(alert).queryByRole("button", { name: "Sync now" })).toBeNull();
    const menu = await openMoreActions();
    expect(within(menu).queryByRole("menuitem", { name: /Sync now/ })).toBeNull();
  });

  it("puts a failed sync at the top of the list, with the reason and Sync now", async () => {
    serverList("mdblist", MDBLIST, {
      last_sync_status: "failed",
      last_sync_message: "MDBList didn't answer.",
      last_sync_at: new Date(Date.now() - 6 * HOUR).toISOString(),
      item_count: 212,
    });
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", syncRun());
    showPage(SERVER_EDIT);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The last sync failed 6 hours ago.");
    expect(alert).toHaveTextContent("MDBList didn't answer.");
    expect(alert).toHaveTextContent("The collection keeps its 212 titles.");
    fireEvent.click(await within(alert).findByRole("button", { name: "Sync now" }));
    await vi.waitFor(() =>
      expect(v2Recorder.callsOf("POST /api/v2/admin/collections/{id}/sync")).toHaveLength(1),
    );
  });

  it("says what the list decides and what the admin decides", async () => {
    serverList("mdblist", MDBLIST);
    showPage(SERVER_EDIT);
    const list = await screen.findByRole("list", { name: "The list decides" });
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["Which titles are in it", "Their order, while the sort is “List order”"]);
    const you = screen.getByRole("list", { name: "You decide" });
    expect(
      within(you)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual([
      "Name, description and artwork",
      "Order, max titles and the schedule",
      "Where it shows",
    ]);
  });

  it("changes an MDBList link, and sends it without its query or fragment", async () => {
    serverList("mdblist", MDBLIST);
    showPage(SERVER_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change link" }));
    const link = screen.getByRole("textbox", { name: "MDBList link" });
    fireEvent.change(link, { target: { value: "https://mdblist.com/lists/u/top?sort=rank#x" } });
    expect(saveBar()).toHaveTextContent("List not saved");
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]).toMatchObject({
      source_url: "https://mdblist.com/lists/u/top",
      source_config: { mode: "mdblist_json", url: "https://mdblist.com/lists/u/top", limit: 50 },
    });
  });

  it("keeps Save off for a link that isn't an MDBList list", async () => {
    serverList("mdblist", MDBLIST);
    showPage(SERVER_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change link" }));
    fireEvent.change(screen.getByRole("textbox", { name: "MDBList link" }), {
      target: { value: "https://example.com/list" },
    });
    expect(saveButton()).toBeDisabled();
  });

  it("caps Max titles at 500, which sync can fill", async () => {
    serverList("mdblist", MDBLIST);
    showPage(SERVER_EDIT);
    expect(await setMaxTitles("501")).toHaveValue(500);
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]).toMatchObject({
      source_config: { mode: "mdblist_json", limit: 500 },
    });
  });

  it("edits a TMDB list link and stores the canonical list URL", async () => {
    serverList("tmdb", {
      source_url: "https://www.themoviedb.org/list/310",
      source_config: { mode: "tmdb_list", url: "https://www.themoviedb.org/list/310", limit: 40 },
    });
    showPage(SERVER_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change link" }));
    const link = screen.getByRole("textbox", { name: "TMDB list link" });
    expect(link).toHaveValue("https://www.themoviedb.org/list/310");
    fireEvent.change(link, { target: { value: "https://www.themoviedb.org/list/8649937-marvel" } });
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]).toMatchObject({
      collection_type: "tmdb",
      source_url: "https://www.themoviedb.org/list/8649937",
      source_config: {
        mode: "tmdb_list",
        url: "https://www.themoviedb.org/list/8649937",
        limit: 40,
      },
    });
  });

  it("keeps Save off for a TMDB link that isn't a list", async () => {
    serverList("tmdb", {
      source_url: "https://www.themoviedb.org/list/310",
      source_config: { mode: "tmdb_list", url: "https://www.themoviedb.org/list/310" },
    });
    showPage(SERVER_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change link" }));
    fireEvent.change(screen.getByRole("textbox", { name: "TMDB list link" }), {
      target: { value: "https://www.themoviedb.org/movie/550" },
    });
    expect(saveButton()).toBeDisabled();
  });

  it("changes a chart within what TMDB offers, and sends the whole chart", async () => {
    serverList("tmdb", {
      source_url: "tmdb://trending/movie/week",
      source_config: {
        mode: "tmdb_preset",
        preset: "trending",
        media_type: "movie",
        time_window: "week",
      },
    });
    showPage(SERVER_EDIT);
    expect(await screen.findByText("Trending movies, this week")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Change chart" }));
    const charts = screen.getByRole("radiogroup", { name: "Chart" });
    fireEvent.click(within(charts).getByRole("radio", { name: /^Now playing/ }));
    expect(screen.getByText("TMDB has this chart for movies only")).toBeInTheDocument();
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]).toMatchObject({
      source_url: "tmdb://now_playing/movie",
      source_config: { mode: "tmdb_preset", preset: "now_playing", media_type: "movie" },
    });
    expect(patches(ADMIN_PATCH)[0]!.source_config).not.toHaveProperty("time_window");
  });

  it("starts the link empty when a chart moves to a TMDB list", async () => {
    serverList("tmdb", {
      source_url: "tmdb://trending/all/day",
      source_config: { mode: "tmdb_preset", preset: "trending", media_type: "all" },
    });
    showPage(SERVER_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change chart" }));
    fireEvent.mouseDown(screen.getByRole("tab", { name: "TMDB list" }));
    const link = await screen.findByRole("textbox", { name: "TMDB list link" });
    expect(link).toHaveValue("");
    expect(link).not.toHaveAttribute("aria-invalid");
    expect(saveButton()).toBeDisabled();
  });

  it("changes a franchise list's TMDB collection ID", async () => {
    serverList("tmdb", {
      source_url: "tmdb://collection/10",
      source_config: { mode: "tmdb_collection", collection_id: 10, limit: 20 },
    });
    showPage(SERVER_EDIT);
    const id = await screen.findByRole("textbox", { name: "TMDB collection ID" });
    expect(id).toHaveValue("10");
    expect(screen.getByRole("link", { name: "Find the ID on themoviedb.org" })).toHaveAttribute(
      "href",
      "https://www.themoviedb.org/search/collection?query=Original",
    );
    fireEvent.change(id, { target: { value: "119" } });
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]).toMatchObject({
      collection_type: "tmdb",
      source_url: "tmdb://collection/119",
      source_config: { mode: "tmdb_collection", collection_id: 119, limit: 20 },
    });
  });

  it("asks for an ID on a franchise list that has none, and keeps Save off for one that isn't a number", async () => {
    serverList("tmdb", {
      source_url: "tmdb://collection/0",
      source_config: { mode: "tmdb_collection" },
    });
    showPage(SERVER_EDIT);
    const id = await screen.findByRole("textbox", { name: "TMDB collection ID" });
    expect(id).toHaveValue("");
    expect(
      screen.getByText(
        "This list doesn't follow a TMDB collection yet. Add its ID so it can sync.",
      ),
    ).toBeInTheDocument();
    fireEvent.change(id, { target: { value: "lotr" } });
    expect(saveButton()).toBeDisabled();
  });

  it("saves a new max titles on a franchise list that has no ID yet", async () => {
    serverList("tmdb", {
      source_url: "tmdb://collection/0",
      source_config: { mode: "tmdb_collection" },
    });
    showPage(SERVER_EDIT);
    await setMaxTitles("30");
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]!.source_config).toEqual({ mode: "tmdb_collection", limit: 30 });
    expect(patches(ADMIN_PATCH)[0]).not.toHaveProperty("source_url");
  });

  it("unticks a library that can't hold a newly picked chart's titles, and says why", async () => {
    serverList(
      "tmdb",
      {
        source_url: "tmdb://popular/movie",
        source_config: { mode: "tmdb_preset", preset: "popular", media_type: "movie" },
      },
      { library_ids: ["1", "2"] },
    );
    showPage(SERVER_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change chart" }));
    const charts = screen.getByRole("radiogroup", { name: "Chart" });
    fireEvent.click(within(charts).getByRole("radio", { name: /^Airing today/ }));
    expect(
      screen.getByText("This list only has TV shows, so Movies isn't offered."),
    ).toBeInTheDocument();
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]).toMatchObject({
      library_ids: ["2"],
      source_config: { mode: "tmdb_preset", preset: "airing_today", media_type: "tv" },
    });
  });

  it("puts the source card back on Discard, and focuses the field Change link opens", async () => {
    serverList("mdblist", MDBLIST);
    showPage(SERVER_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change link" }));
    const link = screen.getByRole("textbox", { name: "MDBList link" });
    await vi.waitFor(() => expect(link).toHaveFocus());
    fireEvent.change(link, { target: { value: "https://mdblist.com/lists/u/top" } });
    fireEvent.click(within(saveBar()).getByRole("button", { name: "Discard" }));
    expect(await screen.findByRole("button", { name: "Change link" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "MDBList link" })).toBeNull();
  });

  it("closes a custom schedule on Discard", async () => {
    serverList("mdblist", MDBLIST);
    showPage(SERVER_EDIT);
    choose(await screen.findByRole("combobox", { name: "Sync schedule" }), "Custom schedule…");
    fireEvent.change(screen.getByRole("textbox", { name: "Cron schedule" }), {
      target: { value: "15 4 * * *" },
    });
    fireEvent.click(within(saveBar()).getByRole("button", { name: "Discard" }));
    await vi.waitFor(() =>
      expect(screen.queryByRole("textbox", { name: "Cron schedule" })).toBeNull(),
    );
    expect(screen.getByRole("combobox", { name: "Sync schedule" })).toHaveTextContent(
      "No automatic sync",
    );
  });

  it("shows a Discover list's rules read-only and saves without its source", async () => {
    serverList("tmdb", {
      source_url: "tmdb://discover/movie",
      source_config: {
        mode: "tmdb_discover",
        media_type: "movie",
        discover: { sort_by: "popularity.desc", with_genres: [35] },
      },
    });
    showPage(SERVER_EDIT);
    expect(
      await screen.findByText("Made by a starter pack. Its rules can't be changed here."),
    ).toBeInTheDocument();
    expect(screen.getByText("Movies, most popular first")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Change (link|chart)/ })).toBeNull();
    await rename("Popular Comedy");
    await save(ADMIN_PATCH);
    const body = patches(ADMIN_PATCH)[0]!;
    expect(body.title).toBe("Popular Comedy");
    expect(body).not.toHaveProperty("source_url");
    expect(body).not.toHaveProperty("source_config");
  });

  it("keeps a Discover list's other rules when only its max titles change", async () => {
    const config = {
      mode: "tmdb_discover",
      media_type: "movie",
      discover: { sort_by: "popularity.desc" },
      limit: 40,
    };
    serverList("tmdb", { source_url: "tmdb://discover/movie", source_config: config });
    showPage(SERVER_EDIT);
    await setMaxTitles("60");
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]!.source_config).toEqual({ ...config, limit: 60 });
    expect(patches(ADMIN_PATCH)[0]).not.toHaveProperty("source_url");
  });

  it("leaves a Discover list's split to its locked summary", async () => {
    serverList("tmdb", {
      source_url: "tmdb://discover/movie",
      source_config: { mode: "tmdb_discover", media_type: "movie", discover: {}, limit: 40 },
    });
    showPage(SERVER_EDIT);
    await screen.findByText("Made by a starter pack. Its rules can't be changed here.");
    expect(screen.queryByRole("list", { name: "The list decides" })).toBeNull();
    expect(screen.queryByRole("list", { name: "You decide" })).toBeNull();
  });

  describe("a legacy Trakt list", () => {
    const TRAKT: Source = {
      source_url: "trakt://recommended/movie/p-owner",
      source_config: {
        mode: "trakt_preset",
        preset: "recommended",
        media_type: "movie",
        profile_id: "p-owner",
        limit: 40,
      },
    };

    it("shows its source read-only, locks libraries and max titles, and sends no source", async () => {
      serverList("trakt", TRAKT);
      showPage(SERVER_EDIT);
      expect(
        await screen.findByText(
          "New Trakt lists aren't supported. This one keeps its source and libraries.",
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByText(
          "You can still change its name, artwork, order, schedule and where it shows.",
        ),
      ).toBeInTheDocument();
      expect(screen.getByRole("spinbutton", { name: "Max titles" })).toBeDisabled();
      expect(screen.getByRole("spinbutton", { name: "Max titles" })).toHaveValue(40);
      expect(screen.getByRole("button", { name: /Match into/ })).toBeDisabled();
      await rename("For you");
      await save(ADMIN_PATCH);
      const body = patches(ADMIN_PATCH)[0]!;
      expect(body).toMatchObject({
        title: "For you",
        collection_type: "trakt",
        library_ids: ["1"],
      });
      expect(body).not.toHaveProperty("source_url");
      expect(body).not.toHaveProperty("source_config");
    });

    it("can't turn a schedule on, and says why", async () => {
      serverList("trakt", TRAKT);
      showPage(SERVER_EDIT);
      expect(await screen.findByRole("combobox", { name: "Sync schedule" })).toBeDisabled();
      expect(screen.getByText("A stopped Trakt list can't be scheduled again.")).toBeVisible();
    });

    it("can still change or stop a schedule it already has", async () => {
      serverList("trakt", TRAKT, { sync_schedule: "0 3 * * *" });
      showPage(SERVER_EDIT);
      const schedule = await screen.findByRole("combobox", { name: "Sync schedule" });
      expect(schedule).toBeEnabled();
      choose(schedule, "No automatic sync");
      await save(ADMIN_PATCH);
      expect(patches(ADMIN_PATCH)[0]).toMatchObject({ sync_schedule: "" });
    });
  });

  describe("artwork", () => {
    beforeEach(() => {
      serverList("mdblist", MDBLIST, {
        poster_url: "https://images.example/poster.png",
        backdrop_url: "https://images.example/backdrop.png",
      });
      v2Recorder.answer("DELETE /api/v2/admin/collections/{id}/image", undefined);
    });

    it.each(["poster", "backdrop"] as const)(
      "removes the %s after the PATCH on Save",
      async (slot) => {
        showPage(SERVER_EDIT);
        await rename("My draft");
        await chooseArtwork(slot, slot === "poster" ? "Use the collage" : "Remove backdrop");
        await act(async () => {});
        expect(v2Recorder.writes()).toEqual([]);
        await save(ADMIN_PATCH);
        await vi.waitFor(() =>
          expect(v2Recorder.writes().map((call) => call.operation)).toEqual([
            ADMIN_PATCH,
            "DELETE /api/v2/admin/collections/{id}/image",
          ]),
        );
        expect(v2Recorder.writes()[1]!.query).toEqual({ type: slot });
      },
    );

    it("keeps a removal that failed after the save staged, with Retry", async () => {
      v2Recorder.answer("DELETE /api/v2/admin/collections/{id}/image", () => {
        throw new Error("Storage unavailable");
      });
      showPage(SERVER_EDIT);
      await chooseArtwork("poster", "Use the collage");
      await save(ADMIN_PATCH);
      expect(await screen.findByRole("button", { name: "Retry" })).toBeInTheDocument();
      expect(v2Recorder.callsOf(ADMIN_PATCH)).toHaveLength(1);
    });
  });
});

describe("personal Synced list editor", () => {
  const PERSONAL_MDBLIST: Source = {
    source_url: "https://mdblist.com/lists/user/top-watched",
    source_config: { limit: 50, library_ids: [1] },
  };

  it("shows the schedule by name, never the raw cron, and sends a new one", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {
      sync_schedule: "17 4 * * *",
      sync_cadence: "daily",
    });
    showPage(PERSONAL_EDIT);
    const schedule = await screen.findByRole("combobox", { name: "Sync schedule" });
    expect(schedule).toHaveTextContent("Daily");
    expect(screen.queryByText(/17 4/)).toBeNull();
    // It opens once the server says a profile may change it.
    await vi.waitFor(() => expect(schedule).toBeEnabled());
    choose(schedule, "Weekly");
    expect(saveBar()).toHaveTextContent("Sync schedule not saved");
    await save(PERSONAL_PATCH);
    expect(patches(PERSONAL_PATCH)[0]).toEqual({
      name: "Rainy days",
      is_shared: false,
      library_ids: ["1"],
      include_in_server_collections: false,
      sync_schedule: "weekly",
    });
  });

  it("sends no libraries for a legacy Trakt list, which the server won't take", async () => {
    personalList(
      "trakt",
      {
        source_url: "trakt://recommended/movie/p-owner",
        source_config: { limit: 40, library_ids: [1] },
      },
      { sync_schedule: "17 4 * * *", sync_cadence: "daily" },
    );
    showPage(PERSONAL_EDIT);
    const schedule = await screen.findByRole("combobox", { name: "Sync schedule" });
    await vi.waitFor(() => expect(schedule).toBeEnabled());
    choose(schedule, "Weekly");
    await save(PERSONAL_PATCH);
    const body = patches(PERSONAL_PATCH)[0]!;
    expect(body).toMatchObject({ sync_schedule: "weekly" });
    expect(body).not.toHaveProperty("library_ids");
    expect(body).not.toHaveProperty("source_url");
    expect(body).not.toHaveProperty("max_items");
  });

  it("keeps a schedule it can't name until another is picked", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {
      sync_schedule: "0 */6 * * *",
      sync_cadence: "custom",
    });
    showPage(PERSONAL_EDIT);
    expect(await screen.findByRole("combobox", { name: "Sync schedule" })).toHaveTextContent(
      "Custom schedule (current)",
    );
    await rename("Top Watched");
    await save(PERSONAL_PATCH);
    expect(patches(PERSONAL_PATCH)[0]).not.toHaveProperty("sync_schedule");
  });

  it("locks the schedule, and says why, when the server can't change it", async () => {
    v2Recorder.answer("GET /api/v2/collections/capabilities", {
      ...personalCapabilities,
      sync_schedule_editable: false,
    });
    personalList("mdblist", PERSONAL_MDBLIST, {
      sync_schedule: "0 3 * * *",
      sync_cadence: "daily",
    });
    showPage(PERSONAL_EDIT);
    expect(
      await screen.findByText("This server doesn't let profiles change a list's schedule."),
    ).toBeVisible();
    expect(screen.getByRole("combobox", { name: "Sync schedule" })).toBeDisabled();
    // The profile no longer decides the schedule.
    const you = screen.getByRole("list", { name: "You decide" });
    expect(within(you).getByText("Order and max titles")).toBeInTheDocument();
    expect(within(you).queryByText(/schedule/)).toBeNull();
  });

  it("changes its MDBList link", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {});
    showPage(PERSONAL_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change link" }));
    fireEvent.change(screen.getByRole("textbox", { name: "MDBList link" }), {
      target: { value: "https://mdblist.com/lists/user/new-list/" },
    });
    await save(PERSONAL_PATCH);
    expect(patches(PERSONAL_PATCH)[0]).toMatchObject({
      source_url: "https://mdblist.com/lists/user/new-list",
    });
  });

  it("shows the chart it follows as text, set when it was made", async () => {
    personalList(
      "tmdb",
      {
        source_url: "tmdb://trending/movie/week",
        source_config: { preset: "trending", media_type: "movie", time_window: "week" },
      },
      {},
    );
    showPage(PERSONAL_EDIT);
    expect(await screen.findByText("TMDB chart: Trending movies, this week")).toBeInTheDocument();
    expect(screen.getByText("set when it was made")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Change (link|chart)/ })).toBeNull();
  });

  // Spec §3.1: personal editors have no Sync now; the Collections page card has it.
  it("has no Sync now in More actions", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {});
    showPage(PERSONAL_EDIT);
    const menu = await openMoreActions();
    expect(within(menu).getByRole("menuitem", { name: "Delete…" })).toBeInTheDocument();
    expect(within(menu).queryByRole("menuitem", { name: /Sync/ })).toBeNull();
    fireEvent.click(within(statusStrip()).getByRole("button", { name: "Why titles are skipped" }));
    expect(screen.queryByText(/Sync now to count them/)).toBeNull();
  });

  it("shows a failed sync's reason without Sync now", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {
      last_sync_status: "failed",
      last_sync_message: "MDBList didn't answer.",
    });
    showPage(PERSONAL_EDIT);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("MDBList didn't answer.");
    expect(within(alert).queryByRole("button")).toBeNull();
  });
});
