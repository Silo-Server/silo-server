import { describe, expect, it } from "vitest";
import { itemTitleLinks, titleLinks } from "./titleLinks";

describe("titleLinks", () => {
  it("links a movie to IMDb, TMDB and TVDB in that order", () => {
    expect(titleLinks("movie", { imdb: "tt0133093", tmdb: "603", tvdb: "169" })).toEqual([
      { label: "IMDb", href: "https://www.imdb.com/title/tt0133093/" },
      { label: "TMDB", href: "https://www.themoviedb.org/movie/603" },
      { label: "TVDB", href: "https://thetvdb.com/dereferrer/movie/169" },
    ]);
  });

  it("uses TMDB's tv path and TVDB's series path for a series", () => {
    expect(titleLinks("series", { tmdb: 1396, tvdb: 81189 })).toEqual([
      { label: "TMDB", href: "https://www.themoviedb.org/tv/1396" },
      { label: "TVDB", href: "https://thetvdb.com/dereferrer/series/81189" },
    ]);
  });

  it("gives an episode IMDb and TVDB links but no TMDB link", () => {
    expect(titleLinks("episode", { imdb: "tt0959621", tmdb: "62085", tvdb: "349232" })).toEqual([
      { label: "IMDb", href: "https://www.imdb.com/title/tt0959621/" },
      { label: "TVDB", href: "https://thetvdb.com/dereferrer/episode/349232" },
    ]);
  });

  it("skips missing IDs and IDs without their provider's shape", () => {
    expect(titleLinks("movie", {})).toEqual([]);
    expect(titleLinks("movie", { imdb: "", tmdb: "", tvdb: "" })).toEqual([]);
    expect(titleLinks("movie", { imdb: null, tmdb: null, tvdb: null })).toEqual([]);
    expect(titleLinks("movie", { imdb: "0133093", tmdb: "0", tvdb: "abc" })).toEqual([]);
    expect(titleLinks("movie", { imdb: "tt1/../x", tmdb: "603?x", tvdb: -4 })).toEqual([]);
  });
});

describe("itemTitleLinks", () => {
  const ids = { imdb_id: "tt0959621", tmdb_id: "62085", tvdb_id: "349232" };

  it("links movies, series and episodes by their type", () => {
    expect(itemTitleLinks({ type: "series", ...ids }).map((link) => link.href)).toEqual([
      "https://www.imdb.com/title/tt0959621/",
      "https://www.themoviedb.org/tv/62085",
      "https://thetvdb.com/dereferrer/series/349232",
    ]);
    expect(itemTitleLinks({ type: "episode", ...ids }).map((link) => link.label)).toEqual([
      "IMDb",
      "TVDB",
    ]);
  });

  it("gives seasons and other kinds no links", () => {
    expect(itemTitleLinks({ type: "season", ...ids })).toEqual([]);
    expect(itemTitleLinks({ type: "audiobook", ...ids })).toEqual([]);
  });
});
