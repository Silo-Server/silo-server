import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("react-router", () => ({
  useParams: () => ({ id: "movie-123" }),
  useSearchParams: () => [new URLSearchParams()],
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (...args: unknown[]) => mocks.useCatalogItemDetail(...args),
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
  },
}));

vi.mock("@/pages/ItemDetail/MovieContent", () => ({
  default: ({ item }: { item: { title: string } }) => <div>{item.title}</div>,
}));

vi.mock("@/pages/ItemDetail/SeriesContent", () => ({
  default: () => <div>Series</div>,
}));

vi.mock("@/pages/ItemDetail/SeasonContent", () => ({
  default: () => <div>Season</div>,
}));

vi.mock("@/pages/ItemDetail/EpisodeContent", () => ({
  default: () => <div>Episode</div>,
}));

vi.mock("@/pages/ItemDetail/AudiobookContent", () => ({
  default: () => <div>Audiobook</div>,
}));

vi.mock("@/pages/ItemDetail/EbookContent", () => ({
  default: ({ item }: { item: { title: string } }) => <div>Ebook: {item.title}</div>,
}));

import ItemDetail from "./index";

describe("ItemDetail", () => {
  beforeEach(() => {
    mocks.useCatalogItemDetail.mockReset();
    mocks.toastError.mockReset();
    mocks.useCatalogItemDetail.mockReturnValue({
      data: { content_id: "movie-123", title: "Catalog Detail", type: "movie" },
      isLoading: false,
      error: null,
    });
  });

  it("reads item detail through the canonical catalog detail hook", () => {
    const markup = renderToStaticMarkup(<ItemDetail />);

    expect(markup).toContain("Catalog Detail");
    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("movie-123", undefined);
  });

  it("routes ebook items to ebook detail content", () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: { content_id: "ebook-123", title: "A Psalm for the Wild-Built", type: "ebook" },
      isLoading: false,
      error: null,
    });

    const markup = renderToStaticMarkup(<ItemDetail />);

    expect(markup).toContain("Ebook: A Psalm for the Wild-Built");
  });
});
