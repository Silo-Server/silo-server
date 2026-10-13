import { describe, expect, it } from "vitest";
import { sectionKeys } from "@/hooks/queries/keys";
import type { ResolvedSection } from "@/api/types";
import { adminPeekKey, peekItemsOf, profilePeekKey, profilePeekSeed } from "./peek";
import type { HomeRow } from "./types";

function row(overrides: Partial<HomeRow> = {}): HomeRow {
  return {
    id: "a",
    title: "Trending This Week",
    sectionType: "trending_on_server",
    config: { window: "7d" },
    itemLimit: 20,
    hero: false,
    shown: true,
    own: false,
    legacyTrakt: false,
    ...overrides,
  };
}

describe("adminPeekKey", () => {
  it("sits outside the section keys, so refreshing the list does not refetch peeks", () => {
    expect(adminPeekKey({ kind: "home" }, row())[0]).not.toBe(sectionKeys.all[0]);
  });

  it("changes with the row's kind, config and page, but not with its key order or on/off state", () => {
    const base = adminPeekKey({ kind: "home" }, row());
    expect(adminPeekKey({ kind: "home" }, row({ config: { window: "30d" } }))).not.toEqual(base);
    expect(adminPeekKey({ kind: "home" }, row({ sectionType: "most_watched" }))).not.toEqual(base);
    expect(adminPeekKey({ kind: "library", libraryId: 7 }, row())).not.toEqual(base);
    expect(
      adminPeekKey(
        { kind: "home" },
        row({ config: { b: 1, a: 2 }, shown: false, hero: true, title: "Renamed" }),
      ),
    ).toEqual(adminPeekKey({ kind: "home" }, row({ config: { a: 2, b: 1 } })));
  });
});

describe("profilePeekKey", () => {
  it("sits outside the section keys and names the row's kind, config and size", () => {
    const base = profilePeekKey({ kind: "home" }, row());
    expect(base[0]).not.toBe(sectionKeys.all[0]);
    expect(base).not.toEqual(adminPeekKey({ kind: "home" }, row()));
    expect(profilePeekKey({ kind: "home" }, row({ itemLimit: 30 }))).not.toEqual(base);
    expect(profilePeekKey({ kind: "home" }, row({ config: { window: "30d" } }))).not.toEqual(base);
  });
});

describe("profilePeekSeed", () => {
  const section = {
    id: "a",
    section_type: "trending_on_server",
    title: "Trending This Week",
    featured: false,
    item_limit: 20,
    total_count: 3,
    is_custom: false,
    customized: false,
    items: [1, 2, 3, 4].map((n) => ({
      content_id: `m${n}`,
      title: `Movie ${n}`,
      poster_url: `/p/${n}`,
      poster_thumbhash: "",
    })),
  } as unknown as ResolvedSection;
  const cached = { data: { section }, dataUpdatedAt: 1000 };

  it("shows Home's cached titles while the peek loads", () => {
    expect(profilePeekSeed(cached, row(), undefined)).toEqual(peekItemsOf(section));
    expect(peekItemsOf(section)).toEqual([
      { id: "m1", title: "Movie 1", posterUrl: "/p/1", thumbhash: undefined },
      { id: "m2", title: "Movie 2", posterUrl: "/p/2", thumbhash: undefined },
      { id: "m3", title: "Movie 3", posterUrl: "/p/3", thumbhash: undefined },
    ]);
  });

  it("skips a cached row of another kind or size, or one this tab changed since", () => {
    expect(profilePeekSeed(undefined, row(), undefined)).toBeUndefined();
    expect(
      profilePeekSeed(cached, row({ sectionType: "most_watched" }), undefined),
    ).toBeUndefined();
    expect(profilePeekSeed(cached, row({ itemLimit: 10 }), undefined)).toBeUndefined();
    expect(profilePeekSeed(cached, row(), 1000)).toBeUndefined();
    expect(profilePeekSeed(cached, row(), 999)).toEqual(peekItemsOf(section));
  });
});
