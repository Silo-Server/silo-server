import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { UICustomizationContext } from "@/contexts/uiCustomizationContext";
import RecommendationGrid from "./RecommendationGrid";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (...args: unknown[]) => mocks.useCatalogItemDetail(...args),
}));

describe("RecommendationGrid", () => {
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

  it("uses the selected density and omits artwork-only caption rows", () => {
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

    expect(markup).toContain("grid-cols-3 sm:grid-cols-5");
    expect(markup).not.toContain(">A Reader</p>");
  });
});
