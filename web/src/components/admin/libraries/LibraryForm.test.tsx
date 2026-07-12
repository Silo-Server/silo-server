import { describe, expect, it } from "vitest";

import type { LevelChainItem } from "./useLibraryForm";
import {
  contentLevelsForType,
  hasChainProviders,
  levelChainsFromResponse,
  mergeChainWithDefaults,
} from "./useLibraryForm";

describe("contentLevelsForType", () => {
  it("maps ebook libraries to the ebook metadata content level", () => {
    expect(contentLevelsForType("ebooks")).toEqual(["ebook"]);
    expect(contentLevelsForType("ebook")).toEqual(["ebook"]);
  });

  it("includes ebook metadata providers for mixed libraries", () => {
    expect(contentLevelsForType("mixed")).toEqual([
      "movie",
      "series",
      "season",
      "episode",
      "audiobook",
      "ebook",
    ]);
  });
});

describe("levelChainsFromResponse", () => {
  it("orders entries by server priority and keeps the server's slug and enabled state", () => {
    // The same converter handles a saved chain and the provider-defaults
    // response — including a specialist the server seeded disabled.
    const chains = levelChainsFromResponse({
      levels: {
        series: [
          {
            plugin_installation_id: 22,
            capability_id: "sportarr",
            provider_slug: "sportarr",
            priority: 2,
            enabled: false,
          },
          {
            plugin_installation_id: 2,
            capability_id: "tvdb",
            provider_slug: "tvdb",
            priority: 0,
            enabled: true,
          },
          {
            plugin_installation_id: 1,
            capability_id: "tmdb",
            provider_slug: "tmdb",
            priority: 1,
            enabled: true,
          },
        ],
      },
    });

    expect(chains["series"]!.map((e) => [e.provider_slug, e.enabled])).toEqual([
      ["tvdb", true],
      ["tmdb", true],
      ["sportarr", false],
    ]);
  });

  it("returns an empty record for a missing or empty response", () => {
    expect(levelChainsFromResponse(undefined)).toEqual({});
    expect(levelChainsFromResponse({ levels: {} })).toEqual({});
  });
});

describe("mergeChainWithDefaults", () => {
  const item = (slug: string, over: Partial<LevelChainItem> = {}): LevelChainItem => ({
    plugin_installation_id: 1,
    capability_id: slug,
    provider_slug: slug,
    enabled: true,
    ...over,
  });

  it("fills only the levels the saved chain does not cover", () => {
    const merged = mergeChainWithDefaults(
      { series: [item("tvdb")], season: [] },
      { series: [item("tmdb")], season: [item("tmdb")], episode: [item("tmdb")] },
      "series",
    );

    expect(merged["series"]!.map((e) => e.provider_slug)).toEqual(["tvdb"]);
    expect(merged["season"]!.map((e) => e.provider_slug)).toEqual(["tmdb"]);
    expect(merged["episode"]!.map((e) => e.provider_slug)).toEqual(["tmdb"]);
  });

  it("leaves levels empty when the defaults have nothing for them either", () => {
    const merged = mergeChainWithDefaults({}, {}, "movies");
    expect(merged["movie"]).toEqual([]);
  });
});

describe("hasChainProviders", () => {
  const item = (slug: string): LevelChainItem => ({
    plugin_installation_id: 1,
    capability_id: slug,
    provider_slug: slug,
    enabled: false,
  });

  it("is true when the server chain/defaults expose any provider, even disabled builtins", () => {
    // A plugin-less server still seeds the built-in NFO provider (disabled):
    // the chain editor must render instead of the "install a plugin" empty state.
    expect(hasChainProviders({ movie: [item("nfo")] })).toBe(true);
  });

  it("is false only when the resolved chains are empty", () => {
    expect(hasChainProviders({})).toBe(false);
    expect(hasChainProviders({ movie: [], series: [] })).toBe(false);
  });
});
