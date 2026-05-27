import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";
import ItemCard from "@/components/ItemCard";

vi.mock("@/components/MediaItemMenu", () => ({
  default: () => null,
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
