import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { components } from "@/api/v2/schema";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";

import { STARTER_PACK_BLOCKS_DELETE } from "@/lib/collections/copy";

import AdminCollections from "./AdminCollections";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 1, name: "Movies" },
      { id: 2, name: "Kids" },
      { id: 3, name: "TV Shows" },
    ],
  }),
}));
/** Starter pack jobs the page sees; `set` re-renders it as a job starts. */
const applyJobs = vi.hoisted(() => {
  let jobs: unknown[] = [];
  const listeners = new Set<() => void>();
  return {
    get: () => jobs,
    set(next: unknown[]) {
      jobs = next;
      listeners.forEach((listener) => listener());
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => void listeners.delete(listener);
    },
  };
});
vi.mock("@/hooks/queries/admin/taskJobs", async () => {
  const { useSyncExternalStore } = await import("react");
  return {
    useAdminTaskJobs: () => ({
      data: useSyncExternalStore(applyJobs.subscribe, applyJobs.get),
    }),
  };
});
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: vi.fn() }));
vi.mock("@/components/collections/StarterPacksDialog", () => ({
  StarterPacksDialog: () => null,
}));

installV2Recorder();

type AdminCollection = components["schemas"]["AdminCollection"];

function stored(title: string, overrides: Partial<AdminCollection> = {}): AdminCollection {
  return {
    id: title.toLowerCase().replace(/\W+/g, "-"),
    library_id: "1",
    library_ids: ["1"],
    slug: "",
    title,
    description: "",
    collection_type: "manual",
    visibility: "visible",
    sort_order: 0,
    group_id: null,
    featured: false,
    poster_url: "",
    poster_is_collage: false,
    backdrop_url: "",
    source_url: "",
    query_definition: {},
    sort_config: {},
    source_config: {},
    management_mode: "manual",
    management_source: "",
    management_key: "",
    last_sync_status: "",
    last_sync_message: "",
    item_count: 0,
    home_row_count: 0,
    row_count: 0,
    created_at: "2026-01-02T03:04:05.000Z",
    updated_at: "2026-01-02T03:04:05.000Z",
    ...overrides,
  };
}

let items: AdminCollection[];

function Where() {
  const location = useLocation();
  return <output aria-label="Location">{`${location.pathname}${location.search}`}</output>;
}

function renderPage(path = "/admin/collections", client = new QueryClient()) {
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route
            path="/admin/collections"
            element={
              <>
                <AdminCollections />
                <Where />
              </>
            }
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const location = () => screen.getByRole("status", { name: "Location" }).textContent;
const bar = () => screen.getByRole("group", { name: "Selected collections" });
const pathsOf = (operation: string) => v2Recorder.callsOf(operation).map((call) => call.path);

async function enterSelectMode(user = userEvent.setup()) {
  await screen.findByText("Studio Ghibli");
  await user.click(screen.getByRole("button", { name: "More" }));
  await user.click(await screen.findByRole("menuitem", { name: "Select collections" }));
  return user;
}

async function pick(user: ReturnType<typeof userEvent.setup>, ...titles: string[]) {
  for (const title of titles)
    await user.click(screen.getByRole("checkbox", { name: `Select ${title}` }));
}

beforeEach(() => {
  applyJobs.set([]);
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      readonly root = null;
      readonly rootMargin = "0px";
      readonly thresholds = [0];
      observe = () => undefined;
      unobserve = () => undefined;
      disconnect = () => undefined;
      takeRecords = () => [];
    },
  );
  items = [
    stored("Best Picture Winners", {
      collection_type: "mdblist",
      library_ids: ["1", "2"],
      item_count: 96,
      home_row_count: 1,
      row_count: 1,
    }),
    stored("Christmas Classics", { collection_type: "smart", item_count: 41 }),
    stored("Netflix Originals", {
      collection_type: "tmdb",
      library_ids: ["1", "3"],
      item_count: 182,
    }),
    stored("Staff Picks", { visibility: "hidden", row_count: 2, item_count: 14 }),
    stored("Studio Ghibli", { library_ids: ["1", "2"], item_count: 23 }),
    stored("Trending This Week", { collection_type: "tmdb", item_count: 20, row_count: 2 }),
  ];
  v2Recorder.answer("GET /api/v2/admin/collections", () => ({ items, groups: [] }));
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
    groups: true,
    imports: true,
    import_sources: [],
    artwork: true,
    item_reorder: true,
    section_references: true,
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", (call: RecordedCall) =>
    items.find((entry) => call.path.endsWith(`/${entry.id}`)),
  );
  v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", undefined);
  v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", {
    status: "success",
    message: "",
    items_matched: 3,
  });
});

describe("AdminCollections More", () => {
  it("holds the page's rare actions, each with one line of help", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Studio Ghibli");
    await user.click(screen.getByRole("button", { name: "More" }));
    const menu = await screen.findByRole("menu");
    const entries = within(menu).getAllByRole("menuitem");
    expect(entries.map((entry) => entry.getAttribute("aria-describedby") !== null)).toEqual([
      true,
      true,
      true,
    ]);
    // Templates are suggestions inside New collection's Synced list step.
    expect(entries.map((entry) => entry.textContent)).toEqual([
      "Starter packs…Add a ready-made set of collections to a library.",
      "Select collectionsSync, show, hide or delete several at once.",
      "Delete all in this view…Every collection the current filters show.",
    ]);
  });
});

describe("AdminCollections Select collections", () => {
  it("puts a checkbox on each row, focuses Select all, and counts what's picked", async () => {
    renderPage();
    const user = await enterSelectMode();
    const selectAll = screen.getByRole("checkbox", { name: "Select all" });
    await waitFor(() => expect(selectAll).toHaveFocus());
    expect(screen.getByText("Up to 100 collections at a time")).toBeInTheDocument();
    // In select mode the bar holds the actions; rows have no ⋯.
    expect(screen.queryByRole("button", { name: "More for Studio Ghibli" })).toBeNull();

    await pick(user, "Netflix Originals", "Best Picture Winners", "Christmas Classics");
    expect(within(bar()).getByRole("status")).toHaveTextContent("3 selected");
    expect(within(bar()).getByRole("button", { name: "Sync 2 lists" })).toBeEnabled();
    expect(screen.getByText("Sync skips smart collections (1 here).")).toBeInTheDocument();

    await pick(user, "Studio Ghibli");
    expect(
      screen.getByText("Sync skips smart and manual collections (2 here)."),
    ).toBeInTheDocument();

    await user.click(selectAll);
    expect(within(bar()).getByRole("status")).toHaveTextContent("6 selected");
    expect(within(bar()).getByRole("button", { name: "Sync 3 lists" })).toBeEnabled();
  });

  it("can't sync when only smart and manual collections are picked", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Christmas Classics");
    expect(within(bar()).getByRole("button", { name: "Sync 0 lists" })).toBeDisabled();
    expect(within(bar()).getByRole("button", { name: "Hide from tabs" })).toBeEnabled();
  });

  it("offers no Sync when the server can't import lists", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
      groups: true,
      imports: false,
      import_sources: [],
      artwork: true,
      item_reorder: true,
      section_references: true,
    });
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Christmas Classics");
    expect(within(bar()).getByRole("status")).toHaveTextContent("2 selected");
    expect(within(bar()).queryByRole("button", { name: /^Sync/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/^Sync skips/)).not.toBeInTheDocument();
    expect(within(bar()).getByRole("button", { name: "Hide from tabs" })).toBeEnabled();
  });

  it("refuses more than 100 at a time", async () => {
    items = Array.from({ length: 101 }, (_, index) => stored(`List ${index + 1}`));
    items.push(stored("Studio Ghibli"));
    renderPage();
    const user = await enterSelectMode();
    await user.click(screen.getByRole("checkbox", { name: "Select all" }));
    expect(within(bar()).getByRole("status")).toHaveTextContent(
      "102 selectedSelect up to 100 collections at a time.",
    );
    expect(within(bar()).getByRole("button", { name: "Hide from tabs" })).toBeDisabled();
  });

  it("syncs only the synced lists picked, and says how many synced", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Christmas Classics", "Netflix Originals");
    await user.click(within(bar()).getByRole("button", { name: "Sync 2 lists" }));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Synced 2 lists."));
    expect(pathsOf("POST /api/v2/admin/collections/{id}/sync").sort()).toEqual([
      "/api/v2/admin/collections/best-picture-winners/sync",
      "/api/v2/admin/collections/netflix-originals/sync",
    ]);
  });

  it("keeps at most four syncs in flight", async () => {
    items = Array.from({ length: 7 }, (_, index) =>
      stored(`Chart ${index + 1}`, { collection_type: "tmdb" }),
    );
    items.push(stored("Studio Ghibli"));
    const answers: Array<() => void> = [];
    let inFlight = 0;
    let most = 0;
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", () => {
      inFlight++;
      most = Math.max(most, inFlight);
      return new Promise((resolve) =>
        answers.push(() => {
          inFlight--;
          resolve({ status: "success", message: "", items_matched: 1 });
        }),
      );
    });
    renderPage();
    const user = await enterSelectMode();
    await user.click(screen.getByRole("checkbox", { name: "Select all" }));
    await user.click(within(bar()).getByRole("button", { name: "Sync 7 lists" }));
    await waitFor(() => expect(answers).toHaveLength(4));
    // Busy while it runs: a second press can't start another batch.
    expect(within(bar()).getByRole("button", { name: "Sync 7 lists" })).toBeDisabled();
    while (answers.length > 0 || inFlight > 0) {
      await act(async () => answers.shift()?.());
    }
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Synced 7 lists."));
    expect(v2Recorder.callsOf("POST /api/v2/admin/collections/{id}/sync")).toHaveLength(7);
    expect(most).toBe(4);
  });

  it("says how many synced with warnings", async () => {
    vi.mocked(toast.success).mockClear();
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", (call: RecordedCall) => ({
      status: call.path.includes("netflix") ? "warning" : "success",
      message: "",
      items_matched: 3,
    }));
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Netflix Originals");
    await user.click(within(bar()).getByRole("button", { name: "Sync 2 lists" }));
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith("Synced 2 lists, 1 with warnings."),
    );
    expect(toast.success).not.toHaveBeenCalledWith("Synced 2 lists.");
  });

  it("reports a partial failure, naming each list it couldn't sync", async () => {
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", (call: RecordedCall) => {
      if (call.path.includes("netflix")) throw new Error("TMDB answered 401");
      return { status: "success", message: "", items_matched: 3 };
    });
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Netflix Originals");
    await user.click(within(bar()).getByRole("button", { name: "Sync 2 lists" }));
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith("Synced 1 of 2 lists.", {
        description: "Netflix Originals: TMDB answered 401",
      }),
    );
  });

  it("shows only the hidden ones it picked, each with its type and a fresh ETag", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Staff Picks", "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Show on tabs" }));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Showed 1 collection on Collections tabs."),
    );
    const patches = v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}");
    expect(patches).toHaveLength(1);
    expect(patches[0]!.path).toBe("/api/v2/admin/collections/staff-picks");
    expect(patches[0]!.headers["If-Match"]).toBe('"/api/v2/admin/collections/staff-picks#1"');
    expect(patches[0]!.body).toEqual({ collection_type: "manual", visibility: "visible" });
  });

  it("says so when everything picked is already shown", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Show on tabs" }));
    expect(toast.success).toHaveBeenCalledWith(
      "The selected collections are already on Collections tabs.",
    );
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("hides at once when no row uses what it hides", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli", "Christmas Classics", "Staff Picks");
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Hid 2 collections from Collections tabs."),
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(pathsOf("PATCH /api/v2/admin/collections/{id}").sort()).toEqual([
      "/api/v2/admin/collections/christmas-classics",
      "/api/v2/admin/collections/studio-ghibli",
    ]);
  });

  it("asks first with the rows' total when rows use any it hides, and Cancel sends nothing", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Trending This Week", "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    const dialog = await screen.findByRole("alertdialog", {
      name: "Hide 3 collections from Collections tabs?",
    });
    expect(dialog).toHaveTextContent(
      "3 rows still show them, but those rows' See all won't open while they're hidden.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(v2Recorder.writes()).toEqual([]);

    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Hide" }),
    );
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(3),
    );
  });

  it("reads the rows that use them afresh, so a row added since still asks first", async () => {
    renderPage();
    const user = await enterSelectMode();
    // Another admin adds a row showing Studio Ghibli after the List was read.
    items = items.map((entry) =>
      entry.title === "Studio Ghibli" ? { ...entry, row_count: 1 } : entry,
    );
    await pick(user, "Studio Ghibli", "Christmas Classics");
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    expect(
      await screen.findByRole("alertdialog", { name: "Hide 2 collections from Collections tabs?" }),
    ).toHaveTextContent("1 row still shows one of them");
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("hides nothing and says so when the rows that use them can't be read", async () => {
    vi.mocked(toast.error).mockClear();
    renderPage(
      "/admin/collections",
      new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    );
    const user = await enterSelectMode();
    v2Recorder.answer("GET /api/v2/admin/collections", () => {
      throw new Error("Server unavailable");
    });
    await pick(user, "Studio Ghibli", "Christmas Classics");
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("uses the one collection's own wording when only one would hide", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Staff Picks");
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    expect(
      await screen.findByRole("alertdialog", {
        name: "Hide Best Picture Winners from Collections tabs?",
      }),
    ).toHaveTextContent(
      "It leaves Movies › Collections and Kids › Collections. 1 row still shows it, but its See all won't open while it's hidden.",
    );
  });

  it("deletes only what rows don't use, and names the ones it keeps", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Netflix Originals", "Trending This Week", "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete 2 collections?" });
    expect(dialog).toHaveTextContent(
      "They're removed for everyone. Their titles stay in your libraries.",
    );
    expect(dialog).toHaveTextContent("1 is kept because rows use it: Trending This Week.");
    expect(dialog).toHaveTextContent("This can't be undone.");
    await user.click(within(dialog).getByRole("button", { name: "Delete 2" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());

    const removes = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(removes.map((call) => call.path).sort()).toEqual([
      "/api/v2/admin/collections/netflix-originals",
      "/api/v2/admin/collections/studio-ghibli",
    ]);
    for (const call of removes) expect(call.headers["If-Match"]).toBe(`"${call.path}#1"`);
  });

  it("deletes nothing and says why when rows use everything picked", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Trending This Week", "Staff Picks");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    expect(toast.error).toHaveBeenCalledWith("Rows still use them. Remove the rows first.");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("leaves on Escape from the list and gives focus back to More", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("group", { name: "Selected collections" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "Select Studio Ghibli" })).toBeNull();
    await waitFor(() => expect(screen.getByRole("button", { name: "More" })).toHaveFocus());
  });

  it("stays in select mode when Escape only closes a menu", async () => {
    renderPage();
    const user = await enterSelectMode();
    await user.click(screen.getByRole("button", { name: "More" }));
    await screen.findByRole("menu");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(screen.getByRole("group", { name: "Selected collections" })).toBeInTheDocument();
  });

  it("leaves with Done", async () => {
    renderPage();
    const user = await enterSelectMode();
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("checkbox", { name: "Select all" })).toBeNull();
  });

  it("opens from Arrange on that library's List", async () => {
    v2Recorder.answer("GET /api/v2/admin/libraries/{library_id}/collection-groups", {
      items: [],
      ungrouped_sort_order: 0,
    });
    const order = { library_id: "2", group_id: "", ordered_ids: ["ungrouped"], has_more: false };
    v2Recorder.answer("GET /api/v2/admin/libraries/{library_id}/collection-groups/order", order);
    v2Recorder.answer("GET /api/v2/admin/collection-groups/{group_id}/collections/order", {
      ...order,
      ordered_ids: [],
    });
    renderPage("/admin/collections?view=arrange&libraryId=2");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "More" }));
    await user.click(await screen.findByRole("menuitem", { name: "Select collections" }));
    await waitFor(() => expect(location()).toBe("/admin/collections?view=list&libraryId=2"));
    expect(await screen.findByRole("checkbox", { name: "Select all" })).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Select Studio Ghibli" })).toBeInTheDocument();
  });
});

describe("AdminCollections Select collections with nothing to pick", () => {
  const onlyTvShows = () => items.filter((entry) => entry.library_ids.includes("3"));
  const notTvShows = (entry: AdminCollection) => !entry.library_ids.includes("3");

  it("can't open on a library with no collections", async () => {
    items = items.filter(notTvShows);
    renderPage("/admin/collections?view=list&libraryId=3");
    const user = userEvent.setup();
    await screen.findByText("No collections in TV Shows yet");
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(await screen.findByRole("menuitem", { name: "Select collections" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("leaves when the library switches to one with no collections", async () => {
    items = items.filter(notTvShows);
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli");
    await user.click(screen.getByRole("button", { name: /^TV Shows/ }));
    await screen.findByText("No collections in TV Shows yet");
    expect(screen.queryByRole("group", { name: "Selected collections" })).toBeNull();

    // Coming back to a library with collections doesn't bring select mode back.
    await user.click(screen.getByRole("button", { name: /^Movies/ }));
    await screen.findByText("Studio Ghibli");
    expect(screen.queryByRole("checkbox", { name: "Select Studio Ghibli" })).toBeNull();
  });

  it("leaves once the last ones are deleted, and gives focus back to More", async () => {
    v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", (call: RecordedCall) => {
      items = items.filter((entry) => !call.path.endsWith(`/${entry.id}`));
      return undefined;
    });
    renderPage("/admin/collections?view=list&libraryId=3");
    const user = userEvent.setup();
    await screen.findByText("Netflix Originals");
    expect(onlyTvShows()).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "More" }));
    await user.click(await screen.findByRole("menuitem", { name: "Select collections" }));
    await pick(user, "Netflix Originals");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Delete 1" }));
    await screen.findByText("No collections in TV Shows yet");
    expect(screen.queryByRole("group", { name: "Selected collections" })).toBeNull();
    await waitFor(() => expect(screen.getByRole("button", { name: "More" })).toHaveFocus());
  });
});

describe("AdminCollections Delete all while select mode works", () => {
  it("waits for a row's own switch to save", async () => {
    let finish!: () => void;
    v2Recorder.answer(
      "PATCH /api/v2/admin/collections/{id}",
      () => new Promise<undefined>((resolve) => (finish = () => resolve(undefined))),
    );
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli");
    await user.click(
      screen.getByRole("switch", {
        name: "Show Studio Ghibli on the Movies and Kids Collections tabs",
      }),
    );
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(1),
    );
    // A bar action now would read the ETag the switch's PATCH is about to change.
    expect(within(bar()).getByRole("button", { name: "Show on tabs" })).toBeDisabled();
    expect(within(bar()).getByRole("button", { name: "Delete…" })).toBeDisabled();
    await act(async () => finish());
    await waitFor(() =>
      expect(within(bar()).getByRole("button", { name: "Show on tabs" })).toBeEnabled(),
    );
  });

  it("holds a list's own switch while it syncs", async () => {
    let finish!: () => void;
    v2Recorder.answer(
      "POST /api/v2/admin/collections/{id}/sync",
      () =>
        new Promise((resolve) => {
          finish = () => resolve({ status: "success", message: "", items_matched: 3 });
        }),
    );
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Netflix Originals");
    await user.click(within(bar()).getByRole("button", { name: "Sync 1 list" }));
    await waitFor(() =>
      expect(v2Recorder.callsOf("POST /api/v2/admin/collections/{id}/sync")).toHaveLength(1),
    );
    // A visibility PATCH now would carry the ETag the sync is about to move.
    const toggle = screen.getByRole("switch", { name: /^Show Netflix Originals on/ });
    expect(toggle).toBeDisabled();
    await act(async () => finish());
    await waitFor(() => expect(toggle).toBeEnabled());
  });

  it("waits for a picked list's own sync from its row", async () => {
    let finish!: () => void;
    v2Recorder.answer(
      "POST /api/v2/admin/collections/{id}/sync",
      () =>
        new Promise((resolve) => {
          finish = () => resolve({ status: "success", message: "", items_matched: 3 });
        }),
    );
    renderPage();
    const user = userEvent.setup();
    await screen.findByText("Netflix Originals");
    await user.click(screen.getByRole("button", { name: "More for Netflix Originals" }));
    await user.click(await screen.findByRole("menuitem", { name: "Sync now" }));
    await waitFor(() =>
      expect(v2Recorder.callsOf("POST /api/v2/admin/collections/{id}/sync")).toHaveLength(1),
    );
    await enterSelectMode(user);
    await pick(user, "Netflix Originals");
    // A bar action now would read the ETag the sync is about to move.
    expect(within(bar()).getByRole("button", { name: "Hide from tabs" })).toBeDisabled();
    expect(within(bar()).getByRole("button", { name: "Sync 1 list" })).toBeDisabled();
    await act(async () => finish());
    await waitFor(() =>
      expect(within(bar()).getByRole("button", { name: "Hide from tabs" })).toBeEnabled(),
    );
  });

  it("waits for a Show or Hide to finish", async () => {
    let finish!: () => void;
    v2Recorder.answer(
      "PATCH /api/v2/admin/collections/{id}",
      () => new Promise<undefined>((resolve) => (finish = () => resolve(undefined))),
    );
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Staff Picks");
    await user.click(within(bar()).getByRole("button", { name: "Show on tabs" }));
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(1),
    );
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(
      await screen.findByRole("menuitem", { name: "Delete all in this view…" }),
    ).toHaveAttribute("aria-disabled", "true");
    await user.keyboard("{Escape}");

    await act(async () => finish());
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Showed 1 collection on Collections tabs."),
    );
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(
      await screen.findByRole("menuitem", { name: "Delete all in this view…" }),
    ).not.toHaveAttribute("aria-disabled");
  });
});

describe("AdminCollections Select collections delete", () => {
  it("reads the rows that use them afresh, so a row removed since still lets it go", async () => {
    renderPage();
    const user = await enterSelectMode();
    // Another admin removes Trending This Week's rows after the List was read.
    items = items.map((entry) =>
      entry.title === "Trending This Week" ? { ...entry, row_count: 0 } : entry,
    );
    await pick(user, "Trending This Week", "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete 2 collections?" });
    expect(dialog).not.toHaveTextContent("kept because rows use");
  });

  it("doesn't name the library when deleting a selection", async () => {
    renderPage("/admin/collections?view=list&libraryId=1");
    const user = await enterSelectMode();
    await pick(user, "Netflix Originals", "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    // Deleting a collection removes it from every library, not just Movies.
    expect(
      await screen.findByRole("alertdialog", { name: "Delete 2 collections?" }),
    ).toBeInTheDocument();
  });

  it("holds the confirm open, and says why, when a starter pack starts adding", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    const name = "Delete 1 manual collection?";
    const dialog = await screen.findByRole("alertdialog", { name });
    act(() =>
      applyJobs.set([
        {
          id: "job-1",
          job_type: "template_bundle_apply",
          status: "running",
          message: "Adding",
          requested_at: new Date().toISOString(),
        },
      ]),
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(STARTER_PACK_BLOCKS_DELETE);
    const confirm = within(dialog).getByRole("button", { name: "Delete 1" });
    expect(confirm).toBeDisabled();
    await user.click(confirm);
    expect(screen.getByRole("alertdialog", { name })).toBeInTheDocument();
    expect(pathsOf("DELETE /api/v2/admin/collections/{id}")).toEqual([]);
  });
});

describe("AdminCollections Delete all in this view", () => {
  it("counts only what goes, names the lists it keeps, and where shared lists also go", async () => {
    renderPage("/admin/collections?view=list&libraryId=1&type=synced");
    const user = userEvent.setup();
    await screen.findByText("Netflix Originals");
    await user.click(screen.getByRole("button", { name: "More" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete all in this view…" }));
    const dialog = await screen.findByRole("alertdialog", {
      name: "Delete 1 synced list in Movies?",
    });
    expect(dialog).toHaveTextContent(
      "It's removed for everyone. Its titles stay in your libraries.",
    );
    expect(dialog).toHaveTextContent(
      "A synced list that's also in another library goes there too: Netflix Originals (TV Shows).",
    );
    expect(dialog).toHaveTextContent(
      "2 are kept because rows use them: Best Picture Winners and Trending This Week.",
    );
    // Only what will go is read for its ETag.
    expect(pathsOf("GET /api/v2/admin/collections/{id}")).toEqual([
      "/api/v2/admin/collections/netflix-originals",
    ]);
    await user.click(within(dialog).getByRole("button", { name: "Delete 1" }));
    await waitFor(() =>
      expect(pathsOf("DELETE /api/v2/admin/collections/{id}")).toEqual([
        "/api/v2/admin/collections/netflix-originals",
      ]),
    );
  });

  it("says in this view when no library is picked", async () => {
    renderPage("/admin/collections?q=studio");
    const user = userEvent.setup();
    await screen.findByText("Studio Ghibli");
    await user.click(screen.getByRole("button", { name: "More" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete all in this view…" }));
    expect(
      await screen.findByRole("alertdialog", { name: "Delete 1 manual collection in this view?" }),
    ).toBeInTheDocument();
  });
});
