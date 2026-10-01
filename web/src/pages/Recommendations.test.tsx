import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import Recommendations from "./Recommendations";

const mockUseTasteProfile = vi.fn();
const mockUseDiscover = vi.fn();

vi.mock("@/hooks/queries/recommendations", () => ({
  useTasteProfile: (...args: unknown[]) => mockUseTasteProfile(...args),
  useDiscover: (...args: unknown[]) => mockUseDiscover(...args),
}));

const documentTitles = vi.hoisted(() => [] as string[]);
vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: (title: string) => {
    documentTitles.push(title);
  },
}));

vi.mock("@/components/MediaCarousel", () => ({
  default: ({
    title,
    titleHref,
    loading,
    children,
  }: {
    title: string;
    titleHref?: string;
    loading?: boolean;
    children: React.ReactNode;
  }) => (
    <div
      data-kind="media-carousel"
      data-title={title}
      data-href={titleHref ?? ""}
      data-loading={loading}
    >
      {children}
    </div>
  ),
}));

vi.mock("@/components/SectionItemCard", () => ({
  default: ({ item }: { item: { content_id: string; title: string } }) => (
    <div data-kind="section-item-card" data-id={item.content_id}>
      {item.title}
    </div>
  ),
}));

function renderPage() {
  const queryClient = new QueryClient();
  return renderToStaticMarkup(
    <QueryClientProvider client={queryClient}>
      <Recommendations />
    </QueryClientProvider>,
  );
}

describe("Recommendations", () => {
  beforeEach(() => {
    mockUseTasteProfile.mockReturnValue({
      data: {
        top_genres: ["Drama"],
        favorite_directors: ["Jane Doe"],
        signal_counts: { rated_high: 3 },
      },
      isLoading: false,
    });
    mockUseDiscover.mockReturnValue({ data: undefined, isLoading: true, isError: false });
  });

  it("renders loading skeletons while discover data is loading", () => {
    const markup = renderPage();

    expect(markup).toContain('data-slot="skeleton"');
    // The viewer-facing name matches the native apps.
    expect(markup).toContain("For You");
    expect(markup).not.toContain(">Recommendations<");
    expect(documentTitles.at(-1)).toBe("For You");
  });

  it("renders empty state when discover returns no rows", () => {
    mockUseDiscover.mockReturnValue({
      data: { rows: [] },
      isLoading: false,
      isError: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Not enough data yet");
  });

  it("renders error state on failure", () => {
    mockUseDiscover.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
    });

    const markup = renderPage();

    expect(markup).toContain("Failed to load recommendations");
    expect(markup).toContain("Retry");
  });

  it("renders carousel rows with enriched items", () => {
    mockUseDiscover.mockReturnValue({
      data: {
        rows: [
          {
            type: "cluster",
            label: "For You",
            section_kind: "for-you-main",
            items: [
              { content_id: "item-1", title: "Movie A", type: "movie", year: 2024, genres: [] },
            ],
          },
          {
            type: "genre_sampler",
            label: "Popular in Action",
            section_kind: "genre",
            section_key: "Action",
            items: [
              { content_id: "item-2", title: "Movie B", type: "movie", year: 2023, genres: [] },
            ],
          },
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = renderPage();

    expect(markup).toContain("For You");
    expect(markup).toContain("Popular in Action");
    expect(markup).toContain("Movie A");
    expect(markup).toContain("Movie B");
    expect(markup).toContain('data-href="/recommendations/section/for-you-main"');
    expect(markup).toContain('data-href="/recommendations/section/genre/Action"');
  });

  it("omits section href when row has no section_kind", () => {
    mockUseDiscover.mockReturnValue({
      data: {
        rows: [
          {
            type: "unknown",
            label: "Custom row",
            items: [
              { content_id: "item-3", title: "Movie C", type: "movie", year: 2023, genres: [] },
            ],
          },
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Custom row");
    expect(markup).toContain('data-href=""');
  });

  it("renders blended premiere items inside the normal For You row", () => {
    mockUseDiscover.mockReturnValue({
      data: {
        rows: [
          {
            type: "cluster",
            label: "For You",
            items: [
              {
                content_id: "series-1",
                title: "Series A",
                type: "series",
                year: 2024,
                genres: [],
                upcoming_event: {
                  type: "episode",
                  air_date: "2026-04-07",
                  season_number: 2,
                  episode_number: 1,
                  badges: ["season_premiere"],
                },
              },
              { content_id: "item-2", title: "Movie B", type: "movie", year: 2023, genres: [] },
            ],
          },
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = renderPage();

    expect(markup).toContain('data-kind="section-item-card"');
    expect(markup).toContain("For You");
    expect(markup).not.toContain("Premiering Soon For You");
  });

  it("renders taste profile when available", () => {
    mockUseDiscover.mockReturnValue({
      data: { rows: [] },
      isLoading: false,
      isError: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Drama");
    expect(markup).toContain("Jane Doe");
    expect(markup).toContain("3");
  });

  it("hides taste profile card when profile is empty", () => {
    mockUseTasteProfile.mockReturnValue({
      data: { top_genres: [], favorite_directors: [], signal_counts: {} },
      isLoading: false,
    });
    mockUseDiscover.mockReturnValue({
      data: { rows: [] },
      isLoading: false,
      isError: false,
    });

    const markup = renderPage();

    expect(markup).not.toContain("Your Taste Profile");
  });
});
