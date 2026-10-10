/**
 * Goldens: the requests the synced-list paths send, admin and personal: the
 * editor's Synced list step (a pasted MDBList link, a TMDB chart, a TMDB list
 * link, a template pick with its poster and an MDBList search pick), the
 * Synced list editor for a saved list, both scopes. New synced lists are
 * imported unpinned, and a save never sends `featured`.
 */
import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Library } from "@/api/types";
import {
  adminCapabilities,
  adminCollection,
  adminCollectionList,
  adminSyncedCollection,
  personalCapabilities,
  personalSyncedCollection,
} from "@/test/fixtures/collectionAnswers";
import { artworkTile, chooseArtwork } from "@/test/collectionArtwork";
import { goldens } from "@/test/fixtures/collectionBodies";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p-owner", name: "Owner" }] }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" } }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: libraries }),
}));
vi.mock("@/hooks/queries/libraries", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  )),
  useUserLibraries: () => ({ data: libraries }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

const libraries = [{ id: 1, name: "Movies", type: "movies" }] as Library[];

installV2Recorder();

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  HTMLElement.prototype.scrollIntoView = () => {};
  HTMLElement.prototype.hasPointerCapture = () => false;
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(adminCollection));
  v2Recorder.answer("GET /api/v2/admin/collections/templates", templateCatalog);
  v2Recorder.answer("GET /api/v2/collections/templates", templateCatalog);
  v2Recorder.answer("GET /api/v2/admin/collections/template-bundles", { bundles: [] });
});

const templateCatalog = {
  categories: [
    {
      category: "trending",
      label: "Trending",
      templates: [
        {
          id: "tmdb_trending_movies_week",
          title: "Trending Movies This Week",
          description: "Top trending movies on TMDB.",
          icon: "🎬",
          category: "trending",
          source: "tmdb",
          media_kind: "movie",
          default_limit: 50,
          default_sync_schedule: "0 4 * * *",
          poster_path: "https://images.example/templates/trending-movies.jpg",
          tmdb: { preset: "trending", media_type: "movie", time_window: "week" },
        },
      ],
    },
    {
      category: "custom",
      label: "Custom",
      templates: [
        {
          id: "mdblist_custom",
          title: "Custom MDBList",
          description: "Any public MDBList list.",
          icon: "📋",
          category: "custom",
          source: "mdblist",
          media_kind: "mixed",
          mdblist: { url: "" },
        },
      ],
    },
  ],
};

function show(element: ReactElement, url = "/") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const router = createMemoryRouter(
    [
      { path: "/", element },
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

async function type(label: string, value: string) {
  fireEvent.change(await screen.findByLabelText(label), { target: { value } });
}

function saveBar() {
  return screen.getByRole("region", { name: "Unsaved changes" });
}

/** Save from the editor page; it stays on the list. */
async function saveAndWait(count: number) {
  fireEvent.click(within(saveBar()).getByRole("button", { name: "Save" }));
  await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(count));
  await vi.waitFor(() =>
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull(),
  );
}

/** A saved server list, as the editor reads it and as the list shows it. */
function answerAdmin(collection: object) {
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", collection);
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(collection));
}

/** Create collection, then wait until the editor has moved to the new list's edit page. */
async function createAndLeave() {
  fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
  await vi.waitFor(() =>
    expect(screen.queryByRole("button", { name: "Create collection" })).toBeNull(),
  );
}

describe("admin Synced list step", () => {
  it("imports a pasted MDBList link, unpinned", async () => {
    show(<></>, "/admin/collections/new?type=synced&source=mdblist&libraryId=1");
    await type("Name", "Top Watched");
    await type("Or paste any MDBList link", "https://mdblist.com/lists/user/top-watched/json");
    await createAndLeave();
    expect(v2Recorder.writes()).toEqual(goldens.adminImportMDBList);
  });

  it("imports a TMDB chart", async () => {
    show(<></>, "/admin/collections/new?type=synced&source=tmdb_chart&libraryId=1");
    fireEvent.click(await screen.findByRole("radio", { name: /^Trending/ }));
    expect(screen.getByLabelText("Name")).toHaveValue("Trending Today");
    await createAndLeave();
    expect(v2Recorder.writes()).toEqual(goldens.adminImportTMDBChart);
  });

  it("imports a TMDB list", async () => {
    show(<></>, "/admin/collections/new?type=synced&source=tmdb_list&libraryId=1");
    await type("Name", "Festival Picks");
    await type("Paste a TMDB list link", "https://www.themoviedb.org/list/310-festival-picks");
    await createAndLeave();
    expect(v2Recorder.writes()).toEqual(goldens.adminImportTMDBList);
  });

  it("creates from a TMDB template pick with its server poster", async () => {
    show(<></>, "/admin/collections/new?type=synced&libraryId=1");
    fireEvent.click(await screen.findByRole("radio", { name: "Trending Movies This Week" }));
    expect(
      within(await artworkTile("poster")).getByRole("img", { name: "Poster preview" }),
    ).toHaveAttribute("src", "https://images.example/templates/trending-movies.jpg");
    await createAndLeave();
    expect(v2Recorder.writes()).toEqual(goldens.adminTemplateTMDB);
  });

  it("creates from a list picked in MDBList search", async () => {
    v2Recorder.answer("GET /api/v2/collections/import/mdblist/search", {
      configured: true,
      items: [mdblistSearchHit],
    });
    show(<></>, "/admin/collections/new?type=synced&libraryId=1");
    await type("Search lists", "oscar");
    fireEvent.click(await screen.findByRole("radio", { name: "Oscar Winners" }));
    await createAndLeave();
    expect(v2Recorder.callsOf("GET /api/v2/collections/import/mdblist/search")).toEqual([
      expect.objectContaining({ query: { q: "oscar" } }),
    ]);
    expect(v2Recorder.writes()).toEqual(goldens.adminTemplateMDBListPick);
  });
});

describe("personal Synced list step", () => {
  it("creates from a TMDB template pick with its server poster", async () => {
    show(<></>, "/collections/new?type=synced");
    fireEvent.click(await screen.findByRole("radio", { name: "Trending Movies This Week" }));
    await createAndLeave();
    expect(v2Recorder.writes()).toEqual(goldens.personalTemplateTMDB);
  });
});

const mdblistSearchHit = {
  id: "4242",
  user_id: "7",
  user_name: "cinephile",
  name: "Oscar Winners",
  slug: "oscar-winners",
  description: "Best Picture winners.",
  media_type: "movie",
  items: 96,
  likes: 12,
  url: "https://mdblist.com/lists/cinephile/oscar-winners",
};

describe("admin Synced list editor", () => {
  it("sends the whole MDBList source_config with a changed limit", async () => {
    answerAdmin(
      adminSyncedCollection("mdblist", {
        source_url: "https://mdblist.com/lists/user/top-watched/json",
        source_config: {
          mode: "mdblist_json",
          url: "https://mdblist.com/lists/user/top-watched/json",
          limit: 50,
        },
      }),
    );
    show(<></>, "/admin/collections/c1/edit?libraryId=1");
    const limit = await screen.findByRole("spinbutton", { name: "Max titles" });
    fireEvent.change(limit, { target: { value: "100" } });
    fireEvent.blur(limit);
    await saveAndWait(1);
    expect(v2Recorder.writes()).toEqual(goldens.adminEditMDBList);
  });

  it("sends the whole TMDB chart source_config on a rename", async () => {
    answerAdmin(
      adminSyncedCollection("tmdb", {
        source_url: "tmdb://trending/movie/week",
        source_config: {
          mode: "tmdb_preset",
          preset: "trending",
          media_type: "movie",
          time_window: "week",
          limit: 40,
        },
      }),
    );
    show(<></>, "/admin/collections/c1/edit?libraryId=1");
    await type("Name", "Trending This Week");
    await saveAndWait(1);
    expect(v2Recorder.writes()).toEqual(goldens.adminEditTMDBChart);
  });

  it("sends the whole TMDB list source_config on a rename", async () => {
    answerAdmin(
      adminSyncedCollection("tmdb", {
        source_url: "https://www.themoviedb.org/list/310",
        source_config: { mode: "tmdb_list", url: "https://www.themoviedb.org/list/310" },
      }),
    );
    show(<></>, "/admin/collections/c1/edit?libraryId=1");
    await type("Name", "Festival Picks");
    await saveAndWait(1);
    expect(v2Recorder.writes()).toEqual(goldens.adminEditTMDBList);
  });

  it("sends no source for a legacy Trakt list", async () => {
    answerAdmin(
      adminSyncedCollection("trakt", {
        source_url: "trakt://recommended/movie/p-owner",
        source_config: {
          mode: "trakt_preset",
          preset: "recommended",
          media_type: "movie",
          profile_id: "p-owner",
          limit: 40,
        },
      }),
    );
    show(<></>, "/admin/collections/c1/edit?libraryId=1");
    await type("Name", "For you");
    await saveAndWait(1);
    expect(v2Recorder.writes()).toEqual(goldens.adminEditTrakt);
  });
});

describe("personal Synced list editor", () => {
  beforeEach(() => {
    v2Recorder.answer(
      "GET /api/v2/collections/{id}",
      personalSyncedCollection("mdblist", {
        source_url: "https://mdblist.com/lists/user/top-watched",
        source_config: { limit: 50, library_ids: [1] },
      }),
    );
  });

  it("sends only what changed", async () => {
    show(<></>, "/collections/c1/edit");
    await type("Name", "Top Watched");
    await saveAndWait(1);
    expect(v2Recorder.writes()).toEqual(goldens.personalSyncedRename);
  });

  describe("poster removal", () => {
    beforeEach(() => {
      // The list carries the poster until the image DELETE lands.
      let posterUrl = "https://images.example/poster.png";
      v2Recorder.answer("GET /api/v2/collections", () => ({
        items: [{ id: "c1", poster_url: posterUrl }],
      }));
      v2Recorder.answer("DELETE /api/v2/collections/{id}/image", () => {
        posterUrl = "";
        return undefined;
      });
    });

    function posterSlot() {
      return screen.getByRole("group", { name: "Poster" });
    }

    async function removePoster() {
      show(<></>, "/collections/c1/edit");
      await chooseArtwork("poster", "Use the collage");
      expect(within(posterSlot()).queryByRole("img")).toBeNull();
      expect(within(saveBar()).getByText(/Poster not saved/)).toBeTruthy();
    }

    it("sends nothing and shows the poster again on Discard", async () => {
      await removePoster();
      await act(async () => {});
      expect(v2Recorder.writes()).toEqual([]);
      fireEvent.click(within(saveBar()).getByRole("button", { name: "Discard" }));
      expect(within(posterSlot()).getByRole("img")).toBeTruthy();
      expect(v2Recorder.writes()).toEqual([]);
    });

    it("deletes the poster after the PATCH on Save", async () => {
      await removePoster();
      await saveAndWait(2);
      expect(v2Recorder.writes()).toEqual(goldens.personalSyncedStagedPosterRemoval);
    });

    it("starts again from the saved collection after Save", async () => {
      await removePoster();
      await saveAndWait(2);
      expect(within(posterSlot()).queryByRole("img")).toBeNull();

      await type("Name", "Top Watched");
      fireEvent.click(within(saveBar()).getByRole("button", { name: "Discard" }));
      expect(within(posterSlot()).queryByRole("img")).toBeNull();

      // The second save sends the ETag the first one left, so it is not a 412.
      const savedETag = v2Recorder.etag("/api/v2/collections/c1");
      await type("Name", "Top Watched");
      await saveAndWait(3);
      expect(v2Recorder.writes()[2]?.headers["If-Match"]).toBe(savedETag);
    });
  });

  it("clears Max titles with max_items 0", async () => {
    show(<></>, "/collections/c1/edit");
    const limit = await screen.findByRole("spinbutton", { name: "Max titles" });
    fireEvent.change(limit, { target: { value: "" } });
    fireEvent.blur(limit);
    await saveAndWait(1);
    expect(v2Recorder.writes()).toEqual(goldens.personalSyncedClearLimit);
  });
});
