import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { ItemDetail, MangaChapter } from "@/api/types";

vi.mock("@/hooks/useAmbientColor", () => ({ useAmbientColor: () => undefined }));
vi.mock("@/components/PageBack", () => ({ default: () => null }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { download_allowed: true } }) }));
vi.mock("@/hooks/queries/items", () => ({
  useWatchedStateMutation: () => ({ mutate: vi.fn(), isPending: false }),
  useRefreshItemMetadata: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/catalogRead", () => ({
  fetchCatalogItemVersions: vi.fn().mockResolvedValue([]),
  useMangaSeriesFiles: () => ({ data: undefined, isLoading: false, error: null }),
}));
vi.mock("@/pages/ItemDetail/components/MetadataBadges", () => ({ default: () => null }));
vi.mock("@/pages/ItemDetail/DetailHero", () => ({
  default: ({ title, actions }: { title: string; actions?: ReactNode }) => (
    <div>
      <h1>{title}</h1>
      {actions}
    </div>
  ),
}));

import MangaContent from "./MangaContent";

function mangaItem(chapters: MangaChapter[]): ItemDetail & { type: "manga" } {
  return {
    content_id: "manga-1",
    type: "manga",
    title: "Test Manga",
    year: 2024,
    overview: "",
    runtime: 0,
    content_rating: "",
    genres: [],
    rating_imdb: null,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    ratings: [],
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [],
    studios: [],
    networks: [],
    countries: [],
    release_date: null,
    first_air_date: null,
    last_air_date: null,
    season_count: null,
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    versions: [],
    subtitles: [],
    intro: null,
    credits: null,
    manga: { chapters },
  } as ItemDetail & { type: "manga" };
}

function volumeSeries(): ItemDetail & { type: "manga" } {
  return mangaItem([
    { content_id: "v01", title: "Railgun v01", chapter_index: 1, volume: "v01" },
    { content_id: "v02", title: "Railgun v02", chapter_index: 2, volume: "v02" },
  ]);
}

const seriesBackTo = "&backTo=" + encodeURIComponent("/item/manga-1?libraryId=7");

describe("MangaContent", () => {
  it("links a flat volume row to the ebook reader by content_id with the library id and a backTo to the series", () => {
    render(
      <MemoryRouter>
        <MangaContent item={volumeSeries()} libraryId={7} />
      </MemoryRouter>,
    );

    // The reader link carries the series content id as backTo so the reader's
    // back action returns to the series instead of looping into the chapter.
    expect(screen.getByRole("link", { name: /^Volume 1$/i })).toHaveAttribute(
      "href",
      "/reader/ebook/v01?libraryId=7" + seriesBackTo,
    );
  });

  it("offers per-row Read, Mark-read, and Download actions", () => {
    render(
      <MemoryRouter>
        <MangaContent item={volumeSeries()} libraryId={7} />
      </MemoryRouter>,
    );

    // Read remains the row link.
    expect(screen.getByRole("link", { name: /^Volume 1$/i })).toBeInTheDocument();
    // Mark-read + Download toggles exist per row (2 volumes → 2 of each).
    expect(screen.getAllByRole("button", { name: /Mark chapter read/i })).toHaveLength(2);
    expect(screen.getAllByRole("button", { name: /Download chapter/i })).toHaveLength(2);
  });

  it("shows a Start Reading hero CTA targeting the first volume on an unread series", () => {
    render(
      <MemoryRouter>
        <MangaContent item={volumeSeries()} libraryId={7} />
      </MemoryRouter>,
    );

    const cta = screen.getByRole("link", { name: /Start Reading/i });
    expect(cta).toHaveTextContent("Volume 1");
    expect(cta).toHaveAttribute("href", "/reader/ebook/v01?libraryId=7" + seriesBackTo);
  });

  it("shows a Continue hero CTA targeting the first unread chapter mid-series", () => {
    render(
      <MemoryRouter>
        <MangaContent
          item={mangaItem([
            {
              content_id: "v01",
              title: "Railgun v01",
              chapter_index: 1,
              volume: "v01",
              read: true,
            },
            { content_id: "v02", title: "Railgun v02", chapter_index: 2, volume: "v02" },
            { content_id: "v03", title: "Railgun v03", chapter_index: 3, volume: "v03" },
          ])}
          libraryId={7}
        />
      </MemoryRouter>,
    );

    const cta = screen.getByRole("link", { name: /Continue/i });
    expect(cta).toHaveTextContent("Volume 2");
    expect(cta).toHaveAttribute("href", "/reader/ebook/v02?libraryId=7" + seriesBackTo);
  });

  // Regression test for issue #188: partial progress on the resume target
  // must surface as "Resume Reading", not "Start Reading".
  it("shows a Resume Reading hero CTA when the target volume is partially read", () => {
    render(
      <MemoryRouter>
        <MangaContent
          item={mangaItem([
            {
              content_id: "v01",
              title: "Railgun v01",
              chapter_index: 1,
              volume: "v01",
              progress: 0.18,
            },
            { content_id: "v02", title: "Railgun v02", chapter_index: 2, volume: "v02" },
          ])}
          libraryId={7}
        />
      </MemoryRouter>,
    );

    const cta = screen.getByRole("link", { name: /Resume Reading/i });
    expect(cta).toHaveTextContent("Volume 1");
    expect(cta).toHaveAttribute("href", "/reader/ebook/v01?libraryId=7" + seriesBackTo);
  });

  it("offers a Read Again CTA from the start once every chapter is read", () => {
    render(
      <MemoryRouter>
        <MangaContent
          item={mangaItem([
            {
              content_id: "v01",
              title: "Railgun v01",
              chapter_index: 1,
              volume: "v01",
              read: true,
            },
            {
              content_id: "v02",
              title: "Railgun v02",
              chapter_index: 2,
              volume: "v02",
              read: true,
            },
          ])}
          libraryId={7}
        />
      </MemoryRouter>,
    );

    const cta = screen.getByRole("link", { name: /Read Again/i });
    expect(cta).toHaveTextContent("Volume 1");
  });
  it("collapses a fully read volume section by default and expands on toggle", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <MangaContent
          item={mangaItem([
            {
              content_id: "v1-c1",
              title: "Chapter 1",
              chapter_index: 1,
              volume: "v01",
              read: true,
            },
            {
              content_id: "v1-c2",
              title: "Chapter 2",
              chapter_index: 2,
              volume: "v01",
              read: true,
            },
          ])}
          libraryId={7}
        />
      </MemoryRouter>,
    );

    const header = screen.getByRole("button", { name: /Volume 1/i });
    expect(header).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: /^Chapter 1/i })).not.toBeInTheDocument();

    await user.click(header);
    expect(screen.getByRole("link", { name: /^Chapter 1/i })).toBeInTheDocument();
  });
});
