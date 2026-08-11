import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, renderHook } from "@testing-library/react";
import { createElement, useEffect, type MutableRefObject, type ReactNode } from "react";
import { PlayerConfigProvider, type PlayerConfig } from "@/player";
import {
  audiobookAbsoluteTime,
  useAudiobookPlayback,
  type AudiobookPlayback,
} from "./useAudiobookPlayback";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import type { PlaybackRealtimeCommandEnvelope } from "@/player/realtime-protocol";

const realtimeOptions = vi.hoisted(() => ({
  current: null as null | {
    sessionId: string | null;
    onCommand: (command: PlaybackRealtimeCommandEnvelope) => Promise<void> | void;
  },
}));

vi.mock("@/hooks/queries/progress", () => ({
  useReportMediaProgress: () => ({ mutate: vi.fn() }),
}));
vi.mock("@/player/hooks/usePlaybackRealtime", () => ({
  usePlaybackRealtime: vi.fn((options) => {
    realtimeOptions.current = options;
    return { connectionState: "connected" };
  }),
}));

const files: AudiobookFile[] = [
  {
    id: 1,
    path: "a.m4b",
    duration_seconds: 600,
    chapters: [
      { index: 0, title: "One", source: "embedded", start_seconds: 0, end_seconds: 300 },
      { index: 1, title: "Two", source: "embedded", start_seconds: 300, end_seconds: 600 },
    ],
  },
];

const multiFile: AudiobookFile[] = [
  {
    id: 1,
    path: "a.mp3",
    duration_seconds: 300,
    chapters: [{ index: 0, title: "One", source: "embedded", start_seconds: 0, end_seconds: 300 }],
  },
  {
    id: 2,
    path: "b.mp3",
    duration_seconds: 300,
    chapters: [{ index: 0, title: "Two", source: "embedded", start_seconds: 0, end_seconds: 300 }],
  },
];

function makeAudio() {
  const audio = document.createElement("audio");
  Object.defineProperty(audio, "duration", { value: 600, writable: true });
  Object.defineProperty(audio, "paused", { value: true, writable: true });
  audio.play = vi.fn().mockResolvedValue(undefined);
  audio.pause = vi.fn();
  audio.load = vi.fn();
  return audio;
}

const playerConfig: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getProfileToken: () => null,
};

function wrapper({ children }: { children: ReactNode }) {
  return createElement(PlayerConfigProvider, { config: playerConfig, children });
}

function renderAudiobookPlayback(
  options: Partial<Parameters<typeof useAudiobookPlayback>[0]> = {},
) {
  return renderHook(
    () =>
      useAudiobookPlayback({
        contentId: "c",
        files,
        initialPositionSeconds: 0,
        ...options,
      }),
    { wrapper },
  );
}

/**
 * A minimal audio-only v3 decision: the `original_http` route the planner takes
 * when the browser can already decode the source, whose player clock starts at
 * the requested position.
 */
function audioOnlyDecision(
  sessionId: string,
  startPosition: number,
  timeline: Partial<{
    stream_origin_seconds: number;
    timeline_offset_seconds: number;
    player_start_seconds: number;
    can_seek_anywhere: boolean;
  }> = {},
) {
  return {
    protocol_version: 3,
    server_features: ["playback_plan_v3"],
    outcome: "playable",
    session_id: sessionId,
    playback_plan: {
      protocol_version: 3,
      plan_id: `plan:${sessionId}`,
      plan_attempt_key: "v3:0000000000000001",
      session_id: sessionId,
      delivery: "original_http",
      stream: {
        url: `/stream/${sessionId}`,
        protocol: "http_progressive",
        headers: {},
        header_refresh: "session",
      },
      timeline: {
        source_start_seconds: startPosition,
        stream_origin_seconds: 0,
        player_start_seconds: startPosition,
        timeline_offset_seconds: 0,
        can_seek_anywhere: true,
        seek_restoration: "player_position",
        ...timeline,
      },
      selected_tracks: {},
      effective_recipe: {},
      claims: {},
      subtitle: { mode: "off", inventory: [] },
      transformations: [],
      applied_quirks: [],
      runtime_corrections: [],
      available_qualities: [{ label: "original", preserves_source: true }],
      degradation_warnings: [],
      decision_reason: "validated_original_playback",
      requested_media_file_id: 1,
      effective_media_file_id: 1,
      source: { media_file_id: 1, duration_seconds: 600 },
      subtitle_fidelity_policy: "preserve",
    },
  };
}

function jsonResponse(body: unknown, init: ResponseInit = {}) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

function realtimeCommand(
  name: PlaybackRealtimeCommandEnvelope["name"],
  payload?: Record<string, unknown>,
): PlaybackRealtimeCommandEnvelope {
  return {
    type: "command",
    command_id: `cmd-${name}`,
    session_id: realtimeOptions.current?.sessionId ?? "session-1",
    name,
    payload,
  };
}

async function flushAsyncWork() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("useAudiobookPlayback", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      ["audio/mp4", "audio/mpeg", "audio/flac", "audio/ogg"].some((supported) =>
        mime.startsWith(supported),
      )
        ? "probably"
        : "",
    );
    let sessionCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/api/v1/playback/start") || url.endsWith("/playback/start")) {
          sessionCount += 1;
          const body = JSON.parse(String(init?.body)) as { start_position?: number };
          return jsonResponse(
            audioOnlyDecision(`session-${sessionCount}`, body.start_position ?? 0),
            { status: 201 },
          );
        }
        if (url.endsWith("/playback/route-events")) {
          return new Response(null, { status: 202 });
        }
        if (url.includes("/progress") || init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        return jsonResponse({});
      }),
    );
    realtimeOptions.current = null;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("returns a flattened chapter list across files", () => {
    const { result } = renderAudiobookPlayback();
    expect(result.current.chapters).toHaveLength(2);
    expect(result.current.chapters[0]!.start_seconds).toBe(0);
    expect(result.current.chapters[1]!.start_seconds).toBe(300);
  });

  it("starts a playback session and builds a tokenized stream URL", async () => {
    const { result } = renderAudiobookPlayback();

    await flushAsyncWork();

    expect(result.current.streamUrl).toBe("/api/v1/stream/session-1?token=token");

    const startCall = vi
      .mocked(fetch)
      .mock.calls.find(([url]) => String(url).endsWith("/playback/start"));
    expect(startCall).toBeTruthy();
    expect(JSON.parse(String(startCall?.[1]?.body))).toMatchObject({
      protocol_version: 3,
      file_id: 1,
      profile_id: "profile-1",
      // Zero is sent explicitly: the book-absolute position is already resolved
      // to this part's local clock, so an omitted value would hand resume policy
      // back to the server for a timeline it does not own.
      start_position: 0,
      progress_persistence: "client",
      quality_preference: "original",
    });
  });

  it("advertises the delivery classes the audio-only planner routes to", async () => {
    renderAudiobookPlayback();

    await flushAsyncWork();

    const startCall = vi
      .mocked(fetch)
      .mock.calls.find(([url]) => String(url).endsWith("/playback/start"));
    const body = JSON.parse(String(startCall?.[1]?.body)) as {
      client_capabilities: { codecs_audio: string[]; containers: string[] };
      client_playback_context: { deliveries: Record<string, unknown> };
    };
    expect(body.client_capabilities.codecs_audio).toEqual(
      expect.arrayContaining(["mp3", "flac", "opus", "vorbis"]),
    );
    expect(body.client_capabilities.containers).toEqual(
      expect.arrayContaining(["mp4", "mp3", "flac", "ogg"]),
    );
    expect(Object.keys(body.client_playback_context.deliveries).sort()).toEqual([
      "hls",
      "original_http",
      "progressive",
    ]);
  });

  it("replans a failed media-element route without starting a new attempt", async () => {
    const observed: {
      startBody?: Record<string, unknown>;
      replanBody?: Record<string, unknown>;
    } = {};
    const onPlayback = vi.fn<(playback: AudiobookPlayback) => void>();
    const replacement = audioOnlyDecision("session-1", 0);
    replacement.playback_plan.plan_id = "plan:replacement";
    replacement.playback_plan.plan_attempt_key = "v3:0000000000000002";
    replacement.playback_plan.stream.url = "/stream/replacement";

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          observed.startBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return jsonResponse(audioOnlyDecision("session-1", 0), { status: 201 });
        }
        if (url.endsWith("/playback/session-1/replan")) {
          observed.replanBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
          return jsonResponse(replacement);
        }
        if (url.endsWith("/playback/route-events")) {
          return new Response(null, { status: 202 });
        }
        if (url.includes("/progress") || init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        return jsonResponse({});
      }),
    );

    function Harness() {
      const playback = useAudiobookPlayback({
        contentId: "c",
        files,
        initialPositionSeconds: 0,
      });
      useEffect(() => {
        onPlayback(playback);
      }, [playback]);
      return createElement("audio", {
        ref: playback.audioRef,
        src: playback.streamUrl || undefined,
      });
    }

    const { container } = render(createElement(Harness), { wrapper });
    await flushAsyncWork();
    expect(onPlayback.mock.lastCall?.[0].streamUrl).toContain("/stream/session-1");

    const audio = container.querySelector("audio");
    if (!audio) throw new Error("expected audio element");
    Object.defineProperty(audio, "error", {
      configurable: true,
      value: { code: 3, message: "decoder rejected original container" },
    });
    fireEvent.error(audio);

    await flushAsyncWork();
    expect(onPlayback.mock.lastCall?.[0].streamUrl).toContain("/stream/replacement");
    expect(observed.replanBody).toMatchObject({
      operation: "failure_recovery",
      playback_attempt_id: observed.startBody?.playback_attempt_id,
      failed_plan_id: "plan:session-1",
      plan_attempt_key: "v3:0000000000000001",
      attempted_plan_keys: ["v3:0000000000000001"],
      attempt_count: 1,
      quality_preference: "original",
      failure: {
        classification: "decoder_error",
        message: "decoder rejected original container",
      },
    });
    expect(typeof observed.replanBody?.plan_attempt_id).toBe("string");
    expect(
      vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith("/playback/start")),
    ).toHaveLength(1);
  });

  it("allows another recovery attempt after a transient recovery request failure", async () => {
    let replanCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          return jsonResponse(audioOnlyDecision("session-1", 0), { status: 201 });
        }
        if (url.endsWith("/playback/session-1/replan")) {
          replanCount += 1;
          return jsonResponse({ message: "temporary failure" }, { status: 503 });
        }
        if (url.endsWith("/playback/route-events")) {
          return new Response(null, { status: 202 });
        }
        if (url.includes("/progress") || init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        return jsonResponse({});
      }),
    );
    vi.spyOn(console, "error").mockImplementation(() => {});

    function Harness() {
      const playback = useAudiobookPlayback({
        contentId: "c",
        files,
        initialPositionSeconds: 0,
      });
      return createElement("audio", {
        ref: playback.audioRef,
        src: playback.streamUrl || undefined,
      });
    }

    const { container } = render(createElement(Harness), { wrapper });
    await flushAsyncWork();
    const audio = container.querySelector("audio");
    if (!audio) throw new Error("expected audio element");
    Object.defineProperty(audio, "error", {
      configurable: true,
      value: { code: 3, message: "decoder rejected original container" },
    });

    fireEvent.error(audio);
    await flushAsyncWork();
    expect(replanCount).toBe(1);

    fireEvent.error(audio);
    await flushAsyncWork();
    expect(replanCount).toBe(2);
  });

  it("starts from the part containing the initial absolute position", async () => {
    const { result } = renderAudiobookPlayback({
      files: multiFile,
      initialPositionSeconds: 450,
    });

    await flushAsyncWork();

    expect(result.current.streamUrl).toBe("/api/v1/stream/session-1?token=token");
    expect(result.current.currentTime).toBe(450);
    expect(result.current.duration).toBe(600);

    const startCall = vi
      .mocked(fetch)
      .mock.calls.find(([url]) => String(url).endsWith("/playback/start"));
    expect(JSON.parse(String(startCall?.[1]?.body))).toMatchObject({
      file_id: 2,
      start_position: 150,
    });
  });

  it("maps anchored player time and restarts a non-global seek at its file-local target", async () => {
    let sessionCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          sessionCount += 1;
          const body = JSON.parse(String(init?.body)) as { start_position: number };
          return jsonResponse(
            audioOnlyDecision(`anchored-${sessionCount}`, body.start_position, {
              stream_origin_seconds: body.start_position,
              timeline_offset_seconds: body.start_position,
              player_start_seconds: 0,
              can_seek_anywhere: false,
            }),
            { status: 201 },
          );
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (url.includes("/progress") || init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        return jsonResponse({});
      }),
    );

    const { result } = renderAudiobookPlayback({ initialPositionSeconds: 300 });
    const audio = makeAudio();
    act(() => {
      (result.current.audioRef as MutableRefObject<HTMLAudioElement>).current = audio;
    });
    await flushAsyncWork();
    expect(audiobookAbsoluteTime(0, 300, 5)).toBe(305);

    act(() => result.current.seekTo(360));
    await flushAsyncWork();
    const starts = vi
      .mocked(fetch)
      .mock.calls.filter(([url]) => String(url).endsWith("/playback/start"));
    expect(starts).toHaveLength(2);
    expect(JSON.parse(String(starts[1]?.[1]?.body))).toMatchObject({
      start_position: 360,
      progress_persistence: "client",
    });
  });

  it("maps native seeks with the contract timeline offset", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          const body = JSON.parse(String(init?.body)) as { start_position: number };
          return jsonResponse(
            audioOnlyDecision("offset-contract", body.start_position, {
              stream_origin_seconds: 300,
              timeline_offset_seconds: 290,
              player_start_seconds: 10,
              can_seek_anywhere: true,
            }),
            { status: 201 },
          );
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (url.includes("/progress") || init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        return jsonResponse({});
      }),
    );

    const { result } = renderAudiobookPlayback({ initialPositionSeconds: 300 });
    const audio = makeAudio();
    act(() => {
      (result.current.audioRef as MutableRefObject<HTMLAudioElement>).current = audio;
    });
    await flushAsyncWork();

    act(() => result.current.seekTo(360));
    expect(audio.currentTime).toBe(70);
  });

  it("preserves negative timeline offsets and clamps absolute buffered ranges", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          const body = JSON.parse(String(init?.body)) as { start_position: number };
          return jsonResponse(
            audioOnlyDecision("negative-offset", body.start_position, {
              timeline_offset_seconds: -5,
              player_start_seconds: 5,
              can_seek_anywhere: true,
            }),
            { status: 201 },
          );
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (url.includes("/progress") || init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        return jsonResponse({});
      }),
    );

    const onPlayback = vi.fn<(playback: AudiobookPlayback) => void>();

    function Harness() {
      const playback = useAudiobookPlayback({
        contentId: "c",
        files,
        initialPositionSeconds: 0,
      });
      useEffect(() => {
        onPlayback(playback);
      }, [playback]);
      return createElement("audio", {
        ref: playback.audioRef,
        src: playback.streamUrl || undefined,
      });
    }

    const { container } = render(createElement(Harness), { wrapper });
    await flushAsyncWork();
    const audio = container.querySelector("audio");
    if (!audio) throw new Error("expected audio element");
    Object.defineProperty(audio, "buffered", {
      configurable: true,
      value: {
        length: 1,
        start: () => 0,
        end: () => 8,
      } as TimeRanges,
    });

    act(() => {
      audio.currentTime = 7;
      fireEvent.timeUpdate(audio);
      fireEvent.progress(audio);
    });
    await flushAsyncWork();

    expect(onPlayback.mock.lastCall?.[0].currentTime).toBe(2);
    expect(onPlayback.mock.lastCall?.[0].buffered?.start(0)).toBe(0);
    expect(onPlayback.mock.lastCall?.[0].buffered?.end(0)).toBe(3);
  });

  it("togglePlay invokes audio.play when paused, audio.pause otherwise", () => {
    const { result } = renderAudiobookPlayback();
    const audio = makeAudio();
    act(() => {
      (result.current.audioRef as MutableRefObject<HTMLAudioElement>).current = audio;
    });
    act(() => result.current.togglePlay());
    expect(audio.play).toHaveBeenCalled();
    Object.defineProperty(audio, "paused", { value: false, writable: true });
    act(() => result.current.togglePlay());
    expect(audio.pause).toHaveBeenCalled();
  });

  it("executes realtime playback commands against the audio element", async () => {
    const onStopRequested = vi.fn();
    const { result } = renderAudiobookPlayback({ onStopRequested });
    const audio = makeAudio();
    act(() => {
      (result.current.audioRef as MutableRefObject<HTMLAudioElement>).current = audio;
    });

    await flushAsyncWork();

    expect(realtimeOptions.current?.sessionId).toBe("session-1");

    await act(async () => {
      await realtimeOptions.current?.onCommand(realtimeCommand("unpause"));
    });
    expect(audio.play).toHaveBeenCalled();

    Object.defineProperty(audio, "paused", { value: false, writable: true });
    await act(async () => {
      await realtimeOptions.current?.onCommand(realtimeCommand("pause"));
    });
    expect(audio.pause).toHaveBeenCalled();

    await act(async () => {
      await realtimeOptions.current?.onCommand(realtimeCommand("seek", { position_seconds: 120 }));
    });
    expect(result.current.currentTime).toBe(120);

    await act(async () => {
      await realtimeOptions.current?.onCommand(realtimeCommand("stop"));
    });
    act(() => {
      vi.runOnlyPendingTimers();
    });
    expect(onStopRequested).toHaveBeenCalled();
  });

  it("seekTo clamps to [0, duration]", () => {
    const { result } = renderAudiobookPlayback();
    const audio = makeAudio();
    act(() => {
      (result.current.audioRef as MutableRefObject<HTMLAudioElement>).current = audio;
    });
    act(() => result.current.seekTo(1_000_000));
    expect(audio.currentTime).toBe(599); // 600 - 1 (clamp to duration - 1 per existing behavior)
    act(() => result.current.seekTo(-50));
    expect(audio.currentTime).toBe(0);
  });

  it("currentChapter starts at the first chapter when currentTime is 0", () => {
    const { result } = renderAudiobookPlayback();
    expect(result.current.currentChapter?.title).toBe("One");
  });

  it("setSleep arms a duration timer that fires after the configured seconds", () => {
    const { result } = renderAudiobookPlayback();
    const audio = makeAudio();
    Object.defineProperty(audio, "paused", { value: false, writable: true });
    act(() => {
      (result.current.audioRef as MutableRefObject<HTMLAudioElement>).current = audio;
    });
    act(() => result.current.setSleep({ kind: "duration", seconds: 1 }));
    expect(result.current.sleep.remainingMs).toBeGreaterThan(0);
    act(() => {
      vi.advanceTimersByTime(1500);
    });
    expect(audio.pause).toHaveBeenCalled();
  });

  it("setSleep with off clears any armed timer", () => {
    const { result } = renderAudiobookPlayback();
    act(() => result.current.setSleep({ kind: "duration", seconds: 5 }));
    expect(result.current.sleep.remainingMs).toBeGreaterThan(0);
    act(() => result.current.setSleep({ kind: "off" }));
    expect(result.current.sleep.remainingMs).toBeNull();
  });
});
