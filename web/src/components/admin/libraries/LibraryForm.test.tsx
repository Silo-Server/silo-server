import { describe, expect, it } from "vitest";

import type { PluginInstallation } from "@/api/types";

import type { MetadataProvider } from "./useLibraryForm";
import {
  buildDefaultLevelChains,
  contentLevelsForType,
  metadataProvidersFromInstallations,
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

describe("metadataProvidersFromInstallations", () => {
  const installation = (over: Partial<PluginInstallation>): PluginInstallation =>
    ({
      id: 1,
      plugin_id: "silo.tmdb",
      version: "1.0.0",
      install_path: "/x",
      enabled: true,
      capabilities: [],
      global_config_schema: [],
      user_config_schema: [],
      routes: [],
      assets: [],
      global_configs: [],
      auth_bindings: [],
      task_bindings: [],
      update_policy: "manual",
      ...over,
    }) as PluginInstallation;

  it("uses the capability id as the provider slug so casing matches the saved chain", () => {
    // The server returns provider_slug = capability_id (lowercase); the default
    // chain must read identically instead of surfacing the display name.
    const providers = metadataProvidersFromInstallations([
      installation({
        id: 7,
        capabilities: [
          {
            type: "metadata_provider.v1",
            id: "tmdb",
            display_name: "TMDB",
            metadata: { metadata: { default_priority: { movie: 2 } } },
          },
        ],
      }),
    ]);

    expect(providers).toHaveLength(1);
    expect(providers[0]!.slug).toBe("tmdb");
    expect(providers[0]!.capability_id).toBe("tmdb");
    expect(providers[0]!.defaultPriority).toEqual({ movie: 2 });
    expect(providers[0]!.defaultEnabled).toBe(true);
  });

  it("reads default_enabled from the metadata envelope, failing open like the server", () => {
    const providers = metadataProvidersFromInstallations([
      installation({
        id: 22,
        capabilities: [
          {
            type: "metadata_provider.v1",
            id: "sportarr",
            display_name: "Sportarr",
            metadata: { metadata: { default_priority: { series: 50 }, default_enabled: false } },
          },
          {
            type: "metadata_provider.v1",
            id: "top-level-opt-out",
            display_name: "X",
            metadata: { default_enabled: false },
          },
          {
            type: "metadata_provider.v1",
            id: "non-boolean",
            display_name: "Y",
            metadata: { metadata: { default_enabled: "nope" } },
          },
        ],
      }),
    ]);

    expect(providers.map((p) => [p.slug, p.defaultEnabled])).toEqual([
      ["sportarr", false],
      ["top-level-opt-out", false],
      ["non-boolean", true],
    ]);
  });

  it("skips disabled installations and non-metadata capabilities", () => {
    const providers = metadataProvidersFromInstallations([
      installation({
        id: 1,
        enabled: false,
        capabilities: [{ type: "metadata_provider.v1", id: "tvdb", display_name: "TVDB" }],
      }),
      installation({
        id: 2,
        capabilities: [{ type: "request_router.v1", id: "seerr", display_name: "Seerr" }],
      }),
    ]);
    expect(providers).toHaveLength(0);
  });
});

// Mirrors the server's TestBuildSeededChainEntries_* cases: the form's
// client-built default chain replaces the server-seeded one whenever the user
// touches the chain (e.g. changing the library type marks it dirty), so it must
// apply the same rules or the seeding fix evaporates on the UI create path.
describe("buildDefaultLevelChains", () => {
  const provider = (over: Partial<MetadataProvider>): MetadataProvider => ({
    plugin_installation_id: 1,
    capability_id: "tmdb",
    slug: "tmdb",
    defaultPriority: {},
    defaultEnabled: true,
    ...over,
  });

  it("seeds opted-out specialists disabled at their declared priority and drops unsupported providers", () => {
    const chains = buildDefaultLevelChains(
      [
        provider({
          plugin_installation_id: 1,
          capability_id: "tmdb",
          slug: "tmdb",
          defaultPriority: { series: 3, season: 3, episode: 3 },
        }),
        provider({
          plugin_installation_id: 2,
          capability_id: "tvdb",
          slug: "tvdb",
          defaultPriority: { series: 2, season: 2, episode: 2 },
        }),
        provider({
          plugin_installation_id: 22,
          capability_id: "sportarr",
          slug: "sportarr",
          defaultPriority: { series: 50, season: 50, episode: 50 },
          defaultEnabled: false,
        }),
        // Declares only audiobook — must not appear in a series chain at all.
        provider({
          plugin_installation_id: 3,
          capability_id: "audiobook-metadata",
          slug: "audiobook-metadata",
          defaultPriority: { audiobook: 1 },
        }),
      ],
      "series",
    );

    expect(chains["series"]!.map((e) => [e.capability_id, e.enabled])).toEqual([
      ["tvdb", true],
      ["tmdb", true],
      ["sportarr", false],
    ]);
  });

  it("keeps a legacy catch-all (no declared levels) parked last and disabled", () => {
    const chains = buildDefaultLevelChains(
      [
        provider({ plugin_installation_id: 9, capability_id: "legacy", slug: "legacy" }),
        provider({
          plugin_installation_id: 1,
          capability_id: "tmdb",
          slug: "tmdb",
          defaultPriority: { movie: 3 },
        }),
      ],
      "movies",
    );

    expect(chains["movie"]!.map((e) => [e.capability_id, e.enabled])).toEqual([
      ["tmdb", true],
      ["legacy", false],
    ]);
  });
});
