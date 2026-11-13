import { afterEach, describe, expect, it, vi } from "vitest";
import {
  getPlexFallbackURLs,
  loadPlexHome,
  MAX_PLEX_FALLBACK_URLS,
  resolvePlexHomeUserServer,
  type BrowserPlexServer,
} from "./plexAuth";

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

// Answers plex.tv requests by method and path; anything unlisted is a 404.
function stubPlexTV(routes: Record<string, () => Response>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const route = `${init?.method ?? "GET"} ${new URL(input).pathname}`;
      return routes[route]?.() ?? new Response("not found", { status: 404 });
    }),
  );
}

const json =
  (body: unknown, status = 200) =>
  () =>
    Response.json(body, { status });

describe("Plex Home", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("treats an account outside a Plex Home as having no Home users", async () => {
    stubPlexTV({ "GET /api/v2/user": json({ uuid: "owner" }) });

    expect((await loadPlexHome("owner-token")).users).toEqual([]);
  });

  it("explains a rejected PIN instead of showing plex.tv's error body", async () => {
    stubPlexTV({
      "POST /api/v2/home/users/kid/switch": json({ errors: [{ code: 1041 }] }, 403),
    });
    const kid = { uuid: "kid", title: "Kid", protected: true, restricted: true };

    await expect(resolvePlexHomeUserServer("owner-token", kid, "0000", "srv")).rejects.toThrow(
      "Plex did not accept that PIN",
    );
  });

  it("refuses a server the Home user cannot reach", async () => {
    stubPlexTV({
      "POST /api/v2/home/users/kid/switch": json({ authToken: "kid-token" }),
      "GET /api/v2/resources": json([]),
    });
    const kid = { uuid: "kid", title: "Kid", protected: false, restricted: true };

    await expect(resolvePlexHomeUserServer("owner-token", kid, "", "srv")).rejects.toThrow(
      "Kid does not have access to this Plex server",
    );
  });
});
