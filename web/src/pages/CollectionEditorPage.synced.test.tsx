/**
 * Creating a Synced list on the collection editor page, both scopes: one
 * source panel with MDBList, TMDB chart and TMDB list tabs, ready-made picks
 * that fill only untouched fields, libraries the list can match into, and a
 * Create that stays on the new list.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { toast } from "sonner";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import {
  adminCapabilities,
  adminSyncedCollection,
  personalCapabilities,
  personalSyncedCollection,
} from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (value: string) => value }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner", name: "Sam" }, isLoading: false }),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({
    data: [
      { id: "p-owner", name: "Sam" },
      { id: "p-maya", name: "Maya" },
    ],
  }),
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

const IMDB_TOP = {
  id: "mdblist_imdb_top_250_movies",
  title: "IMDb Top 250 Movies",
  description: "The 250 highest-rated movies on IMDb.",
  icon: "🏆",
  category: "editorial",
  source: "mdblist",
  media_kind: "movie",
  default_limit: 250,
  default_sync_schedule: "0 3 * * *",
  poster_path: "/images/collection-templates/mdblist_imdb_top_250_movies.jpg",
  mdblist: { url: "https://mdblist.com/lists/linaspurinis/top-watched-movies" },
};
const POPULAR_TV = {
  id: "tmdb_popular_tv",
  title: "Popular TV Shows",
  description: "What people watch on TV now.",
  icon: "📺",
  category: "popular",
  source: "tmdb",
  media_kind: "tv",
  default_limit: 100,
  default_sync_schedule: "0 4 * * *",
  tmdb: { preset: "popular", media_type: "tv" },
};
const CATALOG = {
  categories: [
    { category: "editorial", label: "Editorial", templates: [IMDB_TOP] },
    {
      category: "popular",
      label: "Popular",
      templates: [
        POPULAR_TV,
        {
          id: "tmdb_discover_popular_comedy",
          title: "Popular Comedy",
          description: "",
          icon: "",
          category: "popular",
          source: "tmdb_discover",
          media_kind: "movie",
          tmdb_discover: { media_type: "movie", sort_by: "popularity.desc" },
        },
        {
          id: "tmdb_franchise_lotr",
          title: "The Lord of the Rings",
          description: "",
          icon: "",
          category: "popular",
          source: "tmdb_collection",
          media_kind: "movie",
          tmdb_collection: { collection_id: 119 },
        },
      ],
    },
  ],
};

const HIT = {
  id: "77",
  user_id: "7",
  user_name: "hdlists",
  name: "Top 250 Sci-Fi Movies",
  slug: "top-250-sci-fi",
  description: "Science fiction greats.",
  media_type: "movie",
  items: 250,
  likes: 1204,
  url: "https://mdblist.com/lists/hdlists/top-250-sci-fi",
};

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
  vi.mocked(toast.warning).mockClear();
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
  v2Recorder.answer("GET /api/v2/admin/collections", { items: [], groups: [] });
  v2Recorder.answer("GET /api/v2/admin/collections/templates", CATALOG);
  v2Recorder.answer("GET /api/v2/collections/templates", CATALOG);
  v2Recorder.answer("GET /api/v2/collections/import/mdblist/search", {
    configured: true,
    items: [HIT],
  });
  // After Create the page opens the new list in the same editor.
  const source = {
    source_url: "https://mdblist.com/lists/linaspurinis/top-watched-movies/json",
    source_config: { url: "https://mdblist.com/lists/linaspurinis/top-watched-movies/json" },
  };
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminSyncedCollection("mdblist", source));
  v2Recorder.answer("GET /api/v2/collections/{id}", personalSyncedCollection("mdblist", source));
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

const SERVER_NEW = "/admin/collections/new?type=synced&libraryId=1";
const nameField = () => screen.findByRole("textbox", { name: "Name" });
const createButton = () => screen.getByRole("button", { name: "Create collection" });
const imports = (route: string) => v2Recorder.callsOf(`POST ${route}`);

function choose(combobox: HTMLElement, option: string) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  fireEvent.click(screen.getByRole("option", { name: option }));
}

async function pick(name: string) {
  fireEvent.click(await screen.findByRole("radio", { name }));
}

async function pickChart(name: string) {
  const charts = await screen.findByRole("radiogroup", { name: "Chart" });
  fireEvent.click(within(charts).getByRole("radio", { name: new RegExp(`^${name}`) }));
}

async function create() {
  fireEvent.click(createButton());
  await vi.waitFor(() =>
    expect(screen.queryByRole("button", { name: "Create collection" })).toBeNull(),
  );
}

describe("Synced list step, server", () => {
  it("offers only lists it can create: no Discover or Franchise suggestion", async () => {
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    const lists = screen.getByRole("radiogroup", { name: "Lists" });
    expect(within(lists).getByRole("radio", { name: "Popular TV Shows" })).toBeInTheDocument();
    expect(within(lists).queryByRole("radio", { name: "Popular Comedy" })).toBeNull();
    expect(within(lists).queryByRole("radio", { name: "The Lord of the Rings" })).toBeNull();
  });

  it("waits for a list and a name before Create, and says which is missing", async () => {
    showPage(SERVER_NEW);
    const link = await screen.findByLabelText("Or paste any MDBList link");
    expect(createButton()).toBeDisabled();
    expect(screen.getByText("Pick a list, then create it.")).toBeInTheDocument();
    fireEvent.change(link, {
      target: { value: "https://mdblist.com/lists/u/top" },
    });
    expect(createButton()).toBeDisabled();
    expect(screen.getByText("Name it, then create it.")).toBeInTheDocument();
    fireEvent.change(await nameField(), { target: { value: "Top" } });
    expect(createButton()).toBeEnabled();
    expect(screen.getByText("It syncs for the first time when you create it.")).toBeInTheDocument();
  });

  it("sends a pasted link without its query, fragment or trailing slash", async () => {
    showPage(SERVER_NEW);
    fireEvent.change(await nameField(), { target: { value: "Top" } });
    fireEvent.change(await screen.findByLabelText("Or paste any MDBList link"), {
      target: { value: "https://mdblist.com/lists/u/top/?sort=rank#x" },
    });
    await create();
    expect(imports("/api/v2/admin/collections/import/mdblist")[0]?.body).toMatchObject({
      url: "https://mdblist.com/lists/u/top",
      featured: false,
    });
  });

  it("flags a link that isn't an MDBList list and keeps Create off", async () => {
    showPage(SERVER_NEW);
    fireEvent.change(await nameField(), { target: { value: "Top" } });
    fireEvent.change(await screen.findByLabelText("Or paste any MDBList link"), {
      target: { value: "https://example.com/lists/u/top" },
    });
    expect(screen.getByLabelText("Or paste any MDBList link")).toHaveAccessibleDescription(
      /Paste the link to a list on mdblist.com/,
    );
    expect(createButton()).toBeDisabled();
  });

  it("fills the details from a pick, and a later pick keeps an edited name", async () => {
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    expect(await nameField()).toHaveValue("IMDb Top 250 Movies");
    expect(screen.getByLabelText(/^Description/)).toHaveValue(
      "The 250 highest-rated movies on IMDb.",
    );
    expect(screen.getByText(/Filled in from the list/)).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: "Max titles" })).toHaveValue(250);
    expect(screen.getByRole("combobox", { name: "Sync schedule" })).toHaveTextContent(
      "Every day at 3:00 AM",
    );

    fireEvent.change(await nameField(), { target: { value: "Our top 250" } });
    await pick("Popular TV Shows");
    expect(await nameField()).toHaveValue("Our top 250");
    expect(screen.getByText("Kept your name")).toBeInTheDocument();
    expect(screen.getByLabelText(/^Description/)).toHaveValue("What people watch on TV now.");
    expect(screen.getByRole("spinbutton", { name: "Max titles" })).toHaveValue(100);
  });

  it("follows a list found in MDBList search, with its own description and no template poster", async () => {
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    fireEvent.change(screen.getByRole("searchbox", { name: "Search lists" }), {
      target: { value: "sci-fi" },
    });
    await pick("Top 250 Sci-Fi Movies");
    expect(
      screen.getByRole("radio", { name: "Top 250 Sci-Fi Movies" }),
    ).toHaveAccessibleDescription("by hdlists · Movies · 250 titles · ♥ 1,204");
    await create();
    const body = imports("/api/v2/admin/collections/import/mdblist")[0]?.body;
    expect(body).toMatchObject({
      title: "Top 250 Sci-Fi Movies",
      description: "Science fiction greats.",
      url: "https://mdblist.com/lists/hdlists/top-250-sci-fi/json",
    });
    expect(body).not.toHaveProperty("poster_url");
  });

  it("keeps the typed search and the last results when a search fails", async () => {
    showPage(SERVER_NEW);
    const search = await screen.findByRole("searchbox", { name: "Search lists" });
    fireEvent.change(search, { target: { value: "sci-fi" } });
    await screen.findByRole("radio", { name: "Top 250 Sci-Fi Movies" });
    v2Recorder.answer("GET /api/v2/collections/import/mdblist/search", () => {
      throw new Error("MDBList didn't answer");
    });
    fireEvent.change(search, { target: { value: "sci-fi classics" } });
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't search MDBList");
    expect(search).toHaveValue("sci-fi classics");
    expect(screen.getByRole("radio", { name: "Top 250 Sci-Fi Movies" })).toBeInTheDocument();
  });

  it("shows Searching… for a new term, not the last term's results", async () => {
    showPage(SERVER_NEW);
    const search = await screen.findByRole("searchbox", { name: "Search lists" });
    fireEvent.change(search, { target: { value: "sci-fi" } });
    await screen.findByRole("radio", { name: "Top 250 Sci-Fi Movies" });
    v2Recorder.answer("GET /api/v2/collections/import/mdblist/search", () => new Promise(() => {}));
    fireEvent.change(search, { target: { value: "horror" } });
    expect(await screen.findByText("Searching…")).toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: "Top 250 Sci-Fi Movies" })).toBeNull();
  });

  it("with MDBList search off, disables the box and still takes picks and links", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
      ...adminCapabilities,
      mdblist_search: false,
    });
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    const search = screen.getByRole("searchbox", { name: "Search lists" });
    expect(search).toBeDisabled();
    expect(search).toHaveAccessibleDescription(/Searching MDBList needs an API key/);
    expect(screen.getByRole("link", { name: "Settings › Subtitles & Metadata" })).toHaveAttribute(
      "href",
      "/admin/settings/providers",
    );
    await create();
    expect(imports("/api/v2/admin/collections/import/mdblist")).toHaveLength(1);
    expect(v2Recorder.callsOf("GET /api/v2/collections/import/mdblist/search")).toEqual([]);
  });

  it("binds a chart's Show to what TMDB offers and names it", async () => {
    showPage("/admin/collections/new?type=synced&source=tmdb_chart&libraryId=1");
    await pickChart("Trending");
    const show = screen.getByRole("radiogroup", { name: "Show" });
    expect(within(show).getByRole("radio", { name: "Both" })).toBeChecked();
    expect(screen.getByRole("radiogroup", { name: "Trending over" })).toBeInTheDocument();

    await pickChart("Now playing");
    expect(within(show).getByRole("radio", { name: "Movies" })).toBeChecked();
    expect(within(show).getByRole("radio", { name: "TV shows" })).toBeDisabled();
    expect(within(show).queryByRole("radio", { name: "Both" })).toBeNull();
    expect(screen.queryByRole("radiogroup", { name: "Trending over" })).toBeNull();
    expect(screen.getByText("TMDB has this chart for movies only")).toBeInTheDocument();
    expect(await nameField()).toHaveValue("Now Playing Movies");
    expect(screen.getByRole("link", { name: "Starter packs" })).toBeInTheDocument();

    await create();
    expect(imports("/api/v2/admin/collections/import/tmdb")[0]?.body).toMatchObject({
      preset: "now_playing",
      media_type: "movie",
      title: "Now Playing Movies",
    });
  });

  it("uses the ready-made pick for a chart that has one", async () => {
    showPage("/admin/collections/new?type=synced&source=tmdb_chart&libraryId=2");
    await pickChart("Popular");
    fireEvent.click(screen.getByRole("radio", { name: "TV shows" }));
    expect(await nameField()).toHaveValue("Popular TV Shows");
    expect(screen.getByRole("spinbutton", { name: "Max titles" })).toHaveValue(100);
  });

  it("leaves out libraries that can't hold the list's titles, and says why", async () => {
    showPage("/admin/collections/new?type=synced&libraryId=2");
    await pick("IMDb Top 250 Movies");
    expect(
      screen.getByText("This list only has movies, so TV Shows isn't offered."),
    ).toBeInTheDocument();
    // The library it was opened from can't hold movies, so it is unticked.
    expect(createButton()).toBeDisabled();
    expect(screen.getByText("Pick its libraries, then create it.")).toBeInTheDocument();
  });

  it("caps Max titles at 500 and labels the schedule with the server's offset", async () => {
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    expect(screen.getByText("Server time (UTC−5)")).toBeInTheDocument();
    const limit = screen.getByRole("spinbutton", { name: "Max titles" });
    fireEvent.change(limit, { target: { value: "900" } });
    fireEvent.blur(limit);
    choose(screen.getByRole("combobox", { name: "Sync schedule" }), "Every 6 hours");
    await create();
    expect(imports("/api/v2/admin/collections/import/mdblist")[0]?.body).toMatchObject({
      limit: 500,
      sync_schedule: "0 */6 * * *",
      featured: false,
      poster_url: "/images/collection-templates/mdblist_imdb_top_250_movies.jpg",
    });
  });

  it("creates it hidden when the Collections tab switch is off, with a guarded PATCH", async () => {
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    const tabSwitch = screen.getByRole("switch", { name: "Show on the Collections tab" });
    fireEvent.click(tabSwitch);
    expect(tabSwitch).not.toBeChecked();
    await create();
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(2));
    expect(v2Recorder.writes().map((call) => call.operation)).toEqual([
      "POST /api/v2/admin/collections/import/mdblist",
      "PATCH /api/v2/admin/collections/{id}",
    ]);
    const [, patch] = v2Recorder.writes();
    expect(patch?.body).toEqual({ collection_type: "mdblist", visibility: "hidden" });
    expect(patch?.headers["If-Match"]).toBe('"/api/v2/admin/collections/c1#1"');
  });

  it("leaves the switch on: imports only, with no PATCH", async () => {
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    await create();
    expect(v2Recorder.writes().map((call) => call.operation)).toEqual([
      "POST /api/v2/admin/collections/import/mdblist",
    ]);
  });

  it("says the list still shows when hiding it fails", async () => {
    v2Recorder.answer("PATCH /api/v2/admin/collections/{id}", () => {
      throw new Error("Server unavailable");
    });
    showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    fireEvent.click(screen.getByRole("switch", { name: "Show on the Collections tab" }));
    await create();
    await vi.waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(expect.any(String), {
        description: "It still shows on Collections tabs: Server unavailable",
      }),
    );
  });

  it("stays on the new list after Create", async () => {
    const router = showPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    await create();
    await vi.waitFor(() =>
      expect(router.state.location.pathname).toBe("/admin/collections/c1/edit"),
    );
  });
});

describe("Synced list step, personal", () => {
  it("offers the tabs the profile's import sources allow", async () => {
    v2Recorder.answer("GET /api/v2/collections/capabilities", {
      ...personalCapabilities,
      import_sources: ["tmdb"],
    });
    showPage("/collections/new?type=synced");
    const tabs = await screen.findByRole("tablist", { name: "Where the list comes from" });
    expect(
      within(tabs)
        .getAllByRole("tab")
        .map((tab) => tab.textContent),
    ).toEqual(["TMDB chart"]);
  });

  it("shows no server fields: named schedules, a poster only, no Server time", async () => {
    showPage("/collections/new?type=synced");
    await pick("IMDb Top 250 Movies");
    expect(screen.getByRole("combobox", { name: "Sync schedule" })).toHaveTextContent("Daily");
    expect(screen.queryByText(/Server time/)).toBeNull();
    expect(screen.queryByText("Backdrop")).toBeNull();
    expect(screen.getByRole("button", { name: /Match into/ })).toHaveTextContent(
      "All my libraries",
    );
  });

  it("says search is off in the profile's words", async () => {
    v2Recorder.answer("GET /api/v2/collections/capabilities", {
      ...personalCapabilities,
      mdblist_search: false,
    });
    showPage("/collections/new?type=synced");
    expect(
      await screen.findByRole("searchbox", { name: "Search lists" }),
    ).toHaveAccessibleDescription(
      "Searching MDBList is off on this server. Popular picks and pasted links still work.",
    );
  });

  it("imports, then shows it on the Collections tab with a guarded PATCH", async () => {
    showPage("/collections/new?type=synced");
    await pick("IMDb Top 250 Movies");
    fireEvent.click(screen.getByRole("switch", { name: "Show on the Collections tab" }));
    expect(screen.getByRole("switch", { name: "Show on the Collections tab" })).toBeChecked();
    await create();
    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(2));
    expect(v2Recorder.writes().map((call) => call.operation)).toEqual([
      "POST /api/v2/collections/import/mdblist",
      "PATCH /api/v2/collections/{id}",
    ]);
    const [, patch] = v2Recorder.writes();
    expect(patch?.body).toEqual({ include_in_server_collections: true });
    expect(patch?.headers["If-Match"]).toBe('"/api/v2/collections/c1#1"');
    expect(v2Recorder.writes()[0]?.body).toMatchObject({
      title: "IMDb Top 250 Movies",
      sync_schedule: "daily",
      is_shared: false,
      poster_url: "/images/collection-templates/mdblist_imdb_top_250_movies.jpg",
    });
  });
});
