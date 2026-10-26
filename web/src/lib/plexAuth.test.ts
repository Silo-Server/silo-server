import { describe, expect, it } from "vitest";
import { getPlexFallbackURLs, MAX_PLEX_FALLBACK_URLS, type BrowserPlexServer } from "./plexAuth";

function server(fields: Partial<BrowserPlexServer>): BrowserPlexServer {
  return {
    name: "Plex",
    clientIdentifier: "server-1",
    accessToken: "token",
    remoteURL: "",
    localURL: "",
    connectionURLs: [],
    owned: true,
    hasRemoteURL: false,
    hasLocalURL: false,
    ...fields,
  } as BrowserPlexServer;
}

describe("getPlexFallbackURLs", () => {
  it("leaves out the preferred address and keeps every other advertised one", () => {
    const fallbacks = getPlexFallbackURLs(
      server({
        remoteURL: "https://remote.plex.direct:32400",
        localURL: "https://local.plex.direct:32400",
        // Plex advertises local connections first for owned servers.
        connectionURLs: [
          "https://local.plex.direct:32400",
          "https://remote.plex.direct:32400",
          "https://relay.plex.direct:443",
        ],
      }),
    );

    expect(fallbacks).toEqual(["https://local.plex.direct:32400", "https://relay.plex.direct:443"]);
  });

  it("puts HTTPS addresses ahead of cleartext ones before applying the limit", () => {
    const cleartext = Array.from({ length: 40 }, (_, i) => `http://192.168.1.${10 + i}:32400`);
    const fallbacks = getPlexFallbackURLs(
      server({
        remoteURL: "https://remote.plex.direct:32400",
        connectionURLs: [...cleartext, "https://relay.plex.direct:443"],
      }),
    );

    expect(fallbacks).toHaveLength(MAX_PLEX_FALLBACK_URLS);
    expect(fallbacks[0]).toBe("https://relay.plex.direct:443");
  });
});
