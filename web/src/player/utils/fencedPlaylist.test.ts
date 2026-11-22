import { describe, expect, it, vi } from "vitest";

import {
  createFencedPlaylistLoader,
  createPlaylistGenerationGate,
  generationTokenFromSegmentUrl,
  playlistNamesGeneration,
} from "./fencedPlaylist";

const OLD = "session-1:inc:1";
const NEW = "session-1:inc:2";

function mediaPlaylist(token: string): string {
  const encoded = encodeURIComponent(token);
  return [
    "#EXTM3U",
    "#EXT-X-VERSION:3",
    "#EXT-X-TARGETDURATION:2",
    "#EXT-X-PLAYLIST-TYPE:VOD",
    "#EXT-X-MEDIA-SEQUENCE:0",
    "#EXTINF:2.0,",
    `seg_00000.ts?sgen=${encoded}`,
    "#EXT-X-ENDLIST",
    "",
  ].join("\n");
}

const MASTER = [
  "#EXTM3U",
  "#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1280x720",
  "media.m3u8?token=abc",
  "",
].join("\n");

const stats = () => ({
  aborted: false,
  loaded: 0,
  retry: 0,
  total: 0,
  chunkCount: 0,
  bwEstimate: 0,
  loading: { start: 0, first: 0, end: 0 },
  parsing: { start: 0, end: 0 },
  buffering: { start: 0, first: 0, end: 0 },
});

describe("generationTokenFromSegmentUrl", () => {
  it("reads the generation token from a relative segment URL", () => {
    expect(
      generationTokenFromSegmentUrl(
        "/playback/transcode/session-1/seg_00004.ts?sgen=session-1%3Ainc%3A1",
      ),
    ).toBe(OLD);
  });

  it("returns null for a URL without a token or no URL", () => {
    expect(generationTokenFromSegmentUrl("/playback/transcode/session-1/seg_00004.ts")).toBe(null);
    expect(generationTokenFromSegmentUrl(null)).toBe(null);
    expect(generationTokenFromSegmentUrl("")).toBe(null);
  });
});

describe("playlistNamesGeneration", () => {
  it("recognises a media playlist that still lists the fenced generation", () => {
    expect(playlistNamesGeneration(mediaPlaylist(OLD), OLD, "http://localhost/media.m3u8")).toBe(
      true,
    );
    expect(playlistNamesGeneration(mediaPlaylist(NEW), OLD, "http://localhost/media.m3u8")).toBe(
      false,
    );
  });

  it("does not treat a multivariant playlist as naming a generation", () => {
    expect(playlistNamesGeneration(MASTER, OLD, "http://localhost/master.m3u8")).toBe(false);
  });

  it("matches the whole token, not a prefix", () => {
    expect(
      playlistNamesGeneration(mediaPlaylist(`${OLD}0`), OLD, "http://localhost/media.m3u8"),
    ).toBe(false);
  });

  it("inspects URI attributes such as EXT-X-MAP", () => {
    const body = [
      "#EXTM3U",
      `#EXT-X-MAP:URI="init.mp4?sgen=${encodeURIComponent(OLD)}"`,
      "#EXTINF:2.0,",
      "seg_00000.ts",
    ].join("\n");
    expect(playlistNamesGeneration(body, OLD, "http://localhost/media.m3u8")).toBe(true);
  });
});

describe("createFencedPlaylistLoader", () => {
  const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

  function harness(bodies: string[], maxRetries = 3) {
    const gate = createPlaylistGenerationGate();
    let loads = 0;
    class BaseLoader {
      stats = stats();
      context = null;
      load(
        context: { url: string },
        _config: unknown,
        callbacks: {
          onSuccess: (
            response: { url: string; data: string; code: number },
            stats: unknown,
            context: unknown,
            networkDetails: unknown,
          ) => void;
          onError: () => void;
          onTimeout: () => void;
        },
      ) {
        const body = bodies[Math.min(loads, bodies.length - 1)]!;
        loads += 1;
        queueMicrotask(() =>
          callbacks.onSuccess(
            { url: context.url, data: body, code: 200 },
            this.stats,
            context,
            null,
          ),
        );
      }
      destroy() {}
      abort() {}
    }
    const waits: Array<() => void> = [];
    const Loader = createFencedPlaylistLoader(BaseLoader as never, gate, {
      maxRetries,
      schedule: (callback) => {
        waits.push(callback);
        return waits.length - 1;
      },
      cancel: () => {},
    });
    const loader = new Loader({} as never);
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const onTimeout = vi.fn();
    const load = () =>
      loader.load(
        { url: "http://localhost/media.m3u8", responseType: "text" } as never,
        {} as never,
        { onSuccess, onError, onTimeout },
      );
    return {
      gate,
      loader,
      load,
      waits,
      onSuccess,
      onError,
      get loads() {
        return loads;
      },
    };
  }

  it("holds a stale playlist until the rebuilt generation is served", async () => {
    const run = harness([mediaPlaylist(OLD), mediaPlaylist(OLD), mediaPlaylist(NEW)]);
    run.gate.fence(OLD);
    run.load();
    await flush();
    expect(run.loads).toBe(1);
    expect(run.onSuccess).not.toHaveBeenCalled();
    expect(run.waits).toHaveLength(1);

    run.waits.shift()!();
    await flush();
    expect(run.loads).toBe(2);
    expect(run.onSuccess).not.toHaveBeenCalled();

    run.waits.shift()!();
    await flush();
    expect(run.loads).toBe(3);
    expect(run.onSuccess).toHaveBeenCalledOnce();
    expect(run.onSuccess.mock.calls[0]?.[0].data).toBe(mediaPlaylist(NEW));
  });

  it("lets a clean playlist through without waiting", async () => {
    const run = harness([mediaPlaylist(NEW)]);
    run.gate.fence(OLD);
    run.load();
    await flush();
    expect(run.onSuccess).toHaveBeenCalledOnce();
    expect(run.waits).toHaveLength(0);
  });

  it("does not hand hls.js a stale playlist after its bound", async () => {
    const run = harness([mediaPlaylist(OLD)], 1);
    const exhausted = vi.spyOn(run.gate, "exhausted");
    run.gate.fence(OLD);
    run.load();
    await flush();
    expect(run.onSuccess).not.toHaveBeenCalled();
    run.waits.shift()!();
    await flush();
    expect(run.loads).toBe(2);
    expect(run.onSuccess).not.toHaveBeenCalled();
    expect(run.onError).toHaveBeenCalledOnce();
    expect(exhausted).toHaveBeenCalledOnce();
  });

  it("does not gate playlists when no generation is fenced", async () => {
    const run = harness([mediaPlaylist(OLD)]);
    run.load();
    await flush();
    expect(run.onSuccess).toHaveBeenCalledOnce();
  });
});
