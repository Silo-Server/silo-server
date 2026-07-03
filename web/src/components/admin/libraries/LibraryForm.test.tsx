import { describe, expect, it } from "vitest";

import type { PluginInstallation } from "@/api/types";

import { contentLevelsForType, metadataProvidersFromInstallations } from "./useLibraryForm";

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
