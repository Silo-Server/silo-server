// @vitest-environment node

import { describe, expect, it } from "vitest";

import { parseTMDBListID } from "./tmdbList";

describe("parseTMDBListID", () => {
  it.each([
    ["https://www.themoviedb.org/list/310-my-movie-list", 310],
    ["https://www.themoviedb.org/list/310", 310],
    ["http://themoviedb.org/list/310-my-movie-list/", 310],
    ["www.themoviedb.org/list/8649937-marvel?language=fr-FR#top", 8649937],
    ["  https://WWW.THEMOVIEDB.ORG/list/310  ", 310],
    ["310", 310],
  ])("accepts %s", (raw, id) => {
    expect(parseTMDBListID(raw)).toBe(id);
  });

  it.each([
    "",
    "0",
    "-3",
    "abc",
    "99999999999999999999999",
    "https://www.themoviedb.org/collection/10-star-wars-collection",
    "https://www.themoviedb.org/movie/550-fight-club",
    "https://www.themoviedb.org/list/my-movie-list",
    "https://www.themoviedb.org/list/310-my-movie-list/edit",
    "https://evil.example/list/310",
    "https://themoviedb.org.evil.example/list/310",
    "ftp://www.themoviedb.org/list/310",
    "https://mdblist.com/lists/user/slug",
  ])("rejects %s", (raw) => {
    expect(parseTMDBListID(raw)).toBeNull();
  });
});
