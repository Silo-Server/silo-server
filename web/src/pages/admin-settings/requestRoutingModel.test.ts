import { describe, expect, it } from "vitest";
import type { PluginAdminForm, PluginAdminFormField, RequestIntegration } from "@/api/types";
import type { RequestRoute, RequestRouteConditions } from "@/api/v2/adminRequests";

import {
  cleanConditions,
  conditionRows,
  overrideFields,
  ratingAge,
  ROUTING_RATINGS,
  ruleBody,
  ruleDraft,
  rowsToConditions,
  splitOverrideFields,
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
  serverNeedsAPIKey,
  serverNeedsBaseURL,
  serverReady,
  serverRouteUsage,
  serverServesMediaType,
  type RequestRouterInstallation,
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

describe("condition rows", () => {
  it("round-trips includes, exclusions, years and a rating", () => {
    const conditions: RequestRouteConditions = {
      anime: false,
      genre_ids: [16],
      exclude_genre_ids: [27],
      exclude_original_languages: ["en"],
      year_from: 1990,
      series_statuses: ["ended", "cancelled"],
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
      "status",
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
    // A series has one status, so wanting Returning rules out Ended.
    expect(
      conditionsCover({ exclude_series_statuses: ["ended"] }, { series_statuses: ["returning"] }),
    ).toBe(true);
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
      installations: [],
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
  function routerInstallation(
    request_router?: RequestRouterInstallation["capability"]["request_router"],
  ): RequestRouterInstallation {
    return {
      installationID: 1,
      pluginID: "silo.requests.local",
      capability: { type: "request_router.v1", id: "local", display_name: "Local", request_router },
    };
  }
  const keyless = server("k1", "Local", "radarr", { has_api_key: false, capability_id: "local" });

  it("needs a URL and a key unless the server's plugin works without them", () => {
    expect(serverNeedsAPIKey(undefined)).toBe(true);
    expect(serverNeedsBaseURL(undefined)).toBe(true);
    expect(serverNeedsAPIKey(routerInstallation())).toBe(true);
    expect(serverNeedsBaseURL(routerInstallation())).toBe(true);
    const neither = routerInstallation({ api_key_optional: true, base_url_optional: true });
    expect(serverNeedsAPIKey(neither)).toBe(false);
    expect(serverNeedsBaseURL(neither)).toBe(false);
  });

  it("counts a server without a key as ready only when its plugin needs none", () => {
    const keyOptional = routerInstallation({ api_key_optional: true, base_url_optional: false });
    expect(serverReady(keyless, [keyOptional])).toBe(true);
    expect(serverReady(keyless, [routerInstallation()])).toBe(false);
    // A server whose plugin is not installed needs a key.
    expect(serverReady(keyless, [])).toBe(false);
    expect(serverReady({ ...keyless, enabled: false }, [keyOptional])).toBe(false);
    expect(serverReady({ ...keyless, installation_id: undefined }, [keyOptional])).toBe(false);
    expect(serverReady(radarr, [])).toBe(true);
  });

  it("warns that a server without a key needs setup only when its plugin needs one", () => {
    const warns = (installations: RequestRouterInstallation[]) =>
      (
        routingWarnings({
          mediaType: "movie",
          rules: [],
          fallback: fallback("k1"),
          servers: [keyless],
          installations,
          serverFields: () => [],
          language: "en",
          forceDual: false,
        }).get(fallback("k1").id) ?? []
      ).some((warning) => warning.text === "Local needs setup.");
    expect(warns([routerInstallation()])).toBe(true);
    expect(warns([routerInstallation({ api_key_optional: true, base_url_optional: false })])).toBe(
      false,
    );
  });

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
          installations: [],
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
