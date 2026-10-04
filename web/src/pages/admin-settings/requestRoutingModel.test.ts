import { describe, expect, it } from "vitest";

import type { PluginAdminForm, PluginAdminFormField, RequestIntegration } from "@/api/types";
import type { RequestRoute, RequestRoutePreviewRule } from "@/api/v2/adminRequests";

import {
  autoRuleName,
  cleanConditions,
  conditionRows,
  destinationSentence,
  factsSentence,
  overrideFields,
  overridesSummary,
  passThroughLabel,
  ratingAge,
  ROUTING_RATINGS,
  ruleBody,
  ruleDraft,
  ruleSentence,
  rowsToConditions,
  splitOverrideFields,
  traceLine,
} from "./requestRoutingModel";
import { adminLanguage, presetsInUse } from "./requestRoutingPresets";
import {
  conditionsCover,
  routingWarnings,
  ruleShadows,
  type RoutingWarningInput,
} from "./requestRoutingWarnings";
import {
  serverDeleteBlockers,
  serverRouteUsage,
  serverServesMediaType,
} from "./requestServerModel";

function server(id: string, name: string, kind: "radarr" | "sonarr", extra = {}) {
  return {
    id,
    name,
    enabled: true,
    base_url: "",
    has_api_key: true,
    installation_id: 1,
    plugin_config: { service_kind: kind },
    ...extra,
  } as RequestIntegration;
}
const radarr = server("r1", "Radarr", "radarr");
const radarr4k = server("r4", "Radarr 4K", "radarr");
const sonarr = server("s1", "Sonarr", "sonarr");
const sonarrAnime = server("s2", "Sonarr Anime", "sonarr", {
  plugin_config: { service_kind: "sonarr", series_type: "standard" },
});
const servers = [radarr, radarr4k, sonarr, sonarrAnime];

function route(extra: Partial<RequestRoute>): RequestRoute {
  return {
    id: "r",
    media_type: "movie",
    position: 0,
    name: "Rule",
    enabled: true,
    is_fallback: false,
    conditions: {},
    hd: {},
    uhd: {},
    skip_uhd: false,
    etag: '"r"',
    ...extra,
  };
}
const fallback = (hd?: string, uhd?: string, mediaType: "movie" | "series" = "movie") =>
  route({
    id: `fallback-${mediaType}`,
    media_type: mediaType,
    name: "Everything else",
    is_fallback: true,
    position: 1000,
    hd: hd ? { integration_id: hd } : {},
    uhd: uhd ? { integration_id: uhd } : {},
  });

const users = { users: new Map([[2, "kid"]]) };

describe("rule sentences", () => {
  it("reads a rule's conditions as one sentence", () => {
    expect(ruleSentence({ anime: true }, "series")).toBe("When a series is anime");
    expect(ruleSentence({ exclude_original_languages: ["en"] }, "movie")).toBe(
      "When a movie isn't originally in English",
    );
    expect(ruleSentence({ genre_ids: [10751, 10762], max_content_rating: "PG" }, "series")).toBe(
      "When a series is Family or Kids and is rated PG or lower",
    );
    expect(
      ruleSentence(
        { genre_ids: [10751], max_content_rating: "PG", requester_user_ids: [2] },
        "series",
        users,
      ),
    ).toBe("When a series is Family, is rated PG or lower and is requested by kid");
    expect(ruleSentence({ year_to: 1989 }, "movie")).toBe(
      "When a movie came out in 1989 or earlier",
    );
    expect(ruleSentence({ year_from: 1980, year_to: 1989 }, "series")).toBe(
      "When a series first aired in 1980–1989",
    );
    expect(ruleSentence({ exclude_genre_ids: [27, 53] }, "movie")).toBe(
      "When a movie is neither Horror nor Thriller",
    );
    expect(ruleSentence({ company_ids: [10342] }, "movie")).toBe(
      "When a movie is made by studio 10342",
    );
  });

  it("names a rule from its conditions when the admin leaves the name blank", () => {
    expect(autoRuleName({ anime: true }, "series")).toBe("Anime");
    expect(autoRuleName({ exclude_original_languages: ["en"] }, "movie")).toBe("Not English");
    expect(autoRuleName({ genre_ids: [10751], max_content_rating: "PG" }, "movie")).toBe(
      "Family, PG or lower",
    );
    expect(autoRuleName({ requester_user_ids: [2] }, "movie", users)).toBe("Requested by kid");
  });

  it("reads a destination with names, not IDs, and only what it overrides", () => {
    const names = {
      options: {
        quality_profile_id: [{ value: "3", label: "Anime 1080p" }],
        tags: [{ value: "2", label: "anime" }],
      },
      fields: [
        {
          key: "series_type",
          label: "Series type",
          control: "SELECT",
          options: [{ value: "anime", label: "Anime" }],
        } as PluginAdminFormField,
      ],
    };
    expect(
      destinationSentence(
        {
          integration_id: "s2",
          overrides: {
            series_type: "anime",
            tags: [2],
            root_folder: "/tv/anime",
            quality_profile_id: 3,
          },
        },
        servers,
        names,
      ),
    ).toBe("Sonarr Anime · /tv/anime · Anime 1080p · anime · Series type: Anime");
    expect(destinationSentence({ integration_id: "r1" }, servers)).toBe("Radarr");
  });

  it("names overrides, from the server's options once loaded", () => {
    expect(overridesSummary({ root_folder: "/anime", quality_profile_id: 4 })).toBe(
      "Folder /anime · Quality 4",
    );
    expect(
      overridesSummary(
        { quality_profile_id: 4 },
        { options: { quality_profile_id: [{ value: "4", label: "Ultra-HD" }] } },
      ),
    ).toBe("Quality Ultra-HD");
  });
});

describe("the pass-through choice", () => {
  const below = [route({ id: "b", hd: { integration_id: "r4" } })];

  it("names Everything else's server when no rule below decides the copy", () => {
    expect(passThroughLabel("hd", [], fallback("r1", "r4"), servers)).toBe(
      "Same as Everything else (Radarr)",
    );
    expect(passThroughLabel("uhd", below, fallback("r1"), servers)).toBe(
      "Same as Everything else (no 4K version)",
    );
  });

  it("leaves it to the rules below when one of them decides the copy", () => {
    expect(passThroughLabel("hd", below, fallback("r1"), servers)).toBe(
      "Let the rules below decide",
    );
    // A rule that is off decides nothing.
    expect(
      passThroughLabel("hd", [{ ...below[0]!, enabled: false }], fallback("r1"), servers),
    ).toBe("Same as Everything else (Radarr)");
    // Skipping 4K decides the 4K copy.
    expect(
      passThroughLabel("uhd", [route({ skip_uhd: true })], fallback("r1", "r4"), servers),
    ).toBe("Let the rules below decide");
  });
});

describe("condition rows", () => {
  it("round-trips includes, exclusions, years and a rating", () => {
    const conditions = {
      anime: false,
      genre_ids: [16],
      exclude_genre_ids: [27],
      exclude_original_languages: ["en"],
      year_from: 1990,
      max_content_rating: "PG-13",
      requester_user_ids: [2],
    };
    const rows = conditionRows(conditions);
    expect(rows.map((row) => row.kind)).toEqual([
      "anime",
      "genre",
      "genre",
      "language",
      "year",
      "rating",
      "requester",
    ]);
    expect(rows[2]).toEqual({ kind: "genre", mode: "none", values: ["27"] });
    expect(rowsToConditions(rows)).toEqual(conditions);
  });

  it("drops empty lines and merges two lines on the same field", () => {
    expect(
      rowsToConditions([
        { kind: "genre", mode: "any", values: [] },
        { kind: "year" },
        { kind: "language", mode: "none", values: ["en"] },
        { kind: "language", mode: "none", values: ["en", "fr"] },
        { kind: "network", mode: "any", values: ["49"] },
      ]),
    ).toEqual({ exclude_original_languages: ["en", "fr"], network_ids: [49] });
  });
});

describe("rule presets", () => {
  it("picks the admin's language from the browser, or English", () => {
    expect(adminLanguage("fr-CA")).toBe("fr");
    expect(adminLanguage("ja")).toBe("ja");
    expect(adminLanguage("xx-YY")).toBe("en");
    expect(adminLanguage(undefined)).toBe("en");
  });

  it("knows which presets a media type's rules already hold", () => {
    const used = presetsInUse(
      [
        route({ conditions: { anime: true } }),
        route({ conditions: { genre_ids: [10762, 10751], max_content_rating: "PG" } }),
        route({ conditions: { exclude_original_languages: ["en"], anime: false } }),
      ],
      "series",
      "en",
    );
    expect([...used].sort()).toEqual(["anime", "kids"]);
  });
});

describe("never-used rules", () => {
  it("covers a narrower rule, and only then", () => {
    expect(conditionsCover({ anime: true }, { anime: true, genre_ids: [16] })).toBe(true);
    expect(conditionsCover({ genre_ids: [16, 10751] }, { genre_ids: [16] })).toBe(true);
    expect(conditionsCover({ genre_ids: [16] }, { genre_ids: [16, 10751] })).toBe(false);
    expect(conditionsCover({ genre_ids: [16] }, { anime: true })).toBe(false);
    expect(conditionsCover({ anime: true }, { anime: false })).toBe(false);
    expect(
      conditionsCover({ exclude_original_languages: ["en"] }, { original_languages: ["ja", "ko"] }),
    ).toBe(true);
    // A title has many genres, so wanting Animation does not rule out Horror.
    expect(conditionsCover({ exclude_genre_ids: [27] }, { genre_ids: [16] })).toBe(false);
    expect(conditionsCover({ exclude_genre_ids: [27] }, { exclude_genre_ids: [27, 53] })).toBe(
      true,
    );
    expect(conditionsCover({ year_from: 1980 }, { year_from: 1990, year_to: 1999 })).toBe(true);
    expect(conditionsCover({ year_to: 1989 }, { year_from: 1980 })).toBe(false);
    expect(conditionsCover({ max_content_rating: "PG-13" }, { max_content_rating: "TV-Y7" })).toBe(
      true,
    );
    expect(conditionsCover({ max_content_rating: "PG" }, { max_content_rating: "PG-13" })).toBe(
      false,
    );
    expect(conditionsCover({ max_content_rating: "PG" }, {})).toBe(false);
  });

  it("needs the rule above to decide every copy the rule below decides", () => {
    const above = route({ conditions: { anime: true }, hd: { integration_id: "s2" } });
    const below = route({ conditions: { anime: true, genre_ids: [16] } });
    expect(ruleShadows(above, { ...below, hd: { integration_id: "s1" } })).toBe(true);
    expect(ruleShadows(above, { ...below, skip_uhd: true })).toBe(false);
    expect(
      ruleShadows({ ...above, enabled: false }, { ...below, hd: { integration_id: "s1" } }),
    ).toBe(false);
  });
});

describe("routing warnings", () => {
  const animeFields = [
    {
      key: "series_type",
      label: "Series type",
      control: "SELECT",
      options: [
        { value: "standard", label: "Standard" },
        { value: "anime", label: "Anime" },
      ],
    } as PluginAdminFormField,
  ];
  function input(extra: Partial<RoutingWarningInput>): RoutingWarningInput {
    return {
      mediaType: "series",
      rules: [],
      fallback: fallback("s1", undefined, "series"),
      servers,
      serverFields: () => animeFields,
      language: "en",
      forceDual: false,
      ...extra,
    };
  }

  it("flags a rule a rule above always beats, and one sent to a server that can't take it", () => {
    const warnings = routingWarnings(
      input({
        rules: [
          route({
            id: "a",
            name: "Anime",
            conditions: { anime: true },
            hd: { integration_id: "s2", overrides: { series_type: "anime" } },
          }),
          route({
            id: "b",
            name: "Old anime",
            conditions: { anime: true, year_to: 1999 },
            hd: { integration_id: "r1" },
          }),
        ],
      }),
    );
    expect(warnings.get("a")).toBeUndefined();
    expect(warnings.get("b")?.map((w) => w.text)).toEqual([
      "Never used: “Anime” above catches every request this rule would.",
      "Radarr is a Radarr server; series need Sonarr.",
    ]);
    expect(warnings.get("b")?.[0]?.fix).toEqual({ kind: "move-above", targetId: "a" });
  });

  it("says Everything else makes no 4K copies while every request asks for one", () => {
    const warnings = routingWarnings(input({ forceDual: true }));
    expect(warnings.get("fallback-series")?.[0]).toMatchObject({
      text: "“Also request a 4K version of every title” is on, but Everything else doesn't send 4K versions. Titles no rule sends to a 4K server get HD only.",
      fix: { kind: "edit-fallback" },
    });
    expect(
      routingWarnings(input({ forceDual: true, fallback: fallback("s1", "s2", "series") })).size,
    ).toBe(0);
  });
});

describe("how it was decided", () => {
  const facts = {
    anime: false,
    genre_ids: [16],
    keyword_ids: [],
    network_ids: [],
    company_ids: [],
    origin_countries: ["US"],
    original_language: "en",
    year: 2019,
  };
  const ctx = { facts, mediaType: "series" as const, names: users };
  const step = (extra: Partial<RequestRoutePreviewRule>): RequestRoutePreviewRule => ({
    route_id: "x",
    route_name: "Rule",
    is_fallback: false,
    enabled: true,
    unmet_conditions: [],
    hd: "no_match",
    uhd: "no_match",
    ...extra,
  });

  it("lists the title's facts", () => {
    expect(factsSentence({ ...facts, content_rating: "TV-Y7" }, "series")).toBe(
      "English · United States · 2019 · Rated TV-Y7 · Animation",
    );
    expect(factsSentence({ ...facts, anime: true }, "movie")).toBe(
      "anime · English · United States · 2019 · No rating · Animation",
    );
    // A title's own country's rating, when it has no US one.
    expect(factsSentence({ ...facts, content_rating: "JP:PG12" }, "movie")).toBe(
      "English · United States · 2019 · Rated PG12 (JP) · Animation",
    );
    expect(
      traceLine(
        step({ route_name: "Kids", unmet_conditions: ["max_content_rating"] }),
        1,
        { max_content_rating: "PG" },
        { ...ctx, facts: { ...facts, content_rating: "JP:PG12" } },
      ),
    ).toBe("1. Kids — doesn't match: rated PG12 (JP) (wants PG or lower)");
  });

  it("explains each rule in plain sentences", () => {
    expect(
      traceLine(step({ route_name: "Anime", hd: "sends", uhd: "passes" }), 1, { anime: true }, ctx),
    ).toBe("1. Anime — matches · decides HD");
    expect(
      traceLine(
        step({
          route_name: "Kids & family",
          unmet_conditions: ["genre_ids", "max_content_rating"],
        }),
        3,
        { genre_ids: [10751, 10762], max_content_rating: "PG" },
        ctx,
      ),
    ).toBe(
      "3. Kids & family — doesn't match: genre is Animation (wants Family or Kids); no rating (wants PG or lower)",
    );
    expect(traceLine(step({ route_name: "Old anime", enabled: false }), 2, {}, ctx)).toBe(
      "Off: Old anime",
    );
    expect(
      traceLine(
        step({
          is_fallback: true,
          route_name: "Everything else",
          hd: "already_decided",
          uhd: "skips",
        }),
        0,
        {},
        ctx,
      ),
    ).toBe("Everything else — decides 4K: none");
    expect(
      traceLine(
        step({ unmet_conditions: ["year_from", "year_to"] }),
        1,
        { year_from: 1980, year_to: 1989 },
        ctx,
      ),
    ).toBe("1. Rule — doesn't match: year is 2019 (wants 1980–1989)");
    expect(
      traceLine(
        step({ unmet_conditions: ["requester_user_ids", "exclude_original_languages"] }),
        1,
        { requester_user_ids: [2], exclude_original_languages: ["en"] },
        ctx,
      ),
    ).toBe(
      "1. Rule — doesn't match: requested by anyone (wants kid); language is English (wants anything but English)",
    );
  });
});

describe("request routing bodies", () => {
  it("drops empty conditions and the 4K destination of a rule that skips 4K", () => {
    const draft = {
      ...ruleDraft(null),
      name: "  Kids  ",
      conditions: { genre_ids: [], exclude_keyword_ids: [10], year_to: 0 },
      hd: { integration_id: "r2", overrides: { root_folder: undefined } },
      uhd: { integration_id: "r3", overrides: {} },
      skipUhd: true,
    };
    expect(ruleBody(draft, "series")).toEqual({
      media_type: "series",
      name: "Kids",
      enabled: true,
      conditions: { exclude_keyword_ids: [10] },
      hd: { integration_id: "r2" },
      uhd: {},
      skip_uhd: true,
    });
    expect(cleanConditions({ anime: false, max_content_rating: "" })).toEqual({ anime: false });
  });
});

describe("request servers", () => {
  it("routes movies to Radarr and series to Sonarr", () => {
    expect(serverServesMediaType(radarr, "movie")).toBe(true);
    expect(serverServesMediaType(radarr, "series")).toBe(false);
    const other = { supported_media_types: ["series"] } as unknown as RequestIntegration;
    expect(serverServesMediaType(other, "series")).toBe(true);
    expect(serverServesMediaType(other, "movie")).toBe(false);
  });

  it("lists where a server is used", () => {
    expect(
      serverRouteUsage("r4", [
        fallback("r1", "r4"),
        route({ name: "Anime", hd: { integration_id: "r4" } }),
        route({ name: "Kids", media_type: "series", uhd: { integration_id: "r4" } }),
      ]),
    ).toBe("Everything else 4K (movies) · Anime (movies) · Kids 4K (series)");
    expect(serverRouteUsage("r1", [fallback("r1")])).toBe("Everything else (movies)");
  });

  it("lets the last server of its kind go with an Everything else only it serves", () => {
    expect(serverDeleteBlockers("r1", [fallback("r1")], [radarr, sonarr], false)).toBe("");
    // Another Radarr, a rule, or another server on Everything else keeps it.
    expect(serverDeleteBlockers("r1", [fallback("r1")], [radarr, radarr4k], false)).toBe(
      "Everything else (movies)",
    );
    expect(
      serverDeleteBlockers(
        "r1",
        [fallback("r1"), route({ name: "Kids", hd: { integration_id: "x" } })],
        [radarr],
        false,
      ),
    ).toBe("Everything else (movies)");
    expect(serverDeleteBlockers("r1", [fallback("r1", "r4")], [radarr], false)).toBe(
      "Everything else (movies)",
    );
  });

  it("never lets the hidden Everything else keep a server under Standard", () => {
    expect(serverDeleteBlockers("r1", [fallback("r1", "r4")], [radarr, radarr4k], true)).toBe("");
    // A paused rule still keeps it.
    expect(
      serverDeleteBlockers(
        "r1",
        [fallback("r1"), route({ name: "Kids", hd: { integration_id: "r1" } })],
        [radarr, radarr4k],
        true,
      ),
    ).toBe("Kids (movies)");
  });

  it("offers overrides for the server's own settings, never the ones routing sets, and splits them", () => {
    const form = {
      fields: [
        { key: "service_kind", label: "Service", control: "SELECT" },
        { key: "tags", label: "Tags", control: "MULTI_SELECT" },
        { key: "root_folder", label: "Root folder", control: "SELECT", dynamic_options: true },
        { key: "is_default", label: "Default", control: "SWITCH" },
        { key: "anime_tags", label: "Anime tags", control: "MULTI_SELECT" },
        {
          key: "minimum_availability",
          label: "Minimum availability",
          control: "SELECT",
          show_when: [{ field: "service_kind", equals: ["radarr"] }],
        },
        {
          key: "series_type",
          label: "Series type",
          control: "SELECT",
          show_when: [{ field: "service_kind", equals: ["sonarr"] }],
        },
      ],
    } as unknown as PluginAdminForm;
    const fields = overrideFields(form, { service_kind: "sonarr" });
    expect(fields.map((f) => f.key)).toEqual(["tags", "root_folder", "series_type"]);
    const split = splitOverrideFields(fields);
    expect(split.inline.map((f) => f.key)).toEqual(["root_folder", "tags"]);
    expect(split.more.map((f) => f.key)).toEqual(["series_type"]);
  });
});

describe("review fixes", () => {
  it("compares ratings by each one's own minimum age, as the server does", () => {
    expect(ratingAge("TV-Y7")).toBe(7);
    expect(ratingAge("pg-13")).toBe(13);
    expect(ratingAge("Rated TV-14")).toBe(14);
    expect(ratingAge("NC-17")).toBe(18);
    expect(ratingAge("12A")).toBeUndefined();
    expect(ROUTING_RATINGS).toEqual([
      "G",
      "TV-Y",
      "TV-G",
      "TV-Y7",
      "PG",
      "TV-PG",
      "PG-13",
      "TV-14",
      "R",
    ]);
    // PG-13 (13) takes less than TV-14 (14), and TV-Y7 (7) less than TV-PG (8).
    expect(conditionsCover({ max_content_rating: "PG-13" }, { max_content_rating: "TV-14" })).toBe(
      false,
    );
    expect(conditionsCover({ max_content_rating: "TV-Y7" }, { max_content_rating: "TV-PG" })).toBe(
      false,
    );
    expect(conditionsCover({ max_content_rating: "PG" }, { max_content_rating: "TV-PG" })).toBe(
      true,
    );
    expect(conditionsCover({ max_content_rating: "TV-14" }, { max_content_rating: "PG-13" })).toBe(
      true,
    );
  });

  it("names networks and studios from separate maps", () => {
    const names = {
      networks: new Map([[174, "AMC"]]),
      studios: new Map([[174, "Warner Bros. Pictures"]]),
    };
    expect(ruleSentence({ network_ids: [174] }, "series", names)).toBe("When a series is on AMC");
    expect(ruleSentence({ company_ids: [174] }, "movie", names)).toBe(
      "When a movie is made by Warner Bros. Pictures",
    );
    expect(autoRuleName({ network_ids: [174] }, "series", names)).toBe("AMC");
    expect(autoRuleName({ company_ids: [174] }, "movie", names)).toBe("Warner Bros. Pictures");
    const facts = {
      anime: false,
      genre_ids: [],
      keyword_ids: [],
      network_ids: [174],
      company_ids: [174],
      origin_countries: [],
    };
    const step = {
      route_id: "x",
      route_name: "Rule",
      is_fallback: false,
      enabled: true,
      hd: "no_match" as const,
      uhd: "no_match" as const,
    };
    expect(
      traceLine(
        { ...step, unmet_conditions: ["exclude_network_ids"] },
        1,
        { exclude_network_ids: [174] },
        {
          facts,
          mediaType: "series",
          names,
        },
      ),
    ).toBe("1. Rule — doesn't match: network is AMC (wants anything but AMC)");
    expect(
      traceLine(
        { ...step, unmet_conditions: ["exclude_company_ids"] },
        1,
        { exclude_company_ids: [174] },
        {
          facts,
          mediaType: "movie",
          names,
        },
      ),
    ).toBe(
      "1. Rule — doesn't match: studio is Warner Bros. Pictures (wants anything but Warner Bros. Pictures)",
    );
  });

  it("warns about anime order only below a rule on language alone", () => {
    const anime = route({
      id: "a",
      name: "Anime",
      media_type: "series",
      conditions: { anime: true },
      hd: { integration_id: "s2", overrides: { series_type: "anime" } },
    });
    const foreign = (conditions: RequestRoute["conditions"]) =>
      route({
        id: "f",
        name: "Foreign",
        media_type: "series",
        conditions,
        hd: { integration_id: "s1" },
      });
    const warned = (conditions: RequestRoute["conditions"]) =>
      (
        routingWarnings({
          mediaType: "series",
          rules: [foreign(conditions), anime],
          fallback: fallback("s1", undefined, "series"),
          servers,
          serverFields: () => [],
          language: "en",
          forceDual: false,
        }).get("a") ?? []
      ).some((warning) => warning.key === "anime-order");
    expect(warned({ exclude_original_languages: ["en"] })).toBe(true);
    expect(warned({ original_languages: ["ja"] })).toBe(true);
    expect(warned({ exclude_original_languages: ["en"], requester_user_ids: [2] })).toBe(false);
    expect(warned({ original_languages: ["ja"], year_to: 1999 })).toBe(false);
  });
});
