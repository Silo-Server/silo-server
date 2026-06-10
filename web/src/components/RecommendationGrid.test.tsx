import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
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
});
