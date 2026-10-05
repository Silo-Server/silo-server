import { describe, expect, it } from "vitest";

import type { CollectionTemplate } from "@/lib/collectionTemplates";

import {
  applyPick,
  chartPick,
  cleanMDBListLink,
  emptySyncedDraft,
  isCreatableTemplate,
  isMDBListLink,
  linkPick,
  mdblistPick,
  namedSchedule,
  popularPicks,
  templatePick,
  type SyncedDraft,
} from "./synced";

function template(overrides: Partial<CollectionTemplate>): CollectionTemplate {
  return {
    id: "t",
    title: "T",
    description: "",
    icon: "",
    category: "trending",
    source: "mdblist",
    media_kind: "movie",
    ...overrides,
  };
}

const IMDB_TOP = template({
  id: "mdblist_imdb_top",
  title: "IMDb Top 250 Movies",
  description: "The 250 highest-rated movies on IMDb.",
  mdblist: { url: "https://mdblist.com/lists/linaspurinis/top-watched-movies-of-the-week" },
  poster_path: "/images/collection-templates/mdblist_imdb_top.jpg",
  default_limit: 250,
  default_sync_schedule: "0 3 * * *",
});
const TRENDING_WEEK = template({
  id: "tmdb_trending_movies_week",
  title: "Trending Movies This Week",
  source: "tmdb",
  tmdb: { preset: "trending", media_type: "movie", time_window: "week" },
  default_sync_schedule: "0 6 * * 1",
});

const ALL_SOURCES = ["mdblist", "tmdb", "tmdb_list"];

function draft(fields: Partial<{ name: string; description: string }> = {}, synced?: SyncedDraft) {
  return { name: "", description: "", synced: synced ?? emptySyncedDraft(), ...fields };
}

describe("MDBList links", () => {
  it("drops the query, fragment and trailing slash and keeps /json", () => {
    expect(cleanMDBListLink(" https://mdblist.com/lists/u/top?sort=rank#x ")).toBe(
      "https://mdblist.com/lists/u/top",
    );
    expect(cleanMDBListLink("https://mdblist.com/lists/u/top/json/")).toBe(
      "https://mdblist.com/lists/u/top/json",
    );
  });

  it("accepts only list pages on mdblist.com", () => {
    expect(isMDBListLink("https://mdblist.com/lists/u/top")).toBe(true);
    expect(isMDBListLink("https://www.mdblist.com/lists/u/top/json")).toBe(true);
    expect(isMDBListLink("https://mdblist.com/movie/tt1")).toBe(false);
    expect(isMDBListLink("https://evil.example/lists/u/top")).toBe(false);
    expect(isMDBListLink("mdblist.com/lists/u/top")).toBe(false);
  });

  it("rejects the credentials and ports the server refuses", () => {
    expect(isMDBListLink("https://user:pass@mdblist.com/lists/u/top")).toBe(false);
    expect(isMDBListLink("https://user@mdblist.com/lists/u/top")).toBe(false);
    expect(isMDBListLink("https://mdblist.com:8443/lists/u/top")).toBe(false);
    expect(isMDBListLink("https://mdblist.com:443/lists/u/top")).toBe(true);
    expect(isMDBListLink("http://mdblist.com:80/lists/u/top")).toBe(true);
    expect(isMDBListLink("http://mdblist.com:443/lists/u/top")).toBe(true);
    expect(isMDBListLink("https://mdblist.com./lists/u/top")).toBe(true);
  });
});

describe("ready-made picks", () => {
  it("offers only templates the scope can create, and none that ask for a link", () => {
    const groups = [
      {
        category: "trending" as const,
        label: "Trending",
        templates: [
          IMDB_TOP,
          TRENDING_WEEK,
          template({ id: "discover", source: "tmdb_discover" }),
          template({ id: "franchise", source: "tmdb_collection" }),
          template({ id: "trakt", source: "trakt", requires_profile: true }),
          template({ id: "byo", source: "mdblist", mdblist: { url: "" } }),
        ],
      },
      { category: "custom" as const, label: "Custom", templates: [template({ id: "empty" })] },
    ];
    expect(popularPicks(groups, ALL_SOURCES)).toEqual([
      { category: "trending", label: "Trending", templates: [IMDB_TOP, TRENDING_WEEK] },
    ]);
    expect(popularPicks(groups, ["tmdb"])[0]!.templates).toEqual([TRENDING_WEEK]);
    expect(isCreatableTemplate(template({ source: "tmdb_discover" }), ALL_SOURCES)).toBe(false);
  });

  it("maps a template's cron to a named schedule for a profile", () => {
    expect(namedSchedule(undefined)).toBe("");
    expect(namedSchedule("0 */6 * * *")).toBe("daily");
    expect(namedSchedule("0 6 * * 1")).toBe("weekly");
    expect(namedSchedule("0 3 1 * *")).toBe("monthly");
    expect(templatePick(TRENDING_WEEK, "server").schedule).toBe("0 6 * * 1");
    expect(templatePick(TRENDING_WEEK, "personal").schedule).toBe("weekly");
  });

  it("uses the matching template for a chart and names one with none", () => {
    expect(
      chartPick(
        { preset: "trending", mediaType: "movie", timeWindow: "week" },
        [TRENDING_WEEK],
        "server",
      ),
    ).toEqual(templatePick(TRENDING_WEEK, "server"));
    expect(chartPick({ preset: "upcoming", mediaType: "all" }, [TRENDING_WEEK], "server")).toEqual({
      list: {
        source: "tmdb_chart",
        chart: { preset: "upcoming", mediaType: "movie" },
        mediaKind: "movie",
      },
      name: "Upcoming Movies",
      description: "",
      schedule: "",
    });
  });

  it("follows an MDBList search hit's own JSON feed, name and description", () => {
    const pick = mdblistPick({
      id: 7,
      user_id: 1,
      user_name: "u",
      name: "Oscar Winners",
      slug: "oscar-winners",
      description: "Best Picture winners.",
      mediatype: "show",
      items: 10,
      likes: 0,
      url: "https://mdblist.com/lists/u/oscar-winners",
    });
    expect(pick.list).toEqual({
      source: "mdblist",
      url: "https://mdblist.com/lists/u/oscar-winners/json",
      pickId: "mdblist:7",
      mediaKind: "tv",
    });
    expect(pick).toMatchObject({ name: "Oscar Winners", description: "Best Picture winners." });
    expect(pick.posterUrl).toBeUndefined();
  });
});

describe("applyPick", () => {
  it("fills every field on a fresh draft", () => {
    const next = applyPick(draft(), templatePick(IMDB_TOP, "server"));
    expect(next).toMatchObject({
      name: "IMDb Top 250 Movies",
      description: "The 250 highest-rated movies on IMDb.",
      synced: {
        limit: 250,
        schedule: "0 3 * * *",
        posterUrl: "/images/collection-templates/mdblist_imdb_top.jpg",
        kept: [],
      },
    });
    expect(next.synced.list?.pickId).toBe("mdblist_imdb_top");
  });

  it("replaces what the last pick filled, but keeps a name typed by hand", () => {
    const picked = applyPick(draft(), templatePick(IMDB_TOP, "server"));
    const renamed = { ...picked, name: "Our top 250" };
    const next = applyPick(renamed, templatePick(TRENDING_WEEK, "server"));
    expect(next.name).toBe("Our top 250");
    expect(next.description).toBe("");
    expect(next.synced).toMatchObject({
      schedule: "0 6 * * 1",
      posterUrl: undefined,
      kept: ["name"],
    });
    expect(next.synced.limit).toBeUndefined();
  });

  it("keeps an edited limit and schedule", () => {
    const picked = applyPick(draft(), templatePick(IMDB_TOP, "server"));
    const edited = { ...picked, synced: { ...picked.synced, limit: 40, schedule: "0 * * * *" } };
    const next = applyPick(edited, templatePick(TRENDING_WEEK, "server"));
    expect(next.synced).toMatchObject({ limit: 40, schedule: "0 * * * *" });
  });

  it("treats a cleared name as untouched", () => {
    const picked = applyPick(draft(), templatePick(IMDB_TOP, "server"));
    const next = applyPick({ ...picked, name: " " }, templatePick(TRENDING_WEEK, "server"));
    expect(next.name).toBe("Trending Movies This Week");
    expect(next.synced.kept).toEqual([]);
  });

  it("empties what a pick filled when a link replaces it", () => {
    const picked = applyPick(draft(), templatePick(IMDB_TOP, "server"));
    const next = applyPick(picked, linkPick(null));
    expect(next).toMatchObject({ name: "", description: "", synced: { list: null } });
  });
});
