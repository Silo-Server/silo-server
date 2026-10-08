import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { components } from "@/api/v2/schema";
import { PEEK_MAX_IN_FLIGHT, PEEK_STALE_MS } from "@/components/calm/usePeekLimiter";
import { SHOW_IT_FIRST } from "@/lib/collections/copy";
import { useListReturnPath } from "@/lib/collections/listReturn";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";

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
vi.mock("@/hooks/queries/admin/taskJobs", () => ({
  useAdminTaskJobs: () => ({ data: [] }),
}));
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: vi.fn() }));

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

// Peeks load only once their row is near the screen. This stand-in records
// what each observer watches; `reveal` reports chosen rows as on screen.
let observers: Array<{ callback: IntersectionObserverCallback; targets: Set<Element> }>;

function rowOf(title: string): HTMLElement {
  const row = screen
    .getAllByRole("listitem")
    .find((item) => within(item).queryByText(title, { exact: true }));
  if (!row) throw new Error(`No row titled ${title}`);
  return row;
}

function reveal(...titles: string[]) {
  act(() => {
    for (const title of titles) {
      const row = rowOf(title);
      for (const observer of observers) {
        const target = [...observer.targets].find((node) => row.contains(node));
        if (!target) continue;
        observer.callback(
          [{ isIntersecting: true, target } as IntersectionObserverEntry],
          {} as IntersectionObserver,
        );
      }
    }
  });
}

function Where() {
  const location = useLocation();
  return <output aria-label="Location">{`${location.pathname}${location.search}`}</output>;
}

function Editor() {
  return <output aria-label="Back goes to">{useListReturnPath("/fallback")}</output>;
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
          <Route path="/admin/collections/:id/edit" element={<Editor />} />
          <Route path="/admin/collections/new" element={<Editor />} />
          <Route path="/catalog" element={<Where />} />
          <Route path="/admin/sections" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const location = () => screen.getByRole("status", { name: "Location" }).textContent;
const listed = () =>
  within(screen.getByRole("region", { name: "Collections" }))
    .queryAllByRole("listitem")
    .map((row) => row.querySelector("span.truncate")?.textContent);

function problem(status: number, type: string) {
  return async () => {
    const { V2ProblemError } =
      await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
    throw new V2ProblemError("op", {
      type: `https://siloserver.org/docs/api/v2/problems/${type}`,
      title: type,
      status,
    } as never);
  };
}

async function openMenu(title: string) {
  const user = userEvent.setup();
  await user.click(within(rowOf(title)).getByRole("button", { name: `More for ${title}` }));
  return { user, menu: await screen.findByRole("menu") };
}

beforeEach(() => {
  observers = [];
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      readonly root = null;
      readonly rootMargin = "0px";
      readonly thresholds = [0];
      private readonly record: (typeof observers)[number];
      constructor(callback: IntersectionObserverCallback) {
        this.record = { callback, targets: new Set() };
        observers.push(this.record);
      }
      observe = (target: Element) => this.record.targets.add(target);
      unobserve = (target: Element) => this.record.targets.delete(target);
      disconnect = () => this.record.targets.clear();
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
    stored("Studio Ghibli", { library_ids: ["1", "2"], item_count: 23 }),
    stored("Christmas Classics", { collection_type: "smart", item_count: 41 }),
    stored("IMDb Top 250 Shows", {
      collection_type: "tmdb",
      library_id: "3",
      library_ids: ["3"],
      item_count: 212,
      last_sync_status: "failed",
      last_sync_message: "TMDB answered 401",
      last_sync_at: new Date(Date.now() - 6 * 3600_000).toISOString(),
    }),
    stored("Staff Picks", {
      visibility: "hidden",
      home_row_count: 1,
      row_count: 2,
      item_count: 14,
    }),
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

describe("AdminCollections List", () => {
  it("lists every collection with its type, libraries and titles, and at most one tag", async () => {
    renderPage();
    expect(await screen.findByText("Best Picture Winners")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "List" })).toHaveAttribute("aria-pressed", "true");
    expect(listed()).toEqual([
      "Best Picture Winners",
      "Christmas Classics",
      "IMDb Top 250 Shows",
      "Staff Picks",
      "Studio Ghibli",
    ]);
    expect(rowOf("Best Picture Winners")).toHaveTextContent(
      "Best Picture WinnersOn HomeSynced list·Movies, Kids·96 titles",
    );
    // Hidden wins over On Home: one tag at most.
    expect(rowOf("Staff Picks")).toHaveTextContent("Staff PicksHidden from Collections tabManual");
    expect(within(rowOf("Staff Picks")).queryByText("On Home")).not.toBeInTheDocument();
    expect(within(rowOf("Studio Ghibli")).queryByText("On Home")).not.toBeInTheDocument();
    expect(screen.getByText("5 collections")).toBeInTheDocument();
  });

  it("says when a list's sync failed or is running, inline at the end of its meta line", async () => {
    items.push(
      stored("Trending This Week", { collection_type: "tmdb", last_sync_status: "running" }),
    );
    renderPage();
    await screen.findByText("IMDb Top 250 Shows");
    const failed = within(rowOf("IMDb Top 250 Shows")).getByText("Sync failed 6h ago");
    expect(failed).toHaveAttribute("title", "TMDB answered 401");
    // The reason is read out too, not only shown on hover.
    expect(failed).toHaveTextContent("Sync failed 6h ago: TMDB answered 401");
    expect(rowOf("Trending This Week")).toHaveTextContent("Syncing now");
    expect(rowOf("Studio Ghibli")).not.toHaveTextContent("Sync");
  });

  it("drops library names from the meta line when one library is selected, and keeps the choice in the URL", async () => {
    renderPage();
    await screen.findByText("Studio Ghibli");
    await userEvent.click(screen.getByRole("button", { name: "Kids 2" }));
    expect(location()).toBe("/admin/collections?view=list&libraryId=2");
    expect(listed()).toEqual(["Best Picture Winners", "Studio Ghibli"]);
    expect(rowOf("Studio Ghibli")).toHaveTextContent("Studio GhibliManual·23 titles");
    expect(screen.getByRole("button", { name: "List" })).toHaveAttribute("aria-pressed", "true");
  });

  it("filters by search, type and failed syncs, and reads the same filters back from the URL", async () => {
    const user = userEvent.setup();
    const { unmount } = renderPage();
    await screen.findByText("Studio Ghibli");

    await user.click(screen.getByRole("button", { name: "Synced list" }));
    expect(listed()).toEqual(["Best Picture Winners", "IMDb Top 250 Shows"]);
    await user.click(screen.getByRole("button", { name: "1 sync failed" }));
    expect(listed()).toEqual(["IMDb Top 250 Shows"]);
    await user.click(screen.getByRole("button", { name: "1 sync failed" }));
    await user.type(screen.getByRole("searchbox", { name: "Search collections" }), "best");
    expect(listed()).toEqual(["Best Picture Winners"]);
    expect(location()).toBe("/admin/collections?type=synced&q=best");
    unmount();

    renderPage("/admin/collections?type=synced&q=best");
    await screen.findByText("Best Picture Winners");
    expect(listed()).toEqual(["Best Picture Winners"]);
    expect(screen.getByRole("searchbox", { name: "Search collections" })).toHaveValue("best");
    expect(screen.getByRole("button", { name: "Synced list" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("says when nothing matches and clears the filters", async () => {
    renderPage("/admin/collections?q=nothing-like-this");
    expect(await screen.findByText("No collections match.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(listed()).toHaveLength(5);
    expect(location()).toBe("/admin/collections");
  });

  it("offers both ways in for a library with no collections", async () => {
    items = items.filter((entry) => !entry.library_ids.includes("3"));
    renderPage("/admin/collections?view=list&libraryId=3");
    const region = screen.getByRole("region", { name: "Collections" });
    expect(await within(region).findByText("No collections in TV Shows yet")).toBeInTheDocument();
    expect(within(region).getByRole("button", { name: "Add a starter pack" })).toBeEnabled();
    await userEvent.click(within(region).getByRole("button", { name: "New collection" }));
    const picker = await screen.findByRole("dialog", { name: "New collection" });
    expect(within(picker).getByRole("link", { name: "Manual" })).toHaveAttribute(
      "href",
      "/admin/collections/new?type=manual&libraryId=3",
    );
  });

  it("shows a skeleton while loading and Retry when the list can't load", async () => {
    let fail = true;
    v2Recorder.answer("GET /api/v2/admin/collections", async () => {
      if (fail) throw new Error("offline");
      return { items, groups: [] };
    });
    renderPage(
      "/admin/collections",
      new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    );
    expect(screen.getByText("Loading collections…")).toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't load collections");
    fail = false;
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Studio Ghibli")).toBeInTheDocument();
  });

  it("opens the editor on a click and hands it this exact list view to come back to", async () => {
    renderPage("/admin/collections?view=list&libraryId=1&type=manual");
    await userEvent.click(await screen.findByText("Studio Ghibli"));
    expect(screen.getByRole("status", { name: "Back goes to" })).toHaveTextContent(
      "/admin/collections?view=list&libraryId=1&type=manual",
    );
  });

  it("opens a bare ?libraryId=N link in Arrange for that library", async () => {
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
    renderPage("/admin/collections?libraryId=2");
    expect(await screen.findByRole("button", { name: "Arrange" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(await screen.findByRole("button", { name: "Kids 2" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "All libraries" })).toBeDisabled();
    await waitFor(() =>
      expect(
        v2Recorder
          .callsOf("GET /api/v2/admin/collections")
          .some((call) => call.query?.library_id === "2"),
      ).toBe(true),
    );
  });
});

describe("AdminCollections List switch", () => {
  it("hides a collection no row uses at once, sending only its type and visibility with a fresh ETag", async () => {
    renderPage();
    await screen.findByText("Christmas Classics");
    await userEvent.click(
      screen.getByRole("switch", { name: "Show Christmas Classics on the Movies Collections tab" }),
    );
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(1),
    );
    const [read] = v2Recorder.callsOf("GET /api/v2/admin/collections/{id}");
    const [patch] = v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}");
    expect(read!.path).toBe("/api/v2/admin/collections/christmas-classics");
    expect(patch!.headers["If-Match"]).toBe('"/api/v2/admin/collections/christmas-classics#1"');
    expect(patch!.body).toEqual({ collection_type: "smart", visibility: "hidden" });
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("never sends rules when it shows a manual collection again", async () => {
    renderPage();
    await screen.findByText("Staff Picks");
    const toggle = screen.getByRole("switch", {
      name: "Show Staff Picks on the Movies Collections tab",
    });
    expect(toggle).not.toBeChecked();
    await userEvent.click(toggle);
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(1),
    );
    expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")[0]!.body).toEqual({
      collection_type: "manual",
      visibility: "visible",
    });
  });

  it("asks first when rows show the collection, and Cancel sends nothing", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Best Picture Winners");
    const toggle = screen.getByRole("switch", {
      name: "Show Best Picture Winners on the Movies and Kids Collections tabs",
    });
    await user.click(toggle);
    const dialog = await screen.findByRole("alertdialog", {
      name: "Hide Best Picture Winners from Collections tabs?",
    });
    expect(dialog).toHaveTextContent(
      "It leaves Movies › Collections and Kids › Collections. 1 row still shows it, but its See all won't open while it's hidden.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(toggle).toBeChecked();
    expect(v2Recorder.writes()).toEqual([]);

    await user.click(toggle);
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Hide" }),
    );
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")[0]?.body).toEqual({
        collection_type: "mdblist",
        visibility: "hidden",
      }),
    );
  });

  it("frees every row's switch when changes to two rows answer out of order", async () => {
    const answers = new Map<string, () => void>();
    v2Recorder.answer(
      "PATCH /api/v2/admin/collections/{id}",
      (call: RecordedCall) =>
        new Promise<undefined>((resolve) => answers.set(call.path, () => resolve(undefined))),
    );
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Christmas Classics");
    const first = screen.getByRole("switch", {
      name: "Show Christmas Classics on the Movies Collections tab",
    });
    const second = screen.getByRole("switch", {
      name: "Show Studio Ghibli on the Movies and Kids Collections tabs",
    });
    await user.click(first);
    await user.click(second);
    await waitFor(() => expect(answers.size).toBe(2));
    expect(first).toBeDisabled();
    expect(second).toBeDisabled();

    await act(async () => answers.get("/api/v2/admin/collections/studio-ghibli")!());
    await waitFor(() => expect(second).toBeEnabled());
    await act(async () => answers.get("/api/v2/admin/collections/christmas-classics")!());
    await waitFor(() => expect(first).toBeEnabled());
  });
});

describe("AdminCollections List row menu", () => {
  it("offers no Sync now when the server can't import lists", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
      groups: true,
      imports: false,
      import_sources: [],
      artwork: true,
      item_reorder: true,
      section_references: true,
    });
    renderPage();
    await screen.findByText("Best Picture Winners");
    const { menu } = await openMenu("Best Picture Winners");
    expect(within(menu).getByRole("menuitem", { name: "Edit collection" })).toBeInTheDocument();
    expect(within(menu).queryByRole("menuitem", { name: "Sync now" })).not.toBeInTheDocument();
  });

  it("offers Sync now only on synced lists", async () => {
    renderPage();
    await screen.findByText("Studio Ghibli");
    let { menu } = await openMenu("Studio Ghibli");
    expect(within(menu).queryByRole("menuitem", { name: "Sync now" })).not.toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    ({ menu } = await openMenu("Christmas Classics"));
    expect(within(menu).queryByRole("menuitem", { name: "Sync now" })).not.toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    const opened = await openMenu("Best Picture Winners");
    await opened.user.click(within(opened.menu).getByRole("menuitem", { name: "Sync now" }));
    await waitFor(() =>
      expect(v2Recorder.callsOf("POST /api/v2/admin/collections/{id}/sync")[0]?.path).toBe(
        "/api/v2/admin/collections/best-picture-winners/sync",
      ),
    );
  });

  it("clears Syncing now on each list when two syncs answer out of order", async () => {
    const answers = new Map<string, () => void>();
    v2Recorder.answer(
      "POST /api/v2/admin/collections/{id}/sync",
      (call: RecordedCall) =>
        new Promise((resolve) =>
          answers.set(call.path, () =>
            resolve({ status: "success", message: "", items_matched: 3 }),
          ),
        ),
    );
    renderPage();
    await screen.findByText("Best Picture Winners");
    let opened = await openMenu("Best Picture Winners");
    await opened.user.click(within(opened.menu).getByRole("menuitem", { name: "Sync now" }));
    opened = await openMenu("IMDb Top 250 Shows");
    await opened.user.click(within(opened.menu).getByRole("menuitem", { name: "Sync now" }));
    await waitFor(() => expect(answers.size).toBe(2));
    expect(rowOf("Best Picture Winners")).toHaveTextContent("Syncing now");
    expect(rowOf("IMDb Top 250 Shows")).toHaveTextContent("Syncing now");

    await act(async () => answers.get("/api/v2/admin/collections/imdb-top-250-shows/sync")!());
    await waitFor(() => expect(rowOf("IMDb Top 250 Shows")).not.toHaveTextContent("Syncing now"));
    await act(async () => answers.get("/api/v2/admin/collections/best-picture-winners/sync")!());
    await waitFor(() => expect(rowOf("Best Picture Winners")).not.toHaveTextContent("Syncing now"));
  });

  it("opens a collection in one of its libraries from a submenu", async () => {
    renderPage();
    await screen.findByText("Studio Ghibli");
    const { user, menu } = await openMenu("Studio Ghibli");
    const openIn = within(menu).getByRole("menuitem", { name: "Open in" });
    openIn.focus();
    await user.keyboard("{ArrowRight}");
    await user.click(await screen.findByRole("menuitem", { name: "Kids" }));
    expect(location()).toMatch(/^\/catalog\?/);
    expect(location()).toContain("collection_id=studio-ghibli");
    expect(location()).toContain("library_id=2");
  });

  it("names the one library when there's only one", async () => {
    renderPage();
    await screen.findByText("Christmas Classics");
    const { menu } = await openMenu("Christmas Classics");
    expect(within(menu).getByRole("menuitem", { name: "Open in Movies" })).toBeInTheDocument();
  });

  it("deletes after a confirm that names every library it leaves", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Studio Ghibli");
    const { menu } = await openMenu("Studio Ghibli");
    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Studio Ghibli?" });
    expect(dialog).toHaveTextContent(
      "It's removed from Movies and Kids for everyone. This can't be undone.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    const [remove] = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(remove!.path).toBe("/api/v2/admin/collections/studio-ghibli");
    expect(remove!.headers["If-Match"]).toBe('"/api/v2/admin/collections/studio-ghibli#1"');
  });

  it("reads the collection again when it changed under the dialog, so the next Delete goes through", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Studio Ghibli");
    const { menu } = await openMenu("Studio Ghibli");
    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Studio Ghibli?" });
    v2Recorder.bump("/api/v2/admin/collections/studio-ghibli");
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "It changed since you opened this. Check it, then delete again.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    const removes = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(removes).toHaveLength(2);
    expect(removes[1]!.headers["If-Match"]).toBe('"/api/v2/admin/collections/studio-ghibli#2"');
  });

  it("keeps the collection and says why when rows still use it and the server doesn't list them", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
      groups: true,
      imports: true,
      import_sources: [],
      artwork: true,
      item_reorder: true,
    });
    v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", problem(409, "conflict"));
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Best Picture Winners");
    const { menu } = await openMenu("Best Picture Winners");
    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Best Picture Winners?" });
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Rows still use it. Remove them first.",
    );
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(rowOf("Best Picture Winners")).toBeInTheDocument();
    expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}/sections")).toHaveLength(0);
  });
});

describe("AdminCollections List: rows that show a collection", () => {
  const homeRow = {
    id: "s-home",
    scope: "home",
    library_id: null,
    section_type: "collection",
    title: "Best Picture",
    featured: false,
    enabled: true,
    position: 5,
    page_row_count: 9,
  };
  let sections: Array<typeof homeRow>;

  beforeEach(() => {
    sections = [homeRow];
    v2Recorder.answer("GET /api/v2/admin/collections/{id}/sections", () => ({ items: sections }));
    v2Recorder.answer("GET /api/v2/admin/sections/order", {
      ordered_ids: ["h0", "h1", "h2", "h3", "h4", "s-home", "h6", "h7", "h8"],
    });
    v2Recorder.answer("GET /api/v2/admin/sections/{id}", () => ({
      ...homeRow,
      config: { library_collection_id: "best-picture-winners" },
    }));
    v2Recorder.answer("DELETE /api/v2/admin/sections/{id}", () => {
      sections = [];
    });
  });

  it("adds a collection as a row on everyone's Home", async () => {
    renderPage();
    await screen.findByText("Studio Ghibli");
    const { user, menu } = await openMenu("Studio Ghibli");
    const home = within(menu).getByRole("menuitem", { name: "Add to Home…" });
    expect(home).toHaveAccessibleDescription("A row on everyone's Home");
    await user.click(home);
    expect(location()).toBe("/admin/sections?page=home&add=collection%3Alibrary%3Astudio-ghibli");
  });

  it("adds a collection as a row above a library's grid, its own libraries first", async () => {
    renderPage();
    await screen.findByText("IMDb Top 250 Shows");
    const { user, menu } = await openMenu("IMDb Top 250 Shows");
    const page = within(menu).getByRole("menuitem", { name: "Add to a library page…" });
    expect(page).toHaveAccessibleDescription("A row above a library's grid");
    page.focus();
    await user.keyboard("{ArrowRight}");
    const submenu = (await screen.findAllByRole("menu")).at(-1)!;
    expect(
      within(submenu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["TV Shows", "Movies", "Kids"]);
    await user.click(within(submenu).getByRole("menuitem", { name: "Kids" }));
    expect(location()).toBe("/admin/sections?page=2&add=collection%3Alibrary%3Aimdb-top-250-shows");
  });

  it("can't add a hidden collection as a row, and says why", async () => {
    renderPage();
    await screen.findByText("Staff Picks");
    const { menu } = await openMenu("Staff Picks");
    for (const name of ["Add to Home…", "Add to a library page…"]) {
      const item = within(menu).getByRole("menuitem", { name });
      expect(item).toHaveAttribute("data-disabled");
      expect(item).toHaveAccessibleDescription(SHOW_IT_FIRST);
    }
  });

  it("lists the rows before a delete and deletes them first", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Best Picture Winners");
    const { menu } = await openMenu("Best Picture Winners");
    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Best Picture Winners?" });
    expect(
      await within(dialog).findByRole("link", { name: "Open row: Best Picture" }),
    ).toHaveAttribute("href", "/admin/sections?page=home&edit=s-home");
    expect(dialog).toHaveTextContent("Home · row 6 of 9");
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 1 row" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    const writes = v2Recorder
      .writes()
      .map((call) => `${call.operation.split(" ")[0]} ${call.path}`);
    expect(writes).toEqual([
      "DELETE /api/v2/admin/sections/s-home",
      "DELETE /api/v2/admin/collections/best-picture-winners",
    ]);
  });

  it("deletes no row when the collection changed under the dialog", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Best Picture Winners");
    const { menu } = await openMenu("Best Picture Winners");
    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Best Picture Winners?" });
    const confirm = await within(dialog).findByRole("button", { name: "Delete it and its 1 row" });
    v2Recorder.bump("/api/v2/admin/collections/best-picture-winners");
    await user.click(confirm);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "It changed since you opened this. Check it, then delete again.",
    );
    expect(v2Recorder.writes()).toHaveLength(0);
    // It was read again: the next Delete sends the new token.
    const changed = v2Recorder.etag("/api/v2/admin/collections/best-picture-winners");
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 1 row" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    const [remove] = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(remove!.headers["If-Match"]).toBe(changed);
  });

  it("shows the rows when the server refuses a delete the list thought was free", async () => {
    v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", problem(409, "conflict"));
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Studio Ghibli");
    const { menu } = await openMenu("Studio Ghibli");
    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Studio Ghibli?" });
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    const blocked = await screen.findByRole("button", { name: "Delete it and its 1 row" });
    expect(blocked).toBeEnabled();
    expect(screen.getByRole("link", { name: "Open row: Best Picture" })).toBeInTheDocument();
  });

  it("keeps the collection when a row can't be deleted, and names the row", async () => {
    v2Recorder.answer("DELETE /api/v2/admin/sections/{id}", () => {
      throw new Error("Server error");
    });
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("Best Picture Winners");
    const { menu } = await openMenu("Best Picture Winners");
    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Best Picture Winners?" });
    await user.click(
      await within(dialog).findByRole("button", { name: "Delete it and its 1 row" }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Couldn't delete every row, so the collection was kept. Still showing it: Best Picture (Home).",
    );
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(0);
  });
});

describe("AdminCollections List peeks", () => {
  const peekCalls = () =>
    v2Recorder.callsOf("GET /api/v2/library/{id}/collections/{collection_id}/items");

  it("reads only on-screen rows, at most four at a time, and not again within five minutes", async () => {
    items = Array.from({ length: 200 }, (_, index) =>
      stored(`Collection ${String(index).padStart(3, "0")}`),
    );
    const answers: Array<() => void> = [];
    let inFlight = 0;
    let most = 0;
    v2Recorder.answer(
      "GET /api/v2/library/{id}/collections/{collection_id}/items",
      () =>
        new Promise((resolve) => {
          inFlight += 1;
          most = Math.max(most, inFlight);
          answers.push(() => {
            inFlight -= 1;
            resolve({ items: [{ content_id: "m1", title: "Past Lives", poster_url: "p.jpg" }] });
          });
        }),
    );
    const client = new QueryClient();
    const { unmount } = renderPage("/admin/collections", client);
    await screen.findByText("Collection 000");
    const onScreen = Array.from({ length: 8 }, (_, index) => `Collection 00${index}`);
    reveal(...onScreen);

    await waitFor(() => expect(peekCalls()).toHaveLength(PEEK_MAX_IN_FLIGHT));
    while (answers.length > 0) {
      await act(async () => answers.shift()!());
      await waitFor(() => undefined);
    }
    await waitFor(() => expect(peekCalls()).toHaveLength(8));
    while (answers.length > 0) await act(async () => answers.shift()!());
    expect(most).toBeLessThanOrEqual(PEEK_MAX_IN_FLIGHT);
    expect(new Set(peekCalls().map((call) => call.path))).toEqual(
      new Set(
        onScreen.map(
          (title) =>
            `/api/v2/library/1/collections/${title.toLowerCase().replace(/\W+/g, "-")}/items`,
        ),
      ),
    );
    expect(peekCalls().every((call) => call.query?.limit === 3)).toBe(true);

    // Back to the page within the stale time: the same rows on screen read nothing.
    unmount();
    vi.spyOn(Date, "now").mockReturnValue(Date.now() + PEEK_STALE_MS - 1000);
    renderPage("/admin/collections", client);
    await screen.findByText("Collection 000");
    reveal(...onScreen);
    await act(async () => undefined);
    expect(peekCalls()).toHaveLength(8);
    vi.restoreAllMocks();
  });

  it("drops a row's titles for its own poster as soon as it is hidden", async () => {
    items = [stored("Staff Picks", { poster_url: "own.jpg" })];
    v2Recorder.answer("GET /api/v2/library/{id}/collections/{collection_id}/items", async () => ({
      items: [{ content_id: "m1", title: "Past Lives", poster_url: "title.jpg" }],
    }));
    // The change stays in flight, so the list has not reloaded the collection yet.
    v2Recorder.answer("PATCH /api/v2/admin/collections/{id}", () => new Promise(() => undefined));
    renderPage();
    await screen.findByText("Staff Picks");
    reveal("Staff Picks");
    const posters = () =>
      [...rowOf("Staff Picks").querySelectorAll("img")].map((img) => img.getAttribute("src"));
    await waitFor(() => expect(posters().join()).toContain("title.jpg"));

    await userEvent.click(
      screen.getByRole("switch", { name: "Show Staff Picks on the Movies Collections tab" }),
    );
    await waitFor(() => expect(posters().join()).toContain("own.jpg"));
    expect(posters().join()).not.toContain("title.jpg");
  });

  it("shows a hidden collection's own poster without asking a Collections tab for it", async () => {
    items = [stored("Staff Picks", { visibility: "hidden", poster_url: "own.jpg" })];
    renderPage();
    await screen.findByText("Staff Picks");
    reveal("Staff Picks");
    await act(async () => undefined);
    expect(peekCalls()).toEqual([]);
  });
});
