import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import RecommendationGrid from "./RecommendationGrid";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (...args: unknown[]) => mocks.useCatalogItemDetail(...args),
}));

vi.mock("@/components/CardPlayOverlay", () => ({
  default: ({ contentId, title }: { contentId: string; title: string }) => (
    <a href={`/watch/${contentId}`} aria-label={`Play ${title}`} />
  ),
}));

describe("RecommendationGrid", () => {
  beforeEach(() => {
    mocks.useCatalogItemDetail.mockReset();
  });

  it("renders at most 12 recommendation cards", () => {
    mocks.useCatalogItemDetail.mockImplementation((itemId: string) => ({
      data: { content_id: itemId, title: itemId, poster_url: "" },
    }));
    const items = Array.from({ length: 14 }, (_, index) => ({
      content_id: `item-${index + 1}`,
    }));

    renderToStaticMarkup(
      <MemoryRouter>
        <RecommendationGrid items={items} maxItems={50} />
      </MemoryRouter>,
    );

    expect(mocks.useCatalogItemDetail).toHaveBeenCalledTimes(12);
    expect(mocks.useCatalogItemDetail).not.toHaveBeenCalledWith("item-13");
  });

  it("keeps recommendation details and playback as independent links", () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: {
        content_id: "series 1",
        play_content_id: "episode-4",
        title: "Running Show",
        poster_url: "/poster.jpg",
      },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RecommendationGrid items={[{ content_id: "series 1" }]} />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/series%201"');
    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("series 1");
    expect(markup).toContain('href="/watch/episode-4"');
    expect(markup).toContain('aria-label="Play Running Show"');
  });
});
