import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { createElement, type MutableRefObject, type ReactNode } from "react";
import { PlayerConfigProvider, type PlayerConfig } from "@/player";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
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
    let sessionCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/api/v1/playback/start") || url.endsWith("/playback/start")) {
          sessionCount += 1;
          return jsonResponse(
            {
              session_id: `session-${sessionCount}`,
              user_id: 1,
              profile_id: "profile-1",
              media_file_id: 1,
              play_method: "direct",
              position: 0,
              is_paused: false,
              stream_url: `/stream/session-${sessionCount}`,
              audio_track_index: 0,
              duration_seconds: 600,
            },
            { status: 201 },
          );
        }
        if (url.includes("/progress") || init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        return jsonResponse({});
      }),
    );
    realtimeOptions.current = null;
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
      file_id: 1,
      profile_id: "profile-1",
      play_method: "direct",
      start_position: 0,
      disable_progress_persistence: true,
    });
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
      disable_progress_persistence: true,
    });
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
