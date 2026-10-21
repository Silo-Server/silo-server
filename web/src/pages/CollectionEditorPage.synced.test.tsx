/**
 * Creating a Synced list on the collection editor page, both scopes: one
 * source panel with MDBList, TMDB chart and TMDB list tabs, ready-made picks
 * that fill only untouched fields, libraries the list can match into, and a
 * Create that stays on the new list.
 */
import { fireEvent, screen, within } from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  adminCapabilities,
  adminSyncedCollection,
  personalCapabilities,
  personalSyncedCollection,
} from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import { nameField, prepareEditorPage, showEditorPage } from "./collectionEditorPage.test-support";

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

beforeEach(() => {
  vi.mocked(toast.warning).mockClear();
  prepareEditorPage();
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

const SERVER_NEW = "/admin/collections/new?type=synced&libraryId=1";
const createButton = () => screen.getByRole("button", { name: "Create collection" });
const imports = (route: string) => v2Recorder.callsOf(`POST ${route}`);

async function pick(name: string) {
  fireEvent.click(await screen.findByRole("radio", { name }));
}

async function create() {
  fireEvent.click(createButton());
  await vi.waitFor(() =>
    expect(screen.queryByRole("button", { name: "Create collection" })).toBeNull(),
  );
}

describe("Synced list step, server", () => {
  it("waits for a list and a name before Create, and says which is missing", async () => {
    showEditorPage(SERVER_NEW);
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

  it("keeps the typed search and the last results when a search fails", async () => {
    showEditorPage(SERVER_NEW);
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
    showEditorPage(SERVER_NEW);
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
    showEditorPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    const search = screen.getByRole("searchbox", { name: "Search lists" });
    expect(search).toBeDisabled();
    expect(search).toHaveAccessibleDescription(/Searching MDBList needs an API key/);
    expect(screen.getByRole("link", { name: "Settings › Providers" })).toHaveAttribute(
      "href",
      "/admin/settings/providers",
    );
    await create();
    expect(imports("/api/v2/admin/collections/import/mdblist")).toHaveLength(1);
    expect(v2Recorder.callsOf("GET /api/v2/collections/import/mdblist/search")).toEqual([]);
  });

  it("leaves out libraries that can't hold the list's titles, and says why", async () => {
    showEditorPage("/admin/collections/new?type=synced&libraryId=2");
    await pick("IMDb Top 250 Movies");
    expect(
      screen.getByText("This list only has movies, so TV Shows isn't offered."),
    ).toBeInTheDocument();
    // The library it was opened from can't hold movies, so it is unticked.
    expect(createButton()).toBeDisabled();
    expect(screen.getByText("Pick its libraries, then create it.")).toBeInTheDocument();
  });

  it("creates it hidden when the Collections tab switch is off, with a guarded PATCH", async () => {
    showEditorPage(SERVER_NEW);
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

  it("says the list still shows when hiding it fails", async () => {
    v2Recorder.answer("PATCH /api/v2/admin/collections/{id}", () => {
      throw new Error("Server unavailable");
    });
    showEditorPage(SERVER_NEW);
    await pick("IMDb Top 250 Movies");
    fireEvent.click(screen.getByRole("switch", { name: "Show on the Collections tab" }));
    await create();
    await vi.waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(expect.any(String), {
        description: "It still shows on Collections tabs: Server unavailable",
      }),
    );
  });
});

describe("Synced list step, personal", () => {
  it("offers the tabs the profile's import sources allow", async () => {
    v2Recorder.answer("GET /api/v2/collections/capabilities", {
      ...personalCapabilities,
      import_sources: ["tmdb"],
    });
    showEditorPage("/collections/new?type=synced");
    const tabs = await screen.findByRole("tablist", { name: "Where the list comes from" });
    expect(
      within(tabs)
        .getAllByRole("tab")
        .map((tab) => tab.textContent),
    ).toEqual(["TMDB chart"]);
  });

  it("imports, then shows it on the Collections tab with a guarded PATCH", async () => {
    showEditorPage("/collections/new?type=synced");
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
