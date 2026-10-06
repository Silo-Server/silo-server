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
            section_key: "Sci Fi & Action",
            items: [
              { content_id: "item-2", title: "Movie B", type: "movie", year: 2023, genres: [] },
            ],
          },
          {
            type: "custom",
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

    expect(markup).toContain("For You");
    expect(markup).toContain("Popular in Action");
    expect(markup).toContain("Movie A");
    expect(markup).toContain("Movie B");
    expect(markup).toContain('data-title="Custom row" data-href=""');
    expect(documentTitles.at(-1)).toBe("For You");
    expect(markup).toContain('data-href="/recommendations/section/for-you-main"');
    expect(markup).toContain('data-href="/recommendations/section/genre/Sci%20Fi%20%26%20Action"');
  });
});
