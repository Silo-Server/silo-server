import ItemCard from "@/components/ItemCard";
import { fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/components/MediaItemMenu", () => ({
  default: () => null,
}));

vi.mock("@/components/CardPlayOverlay", () => ({
  default: ({ contentId, title }: { contentId: string; title: string }) => (
    <a href={`/watch/${contentId}`} aria-label={`Play ${title}`} />
  ),
}));

vi.mock("@/lib/thumbhash", () => ({
  decodeThumbhash: () => "",
}));

function renderCard(props: Parameters<typeof ItemCard>[0]) {
  return renderToStaticMarkup(
    <MemoryRouter>
      <ItemCard {...props} />
    </MemoryRouter>,
  );
}

const baseItem = {
  content_id: "series-1",
  type: "series" as const,
  title: "The Last of Us",
  year: 2023,
  genres: [] as string[],
  content_rating: "TV-MA",
  status: "matched" as const,
  rating_imdb: 8.7,
  overview: "",
  poster_url: "",
  poster_thumbhash: "",
  backdrop_url: "",
  backdrop_thumbhash: "",
};

describe("ItemCard SortMeta", () => {
  it("encodes item links while preserving library context", () => {
    const markup = renderCard({
      item: {
        ...baseItem,
        content_id: "ebook 1/isbn:978",
        type: "ebook",
        title: "A Reader",
      },
      libraryId: 12,
    });

    expect(markup).toContain('href="/item/ebook%201%2Fisbn%3A978?libraryId=12"');
  });

  it("renders the series last air date when sorted by last_air_date", () => {
    const markup = renderCard({
      sortField: "last_air_date",
      item: {
        ...baseItem,
        last_air_date: "2026-03-22",
      },
    });

    expect(markup).toContain("Mar");
    expect(markup).toContain("2026");
  });

  it("falls back to default label when last_air_date is null", () => {
    const markup = renderCard({
      sortField: "last_air_date",
      item: {
        ...baseItem,
        last_air_date: null,
      },
    });

    expect(markup).toContain("2023");
  });

  it("renders content rating when sorted by content_rating", () => {
    const markup = renderCard({
      sortField: "content_rating",
      item: baseItem,
    });

    expect(markup).toContain("TV-MA");
  });

  it("renders runtime when sorted by runtime", () => {
    const markup = renderCard({
      sortField: "runtime",
      item: {
        ...baseItem,
        runtime: 95,
      },
    });

    expect(markup).toContain("1h 35m");
  });

  it("renders non-IMDb ratings for matching rating sorts", () => {
    const tmdbMarkup = renderCard({
      sortField: "rating_tmdb",
      item: {
        ...baseItem,
        rating_tmdb: 8.2,
      },
    });
    const criticMarkup = renderCard({
      sortField: "rating_rt_critic",
      item: {
        ...baseItem,
        rating_rt_critic: 96,
      },
    });

    expect(tmdbMarkup).toContain('<span class="not-uppercase">TMDB</span> 8.2');
    expect(criticMarkup).toContain('<span class="not-uppercase">RT</span> 96%');
  });

  it("renders resolution when sorted by resolution", () => {
    const markup = renderCard({
      sortField: "resolution",
      item: {
        ...baseItem,
        overlay_summary: {
          resolution: "2160p",
        },
      },
    });

    expect(markup).toContain("2160p");
  });

  it("renders audiobook-native sort metadata", () => {
    const audiobook = {
      ...baseItem,
      content_id: "audiobook-1",
      type: "audiobook" as const,
      title: "The Way of Kings",
      year: 2010,
      content_rating: "",
      sort_metrics: {
        author: "Brandon Sanderson",
        narrator: "Michael Kramer",
        series_name: "The Stormlight Archive",
      },
    };

    expect(renderCard({ sortField: "author", item: audiobook })).toContain("Brandon Sanderson");
    expect(renderCard({ sortField: "narrator", item: audiobook })).toContain("Michael Kramer");
    expect(renderCard({ sortField: "series", item: audiobook })).toContain(
      "The Stormlight Archive",
    );
  });

  it("renders episode sort metadata when an active sort has a value", () => {
    const markup = renderCard({
      sortField: "release_date",
      item: {
        ...baseItem,
        content_id: "episode-1",
        type: "episode",
        title: "Long, Long Time",
        series_title: "The Last of Us",
        season_number: 1,
        episode_number: 3,
        release_date: "2023-01-29",
      },
    });

    expect(markup).toContain("Jan");
    expect(markup).toContain("2023");
    expect(markup).not.toContain("S01E03");
  });

  it("keeps episode code when sorted by title", () => {
    const markup = renderCard({
      sortField: "title",
      item: {
        ...baseItem,
        content_id: "episode-1",
        play_content_id: "episode-1",
        type: "episode",
        title: "Long, Long Time",
        series_id: "series-1",
        series_title: "The Last of Us",
        season_number: 1,
        episode_number: 3,
      },
    });

    expect(markup).toContain("S01E03");
    expect(markup).toContain('href="/item/series-1"');
    expect(markup).toContain('href="/item/episode-1"');
    expect(markup).toContain('href="/watch/episode-1"');
  });

  it("hides direct play while selection mode is active", () => {
    const markup = renderCard({
      selectionMode: true,
      item: { ...baseItem, play_content_id: "episode-1" },
    });

    expect(markup).not.toContain('href="/watch/episode-1"');
  });

  it("renders a volumes-only manga count chip", () => {
    const markup = renderCard({
      item: {
        ...baseItem,
        content_id: "manga-1",
        type: "manga",
        title: "Railgun",
        manga_chapter_count: 0,
        manga_volume_count: 12,
      },
    });

    expect(markup).toContain("12 Vol");
    expect(markup).not.toContain("Ch");
  });

  it("renders a chapters-only manga count chip", () => {
    const markup = renderCard({
      item: {
        ...baseItem,
        content_id: "manga-2",
        type: "manga",
        title: "One Piece",
        manga_chapter_count: 100,
        manga_volume_count: 0,
      },
    });

    expect(markup).toContain("100 Ch");
    expect(markup).not.toContain("Vol");
  });

  it("renders both counts when a series has volumes and loose chapters", () => {
    const markup = renderCard({
      item: {
        ...baseItem,
        content_id: "manga-4",
        type: "manga",
        title: "Mixed Manga",
        manga_chapter_count: 3,
        manga_volume_count: 12,
      },
    });

    expect(markup).toContain("12 Vol · 3 Ch");
  });

  it("renders a color-coded publication status chip on manga cards", () => {
    const markup = renderCard({
      item: {
        ...baseItem,
        content_id: "manga-st",
        type: "manga",
        title: "Ongoing Manga",
        manga_volume_count: 5,
        show_status: "Ongoing",
      },
    });
    expect(markup).toContain("Ongoing");
    expect(markup).toContain("text-emerald-200");
  });

  it("does not render a status chip on non-manga cards or when status is absent", () => {
    const noStatus = renderCard({
      item: { ...baseItem, content_id: "manga-ns", type: "manga", title: "No Status" },
    });
    expect(noStatus).not.toContain("Ongoing");
    const ebook = renderCard({
      item: {
        ...baseItem,
        content_id: "eb",
        type: "ebook",
        title: "Book",
        show_status: "Completed",
      },
    });
    expect(ebook).not.toContain("Completed");
  });

  it("renders episode cards with series context when available", () => {
    const markup = renderCard({
      item: {
        ...baseItem,
        content_id: "episode-1",
        type: "episode",
        title: "When You're Lost in the Darkness",
        series_title: "The Last of Us",
        season_number: 1,
        episode_number: 1,
      },
    });

    expect(markup).toContain("The Last of Us");
    expect(markup).toContain("S01E01");
    expect(markup).toContain("When You&#x27;re Lost in the Darkness");
  });
});

describe("ItemCard poster loading", () => {
  const rev = "3f9a".repeat(16);
  const signed = (exp: number, sig: string) =>
    `/api/v2/artwork/tmdb/tv/100088/poster/w300.${rev}.webp?exp=${exp}&sig=${sig}`;
  const card = (posterUrl: string) => (
    <MemoryRouter>
      <ItemCard item={{ ...baseItem, poster_url: posterUrl }} />
    </MemoryRouter>
  );

  it("keeps the poster visible across a re-sign and hides it if that request fails", () => {
    const { rerender } = render(card(signed(1758621600, "aaaa")));
    const poster = screen.getByRole("img", { name: "The Last of Us" });
    fireEvent.load(poster);

    rerender(card(signed(1758622500, "bbbb")));
    expect(screen.getByRole("img", { name: "The Last of Us" })).toBe(poster);
    expect(poster).toHaveClass("opacity-100");

    fireEvent.error(poster);
    expect(poster).toHaveClass("opacity-0");
  });
});
