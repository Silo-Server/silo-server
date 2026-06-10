import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { ItemDetail } from "@/api/types";

vi.mock("@/hooks/useAmbientColor", () => ({ useAmbientColor: () => undefined }));
vi.mock("@/components/PageBack", () => ({ default: () => null }));
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

function mangaItem(): ItemDetail & { type: "manga" } {
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
    manga: {
      chapters: [
        { content_id: "v1-c1", title: "Chapter 1", chapter_index: 1, volume: "v1" },
        { content_id: "v1-c2", title: "Chapter 2", chapter_index: 2, volume: "v1" },
        { content_id: "loose", title: "Bonus" },
      ],
    },
  } as ItemDetail & { type: "manga" };
}

describe("MangaContent", () => {
  it("renders a volume header and a fallback Chapters header", () => {
    render(
      <MemoryRouter>
        <MangaContent item={mangaItem()} libraryId={7} />
      </MemoryRouter>,
    );

    expect(screen.getByText("Volume 1")).toBeInTheDocument();
    expect(screen.getByText("Chapters")).toBeInTheDocument();
  });

  it("links each chapter to the ebook reader by content_id with the library id", () => {
    render(
      <MemoryRouter>
        <MangaContent item={mangaItem()} libraryId={7} />
      </MemoryRouter>,
    );

    const firstChapter = screen.getByRole("link", { name: /Chapter 1/i });
    expect(firstChapter).toHaveAttribute("href", "/reader/ebook/v1-c1?libraryId=7");

    const bonus = screen.getByRole("link", { name: /Bonus/i });
    expect(bonus).toHaveAttribute("href", "/reader/ebook/loose?libraryId=7");
  });

  it("orders chapters within a volume by chapter index", () => {
    render(
      <MemoryRouter>
        <MangaContent item={mangaItem()} libraryId={7} />
      </MemoryRouter>,
    );

    const links = screen.getAllByRole("link");
    const labels = links.map((link) => within(link).queryByText(/Chapter|Bonus/)?.textContent);
    const order = labels.filter(Boolean);
    expect(order.indexOf("Chapter 1")).toBeLessThan(order.indexOf("Chapter 2"));
  });
});
