import { renderToStaticMarkup } from "react-dom/server";
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  toastError: vi.fn(),
  search: "",
}));

vi.mock("react-router", () => ({
  useParams: () => ({ id: "movie-123" }),
  useSearchParams: () => [new URLSearchParams(mocks.search)],
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (...args: unknown[]) => mocks.useCatalogItemDetail(...args),
}));

// Mocked like every other data hook in this file: the page renders here
// without a QueryClient, and the badge setting is not what these cases cover.
vi.mock("@/hooks/useShowAdvisoryAge", () => ({
  useShowAdvisoryAge: () => false,
}));
vi.mock("./useThemeMusic", () => ({ useThemeMusic: vi.fn() }));

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
import {
  SidebarItemDetailsReadyContext,
  SidebarItemEnteredFromHomeContext,
} from "@/components/sidebarItemNavigationContext";

describe("ItemDetail", () => {
  beforeEach(() => {
    mocks.useCatalogItemDetail.mockReset();
    mocks.toastError.mockReset();
    mocks.search = "";
    mocks.useCatalogItemDetail.mockReturnValue({
      data: { content_id: "movie-123", title: "Catalog Detail", type: "movie" },
      isLoading: false,
      error: null,
    });
  });

  it("ignores a malformed library id so the detail query matches the prefetch key", () => {
    mocks.search = "libraryId=abc";

    renderToStaticMarkup(<ItemDetail />);

    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("movie-123", undefined);
  });

  it("reads item detail through the canonical catalog detail hook", () => {
    const markup = renderToStaticMarkup(<ItemDetail />);

    expect(markup).toContain("Catalog Detail");
    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("movie-123", undefined);
  });

  it("keeps the lightweight shell mounted until the sidebar transition finishes", () => {
    const markup = renderToStaticMarkup(
      <SidebarItemDetailsReadyContext.Provider value={false}>
        <ItemDetail />
      </SidebarItemDetailsReadyContext.Provider>,
    );

    expect(markup).not.toContain("Catalog Detail");
    expect(markup).toContain("animate-pulse");
  });

  it("uses a small opaque skeleton with no pulsing work for Home entries", () => {
    const markup = renderToStaticMarkup(
      <SidebarItemEnteredFromHomeContext.Provider value>
        <SidebarItemDetailsReadyContext.Provider value={false}>
          <ItemDetail />
        </SidebarItemDetailsReadyContext.Provider>
      </SidebarItemEnteredFromHomeContext.Provider>,
    );

    expect(markup).toContain("home-item-transition-shell");
    expect(markup).toContain("min-h-[60dvh]");
    expect(markup).toContain("home-item-transition-poster");
    expect(markup).toContain("home-item-transition-title");
    expect(markup.match(/home-item-transition-block/g)).toHaveLength(8);
    expect(markup).not.toContain("animate-pulse");
  });

  it("keeps the opaque Home shell while item data is still loading", () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: undefined,
      isLoading: true,
      error: null,
    });

    const markup = renderToStaticMarkup(
      <SidebarItemEnteredFromHomeContext.Provider value>
        <SidebarItemDetailsReadyContext.Provider value>
          <ItemDetail />
        </SidebarItemDetailsReadyContext.Provider>
      </SidebarItemEnteredFromHomeContext.Provider>,
    );

    expect(markup).toContain("home-item-transition-shell");
    expect(markup).not.toContain("animate-pulse");
  });

  it.each([
    ["season", { content_id: "season-1", title: "Season 1", type: "season" }],
    ["episode", { content_id: "episode-1", title: "Pilot", type: "episode" }],
    ["audiobook", { content_id: "audiobook-1", title: "Dune", type: "audiobook" }],
    [
      "logo",
      {
        content_id: "movie-123",
        title: "Catalog Detail",
        type: "movie",
        logo_url: "/api/v1/items/movie-123/logo",
      },
    ],
  ])("keeps neutral shell geometry when cold %s data resolves mid-handoff", (_, item) => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: undefined,
      isLoading: true,
      error: null,
    });

    const homeEntry = () => (
      <SidebarItemEnteredFromHomeContext.Provider value>
        <SidebarItemDetailsReadyContext.Provider value={false}>
          <ItemDetail />
        </SidebarItemDetailsReadyContext.Provider>
      </SidebarItemEnteredFromHomeContext.Provider>
    );
    const view = render(homeEntry());
    const initialShell = screen.getByTestId("home-item-transition-shell");
    const initialGeometry = initialShell.innerHTML;

    expect(initialGeometry).toContain("min-h-[60dvh]");
    expect(initialGeometry).toContain("home-item-transition-poster");
    expect(initialGeometry).toContain("aspect-[2/3]");

    mocks.useCatalogItemDetail.mockReturnValue({
      data: item,
      isLoading: false,
      error: null,
    });
    view.rerender(homeEntry());

    expect(screen.getByTestId("home-item-transition-shell")).toBe(initialShell);
    expect(screen.getByTestId("home-item-transition-shell").innerHTML).toBe(initialGeometry);
  });

  it("matches the compact hero height for a season Home entry", () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: { content_id: "season-1", title: "Season 1", type: "season" },
      isLoading: false,
      error: null,
    });

    const markup = renderToStaticMarkup(
      <SidebarItemEnteredFromHomeContext.Provider value>
        <SidebarItemDetailsReadyContext.Provider value={false}>
          <ItemDetail />
        </SidebarItemDetailsReadyContext.Provider>
      </SidebarItemEnteredFromHomeContext.Provider>,
    );

    expect(markup).toContain("min-h-[max(35vh,300px)]");
    expect(markup).not.toContain("min-h-[60dvh]");
  });

  it("matches episode and audiobook poster geometry without loading artwork", () => {
    mocks.useCatalogItemDetail.mockReturnValue({
      data: { content_id: "episode-1", title: "Pilot", type: "episode" },
      isLoading: false,
      error: null,
    });

    const episodeMarkup = renderToStaticMarkup(
      <SidebarItemEnteredFromHomeContext.Provider value>
        <SidebarItemDetailsReadyContext.Provider value={false}>
          <ItemDetail />
        </SidebarItemDetailsReadyContext.Provider>
      </SidebarItemEnteredFromHomeContext.Provider>,
    );

    expect(episodeMarkup).not.toContain("home-item-transition-poster");

    mocks.useCatalogItemDetail.mockReturnValue({
      data: { content_id: "audiobook-1", title: "Dune", type: "audiobook" },
      isLoading: false,
      error: null,
    });

    const audiobookMarkup = renderToStaticMarkup(
      <SidebarItemEnteredFromHomeContext.Provider value>
        <SidebarItemDetailsReadyContext.Provider value={false}>
          <ItemDetail />
        </SidebarItemDetailsReadyContext.Provider>
      </SidebarItemEnteredFromHomeContext.Provider>,
    );

    expect(audiobookMarkup).toContain("home-item-transition-poster");
    expect(audiobookMarkup).toContain("aspect-square");
    expect(audiobookMarkup).not.toContain("src=");
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
