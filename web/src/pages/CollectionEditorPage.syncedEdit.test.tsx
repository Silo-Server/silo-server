/**
 * Editing a saved Synced list on the collection editor page, both scopes: the
 * list it follows (a link, a chart, a TMDB collection ID, or a locked summary
 * for Discover lists), its sync status and Sync now, and the
 * schedule a profile picks by name.
 */
import { act, fireEvent, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  adminCapabilities,
  adminCollectionList,
  adminSyncedCollection,
  personalCapabilities,
  personalSyncedCollection,
} from "@/test/fixtures/collectionAnswers";
import { chooseArtwork } from "@/test/collectionArtwork";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import {
  choose,
  nameField,
  prepareEditorPage,
  saveBar,
  showEditorPage,
} from "./collectionEditorPage.test-support";

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
beforeEach(prepareEditorPage);

type Source = { source_url: string; source_config: Record<string, unknown> };

/** A saved server Synced list, as the editor reads it and as the list shows it. */
function serverList(type: "mdblist" | "tmdb", source: Source, extra: Record<string, unknown> = {}) {
  const collection = { ...adminSyncedCollection(type, source), ...extra };
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", collection);
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(collection));
  return collection;
}

function personalList(type: "mdblist" | "tmdb", source: Source, extra: Record<string, unknown>) {
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

const SERVER_EDIT = "/admin/collections/c1/edit?libraryId=1";
const PERSONAL_EDIT = "/collections/c1/edit";
const ADMIN_PATCH = "PATCH /api/v2/admin/collections/{id}";
const PERSONAL_PATCH = "PATCH /api/v2/collections/{id}";

const patches = (operation: string) =>
  v2Recorder.callsOf(operation).map((call) => call.body as Record<string, unknown>);

async function rename(name: string) {
  fireEvent.change(await nameField(), { target: { value: name } });
}

function saveButton() {
  return within(saveBar()).getByRole("button", { name: "Save" });
}

async function save(operation: string, count = 1) {
  fireEvent.click(saveButton());
  await vi.waitFor(() => expect(v2Recorder.callsOf(operation)).toHaveLength(count));
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
  it("disables Sync now while a sync runs, then saves with the token after the sync", async () => {
    serverList("mdblist", MDBLIST);
    let finish: (value: unknown) => void = () => {};
    v2Recorder.answer(
      "POST /api/v2/admin/collections/{id}/sync",
      () => new Promise((resolve) => (finish = resolve)),
    );
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
    const alert = await screen.findByRole("alert");
    // Sync answers 501 without import storage.
    expect(within(alert).queryByRole("button", { name: "Sync now" })).toBeNull();
    const menu = await openMoreActions();
    expect(within(menu).queryByRole("menuitem", { name: /Sync now/ })).toBeNull();
  });

  it("changes an MDBList link, and sends it without its query or fragment", async () => {
    serverList("mdblist", MDBLIST);
    showEditorPage(SERVER_EDIT);
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

  it("keeps Max titles above 500", async () => {
    serverList("mdblist", MDBLIST);
    showEditorPage(SERVER_EDIT);
    expect(await setMaxTitles("10000")).toHaveValue(10000);
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]).toMatchObject({
      source_config: { mode: "mdblist_json", limit: 10000 },
    });
  });

  it("saves a new max titles on a franchise list that has no ID yet", async () => {
    serverList("tmdb", {
      source_url: "tmdb://collection/0",
      source_config: { mode: "tmdb_collection" },
    });
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
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

  it("shows a Discover list's rules read-only and saves without its source", async () => {
    serverList("tmdb", {
      source_url: "tmdb://discover/movie",
      source_config: {
        mode: "tmdb_discover",
        media_type: "movie",
        discover: { sort_by: "popularity.desc", with_genres: [35] },
      },
    });
    showEditorPage(SERVER_EDIT);
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
    showEditorPage(SERVER_EDIT);
    await setMaxTitles("60");
    await save(ADMIN_PATCH);
    expect(patches(ADMIN_PATCH)[0]!.source_config).toEqual({ ...config, limit: 60 });
    expect(patches(ADMIN_PATCH)[0]).not.toHaveProperty("source_url");
  });

  describe("artwork", () => {
    beforeEach(() => {
      serverList("mdblist", MDBLIST, {
        poster_url: "https://images.example/poster.png",
        backdrop_url: "https://images.example/backdrop.png",
      });
      v2Recorder.answer("DELETE /api/v2/admin/collections/{id}/image", undefined);
    });

    it("keeps a removal that failed after the save staged, with Retry", async () => {
      v2Recorder.answer("DELETE /api/v2/admin/collections/{id}/image", () => {
        throw new Error("Storage unavailable");
      });
      showEditorPage(SERVER_EDIT);
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
    showEditorPage(PERSONAL_EDIT);
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

  it("keeps a schedule it can't name until another is picked", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {
      sync_schedule: "0 */6 * * *",
      sync_cadence: "custom",
    });
    showEditorPage(PERSONAL_EDIT);
    expect(await screen.findByRole("combobox", { name: "Sync schedule" })).toHaveTextContent(
      "Custom schedule (current)",
    );
    await rename("Top Watched");
    await save(PERSONAL_PATCH);
    expect(patches(PERSONAL_PATCH)[0]).not.toHaveProperty("sync_schedule");
  });

  it("changes its MDBList link", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {});
    showEditorPage(PERSONAL_EDIT);
    fireEvent.click(await screen.findByRole("button", { name: "Change link" }));
    fireEvent.change(screen.getByRole("textbox", { name: "MDBList link" }), {
      target: { value: "https://mdblist.com/lists/user/new-list/" },
    });
    await save(PERSONAL_PATCH);
    expect(patches(PERSONAL_PATCH)[0]).toMatchObject({
      source_url: "https://mdblist.com/lists/user/new-list",
    });
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
    showEditorPage(PERSONAL_EDIT);
    expect(
      await screen.findByText("This server doesn't let profiles change a list's schedule."),
    ).toBeVisible();
    expect(screen.getByRole("combobox", { name: "Sync schedule" })).toBeDisabled();
    // The profile no longer decides the schedule.
    const you = screen.getByRole("list", { name: "You decide" });
    expect(within(you).getByText("Order and max titles")).toBeInTheDocument();
    expect(within(you).queryByText(/schedule/)).toBeNull();
  });

  // Spec §3.1: personal editors have no Sync now; the Collections page card has it.
  it("has no Sync now in More actions", async () => {
    personalList("mdblist", PERSONAL_MDBLIST, {});
    showEditorPage(PERSONAL_EDIT);
    const menu = await openMoreActions();
    expect(within(menu).getByRole("menuitem", { name: "Delete…" })).toBeInTheDocument();
    expect(within(menu).queryByRole("menuitem", { name: /Sync/ })).toBeNull();
    fireEvent.click(within(statusStrip()).getByRole("button", { name: "Why titles are skipped" }));
    expect(screen.queryByText(/Sync now to count them/)).toBeNull();
  });
});
