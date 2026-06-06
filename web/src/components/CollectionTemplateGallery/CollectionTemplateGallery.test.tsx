import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Library } from "@/api/types";
import { CollectionTemplateGallery } from "./CollectionTemplateGallery";

// Radix Select reads element sizes via ResizeObserver, which jsdom does not
// provide. A no-op polyfill is enough to render the dialog content.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
if (typeof window !== "undefined" && !window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const apiClientMocks = vi.hoisted(() => {
  class ApiClientErrorMock extends Error {
    status: number;

    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  }

  return {
    ApiClientErrorMock,
    fetchMock: vi.fn(),
  };
});

const { fetchMock } = apiClientMocks;

vi.mock("@/api/client", () => ({
  ApiClientError: apiClientMocks.ApiClientErrorMock,
  api: (path: string, options?: unknown) => fetchMock(path, options),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [] as Array<{ id: string; name: string }> }),
}));

vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));

const catalogResponse = {
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
          tmdb: { preset: "trending", media_type: "movie", time_window: "week" },
        },
      ],
    },
    {
      category: "popular",
      label: "Popular",
      templates: [
        {
          id: "trakt_popular_shows",
          title: "Trakt Popular Shows",
          description: "Trakt's most-watched shows.",
          icon: "🌟",
          category: "popular",
          source: "trakt",
          media_kind: "tv",
          trakt: { preset: "popular", media_type: "tv" },
        },
      ],
    },
  ],
};

const bundlesResponse = {
  bundles: [
    {
      id: "core_defaults",
      title: "Core Defaults",
      description: "A focused starter set of movie and TV collections.",
      template_ids: ["tmdb_trending_movies_week", "trakt_popular_shows"],
    },
  ],
};

const libraries: Library[] = [
  { id: 1, name: "Movies", type: "movies" } as unknown as Library,
  { id: 2, name: "TV Shows", type: "series" } as unknown as Library,
];

function renderGallery() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <CollectionTemplateGallery
        open
        onOpenChange={() => {}}
        libraries={libraries}
        initialLibraryId={1}
      />
    </QueryClientProvider>,
  );
}

describe("CollectionTemplateGallery", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockImplementation((path: string) => {
      if (path === "/admin/collections/templates") return Promise.resolve(catalogResponse);
      if (path === "/admin/collections/template-bundles") return Promise.resolve(bundlesResponse);
      throw new Error(`unexpected path: ${path}`);
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("loads and displays templates grouped by category", async () => {
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Trending Movies This Week")).toBeInTheDocument();
    });
    expect(screen.getByText("Trakt Popular Shows")).toBeInTheDocument();
    // Section labels render once in headings; pills render once each as well.
    expect(screen.getAllByText("Trending").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Popular").length).toBeGreaterThan(0);
    expect(screen.getByText("Core Defaults")).toBeInTheDocument();
  });

  it("filters templates by search across title and description", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Trending Movies This Week")).toBeInTheDocument();
    });

    await user.type(screen.getByPlaceholderText("Search templates"), "trakt");
    expect(screen.queryByText("Trending Movies This Week")).not.toBeInTheDocument();
    expect(screen.getByText("Trakt Popular Shows")).toBeInTheDocument();
  });

  it("opens the config form when a template card is selected", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Trending Movies This Week")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Trending Movies This Week"));
    // The drawer renders the explicit submit button.
    expect(screen.getByRole("button", { name: /Create Collection/i })).toBeInTheDocument();
  });

  it("does not preselect an ineligible initial library for TV templates", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Trakt Popular Shows")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Trakt Popular Shows"));

    expect(screen.getByRole("button", { name: /TV Shows/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Movies$/i })).not.toBeInTheDocument();
  });

  it("dispatches to the TMDB import endpoint when submitting a TMDB template", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Trending Movies This Week")).toBeInTheDocument();
    });

    fetchMock.mockImplementation((path: string) => {
      if (path === "/admin/collections/templates") return Promise.resolve(catalogResponse);
      if (path === "/admin/collections/template-bundles") return Promise.resolve(bundlesResponse);
      if (path === "/admin/collections/import/tmdb") {
        return Promise.resolve({ collection: { id: "x" } });
      }
      throw new Error(`unexpected path: ${path}`);
    });

    await user.click(screen.getByText("Trending Movies This Week"));
    await user.click(screen.getByRole("button", { name: /Create Collection/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/admin/collections/import/tmdb", expect.any(Object));
    });
  });

  it("dispatches to the Trakt import endpoint when submitting a Trakt template", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Trakt Popular Shows")).toBeInTheDocument();
    });

    fetchMock.mockImplementation((path: string) => {
      if (path === "/admin/collections/templates") return Promise.resolve(catalogResponse);
      if (path === "/admin/collections/template-bundles") return Promise.resolve(bundlesResponse);
      if (path === "/admin/collections/import/trakt") {
        return Promise.resolve({ collection: { id: "y" } });
      }
      throw new Error(`unexpected path: ${path}`);
    });

    await user.click(screen.getByText("Trakt Popular Shows"));
    await user.click(screen.getByRole("button", { name: /Create Collection/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/admin/collections/import/trakt", expect.any(Object));
    });
  });

  it("previews the core defaults bundle", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Core Defaults")).toBeInTheDocument();
    });

    fetchMock.mockImplementation((path: string) => {
      if (path === "/admin/collections/templates") return Promise.resolve(catalogResponse);
      if (path === "/admin/collections/template-bundles") return Promise.resolve(bundlesResponse);
      if (path === "/admin/collections/template-bundles/core_defaults/apply") {
        return Promise.resolve({
          bundle_id: "core_defaults",
          dry_run: true,
          created: [
            {
              template_id: "tmdb_trending_movies_week",
              template_title: "Trending Movies This Week",
              library_id: 1,
              library_name: "Movies",
              reason: "would_create",
            },
          ],
          skipped: [],
          failed: [],
        });
      }
      throw new Error(`unexpected path: ${path}`);
    });

    await user.click(screen.getByText("Core Defaults"));
    expect(screen.getByText("Featured Sections")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Preview$/i }));

    await waitFor(() => {
      expect(screen.getByText(/Would create 1; skipped 0; failed 0/i)).toBeInTheDocument();
    });
    const applyCall = fetchMock.mock.calls.find(
      ([path]) => path === "/admin/collections/template-bundles/core_defaults/apply",
    );
    expect(JSON.parse(String(applyCall?.[1]?.body))).toMatchObject({
      featured: {
        home: { library_id: 1, template_id: "tmdb_trending_movies_week" },
        libraries: { "1": "tmdb_trending_movies_week" },
      },
    });
  });

  it("previews deleting existing server collections before applying defaults", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Core Defaults")).toBeInTheDocument();
    });

    fetchMock.mockImplementation((path: string) => {
      if (path === "/admin/collections/templates") return Promise.resolve(catalogResponse);
      if (path === "/admin/collections/template-bundles") return Promise.resolve(bundlesResponse);
      if (path === "/admin/collections/template-bundles/core_defaults/apply") {
        return Promise.resolve({
          bundle_id: "core_defaults",
          dry_run: true,
          delete_existing: true,
          deleted: [
            {
              library_id: 1,
              library_name: "Movies",
              collection_id: "lc_old",
              collection_title: "Old Movies",
              reason: "would_delete",
            },
          ],
          delete_skipped: [],
          delete_failed: [],
          created: [],
          skipped: [],
          failed: [],
        });
      }
      throw new Error(`unexpected path: ${path}`);
    });

    await user.click(screen.getByText("Core Defaults"));
    await user.click(screen.getByText("Delete Existing Server Collections"));
    await user.click(screen.getByRole("button", { name: /^Preview$/i }));

    await waitFor(() => {
      expect(
        screen.getByText(/Would delete 1; delete skipped 0; delete failed 0/i),
      ).toBeInTheDocument();
    });
    const applyCall = fetchMock.mock.calls.find(
      ([path]) => path === "/admin/collections/template-bundles/core_defaults/apply",
    );
    expect(JSON.parse(String(applyCall?.[1]?.body))).toMatchObject({
      dry_run: true,
      delete_existing: true,
      library_ids: [1],
    });
  });

  it("queues the core defaults bundle apply job", async () => {
    const user = userEvent.setup();
    renderGallery();

    await waitFor(() => {
      expect(screen.getByText("Core Defaults")).toBeInTheDocument();
    });

    fetchMock.mockImplementation((path: string) => {
      if (path === "/admin/collections/templates") return Promise.resolve(catalogResponse);
      if (path === "/admin/collections/template-bundles") return Promise.resolve(bundlesResponse);
      if (path === "/admin/collections/template-bundles/core_defaults/apply-job") {
        return Promise.resolve({
          id: "job-1",
          job_type: "template_bundle_apply",
          status: "queued",
        });
      }
      throw new Error(`unexpected path: ${path}`);
    });

    await user.click(screen.getByText("Core Defaults"));
    await user.click(screen.getByRole("button", { name: /Apply Defaults/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/admin/collections/template-bundles/core_defaults/apply-job",
        expect.objectContaining({
          method: "POST",
          body: expect.stringContaining('"library_ids":[1]'),
        }),
      );
    });
  });
});
