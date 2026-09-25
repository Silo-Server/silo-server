import { renderToStaticMarkup } from "react-dom/server";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { V2ProblemError } from "@/api/v2/request";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  useUserLibraries: vi.fn(),
  refetchLibraries: vi.fn(),
  toastError: vi.fn(),
  search: "",
  id: "movie-123",
}));

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return {
    ...actual,
    useParams: () => ({ id: mocks.id }),
    useSearchParams: () => [new URLSearchParams(mocks.search)],
  };
});

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (...args: unknown[]) => mocks.useCatalogItemDetail(...args),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => mocks.useUserLibraries(),
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

function itemProblem(status: number) {
  return new V2ProblemError("getCatalogItem", {
    type: `https://silo.example/problems/${status === 404 ? "not_found" : "internal_error"}`,
    title: status === 404 ? "Not Found" : "Internal Server Error",
    status,
    detail: status === 404 ? "Item not found." : "The catalog is unavailable.",
    instance: "/api/v2/catalog/items/movie-123",
  });
}

function libraryList(overrides: Record<string, unknown> = {}) {
  return {
    data: [{ id: 4, name: "Movies", type: "movies" }],
    dataUpdatedAt: 0,
    isFetching: false,
    isError: false,
    refetch: mocks.refetchLibraries,
    ...overrides,
  };
}

function renderInRouter(ui: React.ReactElement) {
  return render(<MemoryRouter initialEntries={["/item/movie-123"]}>{ui}</MemoryRouter>);
}

describe("ItemDetail", () => {
  beforeEach(() => {
    mocks.useCatalogItemDetail.mockReset();
    mocks.useUserLibraries.mockReset();
    mocks.refetchLibraries.mockReset();
    mocks.toastError.mockReset();
    mocks.search = "";
    mocks.id = "movie-123";
    mocks.useCatalogItemDetail.mockReturnValue({
      data: { content_id: "movie-123", title: "Catalog Detail", type: "movie" },
      isLoading: false,
      error: null,
    });
    // A read that finished after the page appeared: the only kind that can
    // vouch for the link's library.
    mocks.useUserLibraries.mockReturnValue(libraryList({ dataUpdatedAt: Date.now() + 60_000 }));
    document.title = "Silo";
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

  describe("when the item cannot be shown", () => {
    it("explains a 404 on the page without a toast or a claim about why", () => {
      mocks.useCatalogItemDetail.mockReturnValue({
        data: undefined,
        isLoading: false,
        error: itemProblem(404),
      });

      renderInRouter(<ItemDetail />);

      expect(
        screen.getByRole("heading", { level: 1, name: "This item isn't available" }),
      ).toBeInTheDocument();
      expect(
        screen.getByText("It may have been removed, or you may not have access to it."),
      ).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Go home" })).toHaveAttribute("href", "/");
      expect(mocks.toastError).not.toHaveBeenCalled();
      expect(document.title).toContain("Not found");
    });

    it("lets a refetch's 404 replace an item that was already cached", () => {
      mocks.useCatalogItemDetail.mockReturnValue({
        data: { content_id: "movie-123", title: "Catalog Detail", type: "movie" },
        isLoading: false,
        error: itemProblem(404),
      });

      renderInRouter(<ItemDetail />);

      expect(
        screen.getByRole("heading", { level: 1, name: "This item isn't available" }),
      ).toBeInTheDocument();
      expect(screen.queryByText("Catalog Detail")).not.toBeInTheDocument();
      expect(document.title).toContain("Not found");
      expect(mocks.toastError).not.toHaveBeenCalled();
    });

    it("offers the link's library while the viewer can still open it", () => {
      mocks.search = "libraryId=4";
      mocks.useCatalogItemDetail.mockReturnValue({
        data: undefined,
        isLoading: false,
        error: itemProblem(404),
      });

      renderInRouter(<ItemDetail />);

      expect(screen.getByRole("link", { name: "Browse library" })).toHaveAttribute(
        "href",
        "/library/4",
      );
      expect(mocks.refetchLibraries).toHaveBeenCalledTimes(1);
    });

    it.each([
      [
        "while the fresh read is in flight",
        { isFetching: true, dataUpdatedAt: Date.now() + 60_000 },
      ],
      ["on a list cached before the page appeared", { dataUpdatedAt: 0 }],
      ["when the fresh read failed and left the old list", { isError: true, dataUpdatedAt: 0 }],
    ])("does not vouch for the library %s", (_case, overrides) => {
      mocks.search = "libraryId=4";
      mocks.useCatalogItemDetail.mockReturnValue({
        data: undefined,
        isLoading: false,
        error: itemProblem(404),
      });
      mocks.useUserLibraries.mockReturnValue(libraryList(overrides));

      renderInRouter(<ItemDetail />);

      expect(
        screen.getByRole("heading", { name: "This item isn't available" }),
      ).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "Browse library" })).not.toBeInTheDocument();
    });

    it("starts a new confirmation when the URL moves to another unavailable item", () => {
      vi.useFakeTimers({ toFake: ["Date"] });
      try {
        vi.setSystemTime(1_000_000);
        mocks.search = "libraryId=4";
        mocks.useCatalogItemDetail.mockReturnValue({
          data: undefined,
          isLoading: false,
          error: itemProblem(404),
        });
        // The list was read after the first page appeared, and now names library 5 too.
        mocks.useUserLibraries.mockReturnValue(
          libraryList({
            data: [
              { id: 4, name: "Movies", type: "movies" },
              { id: 5, name: "Shows", type: "series" },
            ],
            dataUpdatedAt: 1_000_500,
          }),
        );
        const view = renderInRouter(<ItemDetail />);
        expect(screen.getByRole("link", { name: "Browse library" })).toHaveAttribute(
          "href",
          "/library/4",
        );

        // Another cached 404 keeps the page mounted; that old read must not vouch for library 5.
        vi.setSystemTime(2_000_000);
        mocks.id = "movie-456";
        mocks.search = "libraryId=5";
        view.rerender(
          <MemoryRouter initialEntries={["/item/movie-456?libraryId=5"]}>
            <ItemDetail />
          </MemoryRouter>,
        );
        expect(screen.queryByRole("link", { name: "Browse library" })).not.toBeInTheDocument();
      } finally {
        vi.useRealTimers();
      }
    });

    it("drops Browse library for a library the viewer can no longer open", () => {
      mocks.search = "libraryId=9";
      mocks.useCatalogItemDetail.mockReturnValue({
        data: undefined,
        isLoading: false,
        error: itemProblem(404),
      });

      renderInRouter(<ItemDetail />);

      expect(
        screen.getByRole("heading", { name: "This item isn't available" }),
      ).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "Browse library" })).not.toBeInTheDocument();
    });

    it("keeps the toast for other failures and offers a retry instead of a 404 message", async () => {
      const refetch = vi.fn().mockResolvedValue(undefined);
      mocks.useCatalogItemDetail.mockReturnValue({
        data: undefined,
        isLoading: false,
        isFetching: false,
        error: itemProblem(500),
        refetch,
      });

      renderInRouter(<ItemDetail />);

      expect(mocks.toastError).toHaveBeenCalledWith("The catalog is unavailable.");
      expect(
        screen.getByRole("heading", { level: 1, name: "Couldn't load this item" }),
      ).toBeInTheDocument();
      expect(screen.queryByText("This item isn't available")).not.toBeInTheDocument();
      expect(document.title).not.toContain("Not found");

      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(refetch).toHaveBeenCalledTimes(1);
    });
  });
});
