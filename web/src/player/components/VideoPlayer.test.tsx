import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import type { WatchTogetherRoomConnectionResult } from "../hooks/useWatchTogetherRoomConnection";
import { fixturePlanV3, fixtureSubtitleInventoryItemV3 } from "../protocol-v3.fixtures";
import type {
  PlaybackRealtimeCommandEnvelope,
  PlaybackRealtimeEventEnvelope,
} from "../realtime-protocol";
import type { PlayerSubtitleInfo, VideoFitMode } from "../types";
import { VideoPlayer } from "./VideoPlayer";

const realtimeOptions = vi.hoisted(() => ({
  current: null as null | {
    onEvent?: (event: PlaybackRealtimeEventEnvelope) => void;
    onCommand: (command: PlaybackRealtimeCommandEnvelope) => Promise<void> | void;
  },
}));
const controls = vi.hoisted(() => ({
  current: null as null | {
    currentTime: number;
    duration: number;
    onSeek: (seconds: number) => void;
    activeSubtitleIndex: number | null;
    subtitleTracks: PlayerSubtitleInfo[];
    visible: boolean;
    onSkip?: { back: () => void; forward: () => void };
    skipSeconds?: { back: number; forward: number };
    onSurfaceTap?: (event: React.MouseEvent<HTMLElement>) => void;
    isFullscreen?: boolean;
    onFullscreenToggle?: () => void;
    videoFit?: VideoFitMode;
    onVideoFitToggle?: () => void;
    onSubtitleJobAccepted?: (jobId: string, resultSubtitleId?: string) => void;
    onMutedChange?: (muted: boolean) => void;
    onVolumeChange?: (volume: number) => void;
    playbackSpeed?: number;
    onPlaybackSpeedChange?: (rate: number) => void;
  },
}));
const playerV2Mock = vi.hoisted(() => vi.fn());
vi.mock("../player-v2", () => ({ playerV2: playerV2Mock }));
const playerSeek = vi.hoisted(() => vi.fn());
const subtitleTimeline = vi.hoisted(() => ({
  textOffsetSeconds: null as number | null,
  assOffsetSeconds: null as number | null,
  liveCues: [] as Array<{ text: string }>,
  liveKey: null as string | null,
  streamGeneration: 0,
  cueRevision: 0,
  assCueRevision: 0,
  setLoadState: null as null | ((state: string) => void),
}));
const toastError = vi.hoisted(() => vi.fn());
const toastInfo = vi.hoisted(() => vi.fn());
const hlsJS = vi.hoisted(() => ({
  supported: false,
  constructed: vi.fn(),
  startLoad: vi.fn(),
  stopLoad: vi.fn(),
  loadSource: vi.fn(),
  latest: null as null | { emit: (event: string, data: unknown) => void },
  // Seconds buffered past the playhead, as hls.js measures them.
  forwardBufferSeconds: 0,
}));

vi.mock("sonner", () => ({
  toast: { error: toastError, info: toastInfo, success: vi.fn(), message: vi.fn() },
}));

vi.mock("../hooks/usePlaybackRealtime", () => ({
  usePlaybackRealtime: vi.fn((options) => {
    realtimeOptions.current = options;
    return { connectionState: "connected" };
  }),
}));
vi.mock("../hooks/useWatchProgress", () => ({
  useWatchProgress: () => vi.fn().mockResolvedValue(undefined),
}));
// Opted into per test: plans whose original the browser engine plays.
const engineRoute = vi.hoisted(() => ({ chosen: false, refused: false }));
vi.mock("../engine/routing", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../engine/routing")>();
  return {
    ...actual,
    resolveOriginalPlaybackRoute: (
      ...args: Parameters<typeof actual.resolveOriginalPlaybackRoute>
    ) =>
      engineRoute.refused
        ? {
            kind: "unsupported",
            failure: { classification: actual.ENGINE_UNSUPPORTED_SOURCE, message: "refused" },
          }
        : engineRoute.chosen
          ? { kind: "engine", source: {} }
          : actual.resolveOriginalPlaybackRoute(...args),
  };
});
const attachBrowserEngine = vi.hoisted(() =>
  vi.fn((_options: Parameters<typeof import("../engine").attachBrowserEngine>[0]) => ({
    destroy: vi.fn(),
  })),
);
vi.mock("../engine", () => ({ attachBrowserEngine }));
const releasedMedia = vi.hoisted(() => vi.fn());
const sourceLoads = vi.hoisted(() => vi.fn());
vi.mock("../bandwidth-probe", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../bandwidth-probe")>();
  return {
    ...actual,
    sourceLoadStarted: () => {
      sourceLoads();
      return actual.sourceLoadStarted();
    },
    mediaElementReleased: (element: HTMLMediaElement) => {
      releasedMedia(element, element.isConnected);
      actual.mediaElementReleased(element);
    },
  };
});
vi.mock("../hooks/useKeyboardShortcuts", () => ({ useKeyboardShortcuts: vi.fn() }));
vi.mock("../hooks/useRemuxSeeking", () => ({
  useRemuxSeeking: () => ({ handleSeek: playerSeek }),
}));
vi.mock("../hooks/useSubtitleTracks", () => ({
  useSubtitleTracks: (...args: unknown[]) => {
    subtitleTimeline.textOffsetSeconds = args[3] as number;
    subtitleTimeline.liveCues = args[7] as Array<{ text: string }>;
    subtitleTimeline.liveKey = args[8] as string | null;
    subtitleTimeline.streamGeneration = args[9] as number;
    subtitleTimeline.setLoadState = args[10] as (state: string) => void;
    subtitleTimeline.cueRevision = args[11] as number;
    return [];
  },
}));
vi.mock("../hooks/useASSSubtitles", () => ({
  useASSSubtitles: (...args: unknown[]) => {
    subtitleTimeline.assOffsetSeconds = args[4] as number;
    subtitleTimeline.assCueRevision = args[9] as number;
    return { isActive: false };
  },
}));
// The delay is the profile's saved offset; plain state stands in for the query.
vi.mock("../hooks/useSubtitleDelay", async () => {
  const { useState } = await import("react");
  return { useSubtitleDelay: () => useState(0) };
});
vi.mock("../hooks/useSubtitleAppearance", () => ({
  useSubtitleAppearance: () => ({
    settings: { position: "bottom", fontSize: "large" },
    containerStyle: {},
    cueStyle: {},
  }),
}));
vi.mock("../hooks/useSubtitleLayout", () => ({
  useSubtitleLayout: () => ({ positionStyle: {}, fontScale: 1 }),
}));
vi.mock("hls.js", () => ({
  default: class MockHls {
    static Events = {
      ERROR: "error",
      MANIFEST_PARSED: "manifestParsed",
      BUFFER_APPENDED: "bufferAppended",
      FRAG_LOADING: "fragLoading",
      FRAG_LOADED: "fragLoaded",
      FRAG_BUFFERED: "fragBuffered",
      BUFFER_EOS: "bufferEos",
    };
    static ErrorTypes = { NETWORK_ERROR: "networkError", MEDIA_ERROR: "mediaError" };
    static ErrorDetails = {
      MANIFEST_LOAD_ERROR: "manifestLoadError",
      MANIFEST_LOAD_TIMEOUT: "manifestLoadTimeOut",
      MANIFEST_PARSING_ERROR: "manifestParsingError",
      FRAG_LOAD_ERROR: "fragLoadError",
    };
    static isSupported = () => hlsJS.supported;

    handlers = new Map<string, (event: string, data: unknown) => void>();
    startLoad = hlsJS.startLoad;
    stopLoad = hlsJS.stopLoad;
    loadSource = hlsJS.loadSource;

    bufferedToEnd = false;
    maxBufferLength: number;

    constructor(config?: { maxBufferLength?: number }) {
      hlsJS.constructed(config);
      hlsJS.latest = this;
      this.maxBufferLength = config?.maxBufferLength ?? 30;
    }

    get mainForwardBufferInfo() {
      return { len: hlsJS.forwardBufferSeconds };
    }

    on(event: string, handler: (event: string, data: unknown) => void) {
      this.handlers.set(event, handler);
    }
    emit(event: string, data: unknown) {
      this.handlers.get(event)?.(event, data);
    }
    attachMedia() {}
    destroy() {}
  },
}));
vi.mock("./PlayerControls", () => ({
  SKIP_BACK_SECONDS: 10,
  SKIP_FORWARD_SECONDS: 30,
  PlayerControls: vi.fn(
    (props: {
      currentTime: number;
      duration: number;
      onSeek: (seconds: number) => void;
      activeSubtitleIndex: number | null;
      subtitleTracks: PlayerSubtitleInfo[];
      visible: boolean;
      onSurfaceTap?: (event: React.MouseEvent<HTMLElement>) => void;
      isFullscreen?: boolean;
      onFullscreenToggle?: () => void;
      videoFit?: VideoFitMode;
      onVideoFitToggle?: () => void;
    }) => {
      controls.current = props;
      return null;
    },
  ),
}));

const playerConfig: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "test-device",
  getProfileToken: () => null,
};

function wrapper({ children }: { children: ReactNode }) {
  return createElement(PlayerConfigProvider, { config: playerConfig, children });
}

const directPlan = fixturePlanV3({
  delivery: "original_http",
  stream: {
    url: "/stream/session-1",
    protocol: "http_progressive",
    headers: {},
    header_refresh: "none",
  },
});

function playerProps(overrides: Partial<Parameters<typeof VideoPlayer>[0]> = {}) {
  return {
    title: "Test movie",
    streamUrl: "/api/v1/stream/session-1?token=token",
    plan: directPlan,
    planRevision: 1,
    sessionId: "session-1",
    activeFileId: 7,
    subtitleUrls: [] as PlayerSubtitleInfo[],
    initialPosition: 0,
    intro: null,
    credits: null,
    qualityPreference: "original",
    onExit: vi.fn(),
    seekIntervals: { back: 10, forward: 30 },
    ...overrides,
  };
}

function renderPlayer(overrides: Partial<Parameters<typeof VideoPlayer>[0]> = {}) {
  const props = playerProps(overrides);
  const rendered = render(createElement(VideoPlayer, props), { wrapper });
  return {
    ...rendered,
    rerenderPlayer(next: Partial<Parameters<typeof VideoPlayer>[0]>) {
      rendered.rerender(createElement(VideoPlayer, { ...props, ...next }));
    },
  };
}

/**
 * Fires the `timeupdate` of a source that has data for its position. jsdom
 * keeps readyState at HAVE_NOTHING, where the player ignores the event as the
 * one a transport teardown queues.
 */
function fireFrameTimeUpdate(video: HTMLVideoElement) {
  const own = Object.getOwnPropertyDescriptor(video, "readyState");
  Object.defineProperty(video, "readyState", {
    configurable: true,
    value: Math.max(video.readyState, HTMLMediaElement.HAVE_CURRENT_DATA),
  });
  fireEvent.timeUpdate(video);
  if (own) Object.defineProperty(video, "readyState", own);
  else Reflect.deleteProperty(video, "readyState");
}

function setMediaError(video: HTMLVideoElement, message: string) {
  Object.defineProperty(video, "error", {
    configurable: true,
    value: { code: 3, message },
  });
}

function roomConnection(
  overrides: Partial<WatchTogetherRoomConnectionResult> = {},
): WatchTogetherRoomConnectionResult {
  return {
    connectionState: "connected",
    room: {
      room_id: "room-1",
      phase: "playing",
      playback_state: "playing",
      selection_mode: "host_pick",
      selection_revision: 1,
      code: "ABC123",
      guest_control_policy: "host_only",
      is_paused: false,
      anchor_position_seconds: 100,
      anchor_updated_at: new Date().toISOString(),
      generation: 1,
      member_count: 2,
      host_connected: true,
      self_role: "guest",
      self_can_control_transport: false,
      self_can_manage_room: false,
      self_ignore_wait: false,
      attached_session_id: "session-1",
    },
    suggestions: [],
    closedReason: null,
    replacementReason: null,
    rejoinRoom: vi.fn(),
    transportCommand: null,
    serverTimeOffsetMs: 0,
    sendRoomMessage: vi.fn(() => ({ ok: true })),
    updatePolicy: vi.fn(async () => null),
    selectItem: vi.fn(async () => null),
    fallbackSource: vi.fn(async () => null),
    closeRoom: vi.fn(async () => {}),
    createSuggestion: vi.fn(async () => {}),
    deleteSuggestion: vi.fn(async () => {}),
    vote: vi.fn(async () => {}),
    unvote: vi.fn(async () => {}),
    promoteSuggestion: vi.fn(async () => null),
    stageItem: vi.fn(async () => null),
    startPlayback: vi.fn(async () => null),
    stopPlayback: vi.fn(async () => null),
    updateSelectionMode: vi.fn(async () => null),
    setLobbyReady: vi.fn(() => ({ ok: true })),
    ...overrides,
  };
}

function roomPlayCommand(overrides: Record<string, unknown> = {}) {
  return {
    command_id: "room-command-1",
    session_id: "session-1",
    selection_revision: 1,
    action: "play" as const,
    position_seconds: 100,
    execute_at: new Date().toISOString(),
    issued_at: new Date().toISOString(),
    playback_state: "playing" as const,
    ...overrides,
  };
}

/**
 * Makes jsdom's paused element report data ahead of the playhead, as a player
 * whose media has loaded does. A room play waits for this before it starts.
 */
function markReady(video: HTMLVideoElement) {
  Object.defineProperty(video, "readyState", {
    configurable: true,
    value: HTMLMediaElement.HAVE_FUTURE_DATA,
  });
}

describe("VideoPlayer room catch-up", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-01T12:00:00Z"));
    playerSeek.mockClear();
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  function setup(localPosition: number, timelineOffset = 0, canSeekAnywhere = false) {
    const connection = roomConnection();
    const onReanchorSeek = vi.fn(() => true);
    const rendered = renderPlayer({
      plan: fixturePlanV3({
        ...directPlan,
        delivery: "server_remux_progressive",
        timeline: {
          ...directPlan.timeline,
          timeline_offset_seconds: timelineOffset,
          can_seek_anywhere: canSeekAnywhere,
        },
      }),
      shouldAutoPlay: false,
      watchTogetherRoomId: "room-1",
      watchTogetherConnection: connection,
      onReanchorSeek,
    });
    const video = rendered.container.querySelector("video")!;
    video.currentTime = localPosition - timelineOffset;
    fireFrameTimeUpdate(video);
    markReady(video);
    const command = roomPlayCommand();
    // A room correction to `position` that executes now.
    const correct = async (commandId: string, position = 100) => {
      rendered.rerenderPlayer({
        watchTogetherConnection: {
          ...connection,
          transportCommand: {
            ...command,
            command_id: commandId,
            position_seconds: position,
            execute_at: new Date().toISOString(),
          },
        },
      });
      await act(() => vi.advanceTimersByTimeAsync(0));
    };
    return { ...rendered, connection, video, command, onReanchorSeek, correct };
  }

  it("starts a member whose media loads after the room's play from where the room is then", async () => {
    const { connection, video, command, rerenderPlayer } = setup(98.5, 80);
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    // Still loading when the play runs: nothing to position or start yet.
    Object.defineProperty(video, "readyState", {
      configurable: true,
      value: HTMLMediaElement.HAVE_NOTHING,
    });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        transportCommand: { ...command, execute_at: new Date(Date.now() - 1_000).toISOString() },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(play).not.toHaveBeenCalled();
    expect(playerSeek).not.toHaveBeenCalled();

    // A clock update re-runs the room effect for the same command meanwhile;
    // it must neither run the play again nor lose it.
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        serverTimeOffsetMs: 0.5,
        transportCommand: { ...command, execute_at: new Date(Date.now() - 1_000).toISOString() },
      },
    });

    // The media arrives 2 s later. The room is at 103 by then, 4.5 s ahead.
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(play).not.toHaveBeenCalled();
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 30 },
    });
    markReady(video);
    await act(async () => fireEvent.canPlay(video));

    expect(playerSeek).toHaveBeenCalledOnce();
    expect(playerSeek.mock.calls[0]?.[0]).toBeCloseTo(23, 2);
    expect(play).toHaveBeenCalledOnce();
    expect(video.playbackRate).toBe(1);
  });

  it("rebuilds a member's stream that fails before playing at the room's position", async () => {
    const { connection, video, command, rerenderPlayer } = setup(0);
    const onPlanFailure = vi.fn();
    Object.defineProperty(video, "readyState", {
      configurable: true,
      value: HTMLMediaElement.HAVE_NOTHING,
    });
    rerenderPlayer({
      onPlanFailure,
      watchTogetherConnection: { ...connection, transportCommand: command },
    });
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    setMediaError(video, "decode failed");
    fireEvent.error(video);

    expect(onPlanFailure).toHaveBeenCalledWith(
      expect.objectContaining({ classification: "decoder_error" }),
      103,
    );
  });

  it("sends no position reports while a room play waits for the member's media", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    const messages = vi.mocked(connection.sendRoomMessage);
    Object.defineProperty(video, "readyState", {
      configurable: true,
      value: HTMLMediaElement.HAVE_NOTHING,
    });
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    messages.mockClear();
    await act(() => vi.advanceTimersByTimeAsync(4_000));
    expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "state_report" }));
  });
});

describe("VideoPlayer plan failure recovery", () => {
  beforeEach(() => {
    realtimeOptions.current = null;
    controls.current = null;
    subtitleTimeline.textOffsetSeconds = null;
    subtitleTimeline.assOffsetSeconds = null;
    hlsJS.supported = false;
    hlsJS.constructed.mockClear();
    toastError.mockClear();
    playerSeek.mockClear();
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  // Deleting a stored subtitle moves the later ones down an ordinal. A pick
  // still waiting for its replan names the old ordinal, which the next plan
  // gives another track.
  it("moves a pending stored-subtitle pick to its ordinal in the next plan", async () => {
    const onSubtitleTrackChange = vi.fn();
    const stored = (index: number, id: number, language: string): PlayerSubtitleInfo => ({
      index,
      media_file_id: 7,
      track_id: `file:7:subtitle:${index}`,
      language,
      codec: "srt",
      label: language,
      source: "downloaded",
      stored_subtitle_id: id,
      sync_key: `stored-${id}`,
      url: `/stream/session-1/subtitles/${index}.vtt`,
    });
    const { rerenderPlayer } = renderPlayer({
      subtitleUrls: [stored(0, 71, "en"), stored(1, 72, "es")],
      onSubtitleTrackChange,
    });
    act(() =>
      (controls.current as unknown as { onSubtitleSelect: (i: number) => void }).onSubtitleSelect(
        1,
      ),
    );
    await waitFor(() =>
      expect(onSubtitleTrackChange).toHaveBeenCalledWith(1, expect.any(Number), 72),
    );
    const callsBefore = onSubtitleTrackChange.mock.calls.length;

    // English was deleted and French added before the pick's replan ran.
    rerenderPlayer({
      plan: fixturePlanV3({ ...directPlan, plan_id: "plan:after-delete" }),
      planRevision: 2,
      subtitleUrls: [stored(0, 72, "es"), stored(1, 73, "fr")],
    });

    await waitFor(() => expect(controls.current?.activeSubtitleIndex).toBe(0));
    await act(async () => Promise.resolve());
    const sentAfter = onSubtitleTrackChange.mock.calls
      .slice(callsBefore)
      .map(([index, , storedId]) => [index, storedId]);
    expect(sentAfter).toEqual([[0, 72]]);
  });

  // A finished translation is selected at the ordinal the server gave it,
  // while the track list may still show another stored subtitle there until
  // the refetch lands. The pick keeps the translation's identity.
  it("keeps a translation handed off at a server ordinal the track list still gives another track", async () => {
    const onSubtitleTrackChange = vi.fn();
    const stored = (index: number, id: number, language: string): PlayerSubtitleInfo => ({
      index,
      media_file_id: 7,
      track_id: `file:7:subtitle:${index}`,
      language,
      codec: "srt",
      label: language,
      source: "downloaded",
      stored_subtitle_id: id,
      sync_key: `stored-${id}`,
      url: `/stream/session-1/subtitles/${index}.vtt`,
    });
    const { rerenderPlayer } = renderPlayer({
      subtitleUrls: [stored(0, 71, "en"), stored(1, 72, "es")],
      onSubtitleTrackChange,
      onApplySubtitleTrack: vi.fn(),
    });
    const base = { session_id: "session-1", file_id: 7, job_id: 1, track_key: "ai-1" };
    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_started",
        payload: { ...base, language: "hr", total_cues: 2 },
      });
    });
    // English was deleted, so Spanish is at 0 on the server and the
    // translation (stored 73) takes 1.
    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_completed",
        payload: {
          ...base,
          subtitle_id: 73,
          language: "hr",
          track: fixtureSubtitleInventoryItemV3({
            combined_index: 1,
            source: "downloaded",
            language: "hr",
            stored_subtitle_id: 73,
          }),
        },
      });
    });
    await waitFor(() =>
      expect(onSubtitleTrackChange).toHaveBeenLastCalledWith(1, expect.any(Number), 73),
    );

    rerenderPlayer({
      plan: fixturePlanV3({ ...directPlan, plan_id: "plan:refetched" }),
      planRevision: 2,
      subtitleUrls: [stored(0, 72, "es"), stored(1, 73, "hr")],
    });
    await act(async () => Promise.resolve());
    expect(controls.current?.activeSubtitleIndex).toBe(1);
    expect(onSubtitleTrackChange).toHaveBeenLastCalledWith(1, expect.any(Number), 73);
  });
});

describe("VideoPlayer source timeline", () => {
  beforeEach(() => {
    realtimeOptions.current = null;
    controls.current = null;
    subtitleTimeline.textOffsetSeconds = null;
    subtitleTimeline.assOffsetSeconds = null;
    hlsJS.supported = false;
    hlsJS.constructed.mockClear();
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === "application/vnd.apple.mpegurl" ? "probably" : "",
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  describe("a seek made while a quality change is fetched", () => {
    // The plan starts the new stream where the quality change was asked for.
    const plan = fixturePlanV3({
      delivery: "server_transcode_hls",
      stream: {
        url: "/playback/transcode/session-1/master.m3u8",
        protocol: "hls",
        headers: {},
        header_refresh: "none",
      },
      timeline: {
        source_start_seconds: 120,
        player_start_seconds: 120,
        stream_origin_seconds: 0,
        timeline_offset_seconds: 0,
        can_seek_anywhere: true,
        seek_restoration: "player_position",
      },
    });

    // Plays the first transport, asks for a quality change, and seeks back
    // to 30 s while the replan is in flight. The old stream completes the seek.
    async function seekDuringReplan(overrides: Partial<Parameters<typeof VideoPlayer>[0]> = {}) {
      const rendered = renderPlayer({ plan, ...overrides });
      const video = rendered.container.querySelector("video")!;
      await waitFor(() =>
        expect(video.src !== "" || hlsJS.constructed.mock.calls.length > 0).toBe(true),
      );
      fireEvent.loadedMetadata(video);
      fireFrameTimeUpdate(video);
      fireEvent.playing(video);
      rendered.rerenderPlayer({ plan, replanning: true });
      act(() => {
        controls.current!.onSeek(30);
      });
      expect(video.currentTime).toBe(30);
      fireEvent.seeked(video);
      fireFrameTimeUpdate(video);
      return { ...rendered, video };
    }

    function newStream(rerenderPlayer: (next: Partial<Parameters<typeof VideoPlayer>[0]>) => void) {
      rerenderPlayer({
        plan,
        replanning: false,
        planRevision: 2,
        streamUrl: "/api/v1/stream/session-1?revision=2",
      });
    }

    it("starts the native HLS stream at the seek", async () => {
      const { video, rerenderPlayer } = await seekDuringReplan();

      newStream(rerenderPlayer);
      await waitFor(() => expect(video.src).toContain("revision=2"));
      fireEvent.loadedMetadata(video);

      expect(video.currentTime).toBe(30);
    });

    // An original file whose metadata has not loaded: the seek waits for the
    // metadata handler instead of reaching the element.
    const originalPlan = fixturePlanV3({
      delivery: "original_http",
      stream: {
        url: "/stream/session-1",
        protocol: "http_progressive",
        headers: {},
        header_refresh: "none",
      },
      timeline: { ...plan.timeline },
    });
    async function seekBeforeMetadata(overrides: Partial<Parameters<typeof VideoPlayer>[0]> = {}) {
      const rendered = renderPlayer({ plan: originalPlan, ...overrides });
      const video = rendered.container.querySelector("video")!;
      await waitFor(() => expect(video.src).toContain("/stream/session-1"));
      rendered.rerenderPlayer({ plan: originalPlan, replanning: true });
      act(() => {
        controls.current!.onSeek(30);
      });
      return rendered;
    }

    it.each([
      ["a playing stream", seekDuringReplan],
      ["an original file whose metadata has not loaded", seekBeforeMetadata],
    ])(
      "after a seek on %s, starts the new stream and a queued or later quality change at it",
      async (_, seek) => {
        hlsJS.supported = true;
        const onSeek = vi.fn();
        const onQualitySelect = vi.fn();
        const { rerenderPlayer } = await seek({ onSeek });
        // The session moves a change queued behind the replan to the seek.
        expect(onSeek).toHaveBeenCalledWith(30);
        hlsJS.constructed.mockClear();

        rerenderPlayer({
          plan,
          replanning: false,
          planRevision: 2,
          streamUrl: "/api/v1/stream/session-1?revision=2",
          onQualitySelect,
        });
        await waitFor(() => expect(hlsJS.constructed).toHaveBeenCalledOnce());
        expect(hlsJS.constructed).toHaveBeenCalledWith(
          expect.objectContaining({ startPosition: 30 }),
        );

        const selectQuality = (
          controls.current as unknown as { onQualitySelect: (id: string) => void }
        ).onQualitySelect;
        act(() => selectQuality("480p"));

        expect(onQualitySelect).toHaveBeenCalledWith("480p", 30, undefined, expect.any(Function));
      },
    );

    it("forgets the seek when the replan settles without a new stream", async () => {
      hlsJS.supported = true;
      const { rerenderPlayer } = await seekDuringReplan();
      rerenderPlayer({ plan, replanning: false, replanError: "Replan refused." });
      hlsJS.constructed.mockClear();

      // A later quality change starts where it was asked for.
      rerenderPlayer({ plan, replanning: true });
      newStream(rerenderPlayer);

      await waitFor(() => expect(hlsJS.constructed).toHaveBeenCalledOnce());
      expect(hlsJS.constructed).toHaveBeenCalledWith(
        expect.objectContaining({ startPosition: 120 }),
      );
    });
  });
});

describe("VideoPlayer translation handoff", () => {
  beforeEach(() => {
    toastError.mockClear();
    playerV2Mock.mockReset().mockResolvedValue({ job: { status: "running" } });
    realtimeOptions.current = null;
    controls.current = null;
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("drops a reused track still waiting for the inventory when a newer job is accepted", async () => {
    const onRefreshSubtitles = vi.fn();
    playerV2Mock.mockResolvedValue({ job: { status: "running" } });
    const { rerenderPlayer } = renderPlayer({ subtitleUrls: [], onRefreshSubtitles });

    await act(async () => {
      controls.current?.onSubtitleJobAccepted?.("13", "44");
    });
    await waitFor(() => expect(onRefreshSubtitles).toHaveBeenCalledOnce());
    await act(async () => {
      controls.current?.onSubtitleJobAccepted?.("14");
    });

    rerenderPlayer({
      onRefreshSubtitles,
      subtitleUrls: [
        {
          index: 4,
          language: "es",
          label: "English → Spanish (AI)",
          source: "downloaded",
          codec: "srt",
          sync_key: "stored-44",
          url: "/es.vtt",
        },
      ],
    });
    await act(async () => {});
    expect(controls.current?.activeSubtitleIndex).not.toBe(4);
  });

  it("keeps a reused track when an earlier live translation finishes later", async () => {
    const german: PlayerSubtitleInfo = {
      index: 2,
      language: "de",
      label: "English → German (AI)",
      source: "downloaded",
      codec: "srt",
      sync_key: "stored-31",
      url: "/de.vtt",
    };
    const onRefreshSubtitles = vi.fn();
    renderPlayer({ subtitleUrls: [german], onRefreshSubtitles });
    const base = { session_id: "session-1", file_id: 7, job_id: 1, track_key: "ai-1" };
    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_started",
        payload: { ...base, language: "hr", total_cues: 2 },
      });
    });

    await act(async () => {
      controls.current?.onSubtitleJobAccepted?.("12", "31");
    });
    await waitFor(() => expect(controls.current?.activeSubtitleIndex).toBe(2));
    expect(subtitleTimeline.liveKey).toBeNull();

    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_completed",
        payload: { ...base, subtitle_id: 44, language: "hr" },
      });
    });
    expect(controls.current?.activeSubtitleIndex).toBe(2);
    expect(onRefreshSubtitles).not.toHaveBeenCalled();
  });
});

describe("VideoPlayer lost connection", () => {
  beforeEach(() => {
    realtimeOptions.current = null;
    controls.current = null;
    hlsJS.supported = false;
    hlsJS.constructed.mockClear();
    hlsJS.startLoad.mockClear();
    hlsJS.stopLoad.mockClear();
    hlsJS.loadSource.mockClear();
    hlsJS.latest = null;
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  function setNetworkError(video: HTMLVideoElement) {
    Object.defineProperty(video, "error", {
      configurable: true,
      value: { code: 2, message: "PIPELINE_ERROR_NETWORK" },
    });
  }

  it("hands a network error on a transport settled paused on its first frame to the reconnect", () => {
    const onConnectionLost = vi.fn();
    const onPlanFailure = vi.fn();
    const { container } = renderPlayer({ onConnectionLost, onPlanFailure, shouldAutoPlay: false });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    markReady(video);
    fireEvent.canPlay(video);

    // Chromium refetches the paused progressive stream and the server refuses
    // the range: the route works, so the stream reopens, still paused.
    setNetworkError(video);
    fireEvent.error(video);

    expect(onConnectionLost).toHaveBeenCalledExactlyOnceWith(expect.any(Number), false);
    expect(onPlanFailure).not.toHaveBeenCalled();
  });

  it("loads the manifest again after a fatal manifest error, then reports the route failed", async () => {
    hlsJS.supported = true;
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    const onPlanFailure = vi.fn();
    const url = "/playback/transcode/session-1/master.m3u8";
    const plan = fixturePlanV3({
      stream: { url, protocol: "hls", headers: {}, header_refresh: "none" },
    });
    renderPlayer({ plan, onPlanFailure });
    await waitFor(() => expect(hlsJS.latest).not.toBeNull());
    expect(hlsJS.loadSource).toHaveBeenCalledOnce();

    // hls.js does not request a failed manifest again on startLoad(), so the
    // recovery has to load the source again for the second failure to come.
    const fatalManifestError = { fatal: true, type: "networkError", details: "manifestLoadError" };
    vi.spyOn(Date, "now").mockReturnValue(10_000);
    act(() => hlsJS.latest?.emit("error", fatalManifestError));
    expect(hlsJS.loadSource).toHaveBeenCalledTimes(2);
    expect(hlsJS.loadSource.mock.calls[1]).toEqual(hlsJS.loadSource.mock.calls[0]);
    expect(hlsJS.startLoad).not.toHaveBeenCalled();
    expect(onPlanFailure).not.toHaveBeenCalled();

    vi.spyOn(Date, "now").mockReturnValue(20_000);
    act(() => hlsJS.latest?.emit("error", fatalManifestError));
    expect(onPlanFailure).toHaveBeenCalledOnce();
    expect(onPlanFailure.mock.calls[0]?.[0]).toMatchObject({ classification: "startup_timeout" });
  });

  it("reports the route failed when the reloaded manifest fails within the recovery cooldown", async () => {
    hlsJS.supported = true;
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    const onPlanFailure = vi.fn();
    const url = "/playback/transcode/session-1/master.m3u8";
    const plan = fixturePlanV3({
      stream: { url, protocol: "hls", headers: {}, header_refresh: "none" },
    });
    renderPlayer({ plan, onPlanFailure });
    await waitFor(() => expect(hlsJS.latest).not.toBeNull());

    const fatalManifestError = {
      fatal: true,
      type: "networkError",
      details: "manifestParsingError",
    };
    vi.spyOn(Date, "now").mockReturnValue(10_000);
    act(() => hlsJS.latest?.emit("error", fatalManifestError));
    expect(hlsJS.loadSource).toHaveBeenCalledTimes(2);

    vi.spyOn(Date, "now").mockReturnValue(10_500);
    act(() => hlsJS.latest?.emit("error", fatalManifestError));
    expect(hlsJS.loadSource).toHaveBeenCalledTimes(2);
    expect(onPlanFailure).toHaveBeenCalledOnce();
    expect(onPlanFailure.mock.calls[0]?.[0]).toMatchObject({ classification: "startup_timeout" });
  });
});
