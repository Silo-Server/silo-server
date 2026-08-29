// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import LibraryRecommended from "./LibraryRecommended";
import { bumpHomeRefreshSignal } from "./homeSurfaceRefresh";
import { invalidateMediaSurfaceQueries } from "@/hooks/queries/mediaSurfaceRefresh";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mockUseLibraryLayout = vi.fn();
const mockFetchLibrarySectionItems = vi.fn();
const mockUseSidebarPins = vi.fn();
const mockUseLibraryCollectionItems = vi.fn();

vi.mock("@/hooks/queries/sections", () => ({
  useLibraryLayout: (...args: unknown[]) => mockUseLibraryLayout(...args),
  fetchLibrarySectionItems: (...args: unknown[]) => mockFetchLibrarySectionItems(...args),
}));

vi.mock("@/hooks/queries/sidebarPins", () => ({
  useSidebarPins: (...args: unknown[]) => mockUseSidebarPins(...args),
}));

vi.mock("@/hooks/queries/libraryCollections", () => ({
  useLibraryCollectionItems: (...args: unknown[]) => mockUseLibraryCollectionItems(...args),
}));

vi.mock("@/components/MediaCarousel", () => ({
  default: ({
    title,
    children,
    loading,
  }: {
    title: string;
    children: ReactNode;
    loading?: boolean;
  }) => (
    <section data-kind="carousel" data-loading={loading ? "true" : "false"}>
      <h2>{title}</h2>
      {children}
    </section>
  ),
}));

vi.mock("@/components/HeroBanner", () => ({
  default: ({ items }: { items: Array<{ title: string }> }) => (
    <div data-kind="hero">{items.map((item) => item.title).join(",")}</div>
  ),
}));

vi.mock("@/components/SectionRow", () => ({
  default: ({
    section,
  }: {
    section: {
      title: string;
      section_type: string;
      items: Array<{ user_state?: { is_favorite: boolean } }>;
    };
  }) => (
    <div
      data-kind="section-row"
      data-section-type={section.section_type}
      data-favorite={section.items[0]?.user_state?.is_favorite ? "true" : "false"}
    >
      {section.title}
    </div>
  ),
}));

vi.mock("@/components/ItemCard", () => ({
  default: ({ item }: { item: { title: string } }) => <div>{item.title}</div>,
}));

function makeLayout(
  overrides: Partial<{
    id: string;
    section_type: string;
    title: string;
    featured: boolean;
  }> = {},
) {
  return {
    id: overrides.id ?? "section-1",
    section_type: overrides.section_type ?? "recently_added",
    title: overrides.title ?? "Recently Added",
    featured: overrides.featured ?? false,
    item_limit: 20,
    is_custom: false,
    customized: false,
  };
}

function makeSection(
  overrides: Partial<{
    id: string;
    section_type: string;
    title: string;
    featured: boolean;
    isFavorite: boolean;
  }> = {},
) {
  return {
    id: overrides.id ?? "section-1",
    section_type: overrides.section_type ?? "recently_added",
    title: overrides.title ?? "Recently Added",
    featured: overrides.featured ?? false,
    item_limit: 20,
    total_count: 1,
    is_custom: false,
    customized: false,
    items: [
      {
        content_id: "item-1",
        type: "movie",
        title: "Item One",
        year: 2024,
        genres: [],
        status: "matched",
        rating_imdb: null,
        overview: "",
        poster_url: "",
        poster_thumbhash: "",
        backdrop_url: "",
        backdrop_thumbhash: "",
        logo_url: "",
        user_state: {
          played: false,
          is_favorite: overrides.isFavorite ?? false,
          in_watchlist: false,
        },
      },
    ],
  };
}

describe("LibraryRecommended", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    mockUseLibraryLayout.mockReturnValue({
      data: {
        sections: [
          makeLayout({
            id: "cw",
            section_type: "continue_watching",
            title: "Continue Watching",
          }),
          makeLayout({
            id: "recent",
            title: "Recently Added",
          }),
        ],
      },
      isLoading: false,
    });
    mockFetchLibrarySectionItems.mockImplementation((_libraryId: number, sectionId: string) =>
      Promise.resolve({
        section:
          sectionId === "cw"
            ? makeSection({
                id: "cw",
                section_type: "continue_watching",
                title: "Continue Watching",
              })
            : makeSection({
                id: "recent",
                title: "Recently Added",
              }),
      }),
    );
    mockUseSidebarPins.mockReturnValue({ pins: {} });
    mockUseLibraryCollectionItems.mockReturnValue({ data: [], isLoading: false });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  async function render(ui: ReactNode) {
    const queryClient = new QueryClient();
    await act(async () => {
      root.render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>);
      await Promise.resolve();
      await Promise.resolve();
    });
    return queryClient;
  }

  it("renders sections from the layout and item APIs", async () => {
    await render(<LibraryRecommended libraryId={42} />);

    expect(container.textContent).toContain("Continue Watching");
    expect(container.textContent).toContain("Recently Added");
    expect(mockFetchLibrarySectionItems).toHaveBeenCalledWith(42, "cw", expect.any(Object));
    expect(mockFetchLibrarySectionItems).toHaveBeenCalledWith(42, "recent", expect.any(Object));
  });

  it("does not invalidate cached library sections on mount", async () => {
    const invalidateQueries = vi.spyOn(QueryClient.prototype, "invalidateQueries");

    await render(<LibraryRecommended libraryId={42} />);

    expect(invalidateQueries).not.toHaveBeenCalled();
    invalidateQueries.mockRestore();
  });

  it("reloads locally held library rows after consecutive media surface refreshes", async () => {
    let isFavorite = false;
    mockFetchLibrarySectionItems.mockImplementation((_libraryId: number, sectionId: string) =>
      Promise.resolve({
        section: makeSection({
          id: sectionId,
          title: sectionId === "cw" ? "Continue Watching" : "Recently Added",
          section_type: sectionId === "cw" ? "continue_watching" : "recently_added",
          isFavorite,
        }),
      }),
    );
    const queryClient = await render(<LibraryRecommended libraryId={42} />);

    expect(
      container
        .querySelector('[data-section-type="recently_added"]')
        ?.getAttribute("data-favorite"),
    ).toBe("false");

    isFavorite = true;
    await act(async () => {
      await invalidateMediaSurfaceQueries(queryClient);
      bumpHomeRefreshSignal(queryClient);
    });

    await waitFor(() => {
      expect(
        container
          .querySelector('[data-section-type="recently_added"]')
          ?.getAttribute("data-favorite"),
      ).toBe("true");
    });

    isFavorite = false;
    await act(async () => {
      await invalidateMediaSurfaceQueries(queryClient);
      bumpHomeRefreshSignal(queryClient);
    });

    await waitFor(() => {
      expect(
        container
          .querySelector('[data-section-type="recently_added"]')
          ?.getAttribute("data-favorite"),
      ).toBe("false");
    });
  });

  it("renders hero banner for featured sections", async () => {
    mockUseLibraryLayout.mockReturnValue({
      data: {
        sections: [
          makeLayout({ id: "hero", title: "Featured", featured: true }),
          makeLayout({ id: "recent", title: "Recently Added" }),
        ],
      },
      isLoading: false,
    });
    mockFetchLibrarySectionItems.mockImplementation((_libraryId: number, sectionId: string) =>
      Promise.resolve({
        section:
          sectionId === "hero"
            ? makeSection({ id: "hero", title: "Featured", featured: true })
            : makeSection({ id: "recent", title: "Recently Added" }),
      }),
    );

    await render(<LibraryRecommended libraryId={42} />);

    expect(container.innerHTML).toContain('data-kind="hero"');
    expect(container.textContent).toContain("Recently Added");
  });

  it("renders nothing while loading with no data", async () => {
    mockUseLibraryLayout.mockReturnValue({
      data: undefined,
      isLoading: true,
    });

    await render(<LibraryRecommended libraryId={42} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders pinned collection rows from sidebar_pins for the current library", async () => {
    mockUseSidebarPins.mockReturnValue({
      pins: {
        "42": [
          { type: "collection", id: "col-1", label: "Pinned Horror" },
          { type: "section", id: "sec-1", label: "Recently Added" },
        ],
        "99": [{ type: "collection", id: "col-2", label: "Other Library Collection" }],
      },
    });
    mockUseLibraryCollectionItems.mockImplementation((libraryId: number, collectionId: string) => {
      if (libraryId === 42 && collectionId === "col-1") {
        return {
          data: [
            {
              content_id: "item-1",
              title: "Scream",
            },
          ],
          isLoading: false,
        };
      }

      return { data: [], isLoading: false };
    });

    await render(<LibraryRecommended libraryId={42} />);

    expect(container.textContent).toContain("Pinned Horror");
    expect(container.textContent).toContain("Scream");
    expect(container.textContent).not.toContain("Other Library Collection");
    expect(mockUseLibraryCollectionItems).toHaveBeenCalledWith(42, "col-1");
  });
});
