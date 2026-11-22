import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";
import EbookContent from "./EbookContent";
import { makeFileVersion, makeItemDetail } from "./itemDetail.test-support";

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
  useEbookReaderProgress: vi.fn(),
  useWatchedStateMutation: vi.fn(),
  useUICustomization: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
}));

vi.mock("@/hooks/queries/ebookReaderProgress", () => ({
  useEbookReaderProgress: mocks.useEbookReaderProgress,
}));

vi.mock("@/hooks/queries/items", () => ({
  useWatchedStateMutation: mocks.useWatchedStateMutation,
}));

vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: mocks.useUICustomization,
}));

vi.mock("@/hooks/useAmbientColor", () => ({
  useAmbientColor: vi.fn(),
}));

vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaApi: undefined,
    emblaRef: vi.fn(),
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: vi.fn(),
    scrollNext: vi.fn(),
  }),
}));

function makeEbook(overrides: Partial<ItemDetail> = {}): ItemDetail & { type: "ebook" } {
  return makeItemDetail("ebook", {
    content_id: "book-1",
    title: "The Glass Cartographer",
    year: 2021,
    genres: ["Science Fiction", "Mystery"],
    studios: ["Northstar Press"],
    overview: "An archivist traces coastal charts.",
    versions: [
      makeFileVersion({
        file_id: 101,
        file_name: "glass.epub",
        file_path: "/books/glass.epub",
        container: "epub",
        file_size: 2048,
      }),
    ],
    ebook: {
      authors: [{ name: "Clara Vance" }],
      publisher: "Northstar Press",
      series: {
        name: "The Meridian Chronicles",
        entries: [
          {
            content_id: "book-1",
            title: "The Glass Cartographer",
            year: 2021,
            series_index: 1,
            progress_ratio: 0.42,
          },
          {
            content_id: "book-2",
            title: "A Map of Salt and Ash",
            year: 2022,
            series_index: 2,
            played: true,
          },
        ],
      },
      related: {
        also_by_author: [],
        similar: [],
      },
    },
    ...overrides,
  });
}

function renderEbook(item: ItemDetail & { type: "ebook" }, libraryId?: number) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <EbookContent item={item} libraryId={libraryId} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("EbookContent", () => {
  beforeEach(() => {
    mocks.useAuth.mockReturnValue({ user: { download_allowed: true } });
    mocks.useEbookReaderProgress.mockReturnValue({ data: null });
    mocks.useWatchedStateMutation.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useUICustomization.mockReturnValue({ cardPresentation: { poster_size: "medium" } });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("shows Continue and reading progress only when unfinished progress matches a visible readable file", () => {
    mocks.useEbookReaderProgress.mockReturnValue({
      data: { file_id: 101, location: "fraction:0.42", progress: 0.42 },
    });
    renderEbook(makeEbook(), 3);
    expect(screen.getByRole("link", { name: "Continue" })).toHaveAttribute(
      "href",
      "/reader/ebook/book-1?file_id=101&libraryId=3",
    );
    expect(screen.getByText("Reading progress")).toBeInTheDocument();
    expect(screen.getByText("42%")).toBeInTheDocument();

    cleanup();

    // Finished book with stored progress=1.0 must show Read + Finished status, not Continue.
    mocks.useEbookReaderProgress.mockReturnValue({
      data: { file_id: 101, location: "fraction:1.0", progress: 1 },
    });
    renderEbook(makeEbook({ user_data: { played: true } }), 3);
    expect(screen.getByRole("link", { name: "Read" })).toBeInTheDocument();
    expect(screen.getByText("100%")).toBeInTheDocument();
    expect(screen.queryByText("Reading progress")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download" })).toHaveTextContent("Download");

    cleanup();

    // Progress referencing a file absent from the scoped library falls back to Read.
    mocks.useEbookReaderProgress.mockReturnValue({
      data: { file_id: 999, location: "fraction:0.5", progress: 0.5 },
    });
    renderEbook(makeEbook(), 3);
    expect(screen.getByRole("link", { name: "Read" })).toBeInTheDocument();
    expect(screen.queryByText("Reading progress")).not.toBeInTheDocument();
  });

  it("omits the empty-files notice when versions exist without visible file paths", () => {
    const nonAdminBook = makeEbook({
      versions: [
        makeFileVersion({
          file_id: 101,
          file_name: "glass.epub",
          file_path: undefined,
          container: "epub",
          file_size: 2048,
        }),
      ],
    });
    renderEbook(nonAdminBook, 3);
    expect(screen.queryByText("No ebook files found.")).not.toBeInTheDocument();

    cleanup();

    renderEbook(makeEbook({ versions: [], user_data: { played: true } }), 3);
    expect(screen.getByText("No ebook files found.")).toBeInTheDocument();
    expect(screen.getByText("100%")).toBeInTheDocument();
  });

  it("links series and genres to the library when libraryId is set, keeps related books unscoped, and avoids dead-end links without libraryId", () => {
    renderEbook(makeEbook(), 3);
    const seriesLink = screen.getByRole("link", { name: "The Meridian Chronicles, book 1" });
    expect(seriesLink.getAttribute("href")).toContain("/library/3?");
    expect(seriesLink.getAttribute("href")).toContain(
      "groups%5B0%5D%5Brules%5D%5B0%5D%5Bfield%5D=series",
    );
    expect(screen.getByRole("link", { name: "Science Fiction" })).toHaveAttribute(
      "href",
      "/library/3?tab=library&genre=Science+Fiction",
    );
    const relatedLink = screen.getByRole("link", { name: /A Map of Salt and Ash/i });
    expect(relatedLink).toHaveAttribute("href", "/item/book-2");

    cleanup();

    renderEbook(makeEbook());
    expect(screen.getByText("The Meridian Chronicles, book 1").tagName).toBe("P");
    expect(screen.queryByRole("link", { name: "Science Fiction" })).not.toBeInTheDocument();
    expect(screen.getByText("Science Fiction")).toBeInTheDocument();
  });

  it("clamps long descriptions by default with a More/Less toggle and resets on book change", () => {
    const scrollHeightSpy = vi
      .spyOn(HTMLElement.prototype, "scrollHeight", "get")
      .mockReturnValue(240);
    const clientHeightSpy = vi
      .spyOn(HTMLElement.prototype, "clientHeight", "get")
      .mockReturnValue(112);

    try {
      const longOverview = "A very long publisher blurb ".repeat(30).trim();
      const queryClient = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      });
      const { rerender } = render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter>
            <EbookContent item={makeEbook({ overview: longOverview })} libraryId={3} />
          </MemoryRouter>
        </QueryClientProvider>,
      );

      const paragraph = screen.getByText(longOverview);
      expect(paragraph).toHaveClass("line-clamp-4");

      const moreButton = screen.getByRole("button", { name: "More" });
      expect(moreButton).toHaveAttribute("aria-expanded", "false");
      fireEvent.click(moreButton);
      expect(paragraph).not.toHaveClass("line-clamp-4");

      const lessButton = screen.getByRole("button", { name: "Less" });
      expect(lessButton).toHaveAttribute("aria-expanded", "true");

      // Navigating to another book resets expanded state back to clamped.
      const nextOverview = "Another long publisher blurb ".repeat(30).trim();
      rerender(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter>
            <EbookContent
              item={makeEbook({ content_id: "book-2", overview: nextOverview })}
              libraryId={3}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
      expect(screen.getByText(nextOverview)).toHaveClass("line-clamp-4");
      expect(screen.getByRole("button", { name: "More" })).toHaveAttribute(
        "aria-expanded",
        "false",
      );
    } finally {
      scrollHeightSpy.mockRestore();
      clientHeightSpy.mockRestore();
    }
  });

  it("renders series, also-by-author, and similar rows inside the shared MediaCarousel without leaking stale rails on book switch", () => {
    const base = makeEbook();
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const firstBook = makeEbook({
      ebook: {
        ...base.ebook!,
        related: {
          also_by_author: [{ content_id: "book-3", title: "Notes from an Inland Sea", year: 2019 }],
          similar: [{ content_id: "book-4", title: "The Paper Observatory", year: 2023 }],
        },
      },
    });
    const { container, rerender } = render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <EbookContent item={firstBook} libraryId={3} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(container.querySelectorAll("section.media-carousel")).toHaveLength(3);
    expect(screen.getByRole("heading", { name: "In The Meridian Chronicles" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Also by Clara Vance" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "You might also like" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Notes from an Inland Sea/i })).toHaveAttribute(
      "href",
      "/item/book-3",
    );
    expect(screen.getByRole("link", { name: /The Paper Observatory/i })).toHaveAttribute(
      "href",
      "/item/book-4",
    );

    // Switching to another cached book with all three rails replaces every rail cleanly.
    const secondBook = makeEbook({
      content_id: "book-9",
      title: "Winter Harbor",
      ebook: {
        authors: [{ name: "Kenji Sato" }],
        series: {
          entries: [{ content_id: "book-9", title: "Winter Harbor", year: 2024 }],
        },
        related: {
          also_by_author: [{ content_id: "book-10", title: "Glass Harbor", year: 2022 }],
          similar: [{ content_id: "book-11", title: "Tidal Atlas", year: 2021 }],
        },
      },
    });
    rerender(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <EbookContent item={secondBook} libraryId={3} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(container.querySelectorAll("section.media-carousel")).toHaveLength(3);
    expect(screen.queryByRole("heading", { name: "Also by Clara Vance" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Also by Kenji Sato" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Glass Harbor/i })).toHaveAttribute(
      "href",
      "/item/book-10",
    );
  });
});
