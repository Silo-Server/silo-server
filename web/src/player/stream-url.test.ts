import { describe, expect, it } from "vitest";
import { buildPlayerStreamUrl } from "./stream-url";

describe("buildPlayerStreamUrl", () => {
  it("joins the access token with `&` when the stream path already has `?st=`", () => {
    const url = buildPlayerStreamUrl(
      "https://api.example.com",
      "/api/v1/playback/stream/abc.m3u8?st=streamtoken123",
      "jwt-access-token",
    );

    const parsed = new URL(url);
    // Both params must survive as separate query keys.
    expect(parsed.searchParams.get("st")).toBe("streamtoken123");
    expect(parsed.searchParams.get("token")).toBe("jwt-access-token");
  });

  it("uses `?` when the stream path has no existing query string", () => {
    const url = buildPlayerStreamUrl(
      "https://api.example.com",
      "/api/v1/playback/stream/abc.m3u8",
      "jwt-access-token",
    );

    expect(url).toBe(
      "https://api.example.com/api/v1/playback/stream/abc.m3u8?token=jwt-access-token",
    );
    const parsed = new URL(url);
    expect(parsed.searchParams.get("token")).toBe("jwt-access-token");
  });

  it("preserves a server-anchored seek param instead of synthesizing one", () => {
    // v3 plans arrive fully anchored: the seek offset is the server's decision
    // and rides in the plan's stream URL. The helper must pass it through
    // untouched and never add one of its own.
    const url = buildPlayerStreamUrl(
      "https://api.example.com",
      "/api/v1/playback/stream/abc.m3u8?st=streamtoken123&seek=12.500",
      "jwt-access-token",
    );

    const parsed = new URL(url);
    expect(parsed.searchParams.get("st")).toBe("streamtoken123");
    expect(parsed.searchParams.get("token")).toBe("jwt-access-token");
    expect(parsed.searchParams.get("seek")).toBe("12.500");
  });

  it("returns the path unchanged when there is no token", () => {
    const url = buildPlayerStreamUrl(
      "https://api.example.com",
      "/api/v1/playback/proxy/sometoken/abc.m3u8",
      null,
    );

    expect(url).toBe("https://api.example.com/api/v1/playback/proxy/sometoken/abc.m3u8");
  });
});

it.each([
  [
    "/api/v1",
    "/api/v2/stream/session?st=opaque%2Bsignature",
    "/api/v2/stream/session?st=opaque%2Bsignature&token=access",
  ],
  ["/api/v1", "/api/v1/stream/session?st=opaque", "/api/v2/stream/session?st=opaque&token=access"],
  [
    "https://silo.example.test/base/api/v1",
    "/api/v2/playback/transcode/session/master.m3u8?st=opaque",
    "https://silo.example.test/base/api/v2/playback/transcode/session/master.m3u8?st=opaque&token=access",
  ],
  ["/api/v1", "/stream/session", "/api/v2/stream/session?token=access"],
])("resolves server media paths from configured API base %s", (base, path, expected) => {
  expect(buildPlayerStreamUrl(base, path, "access")).toBe(expected);
});

it.each(["/api/v1", "/api/v2", "https://silo.example.test/base/api/v1"])(
  "projects realtime subtitle paths without changing signed query bytes from %s",
  (base) => {
    const root = base.replace(/\/api\/v[12]$/, "");
    expect(
      buildPlayerStreamUrl(base, "/stream/session/subtitles/4.vtt?st=a%2Bb&file_id=7", "access"),
    ).toBe(`${root}/api/v2/stream/session/subtitles/4.vtt?st=a%2Bb&file_id=7&token=access`);
    expect(buildPlayerStreamUrl(base, "/stream/session/subtitles/4/fonts?st=a%2Bb", null)).toBe(
      `${root}/api/v2/stream/session/subtitles/4/fonts?st=a%2Bb`,
    );
    expect(
      buildPlayerStreamUrl(base, "https://proxy.example.test/stream/opaque?st=a%2Bb", null),
    ).toBe("https://proxy.example.test/stream/opaque?st=a%2Bb");
  },
);
