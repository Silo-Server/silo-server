import { describe, expect, it } from "vitest";
import { getPlexServerURLs, type BrowserPlexServer } from "./plexAuth";

describe("getPlexServerURLs", () => {
  it("keeps the preferred URL first and preserves every advertised fallback", () => {
    const server = {
      name: "Plex",
      clientIdentifier: "server-1",
      remoteURL: "https://unreachable.plex.direct:32400",
      localURL: "http://plex:32400",
      connectionURLs: [
        "https://unreachable.plex.direct:32400",
        "https://plex.example.com",
        "http://plex:32400",
      ],
      owned: true,
      hasRemoteURL: true,
      hasLocalURL: true,
    } as BrowserPlexServer;

    expect(getPlexServerURLs(server)).toEqual([
      "https://unreachable.plex.direct:32400",
      "https://plex.example.com",
      "http://plex:32400",
    ]);
  });

  it("promotes the preferred URL out of the advertised order without dropping any", () => {
    const server = {
      name: "Plex",
      clientIdentifier: "server-1",
      remoteURL: "https://plex.example.com",
      localURL: "http://plex:32400",
      // Plex advertises local connections first for owned servers; the
      // preferred remote address has to move to the front without losing it
      // from, or duplicating it in, the fallback order.
      connectionURLs: ["http://plex:32400", "https://plex.example.com"],
      owned: true,
      hasRemoteURL: true,
      hasLocalURL: true,
    } as BrowserPlexServer;

    expect(getPlexServerURLs(server)).toEqual([
      "https://plex.example.com",
      "http://plex:32400",
    ]);
  });
});
