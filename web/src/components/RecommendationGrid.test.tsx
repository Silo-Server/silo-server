import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UICustomizationContext } from "@/contexts/uiCustomizationContext";
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

  it("encodes item IDs in detail links", () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: {
        content_id: "ebook 1",
        title: "A Reader",
        poster_url: "",
      },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RecommendationGrid items={[{ media_item_id: "ebook 1" }]} />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/ebook%201"');
    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("ebook 1");
  });

  it("uses the shared Embla carousel at the selected density and omits artwork-only captions", () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: {
        content_id: "ebook 1",
        title: "A Reader",
        poster_url: "/cover.jpg",
      },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <UICustomizationContext.Provider
          value={{
            cardPresentation: { poster_size: "compact", caption: "artwork" },
            cardPresentationSource: "profile_client",
            primaryMenu: null,
            primaryMenuSource: "default",
            shortcuts: { items: [] },
            isSupported: true,
            supportsAtomicShortcuts: true,
            isLoading: false,
            isUnavailable: false,
          }}
        >
          <RecommendationGrid items={[{ media_item_id: "ebook 1" }]} />
        </UICustomizationContext.Provider>
      </MemoryRouter>,
    );

    expect(markup).toContain("embla__viewport");
    expect(markup).toContain("w-[120px] shrink-0 sm:w-[140px] lg:w-[160px]");
    expect(markup).not.toContain("mt-1.5 block truncate");
  });

  it("renders at most 12 recommendation cards", () => {
    mocks.useCatalogItemDetail.mockImplementation((itemId: string) => ({
      data: { content_id: itemId, title: itemId, poster_url: "" },
    }));
    const items = Array.from({ length: 14 }, (_, index) => ({
      media_item_id: `item-${index + 1}`,
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
        content_id: "series-1",
        play_content_id: "episode-4",
        title: "Running Show",
        poster_url: "/poster.jpg",
      },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RecommendationGrid items={[{ media_item_id: "series-1" }]} />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/series-1"');
    expect(markup).toContain('href="/watch/episode-4"');
    expect(markup).toContain('aria-label="Play Running Show"');
  });
});
