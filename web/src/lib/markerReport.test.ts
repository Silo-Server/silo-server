import { beforeEach, describe, expect, it, vi } from "vitest";

import { parseMarkerReportTarget } from "./markerReport";

vi.mock("@/api/v2/request", () => ({ v2: vi.fn() }));

describe("parseMarkerReportTarget", () => {
  it("addresses a TMDB-matched film by its TMDB id", () => {
    // Measured: Silo matched GOAT (2026) as tmdb 1297842 with no TVDB id of its
    // own. Reporting it by a TVDB id nobody looked up would land on the wrong media.
    expect(parseMarkerReportTarget("movie-tmdb-1297842")).toEqual({
      type: "movie",
      tmdb_id: 1297842,
    });
  });

  it("addresses a TVDB-matched film by TVDB", () => {
    expect(parseMarkerReportTarget("movie-tvdb-169")).toEqual({ type: "movie", tvdb_id: 169 });
  });

  it("addresses an episode by series id plus coordinates", () => {
    // Measured: Bones S1E4 is episode-tvdb-75682-1-4, where 75682 is the series.
    expect(parseMarkerReportTarget("episode-tvdb-75682-1-4")).toEqual({
      type: "tv",
      tvdb_id: 75682,
      season: 1,
      episode: 4,
    });
    expect(parseMarkerReportTarget("episode-tmdb-1911-2-10")).toEqual({
      type: "tv",
      tmdb_id: 1911,
      season: 2,
      episode: 10,
    });
  });

  it("rejects an id that is not a single segment", () => {
    // Measured ids are single-segment: TVDB and TMDB are numeric, IMDb is `tt…`.
    // Anything else is not an id this can address, so it is refused rather than
    // joined back together and reported as something else.
    expect(parseMarkerReportTarget("episode-tvdb-123-456-3-7")).toBeNull();
  });

  it("accepts an IMDb-addressed item", () => {
    expect(parseMarkerReportTarget("movie-imdb-tt27613895")).toEqual({
      type: "movie",
      imdb_id: "tt27613895",
    });
    expect(parseMarkerReportTarget("episode-imdb-tt0460627-1-4")).toEqual({
      type: "tv",
      imdb_id: "tt0460627",
      season: 1,
      episode: 4,
    });
  });

  it("returns null rather than guessing", () => {
    // No control at all is better than one that reports the wrong thing.
    expect(parseMarkerReportTarget(null)).toBeNull();
    expect(parseMarkerReportTarget("")).toBeNull();
    expect(parseMarkerReportTarget("nonsense")).toBeNull();
    expect(parseMarkerReportTarget("movie-tvdb-0")).toBeNull();
    expect(parseMarkerReportTarget("movie-imdb-27613895")).toBeNull(); // not a tt id
    expect(parseMarkerReportTarget("series-tvdb-75682")).toBeNull(); // a series is not reportable
    expect(parseMarkerReportTarget("episode-tvdb-75682-1")).toBeNull(); // no coordinates
    expect(parseMarkerReportTarget("movie-itunes-1234")).toBeNull(); // unknown provider
  });
});

/**
 * The send path. The credential here is the server's, so the browser must send
 * none: what these pin is that a report reaches the plugin's route on the host and
 * that each way it can fail is reported as what it is rather than as a generic
 * failure.
 */
describe("reportMarkerSegment", () => {
  beforeEach(() => {
    // The remembered launch is module state; a fresh copy per test keeps one test's
    // cookie from deciding another's outcome.
    vi.resetModules();
    // The mocked client is a single instance for the whole file, so its call history
    // has to be cleared too, or one test's launches are counted in the next.
    vi.clearAllMocks();
  });

  async function freshModule() {
    const request = (await import("@/api/v2/request")) as unknown as {
      v2: ReturnType<typeof vi.fn>;
    };
    const report = await import("./markerReport");
    request.v2.mockResolvedValue({ expires_in: 300 });
    return { v2: request.v2, reportMarkerSegment: report.reportMarkerSegment };
  }

  /** A real Response, so the test gets the shape fetch actually returns. */
  function reply(status: number, body: unknown = {}): Response {
    return new Response(JSON.stringify(body), { status });
  }

  it("sends the target to the plugin route, carrying no credential of its own", async () => {
    const { v2, reportMarkerSegment } = await freshModule();
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(reply(200, { message: "Media flagged successfully" }));

    await expect(reportMarkerSegment(4, { type: "movie", tmdb_id: 1297842 })).resolves.toEqual({
      status: "reported",
    });

    expect(v2).toHaveBeenCalledWith("POST /api/v2/auth/plugin-launch");
    expect(fetchSpy).toHaveBeenCalledWith(
      "/api/v2/plugin-content/plugins/4/report",
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: JSON.stringify({ type: "movie", tmdb_id: 1297842 }),
      }),
    );

    // The launch cookie is the only credential involved, and it is HttpOnly. A
    // report must never carry the server's TheIntroDB key in the browser.
    const init = fetchSpy.mock.calls[0]?.[1] as RequestInit;
    expect(JSON.stringify(init.headers)).not.toContain("Authorization");
  });

  it("mints the launch cookie once for repeated reports", async () => {
    const { v2, reportMarkerSegment } = await freshModule();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(reply(200, {}));

    await reportMarkerSegment(4, { type: "movie", tmdb_id: 1 });
    await reportMarkerSegment(4, { type: "movie", tmdb_id: 1 });

    // The launch is worth one round trip, not one per report.
    expect(v2).toHaveBeenCalledTimes(1);
  });

  it("reports an already-reported segment as settled, not as a failure", async () => {
    const { reportMarkerSegment } = await freshModule();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(reply(409, { message: "already flagged" }));

    await expect(reportMarkerSegment(4, { type: "movie", tmdb_id: 1 })).resolves.toEqual({
      status: "already_reported",
    });
  });

  it("shows the plugin's own words for a refusal", async () => {
    const { reportMarkerSegment } = await freshModule();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      reply(400, { error: 'type must be "movie" or "tv"' }),
    );

    await expect(reportMarkerSegment(4, { type: "movie", tmdb_id: 1 })).resolves.toEqual({
      status: "rejected",
      message: 'type must be "movie" or "tv"',
    });
  });

  it("explains a server with no TheIntroDB key instead of quoting the plugin", async () => {
    const { reportMarkerSegment } = await freshModule();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      reply(503, { error: "introdb: flag requires an API key" }),
    );

    const result = await reportMarkerSegment(4, { type: "movie", tmdb_id: 1 });
    expect(result.status).toBe("rejected");
    // A viewer can act on "ask your admin"; the plugin's internals are not theirs.
    expect(result.status === "rejected" && result.message).not.toContain("introdb:");
  });

  it("mints a new cookie once the host refuses the remembered one", async () => {
    const { v2, reportMarkerSegment } = await freshModule();
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(reply(401, {}));

    await expect(reportMarkerSegment(4, { type: "movie", tmdb_id: 1 })).resolves.toEqual({
      status: "unauthorized",
    });
    expect(v2).toHaveBeenCalledTimes(1);

    // Same module, so the remembered launch is the one the host just refused: it has
    // to be dropped, or every later attempt would keep replaying a dead cookie.
    fetchSpy.mockResolvedValue(reply(200, {}));
    await reportMarkerSegment(4, { type: "movie", tmdb_id: 1 });
    expect(v2).toHaveBeenCalledTimes(2);
  });
});
