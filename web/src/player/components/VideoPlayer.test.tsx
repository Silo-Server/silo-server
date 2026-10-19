import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFullscreenRootContext } from "../context/PlayerFullscreenContext";
import { useKeyboardShortcuts } from "../hooks/useKeyboardShortcuts";
import type { WatchTogetherRoomConnectionResult } from "../hooks/useWatchTogetherRoomConnection";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import type {
  PlaybackRealtimeCommandEnvelope,
  PlaybackRealtimeEventEnvelope,
} from "../realtime-protocol";
import type { PlayerSubtitleInfo, VideoFitMode } from "../types";
import { HLS_STARTUP_TIMEOUT_MS } from "../utils/hlsStartupGuard";
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
    onSubtitleJobAccepted?: (jobId: string) => void;
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
  latest: null as null | { emit: (event: string, data: unknown) => void },
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
const attachBrowserEngine = vi.hoisted(() => vi.fn(() => ({ destroy: vi.fn() })));
vi.mock("../engine", () => ({ attachBrowserEngine }));
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
    };
    static ErrorTypes = { NETWORK_ERROR: "networkError", MEDIA_ERROR: "mediaError" };
    static isSupported = () => hlsJS.supported;

    handlers = new Map<string, (event: string, data: unknown) => void>();
    startLoad = hlsJS.startLoad;
    stopLoad = hlsJS.stopLoad;

    constructor(config?: unknown) {
      hlsJS.constructed(config);
      hlsJS.latest = this;
    }

    on(event: string, handler: (event: string, data: unknown) => void) {
      this.handlers.set(event, handler);
    }
    emit(event: string, data: unknown) {
      this.handlers.get(event)?.(event, data);
    }
    loadSource() {}
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

function planInvalidatedCommand(
  payload: Record<string, unknown> = {
    reason: "video_copy_unsafe",
    plan_id: directPlan.plan_id,
  },
): PlaybackRealtimeCommandEnvelope {
  return {
    type: "command",
    command_id: "cmd-invalidate-1",
    session_id: "session-1",
    name: "plan_invalidated",
    deadline_ms: 8_000,
    payload,
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

const reconnectingMessage = "Reconnecting to room. Controls are temporarily unavailable.";

/** Makes jsdom's element report playback with data ahead of the playhead. */
function markPlaying(video: HTMLVideoElement) {
  Object.defineProperty(video, "paused", { configurable: true, value: false });
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
    const command = {
      command_id: "room-command-1",
      session_id: "session-1",
      selection_revision: 1,
      action: "play" as const,
      position_seconds: 100,
      execute_at: new Date().toISOString(),
      issued_at: new Date().toISOString(),
      playback_state: "playing" as const,
    };
    return { ...rendered, connection, video, command, onReanchorSeek };
  }

  it("pauses displaced playback and offers an explicit room rejoin", async () => {
    const { connection, video, rerenderPlayer } = setup(100);
    vi.mocked(video.pause).mockClear();
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        connectionState: "disconnected",
        replacementReason: "This profile joined the Watch Party on another device.",
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(video.pause).toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "This profile joined the Watch Party on another device.",
    );
    expect(
      screen.queryByText("Reconnecting to room. Controls are temporarily unavailable."),
    ).toBeNull();
    expect(connection.closeRoom).not.toHaveBeenCalled();
    expect(connection.rejoinRoom).not.toHaveBeenCalled();
    // Native media controls and delayed translation resumes also obey the stop.
    vi.mocked(video.pause).mockClear();
    fireEvent.play(video);
    expect(video.pause).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Rejoin Watch Party" }));
    expect(connection.rejoinRoom).toHaveBeenCalledOnce();
  });

  it("does not warn about a room socket that reconnects quickly", async () => {
    const { connection, rerenderPlayer } = setup(100);
    rerenderPlayer({ watchTogetherConnection: { ...connection, connectionState: "disconnected" } });
    await act(() => vi.advanceTimersByTimeAsync(500));
    rerenderPlayer({ watchTogetherConnection: { ...connection, connectionState: "connecting" } });
    await act(() => vi.advanceTimersByTimeAsync(500));
    rerenderPlayer({ watchTogetherConnection: connection });
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(screen.queryByText(reconnectingMessage)).toBeNull();
  });

  it("warns during a sustained room outage and clears the warning on reconnect", async () => {
    const { connection, rerenderPlayer } = setup(100);
    for (let outage = 0; outage < 2; outage++) {
      rerenderPlayer({
        watchTogetherConnection: { ...connection, connectionState: "disconnected" },
      });
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      // Backoff moves between disconnected and connecting without restarting the delay.
      rerenderPlayer({ watchTogetherConnection: { ...connection, connectionState: "connecting" } });
      await act(() => vi.advanceTimersByTimeAsync(999));
      expect(screen.queryByText(reconnectingMessage)).toBeNull();
      await act(() => vi.advanceTimersByTimeAsync(1));
      expect(screen.getByText(reconnectingMessage)).toBeInTheDocument();
      // The warning outlasts the usual notice lifetime while the outage lasts.
      await act(() => vi.advanceTimersByTimeAsync(30_000));
      expect(screen.getByText(reconnectingMessage)).toBeInTheDocument();
      rerenderPlayer({ watchTogetherConnection: connection });
      expect(screen.queryByText(reconnectingMessage)).toBeNull();
      await act(() => vi.advanceTimersByTimeAsync(60_000));
    }
  });

  it("lets the reconnect warning expire once the room has closed", async () => {
    const { connection, rerenderPlayer } = setup(100);
    const disconnected = { ...connection, connectionState: "disconnected" as const };
    rerenderPlayer({ watchTogetherConnection: disconnected });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(screen.getByText(reconnectingMessage)).toBeInTheDocument();
    rerenderPlayer({ watchTogetherConnection: { ...disconnected, closedReason: "ended" } });
    await act(() => vi.advanceTimersByTimeAsync(8_000));
    expect(screen.queryByText(reconnectingMessage)).toBeNull();
  });

  it("shows the reconnect warning after a notice raised during the delay", async () => {
    const { connection, rerenderPlayer } = setup(100);
    rerenderPlayer({ watchTogetherConnection: { ...connection, connectionState: "disconnected" } });
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    const onCommand = realtimeOptions.current?.onCommand;
    if (!onCommand) throw new Error("expected the realtime command handler");
    await act(async () => {
      await onCommand({
        type: "command",
        command_id: "cmd-message-1",
        session_id: "session-1",
        name: "display_message",
        deadline_ms: 8_000,
        payload: { title: "Admin", message: "Server maintenance at midnight." },
      });
    });
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(screen.getByText("Server maintenance at midnight.")).toBeInTheDocument();
    expect(screen.queryByText(reconnectingMessage)).toBeNull();
    // The outage outlasts the message, so the warning takes its place.
    await act(() => vi.advanceTimersByTimeAsync(7_000));
    expect(screen.queryByText("Server maintenance at midnight.")).toBeNull();
    expect(screen.getByText(reconnectingMessage)).toBeInTheDocument();
  });

  it("does not extend an admin notice through a routine room reconnect", async () => {
    const { connection, rerenderPlayer } = setup(100);
    const onCommand = realtimeOptions.current?.onCommand;
    if (!onCommand) throw new Error("expected the realtime command handler");
    await act(async () => {
      await onCommand({
        type: "command",
        command_id: "cmd-message-brief-reconnect",
        session_id: "session-1",
        name: "display_message",
        deadline_ms: 8_000,
        payload: { title: "Admin", message: "Server maintenance at midnight." },
      });
    });

    await act(() => vi.advanceTimersByTimeAsync(7_500));
    rerenderPlayer({ watchTogetherConnection: { ...connection, connectionState: "disconnected" } });
    await act(() => vi.advanceTimersByTimeAsync(499));
    rerenderPlayer({ watchTogetherConnection: connection });
    expect(screen.getByText("Server maintenance at midnight.")).toBeInTheDocument();
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(screen.queryByText("Server maintenance at midnight.")).toBeNull();
    expect(screen.queryByText(reconnectingMessage)).toBeNull();
  });

  it("shows a repeated notice again after the previous one expired", async () => {
    setup(100);
    for (let attempt = 0; attempt < 2; attempt++) {
      act(() => controls.current!.onSeek(50));
      expect(screen.getByText("Only the host can seek the room.")).toBeInTheDocument();
      await act(() => vi.advanceTimersByTimeAsync(8_000));
      expect(screen.queryByText("Only the host can seek the room.")).toBeNull();
    }
  });

  it("keeps a notice raised while minimized until the player is shown again", async () => {
    const { rerenderPlayer } = setup(100);
    rerenderPlayer({ displayMode: "detached" });
    act(() => controls.current!.onSeek(50));
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    rerenderPlayer({ displayMode: "foreground" });
    expect(screen.getByText("Only the host can seek the room.")).toBeInTheDocument();
    await act(() => vi.advanceTimersByTimeAsync(8_000));
    expect(screen.queryByText("Only the host can seek the room.")).toBeNull();
  });

  it("keeps displaced playback stopped on a late lobby read and leaves through the hub", async () => {
    const { connection, rerenderPlayer } = setup(100);
    const onExit = vi.fn();
    rerenderPlayer({
      onExit,
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, phase: "lobby" },
        connectionState: "disconnected",
        replacementReason: "This profile joined the Watch Party on another device.",
      },
    });
    expect(onExit).not.toHaveBeenCalled();
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: "Leave Watch Party" })),
    );
    expect(onExit).toHaveBeenCalledWith(expect.objectContaining({ destinationHref: "/rooms" }));
    expect(connection.closeRoom).not.toHaveBeenCalled();
    expect(connection.rejoinRoom).not.toHaveBeenCalled();
  });

  it.each([
    { at: "at the end of the item", position: 3598, notice: "Playback finished." },
    { at: "mid-item", position: 1200, notice: "The host stopped playback." },
  ])("says why the room returned to the lobby $at", async ({ position, notice }) => {
    const { connection, rerenderPlayer } = setup(position);
    const onExit = vi.fn();
    rerenderPlayer({
      onExit,
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, phase: "lobby", playback_state: "idle" },
      },
    });
    expect(screen.getByText(notice)).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(onExit).toHaveBeenCalledTimes(1);
  });

  it.each(["canplay", "retry", "pending play"])(
    "prevents late autoplay after replacement during %s without reloading the stream",
    async (stage) => {
      const connection = roomConnection();
      const play = vi.mocked(HTMLMediaElement.prototype.play);
      let finishPlay!: () => void;
      if (stage === "pending play")
        play.mockImplementationOnce(
          () =>
            new Promise<void>((resolve) => {
              finishPlay = resolve;
            }),
        );
      if (stage === "retry") play.mockRejectedValueOnce(new Error("play interrupted"));
      const { container, rerenderPlayer } = renderPlayer({
        shouldAutoPlay: true,
        watchTogetherRoomId: "room-1",
        watchTogetherConnection: connection,
      });
      const video = container.querySelector("video")!;
      if (stage !== "canplay") {
        Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
        await act(async () => fireEvent.canPlay(video));
        expect(play).toHaveBeenCalledOnce();
      }
      const callsBeforeReplacement = play.mock.calls.length;
      vi.mocked(video.load).mockClear();
      rerenderPlayer({
        shouldAutoPlay: true,
        watchTogetherConnection: {
          ...connection,
          connectionState: "disconnected",
          replacementReason: "This profile joined the Watch Party on another device.",
        },
      });
      vi.mocked(video.pause).mockClear();
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      if (stage === "pending play") {
        await act(async () => finishPlay());
        expect(video.pause).toHaveBeenCalledOnce();
      }
      fireEvent.canPlay(video);
      fireEvent.loadedData(video);
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      expect(play).toHaveBeenCalledTimes(callsBeforeReplacement);
      expect(video.load).not.toHaveBeenCalled();
    },
  );

  it.each([1500, 30])("shows a requested room seek to %ss before the command arrives", (target) => {
    const { connection, video, rerenderPlayer, onReanchorSeek } = setup(100);
    connection.room = { ...connection.room!, self_can_manage_room: true };
    rerenderPlayer({ watchTogetherConnection: connection });

    act(() => controls.current!.onSeek(target));

    expect(connection.sendRoomMessage).toHaveBeenCalledWith({
      type: "transport_request",
      action: "seek",
      position_seconds: target,
      is_paused: true,
    });
    expect(controls.current!.currentTime).toBe(target);
    expect(onReanchorSeek).not.toHaveBeenCalled();
    expect(playerSeek).not.toHaveBeenCalled();

    video.currentTime = 100.2;
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(target);

    video.currentTime = target + 0.5;
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(target + 0.5);

    video.currentTime = target + 2;
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(target + 2);
  });

  it.each([1500, 30])("holds a room seek to %ss through stale seeked events", async (target) => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(100, 80);
    const commandedConnection = {
      ...connection,
      room: { ...connection.room!, playback_state: "waiting" as const },
      transportCommand: { ...command, action: "seek" as const, position_seconds: target },
    };
    rerenderPlayer({
      watchTogetherConnection: commandedConnection,
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(onReanchorSeek).toHaveBeenCalledWith(target);
    expect(controls.current!.currentTime).toBe(target);

    fireEvent.seeked(video);
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(target);
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready" }),
    );

    // The replacement stream starts at native time zero at the requested
    // media position, including a backward seek before the old stream origin.
    rerenderPlayer({
      watchTogetherConnection: commandedConnection,
      planRevision: 2,
      plan: fixturePlanV3({
        ...directPlan,
        delivery: "server_remux_progressive",
        timeline: {
          ...directPlan.timeline,
          source_start_seconds: target,
          stream_origin_seconds: target,
          timeline_offset_seconds: target,
          player_start_seconds: 0,
          can_seek_anywhere: false,
        },
      }),
    });
    expect(video.currentTime).toBe(0);
    fireEvent.seeked(video);
    expect(controls.current!.currentTime).toBe(target);
    video.currentTime += 0.5;
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(target + 0.5);
  });

  it("keeps the newest skip held while the room's seeks for earlier skips land", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100, 0, true);
    const host = {
      ...connection,
      room: { ...connection.room!, self_can_manage_room: true, self_can_control_transport: true },
    };
    rerenderPlayer({ watchTogetherConnection: host });
    const requested = () =>
      vi
        .mocked(host.sendRoomMessage)
        .mock.calls.map(([message]) => message as Record<string, unknown>)
        .filter((message) => message.type === "transport_request")
        .map((message) => message.position_seconds);
    const roomSeek = async (id: string, target: number) => {
      rerenderPlayer({
        watchTogetherConnection: {
          ...host,
          room: { ...host.room, playback_state: "waiting" as const },
          transportCommand: {
            ...command,
            command_id: id,
            action: "seek" as const,
            position_seconds: target,
            playback_state: "waiting" as const,
          },
        },
      });
      await act(() => vi.advanceTimersByTimeAsync(0));
    };

    act(() => controls.current!.onSkip!.forward());
    act(() => controls.current!.onSkip!.forward());
    expect(requested()).toEqual([130, 160]);

    // The room's seek for the first skip lands; the second is still held.
    await roomSeek("seek-130", 130);
    video.currentTime = 130;
    fireEvent.seeked(video);
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(160);
    act(() => controls.current!.onSkip!.forward());
    expect(requested()).toEqual([130, 160, 190]);

    // The room's seek to the held request releases it.
    await roomSeek("seek-190", 190);
    video.currentTime = 190;
    fireEvent.seeked(video);
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(190);

    // A request the room never runs stops holding the timeline.
    act(() => controls.current!.onSkip!.forward());
    expect(controls.current!.currentTime).toBe(220);
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(190);
  });

  it("releases a held skip when a newer command replaces its room seek", async () => {
    const { connection, command, rerenderPlayer } = setup(100, 0, true);
    const host = {
      ...connection,
      room: { ...connection.room!, self_can_manage_room: true, self_can_control_transport: true },
    };
    rerenderPlayer({ watchTogetherConnection: host });
    act(() => controls.current!.onSkip!.forward());
    expect(controls.current!.currentTime).toBe(130);

    const at = (ms: number) => new Date(Date.now() + ms).toISOString();
    rerenderPlayer({
      watchTogetherConnection: {
        ...host,
        room: { ...host.room, playback_state: "waiting" as const },
        transportCommand: {
          ...command,
          command_id: "seek-130",
          action: "seek" as const,
          position_seconds: 130,
          playback_state: "waiting" as const,
          execute_at: at(500),
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(100));
    // A pause replaces the seek before it runs; the element stays at 100 s.
    rerenderPlayer({
      watchTogetherConnection: {
        ...host,
        room: { ...host.room, playback_state: "paused" as const, is_paused: true },
        transportCommand: {
          ...command,
          command_id: "pause",
          action: "pause" as const,
          position_seconds: 100,
          playback_state: "paused" as const,
          execute_at: at(200),
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(300));
    expect(controls.current!.currentTime).toBe(100);
    act(() => controls.current!.onSkip!.forward());
    expect(controls.current!.currentTime).toBe(130);
  });

  it("releases a held skip when the room selects something else", () => {
    const { connection, rerenderPlayer } = setup(100, 0, true);
    const host = {
      ...connection,
      room: { ...connection.room!, self_can_manage_room: true, self_can_control_transport: true },
    };
    rerenderPlayer({ watchTogetherConnection: host });
    act(() => controls.current!.onSkip!.forward());
    expect(controls.current!.currentTime).toBe(130);

    rerenderPlayer({
      watchTogetherConnection: {
        ...host,
        room: { ...host.room, selection_revision: (host.room.selection_revision ?? 0) + 1 },
      },
    });
    expect(controls.current!.currentTime).toBe(100);
  });

  it("keeps the actual position when a room seek cannot be sent", () => {
    const { connection, rerenderPlayer } = setup(100);
    connection.room = { ...connection.room!, self_can_manage_room: true };
    vi.mocked(connection.sendRoomMessage).mockReturnValue({ ok: false });
    rerenderPlayer({ watchTogetherConnection: connection });

    act(() => controls.current!.onSeek(1500));
    expect(controls.current!.currentTime).toBe(100);
  });

  it("keeps brief buffering local and reports a sustained stall once", async () => {
    const { connection, video } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
    fireEvent.waiting(video);
    // Stalls inside the catch-up band converge by rate without the room.
    await act(() => vi.advanceTimersByTimeAsync(1_500));
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "buffering" }),
    );
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    fireEvent.canPlay(video);
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "buffering" }),
    );
    Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
    fireEvent.waiting(video);
    fireEvent.stalled(video);
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(
      vi
        .mocked(connection.sendRoomMessage)
        .mock.calls.filter(([message]) => message.type === "buffering"),
    ).toHaveLength(1);
    fireEvent.waiting(video);
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(
      vi
        .mocked(connection.sendRoomMessage)
        .mock.calls.filter(([message]) => message.type === "buffering"),
    ).toHaveLength(1);
  });

  it("reports one continuous stall once after the room marks this viewer buffering", async () => {
    const { connection, video, rerenderPlayer } = setup(100);
    const self = {
      user_id: 8,
      profile_id: "guest",
      display_name: "Me",
      is_host: false,
      is_self: true,
      connected: true,
    };
    const withSelf = (member: Record<string, boolean>, ignoreWait = false) => ({
      ...connection,
      room: {
        ...connection.room!,
        self_ignore_wait: ignoreWait,
        members: [{ ...self, ...member }],
      },
    });
    rerenderPlayer({ watchTogetherConnection: withSelf({ is_ready: true }) });
    Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
    fireEvent.waiting(video);
    await act(() => vi.advanceTimersByTimeAsync(2_000));

    // The room kept playing and now shows this viewer buffering.
    rerenderPlayer({ watchTogetherConnection: withSelf({ is_buffering: true }, true) });
    // The browser reports the same outage again.
    fireEvent.stalled(video);
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(
      vi
        .mocked(connection.sendRoomMessage)
        .mock.calls.filter(([message]) => message.type === "buffering"),
    ).toHaveLength(1);

    // After recovery, a new outage is a new stall.
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    fireEvent.canPlay(video);
    rerenderPlayer({ watchTogetherConnection: withSelf({ is_ready: true }) });
    Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
    fireEvent.waiting(video);
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(
      vi
        .mocked(connection.sendRoomMessage)
        .mock.calls.filter(([message]) => message.type === "buffering"),
    ).toHaveLength(2);
  });

  it("cancels a pending buffering report when the room disconnects", async () => {
    const { connection, video, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
    fireEvent.waiting(video);
    rerenderPlayer({ watchTogetherConnection: { ...connection, connectionState: "disconnected" } });
    await act(() => vi.advanceTimersByTimeAsync(2_100));
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "buffering" }),
    );
  });

  it.each(["paused", "lobby"] as const)(
    "cancels delayed buffering when the room becomes %s",
    async (state) => {
      const { connection, video, rerenderPlayer } = setup(100);
      Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
      fireEvent.waiting(video);
      const room = { ...connection.room! };
      if (state === "paused") room.playback_state = "paused";
      else room.phase = "lobby";
      rerenderPlayer({ watchTogetherConnection: { ...connection, room } });
      fireEvent.waiting(video);
      await act(() => vi.advanceTimersByTimeAsync(2_100));
      expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
        expect.objectContaining({ type: "buffering" }),
      );
      expect(screen.queryByLabelText("Syncing playback")).not.toBeInTheDocument();
    },
  );

  it("names the viewers still syncing", () => {
    const { connection, rerenderPlayer } = setup(100);
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: {
          ...connection.room!,
          playback_state: "waiting",
          members: [
            {
              user_id: 8,
              profile_id: "guest",
              display_name: "Alex",
              is_host: false,
              is_self: false,
              connected: true,
              is_syncing: true,
            },
          ],
        },
      },
    });
    expect(screen.getByText("Waiting for Alex")).toBeInTheDocument();
  });

  it.each([
    [1503, true],
    [1497, false],
  ])(
    "reports readiness for a seek that landed at %s only when pre-roll cannot reach the target",
    async (landed, reports) => {
      const { connection, video, command, rerenderPlayer } = setup(100);
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      rerenderPlayer({
        watchTogetherConnection: {
          ...connection,
          room: { ...connection.room!, playback_state: "waiting" as const },
          transportCommand: {
            ...command,
            action: "seek" as const,
            playback_state: "waiting" as const,
            position_seconds: 1500,
            execute_at: new Date(Date.now() + 500).toISOString(),
          },
        },
      });
      await act(() => vi.advanceTimersByTimeAsync(500));
      // A rebuilt stream past the target cannot pre-roll back to it; the
      // server decides whether that landing is close enough. One short of
      // the target still pre-rolls up to it before it reports.
      video.currentTime = landed;
      fireEvent.seeked(video);
      fireEvent.canPlay(video);
      const ready = vi
        .mocked(connection.sendRoomMessage)
        .mock.calls.filter(([m]) => m.type === "ready");
      expect(ready.length > 0).toBe(reports);
      if (reports) {
        expect(ready[0]?.[0]).toMatchObject({
          command_id: command.command_id,
          position_seconds: landed,
        });
      }
    },
  );

  it("lets the room's resume replace a seek target that landed ahead of it", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    const seekCommand = {
      ...command,
      action: "seek" as const,
      playback_state: "waiting" as const,
      position_seconds: 1500,
      execute_at: new Date(Date.now() + 500).toISOString(),
    };
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" as const },
        transportCommand: seekCommand,
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(500));
    // The stream lands 3 s past the target: ready, but the seek never settles.
    video.currentTime = 1503;
    fireEvent.seeked(video);
    fireEvent.canPlay(video);
    expect(connection.sendRoomMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready", position_seconds: 1503 }),
    );
    expect(controls.current!.currentTime).toBe(1500);

    // The room resumes from the barrier.
    markPlaying(video);
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        transportCommand: {
          ...command,
          command_id: "room-command-2",
          position_seconds: 1500,
          execute_at: new Date().toISOString(),
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    // The old target is gone: the shown time follows the element again.
    expect(controls.current!.currentTime).toBe(1503);
    video.currentTime = 1503.5;
    fireFrameTimeUpdate(video);
    expect(controls.current!.currentTime).toBe(1503.5);
    // The 3 s lead is left to a nudge, and the drift loop keeps converging.
    expect(video.playbackRate).toBe(0.96);
    expect(playerSeek).not.toHaveBeenCalled();
    // A second later the room is at 1501; 0.5 s ahead slows by 2 %.
    video.currentTime = 1501.5;
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(video.playbackRate).toBe(0.98);
  });

  it("reports readiness only after the current seek reaches buffered media", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    const waitingConnection = {
      ...connection,
      room: { ...connection.room!, playback_state: "waiting" as const },
      transportCommand: {
        ...command,
        action: "seek" as const,
        playback_state: "waiting" as const,
        position_seconds: 1500,
        execute_at: new Date(Date.now() + 500).toISOString(),
      },
    };
    rerenderPlayer({ watchTogetherConnection: waitingConnection });
    fireEvent.canPlay(video);
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready" }),
    );
    await act(() => vi.advanceTimersByTimeAsync(500));
    fireEvent.canPlay(video);
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready" }),
    );

    video.currentTime = 1500;
    Object.defineProperty(video, "readyState", { configurable: true, value: 1 });
    fireEvent.seeked(video);
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready" }),
    );
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    Object.defineProperty(video, "seeking", { configurable: true, value: true });
    fireEvent.canPlay(video);
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready" }),
    );
    Object.defineProperty(video, "seeking", { configurable: true, value: false });
    fireEvent.canPlay(video);
    fireEvent.canPlay(video);
    expect(
      vi.mocked(connection.sendRoomMessage).mock.calls.filter(([m]) => m.type === "ready"),
    ).toEqual([
      [
        {
          type: "ready",
          session_id: "session-1",
          command_id: command.command_id,
          position_seconds: 1500,
          is_paused: true,
        },
      ],
    ]);

    // Another seek can arrive while the room still waits for other members.
    rerenderPlayer({
      watchTogetherConnection: {
        ...waitingConnection,
        transportCommand: {
          ...waitingConnection.transportCommand,
          command_id: "second-seek",
          position_seconds: 30,
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    fireEvent.canPlay(video);
    video.currentTime = 30;
    fireEvent.seeked(video);
    expect(connection.sendRoomMessage).toHaveBeenCalledWith({
      type: "ready",
      session_id: "session-1",
      command_id: "second-seek",
      position_seconds: 30,
      is_paused: true,
    });
  });

  it("acknowledges a seek from timeupdate when no canplay follows the rebuilt stream", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" as const },
        transportCommand: {
          ...command,
          action: "seek" as const,
          playback_state: "waiting" as const,
          position_seconds: 1500,
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    // The first canplay still belongs to the old stream.
    fireEvent.canPlay(video);
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready" }),
    );
    video.currentTime = 1500.3;
    fireEvent.timeUpdate(video);
    expect(connection.sendRoomMessage).toHaveBeenCalledWith({
      type: "ready",
      session_id: "session-1",
      command_id: command.command_id,
      position_seconds: 1500.3,
      is_paused: true,
    });
  });

  it("pre-rolls a stall seek that joined a reload already loading", async () => {
    // A progressive remux whose stream starts at 1490 s.
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(1491, 1490);
    let paused = true;
    Object.defineProperty(video, "paused", { configurable: true, get: () => paused });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    vi.mocked(video.play).mockImplementation(async () => {
      paused = false;
    });
    vi.mocked(video.pause).mockImplementation(() => {
      paused = true;
    });
    const seekable = (end: number) =>
      Object.defineProperty(video, "seekable", {
        configurable: true,
        value: { length: 1, start: () => 0, end: () => end },
      });
    // A correction to 1500 reopens the stream there, in the same plan.
    seekable(20);
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        transportCommand: { ...command, command_id: "correction", position_seconds: 1500 },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(playerSeek).toHaveBeenCalledExactlyOnceWith(10);
    // The reopened response starts loading, at the keyframe 2.5 s short.
    fireEvent(video, new Event("loadstart"));
    seekable(0);
    video.currentTime = 7.5;

    // The room now waits on this viewer with a seek to the same position.
    vi.mocked(video.play).mockClear();
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" as const },
        transportCommand: {
          ...command,
          command_id: "stall-seek",
          action: "seek" as const,
          playback_state: "waiting" as const,
          position_seconds: 1500,
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    // No second reload, and the running one still pre-rolls up to the target.
    expect(playerSeek).toHaveBeenCalledOnce();
    expect(onReanchorSeek).not.toHaveBeenCalled();
    await act(() => vi.advanceTimersByTimeAsync(600));
    expect(video.play).toHaveBeenCalledOnce();
    expect(video.playbackRate).toBe(4);
  });

  it.each(["target", "play", "pause"] as const)(
    "ends a guest seek pre-roll on %s",
    async (ending) => {
      const { connection, video, command, rerenderPlayer } = setup(100);
      let paused = true;
      Object.defineProperty(video, "paused", { configurable: true, get: () => paused });
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      vi.mocked(video.play).mockImplementation(async () => {
        paused = false;
      });
      vi.mocked(video.pause).mockImplementation(() => {
        paused = true;
      });
      const waitingConnection = {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" as const },
        transportCommand: {
          ...command,
          action: "seek" as const,
          playback_state: "waiting" as const,
          position_seconds: 1500,
        },
      };
      rerenderPlayer({ watchTogetherConnection: waitingConnection });
      await act(() => vi.advanceTimersByTimeAsync(0));

      // The copy remux starts at the keyframe 2.5 s before the target, and the
      // progressive response cannot seek to the plan's player start.
      rerenderPlayer({
        watchTogetherConnection: waitingConnection,
        planRevision: 2,
        plan: fixturePlanV3({
          ...directPlan,
          delivery: "server_remux_progressive",
          timeline: {
            ...directPlan.timeline,
            source_start_seconds: 1500,
            stream_origin_seconds: 1497.5,
            timeline_offset_seconds: 1497.5,
            player_start_seconds: 2.5,
            can_seek_anywhere: false,
          },
        }),
      });
      Object.defineProperty(video, "seekable", {
        configurable: true,
        value: { length: 1, start: () => 0, end: () => 0 },
      });
      video.currentTime = 0.05;
      vi.mocked(video.play).mockClear();
      vi.mocked(connection.sendRoomMessage).mockClear();

      // Until the rebuilt stream loads, the element still holds the old one.
      await act(() => vi.advanceTimersByTimeAsync(600));
      expect(video.play).not.toHaveBeenCalled();
      fireEvent(video, new Event("loadstart"));

      await act(() => vi.advanceTimersByTimeAsync(600));
      expect(video.play).toHaveBeenCalledOnce();
      expect(video.muted).toBe(true);
      expect(video.playbackRate).toBe(4);
      // The temporary mute is not saved as the viewer's preference, while a
      // viewer's volume change still is.
      const setMuted = vi.spyOn(video, "muted", "set");
      act(() => controls.current!.onVolumeChange!(0.4));
      expect(setMuted).not.toHaveBeenCalledWith(false);
      expect(video.muted).toBe(true);
      fireEvent.volumeChange(video);
      expect(localStorage.getItem("player-muted")).not.toBe("true");
      expect(localStorage.getItem("player-volume")).toBe("0.4");
      setMuted.mockClear();
      await act(async () => {
        await realtimeOptions.current!.onCommand({
          type: "command",
          command_id: "volume-command",
          session_id: "session-1",
          name: "set_volume",
          deadline_ms: 8_000,
          payload: { volume: 0.6 },
        });
      });
      expect(setMuted).not.toHaveBeenCalledWith(false);
      expect(video.muted).toBe(true);
      expect(localStorage.getItem("player-volume")).toBe("0.6");

      // The mute shortcut flips the viewer's choice rather than the element's
      // temporary pre-roll mute, which it could only ever turn off.
      const toggleMuted = vi.mocked(useKeyboardShortcuts).mock.lastCall![5];
      act(() => toggleMuted());
      expect(localStorage.getItem("player-muted")).toBe("true");
      expect(video.muted).toBe(true);
      act(() => vi.mocked(useKeyboardShortcuts).mock.lastCall![5]());
      expect(localStorage.getItem("player-muted")).toBe("false");
      expect(video.muted).toBe(true);

      // A mute chosen during the pre-roll holds; the element stays muted until
      // the pre-roll ends and then keeps the viewer's choice.
      act(() => controls.current!.onMutedChange!(true));
      expect(video.muted).toBe(true);
      expect(localStorage.getItem("player-muted")).toBe("true");

      if (ending !== "target") {
        act(() => controls.current!.onMutedChange!(false));
        rerenderPlayer({
          watchTogetherConnection: {
            ...waitingConnection,
            room: {
              ...waitingConnection.room!,
              playback_state: ending === "play" ? "playing" : "paused",
            },
            transportCommand: {
              ...waitingConnection.transportCommand,
              command_id: "next-command",
              action: ending,
              playback_state: ending === "play" ? "playing" : "paused",
              execute_at: new Date(Date.now() + 500).toISOString(),
            },
          },
        });
        // The new command owns playback at its scheduled time. The muted
        // pre-roll must stop before its mute and speed are restored.
        expect(paused).toBe(true);
        expect(video.muted).toBe(false);
        expect(video.playbackRate).toBe(1);
        await act(() => vi.advanceTimersByTimeAsync(500));
        expect(paused).toBe(ending === "pause");
        return;
      }

      // Still in the pre-roll: no acknowledgement yet. Close to the target it
      // slows to normal speed, found by polling even without a timeupdate.
      video.currentTime = 1.8;
      await act(() => vi.advanceTimersByTimeAsync(60));
      expect(paused).toBe(false);
      expect(video.playbackRate).toBe(1);
      expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
        expect.objectContaining({ type: "ready" }),
      );

      video.currentTime = 2.6;
      fireEvent.timeUpdate(video);
      expect(paused).toBe(true);
      expect(video.muted).toBe(true);
      expect(video.playbackRate).toBe(1);
      expect(connection.sendRoomMessage).toHaveBeenCalledWith({
        type: "ready",
        session_id: "session-1",
        command_id: command.command_id,
        position_seconds: 1500.1,
        is_paused: true,
      });
    },
  );

  it("holds a hidden tab's seek pre-roll to the gap a throttled check can spend", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    let paused = true;
    Object.defineProperty(video, "paused", { configurable: true, get: () => paused });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    vi.mocked(video.play).mockImplementation(async () => {
      paused = false;
    });
    vi.mocked(video.pause).mockImplementation(() => {
      paused = true;
    });
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    try {
      const waitingConnection = {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" as const },
        transportCommand: {
          ...command,
          action: "seek" as const,
          playback_state: "waiting" as const,
          position_seconds: 1500,
        },
      };
      rerenderPlayer({ watchTogetherConnection: waitingConnection });
      await act(() => vi.advanceTimersByTimeAsync(0));
      // A 12 s pre-roll at normal speed would outlast the room's 10 s waiting
      // deadline, so a hidden tab still starts fast and slows as it closes in.
      rerenderPlayer({
        watchTogetherConnection: waitingConnection,
        planRevision: 2,
        plan: fixturePlanV3({
          ...directPlan,
          delivery: "server_remux_progressive",
          timeline: {
            ...directPlan.timeline,
            source_start_seconds: 1500,
            stream_origin_seconds: 1488,
            timeline_offset_seconds: 1488,
            player_start_seconds: 12,
            can_seek_anywhere: false,
          },
        }),
      });
      Object.defineProperty(video, "seekable", {
        configurable: true,
        value: { length: 1, start: () => 0, end: () => 0 },
      });
      video.currentTime = 0.05;
      vi.mocked(video.play).mockClear();
      vi.mocked(connection.sendRoomMessage).mockClear();
      fireEvent(video, new Event("loadstart"));
      await act(() => vi.advanceTimersByTimeAsync(600));
      expect(video.play).toHaveBeenCalledOnce();
      expect(video.playbackRate).toBe(4);

      // Within three seconds of the target a throttled check, up to about 1.5 s
      // late, must not carry the element past the one-second tolerance.
      video.currentTime = 9;
      await act(() => vi.advanceTimersByTimeAsync(60));
      expect(video.playbackRate).toBe(2);
      expect(paused).toBe(false);

      video.currentTime = 10.6;
      await act(() => vi.advanceTimersByTimeAsync(60));
      expect(video.playbackRate).toBe(1);
      expect(paused).toBe(false);
      expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
        expect.objectContaining({ type: "ready" }),
      );

      // Inside the last half second a throttled check could carry the element
      // out of the room's tolerance, so the pre-roll gives up the rest of the
      // gap and acknowledges from where it stopped.
      video.currentTime = 11.6;
      fireEvent.timeUpdate(video);
      expect(paused).toBe(true);
      expect(connection.sendRoomMessage).toHaveBeenCalledWith({
        type: "ready",
        session_id: "session-1",
        command_id: command.command_id,
        position_seconds: 1499.6,
        is_paused: true,
      });
    } finally {
      Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    }
  });

  it("lets the host acknowledge a seek that landed short of the target", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: {
          ...connection.room!,
          playback_state: "waiting" as const,
          self_role: "host" as const,
          self_can_manage_room: true,
        },
        transportCommand: {
          ...command,
          action: "seek" as const,
          playback_state: "waiting" as const,
          position_seconds: 1500,
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    video.currentTime = 1494;
    fireEvent.seeked(video);
    expect(connection.sendRoomMessage).toHaveBeenCalledWith({
      type: "ready",
      session_id: "session-1",
      command_id: command.command_id,
      position_seconds: 1494,
      is_paused: true,
    });
  });

  it.each(["playing", "paused"] as const)(
    "acknowledges recovery after the room resumed %s without this viewer",
    async (playbackState) => {
      const { connection, video, command, rerenderPlayer } = setup(100);
      const messages = vi.mocked(connection.sendRoomMessage);
      const recoveredConnection = {
        ...connection,
        room: {
          ...connection.room!,
          playback_state: playbackState,
          self_ignore_wait: true,
        },
        transportCommand: {
          ...command,
          action: playbackState === "playing" ? ("play" as const) : ("pause" as const),
          playback_state: playbackState,
          execute_at: new Date(Date.now() + 500).toISOString(),
        },
      };
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      rerenderPlayer({ watchTogetherConnection: recoveredConnection });
      messages.mockClear();
      // The room's latest command has not executed here yet.
      fireEvent.canPlay(video);
      expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));

      Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));

      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      // Recovery must be detected even when the browser omits canplay.
      await act(() => vi.advanceTimersByTimeAsync(500));
      expect(messages).toHaveBeenCalledWith({
        type: "ready",
        session_id: "session-1",
        command_id: command.command_id,
        position_seconds: video.currentTime,
        is_paused: video.paused,
        is_ready: true,
      });
      messages.mockClear();
      // A lost acknowledgement heals without another media event.
      await act(() => vi.advanceTimersByTimeAsync(500));
      expect(messages).toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));

      rerenderPlayer({
        watchTogetherConnection: {
          ...recoveredConnection,
          room: { ...recoveredConnection.room, self_ignore_wait: false },
        },
      });
      messages.mockClear();
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));
      expect(messages).toHaveBeenCalledWith(expect.objectContaining({ type: "state_report" }));
    },
  );

  it("sends no position reports from paused unplayable media while catching up", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    const messages = vi.mocked(connection.sendRoomMessage);
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "paused", self_ignore_wait: true },
        transportCommand: {
          ...command,
          action: "pause" as const,
          playback_state: "paused" as const,
          execute_at: new Date(Date.now() + 500).toISOString(),
        },
      },
    });
    Object.defineProperty(video, "paused", { configurable: true, value: true });
    Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
    messages.mockClear();
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    // A report matching the paused room would end catching up on the server.
    expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "state_report" }));
    expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));

    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(messages).toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));
  });

  it("clears a buffering status left over from a reconnect", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: {
          ...connection.room!,
          members: [
            {
              user_id: 8,
              profile_id: "guest",
              display_name: "Me",
              is_host: false,
              is_self: true,
              connected: true,
              is_buffering: true,
            },
          ],
        },
        transportCommand: command,
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(connection.sendRoomMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready", command_id: command.command_id }),
    );
  });

  it("sends no position reports while the element is stalled", async () => {
    const { connection, video } = setup(100);
    const messages = vi.mocked(connection.sendRoomMessage);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
    messages.mockClear();
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "state_report" }));

    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    await act(() => vi.advanceTimersByTimeAsync(1_500));
    expect(messages).toHaveBeenCalledWith(expect.objectContaining({ type: "state_report" }));
  });

  it("runs one correction rebuild at a time and aims the next ahead by its startup", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(90);
    const correct = async (commandId: string, position = 100) => {
      rerenderPlayer({
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
    await correct("correction-1");
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);
    expect(onReanchorSeek).toHaveBeenLastCalledWith(100);

    // A correction while the rebuilt stream is still starting keeps playing.
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    await correct("correction-2");
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);

    // The rebuild plays four seconds after it started.
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    video.currentTime = 100;
    fireEvent.timeUpdate(video);

    // The next rebuild waits out the backoff, then aims ahead by that startup.
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    await correct("correction-3", 120);
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(5_000));
    await correct("correction-4", 125);
    expect(onReanchorSeek).toHaveBeenCalledTimes(2);
    expect(onReanchorSeek).toHaveBeenLastCalledWith(129);
  });

  it("paces corrections that must load unbuffered media on a seekable stream", async () => {
    const { connection, video, command, rerenderPlayer } = setup(90, 0, true);
    const correct = async (commandId: string, position: number) => {
      rerenderPlayer({
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
    await correct("correction-1", 100);
    expect(playerSeek).toHaveBeenCalledTimes(1);

    // The first load is still running; chasing the room again would only
    // restart it.
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    await correct("correction-2", 110);
    expect(playerSeek).toHaveBeenCalledTimes(1);

    // Buffered media is reached at once, whatever the budget.
    Object.defineProperty(video, "buffered", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 200 } as unknown as TimeRanges,
    });
    await correct("correction-3", 120);
    expect(playerSeek).toHaveBeenCalledTimes(2);
    expect(playerSeek).toHaveBeenLastCalledWith(120);
  });

  it("does not settle a correction reload on an unrelated stream swap", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(90);
    // The reanchor is still being replanned.
    onReanchorSeek.mockImplementation(() => new Promise<boolean>(() => {}) as unknown as boolean);
    const correct = async (commandId: string, position: number) => {
      rerenderPlayer({
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
    await correct("correction-1", 100);
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);

    // Another stream loads and plays at the target, but it is not the reload.
    await act(() => vi.advanceTimersByTimeAsync(4_000));
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    fireEvent.loadStart(video);
    fireEvent.seeking(video);
    video.currentTime = 100;
    fireEvent.timeUpdate(video);

    // Had it settled, the backoff would have allowed another reload by now.
    await act(() => vi.advanceTimersByTimeAsync(11_000));
    await correct("correction-2", 115);
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);
  });

  it("ignores a stale reload's completion when a newer reload aims at the same position", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(90);
    const resolvers: Array<(accepted: boolean) => void> = [];
    onReanchorSeek.mockImplementation(
      () => new Promise<boolean>((resolve) => resolvers.push(resolve)) as unknown as boolean,
    );
    const correct = async (commandId: string, position: number) => {
      rerenderPlayer({
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
    await correct("correction-1", 100);
    // The first reload goes stale, and a new one aims at the same position.
    await act(() => vi.advanceTimersByTimeAsync(31_000));
    await correct("correction-2", 100);
    expect(onReanchorSeek).toHaveBeenCalledTimes(2);

    // The first reload's replan completes late; it must not count as the second's load.
    await act(async () => resolvers[0]!(true));
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    video.currentTime = 100;
    fireEvent.timeUpdate(video);

    // Had the second reload landed, its 20 s backoff would have ended.
    await act(() => vi.advanceTimersByTimeAsync(21_000));
    await correct("correction-3", 150);
    expect(onReanchorSeek).toHaveBeenCalledTimes(2);
  });

  it("starts reload pacing over when the viewer picks another quality", async () => {
    const { connection, command, rerenderPlayer, onReanchorSeek } = setup(90);
    const onQualitySelect = vi.fn();
    const correct = async (commandId: string) => {
      rerenderPlayer({
        onQualitySelect,
        watchTogetherConnection: {
          ...connection,
          transportCommand: {
            ...command,
            command_id: commandId,
            position_seconds: 100,
            execute_at: new Date().toISOString(),
          },
        },
      });
      await act(() => vi.advanceTimersByTimeAsync(0));
    };
    await correct("correction-1");
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    await correct("correction-2");
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);

    const selectQuality = (controls.current as unknown as { onQualitySelect: (id: string) => void })
      .onQualitySelect;
    act(() => selectQuality("720p"));
    expect(onQualitySelect).toHaveBeenCalledWith("720p", expect.any(Number));
    await correct("correction-3");
    expect(onReanchorSeek).toHaveBeenCalledTimes(2);
  });

  it("offers a lower quality after repeated stalls", async () => {
    const { connection, video, rerenderPlayer } = setup(100);
    const onQualitySelect = vi.fn();
    rerenderPlayer({
      watchTogetherConnection: connection,
      onQualitySelect,
      plan: fixturePlanV3({
        ...directPlan,
        delivery: "server_remux_progressive",
        timeline: { ...directPlan.timeline, can_seek_anywhere: false },
        available_qualities: [
          { label: "original", height: 2160, bitrate_kbps: 40_000, preserves_source: true },
          { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
        ],
      }),
    });
    const stall = async () => {
      Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
      fireEvent.waiting(video);
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      fireEvent.canPlay(video);
    };
    await stall();
    expect(screen.queryByRole("button", { name: "Lower quality" })).not.toBeInTheDocument();
    await stall();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Lower quality" })));
    expect(onQualitySelect).toHaveBeenCalledWith("1080p-medium", expect.any(Number));
  });

  it("chooses the lower quality from the ladder shown when the viewer accepts", async () => {
    const { connection, video, rerenderPlayer } = setup(100);
    const onQualitySelect = vi.fn();
    const planWith = (qualities: ReturnType<typeof fixturePlanV3>["available_qualities"]) =>
      fixturePlanV3({
        ...directPlan,
        delivery: "server_remux_progressive",
        timeline: { ...directPlan.timeline, can_seek_anywhere: false },
        available_qualities: qualities,
      });
    rerenderPlayer({
      watchTogetherConnection: connection,
      onQualitySelect,
      plan: planWith([
        { label: "original", height: 2160, bitrate_kbps: 40_000, preserves_source: true },
        { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
        { label: "720p", height: 720, bitrate_kbps: 3000, preserves_source: false },
      ]),
    });
    for (let stall = 0; stall < 2; stall++) {
      Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
      fireEvent.waiting(video);
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      fireEvent.canPlay(video);
    }
    // A replan drops 1080p from the ladder while the offer shows.
    rerenderPlayer({
      watchTogetherConnection: connection,
      onQualitySelect,
      plan: planWith([
        { label: "original", height: 2160, bitrate_kbps: 40_000, preserves_source: true },
        { label: "720p", height: 720, bitrate_kbps: 3000, preserves_source: false },
      ]),
    });
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Lower quality" })));
    expect(onQualitySelect).toHaveBeenCalledWith("720p", expect.any(Number));
  });

  it("withdraws the lower-quality offer when a replan leaves no lower quality", async () => {
    const { connection, video, rerenderPlayer } = setup(100);
    const planWith = (qualities: ReturnType<typeof fixturePlanV3>["available_qualities"]) =>
      fixturePlanV3({
        ...directPlan,
        delivery: "server_remux_progressive",
        timeline: { ...directPlan.timeline, can_seek_anywhere: false },
        available_qualities: qualities,
      });
    const original = {
      label: "original",
      height: 2160,
      bitrate_kbps: 40_000,
      preserves_source: true,
    };
    rerenderPlayer({
      watchTogetherConnection: connection,
      plan: planWith([
        original,
        { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
      ]),
    });
    for (let stall = 0; stall < 2; stall++) {
      Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
      fireEvent.waiting(video);
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      fireEvent.canPlay(video);
    }
    expect(screen.getByRole("button", { name: "Lower quality" })).toBeInTheDocument();

    rerenderPlayer({ watchTogetherConnection: connection, plan: planWith([original]) });
    expect(screen.queryByRole("button", { name: "Lower quality" })).not.toBeInTheDocument();
  });

  it("withdraws the lower-quality offer when the viewer picks another quality", async () => {
    const { connection, video, rerenderPlayer } = setup(100);
    const plan = fixturePlanV3({
      ...directPlan,
      delivery: "server_remux_progressive",
      timeline: { ...directPlan.timeline, can_seek_anywhere: false },
      available_qualities: [
        { label: "original", height: 2160, bitrate_kbps: 40_000, preserves_source: true },
        { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
        { label: "720p", height: 720, bitrate_kbps: 3000, preserves_source: false },
      ],
    });
    rerenderPlayer({ watchTogetherConnection: connection, plan });
    for (let stall = 0; stall < 2; stall++) {
      Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
      fireEvent.waiting(video);
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      fireEvent.canPlay(video);
    }
    expect(screen.getByRole("button", { name: "Lower quality" })).toBeInTheDocument();

    // The offer was computed for Original; after choosing 720p it would raise quality.
    rerenderPlayer({ watchTogetherConnection: connection, plan, qualityPreference: "720p" });
    expect(screen.queryByRole("button", { name: "Lower quality" })).not.toBeInTheDocument();
  });

  it("explains a room that kept playing without someone", () => {
    const { connection, rerenderPlayer } = setup(100);
    const bob = {
      user_id: 9,
      profile_id: "bob",
      display_name: "Bob",
      is_host: false,
      is_self: false,
      connected: true,
    };
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: {
          ...connection.room!,
          playback_state: "waiting",
          members: [{ ...bob, is_syncing: true }],
        },
      },
    });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "playing", members: [bob] },
      },
    });
    expect(screen.getByText("Continuing without Bob. They'll catch up.")).toBeInTheDocument();

    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, self_ignore_wait: true, members: [bob] },
      },
    });
    expect(screen.getByText(/The party kept playing/)).toBeInTheDocument();
  });

  it("repeats readiness on the state tick while the room waits", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" as const },
        transportCommand: {
          ...command,
          action: "pause" as const,
          playback_state: "waiting" as const,
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    vi.mocked(connection.sendRoomMessage).mockClear();
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    const reports = vi
      .mocked(connection.sendRoomMessage)
      .mock.calls.filter(([m]) => m.type === "state_report");
    expect(reports.length).toBeGreaterThanOrEqual(1);
    expect(reports[0]?.[0]).toEqual({
      type: "state_report",
      session_id: "session-1",
      command_id: command.command_id,
      position_seconds: 100,
      is_paused: true,
      is_ready: true,
    });
  });

  it.each(["readiness reset", "new command"])(
    "stops readiness retries after the server acknowledges this member and resumes on %s",
    async (reset) => {
      const { connection, video, command, rerenderPlayer } = setup(100);
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      const members = [
        {
          user_id: 1,
          profile_id: "guest",
          display_name: "Guest",
          is_host: false,
          is_self: true,
          connected: true,
          is_ready: false,
        },
        {
          user_id: 2,
          profile_id: "host",
          display_name: "Host",
          is_host: true,
          is_self: false,
          connected: true,
          is_ready: false,
        },
      ];
      const waitingConnection = {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" as const, members },
        transportCommand: {
          ...command,
          action: "pause" as const,
          playback_state: "waiting" as const,
        },
      };
      rerenderPlayer({ watchTogetherConnection: waitingConnection });
      await act(() => vi.advanceTimersByTimeAsync(0));
      const messages = vi.mocked(connection.sendRoomMessage);
      messages.mockClear();
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      expect(messages).toHaveBeenCalledWith(
        expect.objectContaining({ type: "state_report", is_ready: true }),
      );

      // The peer is still loading, but our own readiness has reached the server.
      rerenderPlayer({
        watchTogetherConnection: {
          ...waitingConnection,
          room: {
            ...waitingConnection.room,
            members: members.map((member) => ({ ...member, is_ready: member.is_self })),
          },
        },
      });
      messages.mockClear();
      fireEvent.canPlay(video);
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ is_ready: true }));
      expect(messages).not.toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));
      // A routine report names the command it reflects, so the server can
      // take it without waiting out that command's settle window.
      expect(messages).toHaveBeenCalledWith({
        type: "state_report",
        session_id: "session-1",
        command_id: command.command_id,
        position_seconds: 100,
        is_paused: true,
      });

      // The server clears readiness in its snapshot before dispatching a new command.
      rerenderPlayer({ watchTogetherConnection: waitingConnection });
      const nextCommand =
        reset === "new command"
          ? {
              ...waitingConnection.transportCommand,
              command_id: "room-command-2",
              execute_at: new Date().toISOString(),
            }
          : waitingConnection.transportCommand;
      rerenderPlayer({
        watchTogetherConnection: { ...waitingConnection, transportCommand: nextCommand },
      });
      await act(() => vi.advanceTimersByTimeAsync(0));
      fireEvent.canPlay(video);
      expect(messages).toHaveBeenCalledWith(
        expect.objectContaining({ type: "ready", command_id: nextCommand.command_id }),
      );
      messages.mockClear();
      await act(() => vi.advanceTimersByTimeAsync(500));
      expect(messages).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "state_report",
          is_ready: true,
          command_id: nextCommand.command_id,
        }),
      );
    },
  );

  it("waits for execution even when a pending seek already matches the media position", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" },
        transportCommand: {
          ...command,
          action: "seek",
          playback_state: "waiting",
          execute_at: new Date(Date.now() + 500).toISOString(),
        },
      },
    });
    fireEvent.canPlay(video);
    expect(connection.sendRoomMessage).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready" }),
    );
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(connection.sendRoomMessage).toHaveBeenCalledWith({
      type: "ready",
      session_id: "session-1",
      command_id: command.command_id,
      position_seconds: 100,
      is_paused: true,
    });
  });

  it("acknowledges a buffering pause at the actual media position and retries a failed send", async () => {
    // Within the hard-seek bound a pause does not rebuild the stream.
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(96);
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    vi.mocked(connection.sendRoomMessage).mockReturnValueOnce({ ok: false });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: { ...connection.room!, playback_state: "waiting" },
        transportCommand: { ...command, action: "pause", playback_state: "waiting" },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    fireEvent.canPlay(video);
    expect(onReanchorSeek).not.toHaveBeenCalled();
    const ready = vi
      .mocked(connection.sendRoomMessage)
      .mock.calls.filter(([m]) => m.type === "ready");
    expect(ready).toHaveLength(2);
    expect(ready[1]?.[0]).toEqual({
      type: "ready",
      session_id: "session-1",
      command_id: command.command_id,
      position_seconds: 96,
      is_paused: true,
    });
  });

  it("restores the actual position when a seek replan fails", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    const commandedConnection = {
      ...connection,
      transportCommand: { ...command, action: "seek" as const, position_seconds: 1500 },
    };
    rerenderPlayer({ watchTogetherConnection: commandedConnection });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(controls.current!.currentTime).toBe(1500);

    rerenderPlayer({ watchTogetherConnection: commandedConnection, replanning: true });
    rerenderPlayer({
      watchTogetherConnection: commandedConnection,
      replanning: false,
      replanError: "The seek failed.",
    });
    expect(controls.current!.currentTime).toBe(100);
    video.currentTime = 101;
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(101);
  });

  it.each([0, 80])(
    "chooses the advancing play position with a %ss timeline offset",
    async (timelineOffset) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(
        100.5,
        timelineOffset,
      );
      markPlaying(video);
      command.execute_at = new Date(Date.now() - 1_000).toISOString();
      rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
      await act(() => vi.advanceTimersByTimeAsync(0));

      // The room is now at 101, so this member must speed up, not slow down.
      expect(video.playbackRate).toBeGreaterThan(1);
      expect(onReanchorSeek).not.toHaveBeenCalled();
      expect(playerSeek).not.toHaveBeenCalled();

      // The room is at 103 two seconds later; still behind it, the nudge continues.
      video.currentTime = 102.5 - timelineOffset;
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      expect(video.playbackRate).toBeGreaterThan(1);
      // Within the settle bound of the room (103.5 now) it ends.
      video.currentTime = 103.45 - timelineOffset;
      await act(() => vi.advanceTimersByTimeAsync(500));
      expect(video.playbackRate).toBe(1);
    },
  );

  it.each([true, false])(
    "reanchors to the advancing play position beyond the hard-seek bound (playing: %s)",
    async (playing) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(99);
      if (playing) markPlaying(video);
      command.execute_at = new Date(Date.now() - 6_000).toISOString();
      rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
      await act(() => vi.advanceTimersByTimeAsync(0));

      expect(onReanchorSeek).toHaveBeenCalledWith(106);
      expect(video.playbackRate).toBe(1);
    },
  );

  it.each([4, 1.5])(
    "joins a stopped viewer %ss off an unseekable target at 1x without a rebuild",
    async (drift) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(100 - drift);
      rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
      await act(() => vi.advanceTimersByTimeAsync(0));

      expect(onReanchorSeek).not.toHaveBeenCalled();
      expect(video.play).toHaveBeenCalledOnce();
      expect(video.playbackRate).toBe(1);
    },
  );

  it.each([2000, 7000])(
    "recalculates catch-up after autoplay is blocked for %sms",
    async (delayMs) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(101);
      vi.mocked(video.play).mockRejectedValueOnce(
        new DOMException("Autoplay blocked", "NotAllowedError"),
      );
      rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
      await act(() => vi.advanceTimersByTimeAsync(0));

      expect(video.playbackRate).toBe(1);
      vi.setSystemTime(Date.now() + delayMs);
      await act(async () => fireEvent.click(screen.getByRole("button", { name: "Join playback" })));

      // The viewer starts at 1x either way; residual drift is the drift loop's.
      expect(video.playbackRate).toBe(1);
      if (delayMs === 2000) {
        expect(onReanchorSeek).not.toHaveBeenCalled();
      } else {
        expect(onReanchorSeek).toHaveBeenCalledWith(107);
      }
    },
  );

  it("retries a play aborted by a transport swap instead of asking for a click", async () => {
    const { connection, video, command, rerenderPlayer } = setup(101);
    vi.mocked(video.play).mockRejectedValueOnce(
      new DOMException("The play() request was interrupted", "AbortError"),
    );
    Object.defineProperty(video, "paused", { configurable: true, value: true });
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(screen.queryByRole("button", { name: "Join playback" })).not.toBeInTheDocument();
    await act(() => vi.advanceTimersByTimeAsync(400));
    expect(video.play).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("button", { name: "Join playback" })).not.toBeInTheDocument();
  });

  it("resets catch-up when the manual autoplay retry also fails", async () => {
    const { connection, video, command, rerenderPlayer } = setup(101);
    vi.mocked(video.play).mockRejectedValue(
      new DOMException("Autoplay blocked", "NotAllowedError"),
    );
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    vi.setSystemTime(Date.now() + 2000);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Join playback" })));
    expect(video.playbackRate).toBe(1);
  });

  it("keeps the autoplay retry usable after a clock offset update", async () => {
    const { connection, video, command, rerenderPlayer } = setup(101);
    vi.mocked(video.play).mockRejectedValueOnce(
      new DOMException("Autoplay blocked", "NotAllowedError"),
    );
    const commandedConnection = { ...connection, transportCommand: command };
    rerenderPlayer({ watchTogetherConnection: commandedConnection });
    await act(() => vi.advanceTimersByTimeAsync(0));
    rerenderPlayer({
      watchTogetherConnection: { ...commandedConnection, serverTimeOffsetMs: 10 },
    });
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Join playback" })));
    expect(video.play).toHaveBeenCalledTimes(2);
  });

  it.each(["disconnected", "closed"])(
    "abandons an optimistic seek when the room is %s",
    async (state) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(100);
      connection.room = { ...connection.room!, self_can_manage_room: true };
      rerenderPlayer({ watchTogetherConnection: connection });
      act(() => controls.current!.onSeek(1500));
      expect(controls.current!.currentTime).toBe(1500);
      const commandedConnection = {
        ...connection,
        transportCommand: {
          ...command,
          action: "seek" as const,
          position_seconds: 1500,
          execute_at: new Date(Date.now() + 500).toISOString(),
        },
      };
      rerenderPlayer({ watchTogetherConnection: commandedConnection });
      rerenderPlayer({
        watchTogetherConnection: {
          ...commandedConnection,
          ...(state === "disconnected"
            ? { connectionState: "disconnected" as const }
            : { closedReason: "host_left" }),
        },
      });
      expect(controls.current!.currentTime).toBe(100);
      await act(() => vi.advanceTimersByTimeAsync(500));
      expect(onReanchorSeek).not.toHaveBeenCalled();
      video.currentTime = 101;
      fireEvent.timeUpdate(video);
      expect(controls.current!.currentTime).toBe(101);
    },
  );

  it.each(["play", "pause", "seek"] as const)(
    "keeps a seekable late %s command in the current stream",
    async (action) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(98.5, 80);
      Object.defineProperty(video, "seekable", {
        configurable: true,
        value: { length: 1, start: () => 0, end: () => 30 },
      });
      rerenderPlayer({
        watchTogetherConnection: {
          ...connection,
          transportCommand: {
            ...command,
            action,
            playback_state:
              action === "play" ? "playing" : action === "pause" ? "paused" : "waiting",
            execute_at: new Date(Date.now() - 1_000).toISOString(),
          },
        },
      });
      await act(() => vi.advanceTimersByTimeAsync(0));

      // Only a playing room advances past the command's position.
      expect(playerSeek).toHaveBeenCalledWith(action === "play" ? 21 : 20);
      expect(onReanchorSeek).not.toHaveBeenCalled();
      expect(video.playbackRate).toBe(1);
    },
  );

  it("resets an active catch-up when the connection ends", async () => {
    const { connection, video, command, rerenderPlayer } = setup(99);
    markPlaying(video);
    const commandedConnection = { ...connection, transportCommand: command };
    rerenderPlayer({ watchTogetherConnection: commandedConnection });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(video.playbackRate).toBeGreaterThan(1);

    rerenderPlayer({
      watchTogetherConnection: { ...commandedConnection, connectionState: "disconnected" },
    });
    expect(video.playbackRate).toBe(1);
  });

  it("cancels a scheduled catch-up when the room disconnects", async () => {
    const { connection, video, command, rerenderPlayer } = setup(99);
    command.execute_at = new Date(Date.now() + 500).toISOString();
    const commandedConnection = { ...connection, transportCommand: command };
    rerenderPlayer({ watchTogetherConnection: commandedConnection });
    rerenderPlayer({
      watchTogetherConnection: { ...commandedConnection, connectionState: "disconnected" },
    });
    await act(() => vi.advanceTimersByTimeAsync(500));

    expect(video.playbackRate).toBe(1);
    expect(video.play).not.toHaveBeenCalled();
  });

  it.each(["play", "pause", "seek"] as const)(
    "reschedules a pending %s after clock correction without replaying it",
    async (action) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(100);
      Object.defineProperty(video, "seekable", {
        configurable: true,
        value: { length: 1, start: () => 0, end: () => 2000 },
      });
      const commandedConnection = {
        ...connection,
        transportCommand: {
          ...command,
          action,
          position_seconds: 1500,
          execute_at: new Date(Date.now() + 500).toISOString(),
        },
      };
      rerenderPlayer({ watchTogetherConnection: commandedConnection });
      await act(() => vi.advanceTimersByTimeAsync(100));
      rerenderPlayer({
        watchTogetherConnection: { ...commandedConnection, serverTimeOffsetMs: 10 },
      });
      await act(() => vi.advanceTimersByTimeAsync(389));
      expect(playerSeek).not.toHaveBeenCalled();
      await act(() => vi.advanceTimersByTimeAsync(1));
      expect(playerSeek).toHaveBeenCalledExactlyOnceWith(1500);
      expect(onReanchorSeek).not.toHaveBeenCalled();
      expect(action === "play" ? video.play : video.pause).toHaveBeenCalledOnce();

      rerenderPlayer({
        watchTogetherConnection: { ...commandedConnection, serverTimeOffsetMs: 20 },
      });
      await act(() => vi.advanceTimersByTimeAsync(500));
      expect(playerSeek).toHaveBeenCalledOnce();
      expect(action === "play" ? video.play : video.pause).toHaveBeenCalledOnce();
    },
  );

  it("replaces a rescheduled command when a newer command arrives", async () => {
    const { connection, command, rerenderPlayer, onReanchorSeek } = setup(100);
    const commandedConnection = {
      ...connection,
      transportCommand: {
        ...command,
        action: "seek" as const,
        position_seconds: 1500,
        execute_at: new Date(Date.now() + 500).toISOString(),
      },
    };
    rerenderPlayer({ watchTogetherConnection: commandedConnection });
    rerenderPlayer({
      watchTogetherConnection: { ...commandedConnection, serverTimeOffsetMs: 10 },
    });
    rerenderPlayer({
      watchTogetherConnection: {
        ...commandedConnection,
        transportCommand: {
          ...commandedConnection.transportCommand,
          command_id: "room-command-2",
          position_seconds: 2000,
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(onReanchorSeek).toHaveBeenCalledExactlyOnceWith(2000);
  });

  it("ends catch-up when playback pauses outside a room transport command", async () => {
    const { connection, video, command, rerenderPlayer } = setup(101);
    markPlaying(video);
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(video.playbackRate).toBeLessThan(1);

    fireEvent.pause(video);
    expect(video.playbackRate).toBe(1);
  });

  it("nudges a playing viewer toward a buffered target instead of seeking", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(98, 80);
    markPlaying(video);
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 30 },
    });
    Object.defineProperty(video, "buffered", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 30 },
    });
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));

    expect(video.playbackRate).toBe(1.04);
    expect(playerSeek).not.toHaveBeenCalled();
    expect(onReanchorSeek).not.toHaveBeenCalled();
  });

  it("corrects drift that builds after a command by rate alone", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(100);
    markPlaying(video);
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(video.playbackRate).toBe(1);

    // The element stalls in place while the room advances. Right after the
    // command the loop leaves playback alone.
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(video.playbackRate).toBe(1);
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(video.playbackRate).toBe(1.04);

    // Far beyond the hard-seek bound the loop still only nudges; seeking is
    // left to the server's correction command.
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(video.playbackRate).toBe(1.04);
    expect(playerSeek).not.toHaveBeenCalled();
    expect(onReanchorSeek).not.toHaveBeenCalled();
  });

  it("leaves a viewer within the deadband at 1x", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    markPlaying(video);
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    video.currentTime = 100.65;
    // The first loop check sees the room at 101: 0.35 s behind it is not worth a nudge.
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(video.playbackRate).toBe(1);
  });

  it.each(["stalls", "seeks", "the room pauses"])(
    "returns a loop nudge to 1x without resuming when playback %s",
    async (change) => {
      const { connection, video, command, rerenderPlayer } = setup(100);
      markPlaying(video);
      const commandedConnection = { ...connection, transportCommand: command };
      rerenderPlayer({ watchTogetherConnection: commandedConnection });
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      expect(video.playbackRate).toBe(1.04);
      const plays = vi.mocked(video.play).mock.calls.length;

      if (change === "stalls") {
        Object.defineProperty(video, "readyState", {
          configurable: true,
          value: HTMLMediaElement.HAVE_CURRENT_DATA,
        });
      } else if (change === "seeks") {
        Object.defineProperty(video, "seeking", { configurable: true, value: true });
      } else {
        rerenderPlayer({
          watchTogetherConnection: {
            ...commandedConnection,
            room: { ...commandedConnection.room!, playback_state: "paused", is_paused: true },
          },
        });
      }
      await act(() => vi.advanceTimersByTimeAsync(500));
      expect(video.playbackRate).toBe(1);
      expect(video.play).toHaveBeenCalledTimes(plays);
    },
  );

  it.each([3600, 3599.8])(
    "leaves an ended element ended when the room plays at %s",
    async (position) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(3600);
      Object.defineProperty(video, "duration", { configurable: true, value: 3600 });
      Object.defineProperty(video, "ended", { configurable: true, value: true });
      rerenderPlayer({
        watchTogetherConnection: {
          ...connection,
          transportCommand: { ...command, position_seconds: position },
        },
      });
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      // play() would start the media over from the beginning.
      expect(video.play).not.toHaveBeenCalled();
      expect(playerSeek).not.toHaveBeenCalled();
      expect(onReanchorSeek).not.toHaveBeenCalled();
    },
  );

  it("seeks an ended element to a room still short of the end before playing", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(3600);
    Object.defineProperty(video, "duration", { configurable: true, value: 3600 });
    Object.defineProperty(video, "ended", { configurable: true, value: true });
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        transportCommand: { ...command, position_seconds: 3599.5 },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(onReanchorSeek).toHaveBeenCalledExactlyOnceWith(3599.5);
    expect(video.play).toHaveBeenCalledOnce();
  });

  it("seeks an element whose stream window ended before the movie to the room", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(1800);
    Object.defineProperty(video, "duration", { configurable: true, value: 1800 });
    Object.defineProperty(video, "ended", { configurable: true, value: true });
    rerenderPlayer({
      duration: 7200,
      watchTogetherConnection: {
        ...connection,
        transportCommand: { ...command, position_seconds: 2000 },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    // The window ended, not the movie: the room is still playing it.
    expect(onReanchorSeek).toHaveBeenCalledExactlyOnceWith(2000);
    expect(video.play).toHaveBeenCalledOnce();
  });

  it("leaves an element that ended during playback to the room", async () => {
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(100);
    markPlaying(video);
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    Object.defineProperty(video, "ended", { configurable: true, value: true });
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    expect(video.playbackRate).toBe(1);
    expect(playerSeek).not.toHaveBeenCalled();
    expect(onReanchorSeek).not.toHaveBeenCalled();
  });

  it("keeps correcting drift through a wall-clock jump", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    markPlaying(video);
    const commanded = { ...connection, transportCommand: command };
    rerenderPlayer({ watchTogetherConnection: commanded });
    await act(() => vi.advanceTimersByTimeAsync(0));
    // The local clock steps back a minute; the re-estimated server offset
    // follows it, so the room position itself is unchanged.
    vi.setSystemTime(Date.now() - 60_000);
    rerenderPlayer({ watchTogetherConnection: { ...commanded, serverTimeOffsetMs: 60_000 } });
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(video.playbackRate).toBe(1.04);
  });

  it("names a command in routine reports only once its seek has landed", async () => {
    const { connection, video, command, rerenderPlayer } = setup(90);
    markPlaying(video);
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 200 },
    });
    const reports = () =>
      vi
        .mocked(connection.sendRoomMessage)
        .mock.calls.map(([message]) => message)
        .filter((message) => message.type === "state_report");
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(playerSeek).toHaveBeenCalledExactlyOnceWith(100);
    // The correction's media is still loading; the old stream plays on.
    await act(() => vi.advanceTimersByTimeAsync(1_500));
    expect(reports().at(-1)).toMatchObject({ position_seconds: 90 });
    expect(reports().at(-1)).not.toHaveProperty("command_id");
    video.currentTime = 100;
    fireFrameTimeUpdate(video);
    await act(() => vi.advanceTimersByTimeAsync(1_500));
    expect(reports().at(-1)).toMatchObject({ command_id: command.command_id });
  });

  it("names the last applied command in routine state reports", async () => {
    const { connection, video, command, rerenderPlayer } = setup(100);
    markPlaying(video);
    const reports = () =>
      vi
        .mocked(connection.sendRoomMessage)
        .mock.calls.map(([message]) => message)
        .filter((message) => message.type === "state_report");
    await act(() => vi.advanceTimersByTimeAsync(1_500));
    expect(reports().at(-1)).not.toHaveProperty("command_id");

    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(1_500));
    expect(reports().at(-1)).toMatchObject({ command_id: command.command_id });
  });

  it("keeps the rate until a received command executes", async () => {
    const { connection, video, command, rerenderPlayer } = setup(98);
    markPlaying(video);
    const commandedConnection = { ...connection, transportCommand: command };
    rerenderPlayer({ watchTogetherConnection: commandedConnection });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(video.playbackRate).toBe(1.04);

    // Like the server, the snapshot announcing the pause arrives with it.
    rerenderPlayer({
      watchTogetherConnection: {
        ...commandedConnection,
        room: { ...commandedConnection.room!, playback_state: "paused", is_paused: true },
        transportCommand: {
          ...command,
          command_id: "room-command-2",
          action: "pause",
          playback_state: "paused",
          position_seconds: 103,
          execute_at: new Date(Date.now() + 2_000).toISOString(),
        },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(1_999));
    expect(video.playbackRate).toBe(1.04);
    expect(video.pause).not.toHaveBeenCalled();
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(video.pause).toHaveBeenCalledOnce();
    expect(video.playbackRate).toBe(1);
  });
});

describe("VideoPlayer room correction reloads", () => {
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

  const ranges = (start: number, end: number) => ({
    length: 1,
    start: () => start,
    end: () => end,
  });

  function setup(localPosition: number) {
    const connection = roomConnection();
    const onReanchorSeek = vi.fn(() => true);
    const rendered = renderPlayer({
      plan: fixturePlanV3({
        ...directPlan,
        delivery: "server_remux_progressive",
        timeline: { ...directPlan.timeline, timeline_offset_seconds: 0, can_seek_anywhere: false },
      }),
      shouldAutoPlay: false,
      watchTogetherRoomId: "room-1",
      watchTogetherConnection: connection,
      onReanchorSeek,
    });
    const video = rendered.container.querySelector("video")!;
    video.currentTime = localPosition;
    fireFrameTimeUpdate(video);
    const command = (id: string, overrides: Record<string, unknown> = {}) => ({
      command_id: id,
      session_id: "session-1",
      selection_revision: 1,
      action: "play" as const,
      position_seconds: 100,
      execute_at: new Date().toISOString(),
      issued_at: new Date().toISOString(),
      playback_state: "playing" as const,
      ...overrides,
    });
    return { ...rendered, connection, video, command, onReanchorSeek };
  }

  /** Starts a correction reload to 100 in the current stream that never lands. */
  async function startUnlandedReload() {
    const rendered = setup(90);
    const { connection, video, command, rerenderPlayer } = rendered;
    markPlaying(video);
    Object.defineProperty(video, "seekable", { configurable: true, value: ranges(0, 200) });
    Object.defineProperty(video, "buffered", { configurable: true, value: ranges(85, 95) });
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command("c1") } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(playerSeek).toHaveBeenCalledExactlyOnceWith(100);
    // The seek settles short of the reload's landing window.
    video.currentTime = 99.2;
    fireFrameTimeUpdate(video);
    expect(video.playbackRate).toBe(1);
    return rendered;
  }

  it("waits for a reload, then resumes drift correction once it goes stale", async () => {
    const { video } = await startUnlandedReload();
    await act(() => vi.advanceTimersByTimeAsync(29_500));
    // The room has moved on, but the loop waits for the reload to land.
    expect(video.playbackRate).toBe(1);
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(video.playbackRate).toBe(1.04);
    expect(playerSeek).toHaveBeenCalledOnce();
  });

  it("starts a replacement reload right after a stale one expires", async () => {
    const { connection, command, rerenderPlayer } = await startUnlandedReload();
    // The loop expires the reload once it is stale.
    await act(() => vi.advanceTimersByTimeAsync(30_500));
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        transportCommand: command("c2", { position_seconds: 140 }),
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    // No backoff holds the viewer behind: the correction loads at once.
    expect(playerSeek).toHaveBeenCalledTimes(2);
    expect(playerSeek).toHaveBeenLastCalledWith(140);
  });

  it("drops a reload that a room pause replaces", async () => {
    const { connection, video, command, rerenderPlayer } = await startUnlandedReload();
    const paused = { ...connection.room!, playback_state: "paused" as const, is_paused: true };
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: paused,
        transportCommand: command("c2", {
          action: "pause",
          playback_state: "paused",
          position_seconds: 99.5,
        }),
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        transportCommand: command("c3", { position_seconds: 99.2 }),
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(video.playbackRate).toBe(1);
    // The room plays on from 99.2 while this element stays put. With the old
    // reload forgotten, the loop corrects it after its usual settle.
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(video.playbackRate).toBe(1.04);
  });

  it("does not rebuild again for a pause that lands on a rebuild still loading", async () => {
    const { connection, command, rerenderPlayer, onReanchorSeek } = setup(90);
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command("c1") } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(onReanchorSeek).toHaveBeenCalledExactlyOnceWith(100);

    const paused = { ...connection.room!, playback_state: "paused" as const, is_paused: true };
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: paused,
        transportCommand: command("c2", {
          action: "pause",
          playback_state: "paused",
          position_seconds: 100.5,
        }),
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(onReanchorSeek).toHaveBeenCalledOnce();

    // A pause somewhere else still moves there.
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        room: paused,
        transportCommand: command("c3", {
          action: "pause",
          playback_state: "paused",
          position_seconds: 300,
        }),
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(onReanchorSeek).toHaveBeenLastCalledWith(300);
  });
});

describe("VideoPlayer room sync without pitch-kept rate changes", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-01T12:00:00Z"));
    playerSeek.mockClear();
    attachBrowserEngine.mockClear();
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
    engineRoute.chosen = true;
  });

  afterEach(() => {
    engineRoute.chosen = false;
    cleanup();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  async function setup(localPosition: number) {
    const connection = roomConnection();
    const rendered = renderPlayer({
      shouldAutoPlay: false,
      watchTogetherRoomId: "room-1",
      watchTogetherConnection: connection,
      browserEngine: {
        containers: ["mkv"],
        videoCodecs: ["h264"],
        audioCodecs: ["aac"],
        hdr: false,
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(attachBrowserEngine).toHaveBeenCalled();
    const video = rendered.container.querySelector("video")!;
    markPlaying(video);
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 3_000 },
    });
    video.currentTime = localPosition;
    const command = {
      command_id: "room-command-1",
      session_id: "session-1",
      selection_revision: 1,
      action: "play" as const,
      position_seconds: 100,
      execute_at: new Date().toISOString(),
      issued_at: new Date().toISOString(),
      playback_state: "playing" as const,
    };
    return { ...rendered, connection, video, command };
  }

  it("seeks a play correction in place instead of nudging", async () => {
    const { connection, video, command, rerenderPlayer } = await setup(98);
    // Reachable in place but not buffered: still a direct seek.
    Object.defineProperty(video, "buffered", {
      configurable: true,
      value: { length: 1, start: () => 90, end: () => 99 },
    });
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(playerSeek).toHaveBeenCalledExactlyOnceWith(100);
    expect(video.playbackRate).toBe(1);

    // Another correction right away is not held back by reload pacing.
    video.currentTime = 98;
    fireFrameTimeUpdate(video);
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        transportCommand: { ...command, command_id: "room-command-2", position_seconds: 103 },
      },
    });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(playerSeek).toHaveBeenLastCalledWith(103);
  });

  it("leaves drift up to a second alone, then seeks from the loop", async () => {
    const { connection, video, command, rerenderPlayer } = await setup(99.2);
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(playerSeek).not.toHaveBeenCalled();

    // One second later the room is at 101 and the element has not moved.
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(playerSeek).toHaveBeenCalledExactlyOnceWith(101);
    expect(video.playbackRate).toBe(1);
  });

  it("aims ahead of a slow seek instead of chasing the room", async () => {
    const { connection, video, command, rerenderPlayer } = await setup(97);
    // The engine's clock stands still for the two seconds a seek takes, then
    // lands where it was aimed; otherwise it plays at 1x.
    const seekMs = 2_000;
    let seek: { target: number; remainingMs: number } | null = null;
    const setSeeking = (value: boolean) =>
      Object.defineProperty(video, "seeking", { configurable: true, value });
    playerSeek.mockImplementation((nativeSeconds: number) => {
      seek = { target: nativeSeconds, remainingMs: seekMs };
      setSeeking(true);
    });
    const play = async (totalMs: number) => {
      for (let elapsed = 0; elapsed < totalMs; elapsed += 100) {
        await act(() => vi.advanceTimersByTimeAsync(100));
        const running = seek as { target: number; remainingMs: number } | null;
        if (running === null) {
          video.currentTime += 0.1;
          continue;
        }
        running.remainingMs -= 100;
        if (running.remainingMs > 0) continue;
        seek = null;
        video.currentTime = running.target;
        setSeeking(false);
        fireEvent.seeked(video);
        fireFrameTimeUpdate(video);
      }
    };

    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(playerSeek).toHaveBeenCalledExactlyOnceWith(100);
    await play(20_000);

    // The first seek lands two seconds short; the next aims that far ahead
    // and lands with the room. Aimed at the room alone, every seek would land
    // short and the loop would seek again every few seconds.
    expect(playerSeek).toHaveBeenCalledTimes(2);
    const roomPosition = 100 + 20;
    expect(Math.abs(roomPosition - video.currentTime)).toBeLessThan(1);
  });
});

describe("VideoPlayer playback speed", () => {
  beforeEach(() => {
    playerSeek.mockClear();
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("keeps the chosen speed through a pause and a seek", () => {
    const { container } = renderPlayer({ shouldAutoPlay: false });
    const video = container.querySelector("video")!;

    act(() => controls.current!.onPlaybackSpeedChange!(1.5));
    expect(controls.current!.playbackSpeed).toBe(1.5);
    expect(video.playbackRate).toBe(1.5);
    // A media reload resets playbackRate to defaultPlaybackRate.
    expect(video.defaultPlaybackRate).toBe(1.5);

    fireEvent.pause(video);
    expect(video.playbackRate).toBe(1.5);

    act(() => controls.current!.onSeek(50));
    expect(video.playbackRate).toBe(1.5);
  });

  it("leaves the rate to the room in a Watch Party", () => {
    const { container } = renderPlayer({
      shouldAutoPlay: false,
      watchTogetherRoomId: "room-1",
      watchTogetherConnection: roomConnection(),
    });
    const video = container.querySelector("video")!;

    expect(controls.current!.onPlaybackSpeedChange).toBeUndefined();
    expect(video.playbackRate).toBe(1);
    expect(video.defaultPlaybackRate).toBe(1);
  });

  it("restores the viewer's speed after leaving a Watch Party", () => {
    const { container, rerenderPlayer } = renderPlayer({ shouldAutoPlay: false });
    const video = container.querySelector("video")!;
    act(() => controls.current!.onPlaybackSpeedChange!(1.5));

    const connection = roomConnection();
    rerenderPlayer({ watchTogetherRoomId: "room-1", watchTogetherConnection: connection });
    expect(controls.current!.onPlaybackSpeedChange).toBeUndefined();
    expect(video.playbackRate).toBe(1);

    rerenderPlayer({
      watchTogetherRoomId: "room-1",
      watchTogetherConnection: { ...connection, closedReason: "ended" },
    });
    expect(controls.current!.onPlaybackSpeedChange).toBeDefined();
    expect(video.playbackRate).toBe(1.5);
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

  it("uses profile intervals for controls and detached transport, and updates without restarting", async () => {
    const ready = vi.fn();
    const { container, rerenderPlayer } = renderPlayer({
      shouldAutoPlay: false,
      plan: { ...directPlan, source: { ...directPlan.source, duration_seconds: 1000 } },
      seekIntervals: { back: 15, forward: 60 },
      onPlaybackTransportReady: ready,
    });
    const video = container.querySelector("video")!;
    // The element reaching a seek target settles it, as a real timeupdate would.
    const settleAt = (seconds: number) => {
      Object.defineProperty(video, "currentTime", { configurable: true, value: seconds });
      fireEvent.timeUpdate(video);
    };
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    settleAt(100);
    fireEvent.canPlay(video);
    await waitFor(() => expect(controls.current?.skipSeconds?.forward).toBe(60));
    act(() => controls.current?.onSkip?.back());
    expect(playerSeek).toHaveBeenLastCalledWith(85);
    settleAt(85);
    act(() => ready.mock.lastCall![0].skipForward());
    expect(playerSeek).toHaveBeenLastCalledWith(145);
    settleAt(145);
    const callsBeforeTick = ready.mock.calls.length;
    fireEvent.timeUpdate(video);
    expect(ready.mock.calls.length).toBe(callsBeforeTick);
    const load = vi.mocked(HTMLMediaElement.prototype.load);
    load.mockClear();
    rerenderPlayer({ seekIntervals: { back: 5, forward: 90 } });
    // An interval change neither reloads media nor re-publishes the transport.
    expect(ready.mock.calls.length).toBe(callsBeforeTick);
    act(() => controls.current?.onSkip?.forward());
    expect(playerSeek).toHaveBeenLastCalledWith(235);
    expect(load).not.toHaveBeenCalled();
    settleAt(235);
    settleAt(998);
    act(() => ready.mock.lastCall![0].skipForward());
    expect(playerSeek).toHaveBeenLastCalledWith(1000);
    settleAt(1000);
    settleAt(2);
    act(() => ready.mock.lastCall![0].skipBack());
    expect(playerSeek).toHaveBeenLastCalledWith(0);
  });

  it("chains skips from a pending seek instead of the element's stale clock", async () => {
    const ready = vi.fn();
    const { container } = renderPlayer({
      shouldAutoPlay: false,
      plan: { ...directPlan, source: { ...directPlan.source, duration_seconds: 1000 } },
      seekIntervals: { back: 15, forward: 60 },
      onPlaybackTransportReady: ready,
    });
    const video = container.querySelector("video")!;
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    Object.defineProperty(video, "currentTime", { configurable: true, value: 100 });
    fireEvent.canPlay(video);
    await waitFor(() => expect(ready).toHaveBeenCalled());
    // Two quick taps before the element catches up: 100 → 160 → 220, not 160 twice.
    act(() => ready.mock.lastCall![0].skipForward());
    act(() => ready.mock.lastCall![0].skipForward());
    expect(playerSeek).toHaveBeenLastCalledWith(220);
    // A scrub far ahead followed by a skip extends the scrub.
    act(() => ready.mock.lastCall![0].seekTo(700));
    act(() => ready.mock.lastCall![0].skipBack());
    expect(playerSeek).toHaveBeenLastCalledWith(685);
  });

  it("returns relative skips to the media clock after a reanchor fails", () => {
    const ready = vi.fn();
    // The replan is still in flight when it fails.
    const reanchor = vi.fn((_seconds: number) => new Promise<boolean>(() => {}));
    const { container, rerenderPlayer } = renderPlayer({
      shouldAutoPlay: false,
      plan: {
        ...directPlan,
        timeline: { ...directPlan.timeline, can_seek_anywhere: false },
      },
      onPlaybackTransportReady: ready,
      onReanchorSeek: reanchor,
    });
    const video = container.querySelector("video")!;
    fireEvent.loadedMetadata(video);
    Object.defineProperty(video, "currentTime", { configurable: true, value: 100 });
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 110 },
    });
    const transport = ready.mock.lastCall![0];
    act(() => transport.skipForward());
    expect(reanchor).toHaveBeenLastCalledWith(130);
    rerenderPlayer({ replanning: true });
    rerenderPlayer({ replanning: false, replanError: "Reanchor request failed." });
    act(() => transport.skipBack());
    expect(playerSeek).toHaveBeenLastCalledWith(90);
    expect(reanchor).toHaveBeenCalledTimes(1);
  });

  it("returns relative skips to the media clock when a replan refuses a reanchor", async () => {
    const ready = vi.fn();
    const replans: Array<(accepted: boolean) => void> = [];
    const reanchor = vi.fn(
      (_seconds: number) => new Promise<boolean>((resolve) => replans.push(resolve)),
    );
    const { container } = renderPlayer({
      shouldAutoPlay: false,
      plan: {
        ...directPlan,
        timeline: { ...directPlan.timeline, can_seek_anywhere: false },
      },
      onPlaybackTransportReady: ready,
      onReanchorSeek: reanchor,
    });
    const video = container.querySelector("video")!;
    fireEvent.loadedMetadata(video);
    Object.defineProperty(video, "currentTime", { configurable: true, value: 100 });
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 110 },
    });
    const transport = ready.mock.lastCall![0];
    act(() => transport.skipForward());
    act(() => transport.skipForward());
    expect(reanchor).toHaveBeenLastCalledWith(160);
    // The superseded replan resolving false must not drop the newer target.
    await act(async () => replans[0]!(false));
    act(() => transport.skipBack());
    expect(playerSeek).not.toHaveBeenCalled();
    expect(reanchor).toHaveBeenLastCalledWith(150);
    // Refusing the current target releases it without a replan error.
    await act(async () => replans[2]!(false));
    act(() => transport.skipBack());
    expect(playerSeek).toHaveBeenLastCalledWith(90);
    expect(reanchor).toHaveBeenCalledTimes(3);
  });

  it("keeps rejected local seeks out of the next skip origin", () => {
    const ready = vi.fn();
    const { container } = renderPlayer({
      shouldAutoPlay: false,
      plan: {
        ...directPlan,
        timeline: { ...directPlan.timeline, can_seek_anywhere: false },
      },
      onPlaybackTransportReady: ready,
    });
    const video = container.querySelector("video")!;
    fireEvent.loadedMetadata(video);
    Object.defineProperty(video, "currentTime", { configurable: true, value: 100 });
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 0, end: () => 200 },
    });
    const transport = ready.mock.lastCall![0];
    act(() => transport.seekTo(500));
    act(() => transport.skipForward());
    expect(playerSeek).toHaveBeenLastCalledWith(130);
    act(() => transport.seekTo(500));
    act(() => transport.skipForward());
    expect(playerSeek.mock.calls).toEqual([[130], [160]]);
  });

  it("chains accepted room requests while rejected requests preserve the pending target", () => {
    const ready = vi.fn();
    const sendRoomMessage = vi.fn((_message: Record<string, unknown>) => ({ ok: true }));
    const { container } = renderPlayer({
      shouldAutoPlay: false,
      onPlaybackTransportReady: ready,
      watchTogetherConnection: roomConnection({
        room: {
          ...roomConnection().room!,
          playback_state: "paused",
          is_paused: true,
          member_count: 1,
          self_role: "host",
          self_can_control_transport: true,
          self_can_manage_room: true,
        },
        sendRoomMessage,
      }),
    });
    const video = container.querySelector("video")!;
    fireEvent.loadedMetadata(video);
    Object.defineProperty(video, "currentTime", { configurable: true, value: 100 });
    const transport = ready.mock.lastCall![0];
    sendRoomMessage.mockClear();
    sendRoomMessage.mockReturnValueOnce({ ok: false });
    act(() => transport.skipForward());
    act(() => transport.skipForward());
    act(() => transport.skipForward());
    sendRoomMessage.mockReturnValueOnce({ ok: false });
    act(() => transport.seekTo(500));
    act(() => transport.skipBack());
    expect(sendRoomMessage.mock.calls.map(([message]) => message)).toEqual(
      [130, 130, 160, 500, 150].map((position_seconds) => ({
        type: "transport_request",
        action: "seek",
        position_seconds,
        is_paused: true,
      })),
    );
    expect(playerSeek).not.toHaveBeenCalled();
    expect(video.currentTime).toBe(100);
  });

  it("reanchors configured skips on the media timeline across a remux window boundary", () => {
    const ready = vi.fn();
    const reanchor = vi.fn((_seconds: number) => new Promise<boolean>(() => {}));
    const { container } = renderPlayer({
      shouldAutoPlay: false,
      plan: {
        ...directPlan,
        timeline: {
          ...directPlan.timeline,
          timeline_offset_seconds: 400,
          can_seek_anywhere: false,
        },
      },
      seekIntervals: { back: 15, forward: 60 },
      onPlaybackTransportReady: ready,
      onReanchorSeek: reanchor,
    });
    const video = container.querySelector("video")!;
    fireEvent.loadedMetadata(video);
    Object.defineProperty(video, "currentTime", { configurable: true, value: 10 });
    act(() => ready.mock.lastCall![0].skipBack());
    expect(reanchor).toHaveBeenLastCalledWith(395);
    // The reanchor is still being replanned: the next skip continues from its
    // target rather than from the element, which still sits at media time 410.
    act(() => controls.current?.onSkip?.forward());
    expect(reanchor).toHaveBeenLastCalledWith(455);
  });

  it("toggles play on a mouse single click and fullscreen on a double click", async () => {
    vi.useFakeTimers();
    const pause = vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    try {
      const { container } = renderPlayer({ shouldAutoPlay: false });
      const video = container.querySelector("video");
      if (!video) throw new Error("expected video element");
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      Object.defineProperty(video, "paused", { configurable: true, value: false });
      fireEvent.canPlay(video);
      await vi.waitFor(() => expect(controls.current?.onSurfaceTap).toBeTypeOf("function"));

      const playerContainer = video.parentElement;
      if (!playerContainer) throw new Error("expected player container");
      const requestFullscreen = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(playerContainer, "requestFullscreen", {
        configurable: true,
        value: requestFullscreen,
      });

      fireEvent.click(video);
      expect(pause).not.toHaveBeenCalled();
      act(() => vi.advanceTimersByTime(250));
      expect(pause).toHaveBeenCalledOnce();
      expect(requestFullscreen).not.toHaveBeenCalled();

      fireEvent.click(video, { detail: 1 });
      fireEvent.click(video, { detail: 2 });
      expect(requestFullscreen).toHaveBeenCalledOnce();
      act(() => vi.advanceTimersByTime(250));
      expect(pause).toHaveBeenCalledOnce();

      // Two rapid clicks the browser does not count as a double (detail 1
      // both times, e.g. far apart) toggle play/pause once and never enter
      // fullscreen.
      fireEvent.click(video, { detail: 1 });
      fireEvent.click(video, { detail: 1 });
      act(() => vi.advanceTimersByTime(250));
      expect(requestFullscreen).toHaveBeenCalledOnce();
      expect(pause).toHaveBeenCalledTimes(2);

      // A double click slower than our window but recognized by the browser
      // (event.detail === 2) reverts the play/pause that already fired,
      // regardless of how long the OS double-click interval is, and toggles
      // fullscreen.
      fireEvent.click(video, { detail: 1 });
      act(() => vi.advanceTimersByTime(250));
      expect(pause).toHaveBeenCalledTimes(3);
      expect(play).not.toHaveBeenCalled();
      act(() => vi.advanceTimersByTime(5_000));
      fireEvent.click(video, { detail: 2 });
      expect(requestFullscreen).toHaveBeenCalledTimes(2);
      expect(pause).toHaveBeenCalledTimes(3);
      expect(play).toHaveBeenCalledOnce();

      // The third click of a triple click is ignored.
      fireEvent.click(video, { detail: 3 });
      act(() => vi.advanceTimersByTime(250));
      expect(requestFullscreen).toHaveBeenCalledTimes(2);
      expect(pause).toHaveBeenCalledTimes(3);
      expect(play).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });

  it("toggles controls on a coarse-pointer single tap and seeks on a left double tap", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: true,
        media: "(pointer: coarse)",
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    );
    try {
      const { container } = renderPlayer({
        shouldAutoPlay: false,
        seekIntervals: { back: 15, forward: 60 },
      });
      const video = container.querySelector("video");
      if (!video) throw new Error("expected video element");
      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      Object.defineProperty(video, "currentTime", { configurable: true, value: 50 });
      fireEvent.canPlay(video);
      await vi.waitFor(() => expect(controls.current?.onSurfaceTap).toBeTypeOf("function"));

      act(() =>
        controls.current?.onSurfaceTap?.({
          clientX: 200,
          currentTarget: { getBoundingClientRect: () => ({ left: 0, width: 390 }) },
        } as unknown as React.MouseEvent<HTMLElement>),
      );
      act(() => vi.advanceTimersByTime(250));
      expect(controls.current?.visible).toBe(false);

      const leftTap = {
        clientX: 20,
        currentTarget: { getBoundingClientRect: () => ({ left: 0, width: 390 }) },
      } as unknown as React.MouseEvent<HTMLElement>;
      act(() => {
        controls.current?.onSurfaceTap?.(leftTap);
        controls.current?.onSurfaceTap?.(leftTap);
      });
      expect(playerSeek).toHaveBeenCalledWith(35);
    } finally {
      vi.useRealTimers();
      vi.unstubAllGlobals();
    }
  });

  it("loads a replacement transport without resuming paused playback", async () => {
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    const { container } = renderPlayer({ shouldAutoPlay: false });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    fireEvent.canPlay(video);
    expect(play).not.toHaveBeenCalled();
  });

  it("does not report a startup timeout while HLS is intentionally paused", () => {
    vi.useFakeTimers();
    try {
      const onPlanFailure = vi.fn();
      const hlsPlan = fixturePlanV3({
        delivery: "server_remux_hls",
        stream: {
          url: "/stream/session-1/master.m3u8",
          protocol: "hls",
          headers: {},
          header_refresh: "none",
        },
      });
      const { container } = renderPlayer({
        plan: hlsPlan,
        shouldAutoPlay: false,
        onPlanFailure,
      });
      const video = container.querySelector("video");
      if (!video) throw new Error("expected video element");

      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      fireEvent.canPlay(video);
      act(() => vi.advanceTimersByTime(HLS_STARTUP_TIMEOUT_MS));

      expect(HTMLMediaElement.prototype.play).not.toHaveBeenCalled();
      expect(onPlanFailure).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("surfaces a refused replan only for the transport-dead plan revision", async () => {
    const onPlanFailure = vi.fn();
    const { container, rerenderPlayer } = renderPlayer({ onPlanFailure });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    setMediaError(video, "decoder failed");
    fireEvent.error(video);
    expect(onPlanFailure).toHaveBeenCalledOnce();

    rerenderPlayer({ replanError: "Recovery was refused." });
    expect(await screen.findByText("Recovery was refused.")).toBeInTheDocument();

    const nextPlan = fixturePlanV3({
      ...directPlan,
      plan_id: "plan:2222222222222222",
      plan_attempt_key: "v3:2222222222222222",
    });
    rerenderPlayer({ plan: nextPlan, planRevision: 2, replanError: null });
    await waitFor(() =>
      expect(screen.queryByText("Recovery was refused.")).not.toBeInTheDocument(),
    );

    rerenderPlayer({ plan: nextPlan, planRevision: 2, replanError: "Unrelated replan error." });
    await act(async () => Promise.resolve());
    expect(screen.queryByText("Unrelated replan error.")).not.toBeInTheDocument();
  });

  it("re-arms the plan failure guard after a transient recovery request failure", async () => {
    const onPlanFailure = vi.fn();
    const { container, rerenderPlayer } = renderPlayer({ onPlanFailure });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    setMediaError(video, "decoder failed");
    fireEvent.error(video);
    expect(onPlanFailure).toHaveBeenCalledOnce();

    rerenderPlayer({ replanError: "Temporary recovery failure." });
    await screen.findByText("Temporary recovery failure.");

    fireEvent.error(video);
    expect(onPlanFailure).toHaveBeenCalledTimes(2);
  });

  it("replans off a plan the server invalidated", async () => {
    const onPlanInvalidated = vi.fn().mockResolvedValue(true);
    renderPlayer({ onPlanInvalidated });
    const onCommand = realtimeOptions.current?.onCommand;
    if (!onCommand) throw new Error("expected the realtime command handler");

    await act(async () => {
      await onCommand(planInvalidatedCommand());
    });

    expect(onPlanInvalidated).toHaveBeenCalledWith(directPlan.plan_id, "video_copy_unsafe", 0);
  });

  // A rejected result is the server's cue to stop the session, which is what
  // lets the client's own recovery mint a fresh attempt against the persisted
  // verdict. Swallowing the failure here would leave the copy route playing.
  it("rejects the invalidation command when no replacement plan is adopted", async () => {
    const onPlanInvalidated = vi.fn().mockResolvedValue(false);
    renderPlayer({ onPlanInvalidated });
    const onCommand = realtimeOptions.current?.onCommand;
    if (!onCommand) throw new Error("expected the realtime command handler");

    await expect(onCommand(planInvalidatedCommand())).rejects.toThrow(
      "plan_invalidation_replan_failed",
    );
  });

  it("rejects an invalidation command that names no plan", async () => {
    const onPlanInvalidated = vi.fn().mockResolvedValue(true);
    renderPlayer({ onPlanInvalidated });
    const onCommand = realtimeOptions.current?.onCommand;
    if (!onCommand) throw new Error("expected the realtime command handler");

    await expect(
      onCommand(planInvalidatedCommand({ reason: "video_copy_unsafe" })),
    ).rejects.toThrow("invalid_plan_invalidated_payload");
    expect(onPlanInvalidated).not.toHaveBeenCalled();
  });

  it("does not retry an auto-selected subtitle after its replan is refused", async () => {
    const onSubtitleTrackChange = vi.fn();
    const sidecarTrack: PlayerSubtitleInfo = {
      index: 2,
      media_file_id: 7,
      track_id: "file:7:subtitle:2",
      language: "en",
      codec: "srt",
      label: "English",
      source: "external",
      url: "/stream/session-1/subtitles/2.vtt",
    };
    const { rerenderPlayer } = renderPlayer({
      subtitleUrls: [sidecarTrack],
      subtitleMode: "always",
      preferredSubtitleLanguage: "en",
      onSubtitleTrackChange,
    });

    await waitFor(() => expect(onSubtitleTrackChange).toHaveBeenCalledOnce());
    expect(onSubtitleTrackChange).toHaveBeenCalledWith(2, 0);

    rerenderPlayer({ replanError: "Silo could not apply the subtitle selection." });

    await waitFor(() => expect(controls.current?.activeSubtitleIndex).toBeNull());
    expect(onSubtitleTrackChange).toHaveBeenCalledOnce();

    const nextPlan = fixturePlanV3({
      ...directPlan,
      plan_id: "plan:next-session",
      plan_attempt_key: "v3:next-session",
      session_id: "session-2",
    });
    rerenderPlayer({ sessionId: "session-2", plan: nextPlan, replanError: null });

    await waitFor(() => expect(controls.current?.activeSubtitleIndex).toBe(2));
    expect(onSubtitleTrackChange).toHaveBeenCalledTimes(2);
    expect(onSubtitleTrackChange).toHaveBeenLastCalledWith(2, 0);
  });

  // The rollback is otherwise silent: the refusal only renders inside the
  // quality menu, which a user who just picked a subtitle never opens.
  it("toasts the server's refusal when a subtitle change is rolled back", async () => {
    const onSubtitleTrackChange = vi.fn();
    const sidecarTrack: PlayerSubtitleInfo = {
      index: 2,
      media_file_id: 7,
      track_id: "file:7:subtitle:2",
      language: "en",
      codec: "srt",
      label: "English",
      source: "external",
      url: "/stream/session-1/subtitles/2.vtt",
    };
    const { rerenderPlayer } = renderPlayer({
      subtitleUrls: [sidecarTrack],
      subtitleMode: "always",
      preferredSubtitleLanguage: "en",
      onSubtitleTrackChange,
    });

    await waitFor(() => expect(onSubtitleTrackChange).toHaveBeenCalledOnce());
    expect(toastError).not.toHaveBeenCalled();

    rerenderPlayer({
      replanError: "The selected subtitle must be burned into the video, but 4K is disabled.",
      replanErrorTitle: "That subtitle track can't be used",
    });

    await waitFor(() => expect(toastError).toHaveBeenCalledOnce());
    expect(toastError).toHaveBeenCalledWith("That subtitle track can't be used", {
      description: "The selected subtitle must be burned into the video, but 4K is disabled.",
    });
  });

  it("falls back to a generic subtitle refusal title and toasts once", async () => {
    const onSubtitleTrackChange = vi.fn();
    const sidecarTrack: PlayerSubtitleInfo = {
      index: 2,
      media_file_id: 7,
      track_id: "file:7:subtitle:2",
      language: "en",
      codec: "srt",
      label: "English",
      source: "external",
      url: "/stream/session-1/subtitles/2.vtt",
    };
    const { rerenderPlayer } = renderPlayer({
      subtitleUrls: [sidecarTrack],
      subtitleMode: "always",
      preferredSubtitleLanguage: "en",
      onSubtitleTrackChange,
    });

    await waitFor(() => expect(onSubtitleTrackChange).toHaveBeenCalledOnce());
    rerenderPlayer({ replanError: "Silo could not apply the subtitle selection." });
    await waitFor(() => expect(toastError).toHaveBeenCalledOnce());
    expect(toastError).toHaveBeenCalledWith("That subtitle track can't be used", {
      description: "Silo could not apply the subtitle selection.",
    });

    // The ref cleared on rollback, so a re-render with the same refusal must
    // not stack a second toast.
    rerenderPlayer({ replanError: "Silo could not apply the subtitle selection." });
    await waitFor(() => expect(controls.current?.activeSubtitleIndex).toBeNull());
    expect(toastError).toHaveBeenCalledOnce();
  });
});

describe("VideoPlayer first frame", () => {
  beforeEach(() => {
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("reports the first frame of each transport once", async () => {
    const onFirstFrame = vi.fn();
    const { container, rerenderPlayer } = renderPlayer({ onFirstFrame });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    expect(onFirstFrame).not.toHaveBeenCalled();

    fireEvent.playing(video);
    fireFrameTimeUpdate(video);
    fireFrameTimeUpdate(video);
    expect(onFirstFrame).toHaveBeenCalledTimes(1);

    // A replan loads a new transport, which has a first frame of its own.
    rerenderPlayer({ planRevision: 2 });
    expect(onFirstFrame).toHaveBeenCalledTimes(1);
    fireEvent.playing(video);
    expect(onFirstFrame).toHaveBeenCalledTimes(2);
  });

  it("ignores the timeupdate a transport teardown queues", async () => {
    const onFirstFrame = vi.fn();
    const { container, rerenderPlayer } = renderPlayer({ onFirstFrame });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    fireEvent.playing(video);
    expect(onFirstFrame).toHaveBeenCalledTimes(1);

    // Switching transports empties the element with load(), which resets the
    // position and queues a timeupdate while no source has data. It is not the
    // new transport's first frame, and the loading overlay stays up for it.
    rerenderPlayer({ planRevision: 2 });
    fireEvent.timeUpdate(video);
    fireEvent.seeked(video);
    expect(onFirstFrame).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("status", { name: "Loading video" })).toBeInTheDocument();

    fireEvent.playing(video);
    expect(onFirstFrame).toHaveBeenCalledTimes(2);
    fireFrameTimeUpdate(video);
    expect(onFirstFrame).toHaveBeenCalledTimes(2);
  });
});

describe("VideoPlayer intro skip prompt", () => {
  beforeEach(() => {
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  async function enterIntro(mode: "never" | "ask" | "always") {
    const rendered = renderPlayer({
      intro: { start: 10, end: 20 },
      introSkipMode: mode,
    });
    const video = rendered.container.querySelector("video");
    if (!video) throw new Error("expected video element");

    fireEvent.loadedMetadata(video);
    video.currentTime = 12;
    fireFrameTimeUpdate(video);
    await act(async () => Promise.resolve());
    return rendered;
  }

  it("renders the ask pill and consumes Escape", async () => {
    await enterIntro("ask");
    expect(await screen.findByRole("button", { name: "Skip Intro" })).toBeInTheDocument();

    fireEvent.keyDown(document, { key: "Escape" });

    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Skip Intro" })).not.toBeInTheDocument(),
    );
  });

  it("renders the undo action after an automatic skip", async () => {
    await enterIntro("always");
    const undo = await screen.findByRole("button", {
      name: "Watch Intro",
    });

    fireEvent.click(undo);

    await waitFor(() => expect(undo).not.toBeInTheDocument());
  });

  it("renders no intro action in never mode", async () => {
    await enterIntro("never");

    expect(screen.queryByRole("button", { name: /Intro/ })).not.toBeInTheDocument();
  });

  it("prompts for nothing while the intro mode is still unknown", async () => {
    const rendered = renderPlayer({ intro: { start: 10, end: 20 }, introSkipMode: null });
    const video = rendered.container.querySelector("video");
    if (!video) throw new Error("expected video element");

    video.currentTime = 12;
    fireEvent.timeUpdate(video);
    await act(async () => Promise.resolve());

    expect(screen.queryByRole("button", { name: /Intro/ })).not.toBeInTheDocument();
    // Nothing was skipped either: an unknown mode must not act like "always".
    expect(video.currentTime).toBe(12);
  });

  // Space belongs to whatever control has focus. Consuming it at the document
  // both skipped the intro and swallowed the press meant for Play/Pause.
  it("leaves Select to the focused transport control", async () => {
    const rendered = await enterIntro("ask");
    const prompt = await screen.findByRole("button", { name: "Skip Intro" });

    const transport = document.createElement("button");
    transport.textContent = "Play";
    rendered.container.firstElementChild?.appendChild(transport);
    transport.focus();

    const notPrevented = fireEvent.keyDown(transport, { key: " " });

    expect(notPrevented).toBe(true);
    expect(prompt).toBeInTheDocument();
  });

  it("acts on Select while the pill itself is focused", async () => {
    await enterIntro("ask");
    const prompt = await screen.findByRole("button", { name: "Skip Intro" });
    prompt.focus();

    fireEvent.keyDown(prompt, { key: " " });

    await waitFor(() => expect(prompt).not.toBeInTheDocument());
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

  it("applies player_start_seconds before native HLS playback", async () => {
    const plan = fixturePlanV3({
      delivery: "server_remux_hls",
      stream: {
        url: "/playback/transcode/session-1/master.m3u8",
        protocol: "hls",
        headers: {},
        header_refresh: "none",
      },
      timeline: {
        source_start_seconds: 42,
        player_start_seconds: 7,
        stream_origin_seconds: 35,
        timeline_offset_seconds: 0,
        can_seek_anywhere: true,
        seek_restoration: "player_position",
      },
    });
    const { container } = renderPlayer({ plan, initialPosition: 42 });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    fireEvent.loadedMetadata(video);

    expect(video.currentTime).toBe(7);
    expect(subtitleTimeline.textOffsetSeconds).toBe(0);
    expect(subtitleTimeline.assOffsetSeconds).toBe(0);
  });

  // Chrome keeps a Matroska file's container start time on its clock, so a
  // file that starts 30 s in reports currentTime 30 at its first frame.
  // Subtitle cues, the resume seek, and the reported position all have to
  // map through that origin or they drift by it.
  it("maps direct play onto the media timeline when the engine clock starts late", async () => {
    const origin = 29.977;
    const plan = fixturePlanV3({
      ...directPlan,
      timeline: { ...directPlan.timeline, player_start_seconds: 1300, timeline_offset_seconds: 0 },
    });
    const { container } = renderPlayer({ plan, initialPosition: 1300 });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    video.currentTime = origin;
    fireEvent.loadedMetadata(video);

    expect(video.currentTime).toBeCloseTo(1300 + origin);
    await waitFor(() => expect(subtitleTimeline.textOffsetSeconds).toBeCloseTo(-origin));
    expect(subtitleTimeline.assOffsetSeconds).toBeCloseTo(-origin);
    fireFrameTimeUpdate(video);
    expect(controls.current!.currentTime).toBeCloseTo(1300);

    playerSeek.mockClear();
    act(() => {
      controls.current!.onSeek(1400);
    });
    expect(playerSeek).toHaveBeenCalledWith(expect.closeTo(1400 + origin));
  });

  // The engine starts its clock at the file's first frame itself, so the
  // origin a native load measured must not carry into an engine session, and
  // a native load after the engine measures its own again.
  it("drops a native clock origin while the browser engine plays the original", async () => {
    const engine = {
      containers: ["mkv"],
      videoCodecs: ["h264"],
      audioCodecs: ["aac"],
      hdr: false,
    };
    const { container, rerenderPlayer } = renderPlayer();
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    video.currentTime = 29.977;
    fireEvent.loadedMetadata(video);
    await waitFor(() => expect(subtitleTimeline.textOffsetSeconds).toBeCloseTo(-29.977));

    engineRoute.chosen = true;
    try {
      rerenderPlayer({ planRevision: 2, browserEngine: engine });
      await waitFor(() => expect(attachBrowserEngine).toHaveBeenCalled());
      await waitFor(() => expect(subtitleTimeline.textOffsetSeconds).toBe(0));
      expect(subtitleTimeline.assOffsetSeconds).toBe(0);
    } finally {
      engineRoute.chosen = false;
    }

    rerenderPlayer({ planRevision: 3, browserEngine: null });
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    video.currentTime = 1.4;
    fireEvent.loadedMetadata(video);
    await waitFor(() => expect(subtitleTimeline.textOffsetSeconds).toBeCloseTo(-1.4));
  });

  // A route refused before any media event has no position of its own yet;
  // recovery must resume where this plan was starting, not at 0.
  it("reports the resume point when the engine refuses a resumed original", async () => {
    const onPlanFailure = vi.fn();
    const plan = fixturePlanV3({
      ...directPlan,
      timeline: { ...directPlan.timeline, player_start_seconds: 1300, timeline_offset_seconds: 0 },
    });
    engineRoute.refused = true;
    try {
      renderPlayer({
        plan,
        initialPosition: 1300,
        onPlanFailure,
        browserEngine: {
          containers: ["mkv"],
          videoCodecs: ["h264"],
          audioCodecs: ["aac"],
          hdr: false,
        },
      });
      await waitFor(() => expect(onPlanFailure).toHaveBeenCalled());
      expect(onPlanFailure).toHaveBeenCalledWith(
        expect.objectContaining({ classification: "browser_engine_unsupported_source" }),
        1300,
      );
    } finally {
      engineRoute.refused = false;
    }
  });

  // The engine keeps its audio's pitch at speed, so the viewer's speed carries
  // over when it takes the file and stays adjustable.
  it("keeps the speed control and the viewer's speed on the browser engine", async () => {
    const engine = {
      containers: ["mkv"],
      videoCodecs: ["h264"],
      audioCodecs: ["aac"],
      hdr: false,
    };
    const { container, rerenderPlayer } = renderPlayer();
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    act(() => controls.current!.onPlaybackSpeedChange!(1.5));
    expect(video.playbackRate).toBe(1.5);

    engineRoute.chosen = true;
    try {
      rerenderPlayer({ planRevision: 2, browserEngine: engine });
      await waitFor(() => expect(attachBrowserEngine).toHaveBeenCalled());
      // The engine starts at the element's default rate, which carries the speed.
      expect(video.defaultPlaybackRate).toBe(1.5);
      expect(controls.current!.onPlaybackSpeedChange).toBeDefined();
      act(() => controls.current!.onPlaybackSpeedChange!(2));
      expect(video.playbackRate).toBe(2);
    } finally {
      engineRoute.chosen = false;
    }
  });

  // Written to the element before metadata, a seek would become the default
  // start position and be read back as the clock origin.
  it("holds a direct-play seek made before metadata until the origin is known", async () => {
    const origin = 29.977;
    const { container } = renderPlayer();
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));

    playerSeek.mockClear();
    act(() => {
      controls.current!.onSeek(500);
    });
    expect(playerSeek).not.toHaveBeenCalled();
    expect(controls.current!.currentTime).toBe(500);
    video.currentTime = origin;
    fireEvent.loadedMetadata(video);
    expect(video.currentTime).toBeCloseTo(500 + origin);
    await waitFor(() => expect(subtitleTimeline.textOffsetSeconds).toBeCloseTo(-origin));
  });

  // A load queues a timeupdate at position 0. A held seek to 0 must survive it
  // rather than settle on it and fall back to the resume position.
  it("keeps a held seek to the start through a load's reset timeupdate", async () => {
    const origin = 29.977;
    const plan = fixturePlanV3({
      ...directPlan,
      timeline: { ...directPlan.timeline, player_start_seconds: 1300 },
    });
    const { container } = renderPlayer({ plan, initialPosition: 1300 });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));

    act(() => {
      controls.current!.onSeek(0);
    });
    fireEvent.timeUpdate(video);
    video.currentTime = origin;
    fireEvent.loadedMetadata(video);
    expect(video.currentTime).toBeCloseTo(origin);
  });

  it("publishes direct play's duration on the media timeline", async () => {
    const origin = 29.977;
    const { duration_seconds: _omitted, ...source } = directPlan.source;
    const plan = fixturePlanV3({ ...directPlan, source });
    const { container } = renderPlayer({ plan });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));

    // durationchange arrives before loadedmetadata, while the origin is unknown.
    Object.defineProperty(video, "duration", { configurable: true, value: 1830 });
    fireEvent.durationChange(video);
    video.currentTime = origin;
    fireEvent.loadedMetadata(video);
    await waitFor(() => expect(controls.current!.duration).toBeCloseTo(1830 - origin));
  });

  // A room command that runs before metadata must judge drift from where
  // playback is heading, not the unloaded clock, or it can skip a needed seek.
  it("applies a room pause sent before direct-play metadata", async () => {
    const origin = 29.977;
    const plan = fixturePlanV3({
      ...directPlan,
      timeline: { ...directPlan.timeline, player_start_seconds: 1300 },
    });
    const command = {
      command_id: "room-command-1",
      session_id: "session-1",
      selection_revision: 1,
      action: "pause" as const,
      position_seconds: 0,
      execute_at: new Date().toISOString(),
      issued_at: new Date().toISOString(),
      playback_state: "paused" as const,
    };
    const { container } = renderPlayer({
      plan,
      initialPosition: 1300,
      shouldAutoPlay: false,
      watchTogetherRoomId: "room-1",
      watchTogetherConnection: roomConnection({ transportCommand: command }),
    });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    await waitFor(() => expect(controls.current!.currentTime).toBe(0));

    video.currentTime = origin;
    fireEvent.loadedMetadata(video);
    expect(video.currentTime).toBeCloseTo(origin);
  });

  it("skips from the resume position before direct-play metadata", async () => {
    const origin = 29.977;
    const ready = vi.fn();
    const plan = fixturePlanV3({
      ...directPlan,
      timeline: { ...directPlan.timeline, player_start_seconds: 1300 },
    });
    const { container } = renderPlayer({
      plan,
      initialPosition: 1300,
      onPlaybackTransportReady: ready,
    });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    // A load's reset timeupdate leaves the shown position at the resume point.
    fireEvent.timeUpdate(video);
    expect(controls.current!.currentTime).toBe(1300);

    act(() => ready.mock.lastCall![0].skipForward());
    video.currentTime = origin;
    fireEvent.loadedMetadata(video);
    expect(video.currentTime).toBeCloseTo(1330 + origin);
  });

  it("uses native HLS for Dolby Vision when hls.js is also available", async () => {
    hlsJS.supported = true;
    vi.stubGlobal("navigator", {
      ...navigator,
      userAgent:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/26.0 Safari/605.1.15",
    });
    const plan = fixturePlanV3({
      delivery: "server_remux_hls",
      stream: {
        url: "/playback/transcode/session-1/master.m3u8",
        protocol: "hls",
        headers: {},
        header_refresh: "none",
      },
      effective_recipe: {
        video_codec: "hevc",
        audio_codec: "eac3",
        dynamic_range: "dolby_vision",
      },
      timeline: {
        source_start_seconds: 42,
        stream_origin_seconds: 35,
        player_start_seconds: 7,
        timeline_offset_seconds: 0,
        can_seek_anywhere: false,
        seek_restoration: "source_position",
      },
    });
    const { container } = renderPlayer({ plan, initialPosition: 42 });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    fireEvent.loadedMetadata(video);

    expect(video.currentTime).toBe(7);
    expect(hlsJS.constructed).not.toHaveBeenCalled();
  });

  it("uses hls.js for Dolby Vision in Chromium even when native HLS is advertised", async () => {
    hlsJS.supported = true;
    vi.stubGlobal("navigator", {
      ...navigator,
      userAgent:
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/151.0.0.0 Safari/537.36",
    });
    const plan = fixturePlanV3({
      delivery: "server_remux_hls",
      stream: {
        url: "/playback/transcode/session-1/master.m3u8",
        protocol: "hls",
        headers: {},
        header_refresh: "none",
      },
      effective_recipe: {
        video_codec: "hevc",
        audio_codec: "aac",
        dynamic_range: "dolby_vision",
      },
    });

    renderPlayer({ plan });

    await waitFor(() => expect(hlsJS.constructed).toHaveBeenCalledOnce());
  });
});

// A server-invalidated plan swaps the transport without any user gesture, and
// it is the one swap that can cross transport kinds — an optimistic progressive
// remux replaced by a tone-mapping HLS transcode. The replacement has to resume
// on its own: nothing is going to press play, and once the engine has filled its
// buffer it stops fetching, so a player left paused here is a player that stays
// paused until the viewer seeks.
describe("VideoPlayer server-invalidated transport swap", () => {
  const invalidatedHlsPlan = fixturePlanV3({
    delivery: "server_transcode_hls",
    plan_id: "plan:3333333333333333",
    plan_attempt_key: "v3:3333333333333333",
    stream: {
      url: "/playback/transcode/session-1/master.m3u8",
      protocol: "hls",
      headers: {},
      header_refresh: "none",
    },
    timeline: {
      source_start_seconds: 24,
      player_start_seconds: 24,
      stream_origin_seconds: 0,
      timeline_offset_seconds: 0,
      can_seek_anywhere: true,
      seek_restoration: "player_position",
    },
  });

  beforeEach(() => {
    realtimeOptions.current = null;
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
    vi.restoreAllMocks();
  });

  it("resumes playback and restores the position on the replacement transport", async () => {
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    let rerender: ((next: Partial<Parameters<typeof VideoPlayer>[0]>) => void) | null = null;
    const onPlanInvalidated = vi.fn(async () => {
      rerender?.({
        plan: invalidatedHlsPlan,
        planRevision: 2,
        streamUrl: "/api/v1/playback/transcode/session-1/master.m3u8?token=token",
      });
      return true;
    });

    const rendered = renderPlayer({ onPlanInvalidated });
    rerender = rendered.rerenderPlayer;
    const video = rendered.container.querySelector("video");
    if (!video) throw new Error("expected video element");

    await waitFor(() => expect(video.src).toContain("/api/v1/stream/session-1"));
    play.mockClear();

    const onCommand = realtimeOptions.current?.onCommand;
    if (!onCommand) throw new Error("expected the realtime command handler");
    await act(async () => {
      await onCommand(planInvalidatedCommand());
    });

    await waitFor(() => expect(video.src).toContain("master.m3u8"));
    fireEvent.loadedMetadata(video);
    expect(video.currentTime).toBe(24);

    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    fireEvent.canPlay(video);

    expect(play).toHaveBeenCalledOnce();
  });

  // The previous transport is torn down with `load()` in the same commit that
  // builds the replacement, and the load algorithm is required to reject a play
  // that is still pending. Latching the autoplay attempt on that first rejection
  // left the element paused on a healthy buffer with nothing to restart it.
  it("retries a rejected play instead of leaving the replacement paused", async () => {
    vi.useFakeTimers();
    try {
      const play = vi.mocked(HTMLMediaElement.prototype.play);
      play
        .mockRejectedValueOnce(
          Object.assign(new Error("The play() request was interrupted"), { name: "AbortError" }),
        )
        .mockResolvedValue(undefined);

      const { container, rerenderPlayer } = renderPlayer();
      const video = container.querySelector("video");
      if (!video) throw new Error("expected video element");

      rerenderPlayer({
        plan: invalidatedHlsPlan,
        planRevision: 2,
        streamUrl: "/api/v1/playback/transcode/session-1/master.m3u8?token=token",
      });
      play.mockClear();

      Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
      fireEvent.canPlay(video);
      expect(play).toHaveBeenCalledOnce();

      await act(async () => {
        await Promise.resolve();
      });
      await act(async () => {
        vi.advanceTimersByTime(1_000);
      });

      expect(play).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("VideoPlayer subtitle sync", () => {
  const SIDECAR = "external-" + "e".repeat(64);
  const sidecarTrack: PlayerSubtitleInfo = {
    index: 0,
    language: "en",
    label: "Movie.en.srt",
    source: "external",
    codec: "srt",
    sync_key: SIDECAR,
    url: "/api/v1/stream/session-1/subtitles/0.vtt?file_id=7",
  };
  const storedTrack: PlayerSubtitleInfo = {
    index: 2,
    language: "fr",
    label: "French",
    source: "downloaded",
    codec: "srt",
    sync_key: "stored-31",
    url: "/api/v1/stream/session-1/subtitles/2.vtt?file_id=7&downloaded_subtitle_id=31",
  };
  const state = (key: string, overrides: Record<string, unknown> = {}) => ({
    key,
    media_file_id: "7",
    source: key === SIDECAR ? "external" : "downloaded",
    language: "en",
    format: "srt",
    label: "Movie.en.srt",
    timing: { offset_ms: 0, scale: 1 },
    ...overrides,
  });
  const timingChanged = (fileId: number, key: string): PlaybackRealtimeEventEnvelope => ({
    type: "event",
    session_id: "session-1",
    name: "subtitle_timing_changed",
    payload: { session_id: "session-1", file_id: fileId, sync_key: key },
  });
  const syncUpdated = (
    job: Record<string, unknown>,
    timing = { offset_ms: 0, scale: 1 },
  ): PlaybackRealtimeEventEnvelope => ({
    type: "event",
    session_id: "session-1",
    name: "subtitle_sync_updated",
    payload: {
      session_id: "session-1",
      file_id: 7,
      sync_key: SIDECAR,
      timing,
      job: {
        id: "80",
        trigger: "manual",
        confidence: null,
        created_at: "2026-01-02T03:04:05.000Z",
        finished_at: null,
        status: "running",
        ...job,
      } as never,
    },
  });
  const selectTrack = (index: number) =>
    act(() =>
      (controls.current as unknown as { onSubtitleSelect: (i: number) => void }).onSubtitleSelect(
        index,
      ),
    );

  beforeEach(() => {
    realtimeOptions.current = null;
    controls.current = null;
    playerV2Mock
      .mockReset()
      .mockImplementation(
        async (_config: unknown, route: string, options: { path?: { key?: string } }) => {
          if (route === "GET /api/v2/subtitles/sync/status") return { state: "available" };
          if (route === "GET /api/v2/subtitles/{media_file_id}/sync") {
            return { subtitles: [state(SIDECAR), state("stored-31")] };
          }
          if (route === "GET /api/v2/subtitles/{media_file_id}/sync/{key}") {
            return {
              subtitle: state(options.path?.key ?? SIDECAR, {
                timing: { offset_ms: 1200, scale: 1 },
              }),
            };
          }
          if (route === "POST /api/v2/subtitles/{media_file_id}/sync/{key}") {
            return {
              subtitle: state(SIDECAR, {
                sync: {
                  id: "80",
                  status: "pending",
                  trigger: "manual",
                  phase: "queued",
                  progress: 0,
                  confidence: null,
                  created_at: "2026-01-02T03:04:05.000Z",
                  finished_at: null,
                },
              }),
            };
          }
          return {};
        },
      );
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("refetches the active track when its timing changes on this file", async () => {
    renderPlayer({ subtitleUrls: [sidecarTrack, storedTrack] });
    selectTrack(0);
    await act(async () => {});
    expect(subtitleTimeline.cueRevision).toBe(0);

    // Another file's subtitle and an inactive subtitle leave the cues alone.
    act(() => realtimeOptions.current?.onEvent?.(timingChanged(8, SIDECAR)));
    act(() => realtimeOptions.current?.onEvent?.(timingChanged(7, "stored-31")));
    await act(async () => {});
    expect(subtitleTimeline.cueRevision).toBe(0);

    act(() => realtimeOptions.current?.onEvent?.(timingChanged(7, SIDECAR)));
    await act(async () => {});
    expect(subtitleTimeline.cueRevision).toBe(1);
    expect(subtitleTimeline.assCueRevision).toBe(1);
    // The follow-up read refreshes the menu's status without a second reload.
    expect(playerV2Mock).toHaveBeenCalledWith(
      playerConfig,
      "GET /api/v2/subtitles/{media_file_id}/sync/{key}",
      { path: { media_file_id: "7", key: SIDECAR } },
    );
    expect(subtitleTimeline.cueRevision).toBe(1);
    const sync = (
      controls.current as unknown as {
        subtitleSync: { entries: Record<string, { state: { timing: unknown } }> };
      }
    ).subtitleSync;
    expect(sync.entries[SIDECAR]?.state.timing).toEqual({ offset_ms: 1200, scale: 1 });
  });

  it("shows a sync the viewer started from progress to the corrected cues", async () => {
    renderPlayer({ subtitleUrls: [sidecarTrack, storedTrack] });
    selectTrack(0);
    await act(async () => {});
    act(() => subtitleTimeline.setLoadState?.("ready"));

    const sync = (
      controls.current as unknown as { subtitleSync: { requestSync: (k: string) => Promise<void> } }
    ).subtitleSync;
    await act(async () => {
      await sync.requestSync(SIDECAR);
    });
    const indicator = () => screen.getByTestId("subtitle-sync-indicator");
    expect(indicator()).toHaveTextContent("Syncing English subtitles");
    expect(indicator()).toHaveTextContent("Waiting to start…");

    act(() =>
      realtimeOptions.current?.onEvent?.(syncUpdated({ phase: "analyzing", progress: 0.5 })),
    );
    expect(indicator()).toHaveTextContent("Listening to the audio…");
    expect(screen.getByRole("progressbar", { name: "Subtitle sync progress" })).toHaveAttribute(
      "aria-valuenow",
      "50",
    );

    act(() =>
      realtimeOptions.current?.onEvent?.(
        syncUpdated(
          {
            status: "synced",
            result: { offset_ms: 2300, scale: 1 },
            finished_at: "2026-01-02T03:05:05.000Z",
          },
          { offset_ms: 2300, scale: 1 },
        ),
      ),
    );
    await act(async () => {});
    expect(indicator()).toHaveTextContent("Applying new timing…");
    // The server follows the result with the timing event: one reload, and a
    // read that finds the finished job.
    const routes = playerV2Mock.getMockImplementation()!;
    playerV2Mock.mockImplementation(async (config: unknown, route: string, options: unknown) =>
      route === "GET /api/v2/subtitles/{media_file_id}/sync/{key}"
        ? {
            subtitle: state(SIDECAR, {
              timing: { offset_ms: 2300, scale: 1 },
              sync: {
                id: "80",
                status: "synced",
                trigger: "manual",
                confidence: 0.9,
                result: { offset_ms: 2300, scale: 1 },
                created_at: "2026-01-02T03:04:05.000Z",
                finished_at: "2026-01-02T03:05:05.000Z",
              },
            }),
          }
        : routes(config, route, options),
    );
    act(() => realtimeOptions.current?.onEvent?.(timingChanged(7, SIDECAR)));
    await act(async () => {});
    expect(subtitleTimeline.cueRevision).toBe(1);

    act(() => subtitleTimeline.setLoadState?.("loading"));
    act(() => subtitleTimeline.setLoadState?.("ready"));
    expect(indicator()).toHaveTextContent("Subtitles synced");
    expect(indicator()).toHaveTextContent("+2.3 s");
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

  it("rebuilds subtitle tracks after initial metadata and each replacement stream loads", () => {
    const { container, rerenderPlayer } = renderPlayer();
    const video = container.querySelector("video")!;
    expect(subtitleTimeline.streamGeneration).toBe(0);
    fireEvent.loadedMetadata(video);
    expect(subtitleTimeline.streamGeneration).toBe(1);
    rerenderPlayer({ planRevision: 2 });
    // A plan revision alone precedes HLS clearing the old tracks.
    expect(subtitleTimeline.streamGeneration).toBe(1);
    fireEvent.loadedMetadata(video);
    expect(subtitleTimeline.streamGeneration).toBe(2);
  });

  it("reports an accepted job failure before Started without changing subtitles", async () => {
    renderPlayer();
    act(() => controls.current?.onSubtitleJobAccepted?.("8"));
    await act(async () => {});
    act(() =>
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_failed",
        payload: {
          session_id: "session-1",
          file_id: 7,
          job_id: 8,
          track_key: "ai-8",
          message: "Source subtitle unavailable",
        },
      }),
    );
    expect(toastError).toHaveBeenCalledExactlyOnceWith(
      "Subtitle processing failed: Source subtitle unavailable",
    );
    expect(controls.current?.activeSubtitleIndex).toBeNull();
    expect(subtitleTimeline.liveKey).toBeNull();
  });

  it("reconciles a failure before the acceptance response and ignores stale job and session failures", async () => {
    playerV2Mock.mockResolvedValue({
      job: { status: "failed", error_message: "Source subtitle unavailable" },
    });
    const { rerenderPlayer } = renderPlayer();
    const failed = (job: number): PlaybackRealtimeEventEnvelope => ({
      type: "event",
      session_id: "session-1",
      name: "subtitle_translation_failed",
      payload: {
        session_id: "session-1",
        file_id: 7,
        job_id: job,
        track_key: `ai-${job}`,
        message: "Source subtitle unavailable",
      },
    });
    act(() => realtimeOptions.current?.onEvent?.(failed(8)));
    expect(toastError).not.toHaveBeenCalled();
    await act(async () => {
      controls.current?.onSubtitleJobAccepted?.("8");
    });
    expect(toastError).toHaveBeenCalledOnce();
    act(() => realtimeOptions.current?.onEvent?.(failed(8)));
    expect(toastError).toHaveBeenCalledOnce();
    playerV2Mock.mockResolvedValue({ job: { status: "running" } });
    await act(async () => {
      controls.current?.onSubtitleJobAccepted?.("9");
    });
    act(() => realtimeOptions.current?.onEvent?.(failed(8)));
    expect(toastError).toHaveBeenCalledOnce();
    act(() => realtimeOptions.current?.onEvent?.(failed(9)));
    expect(toastError).toHaveBeenCalledTimes(2);
    rerenderPlayer({ sessionId: "session-2" });
    act(() => realtimeOptions.current?.onEvent?.(failed(9)));
    expect(toastError).toHaveBeenCalledTimes(2);
  });

  it("isolates cue batches, completion and failures when a newer AI job starts", () => {
    const onRefreshSubtitles = vi.fn();
    const onApplySubtitleTrack = vi.fn();
    const { container } = renderPlayer({ onRefreshSubtitles, onApplySubtitleTrack });
    const video = container.querySelector("video")!;
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    const base = { session_id: "session-1", file_id: 7 };
    const started = (job: number) => ({
      type: "event" as const,
      session_id: "session-1",
      name: "subtitle_translation_started" as const,
      payload: {
        ...base,
        job_id: job,
        track_key: `ai-${job}`,
        language: job === 1 ? "hr" : "en",
        total_cues: 2,
      },
    });
    act(() => {
      const wrongFile = started(9);
      wrongFile.payload.file_id = 8;
      realtimeOptions.current?.onEvent?.(wrongFile);
      const wrongSession = started(9);
      wrongSession.payload.session_id = "other-session";
      realtimeOptions.current?.onEvent?.(wrongSession);
    });
    expect(subtitleTimeline.liveKey).toBeNull();
    act(() => {
      realtimeOptions.current?.onEvent?.(started(1));
    });
    Object.defineProperty(video, "paused", { configurable: true, value: true });
    vi.mocked(video.play).mockClear();
    act(() => {
      realtimeOptions.current?.onEvent?.(started(2));
    });
    const lateEvents: PlaybackRealtimeEventEnvelope[] = [
      {
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_cues",
        payload: {
          ...base,
          job_id: 1,
          track_key: "ai-1",
          done: 1,
          total: 2,
          cues: [{ start: 0, end: 3, text: "older job" }],
        },
      },
      {
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_completed",
        payload: {
          ...base,
          job_id: 1,
          track_key: "ai-1",
          subtitle_id: 44,
          language: "hr",
        },
      },
      {
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_failed",
        payload: {
          ...base,
          job_id: 1,
          track_key: "ai-1",
          message: "older failure",
        },
      },
    ];
    act(() => {
      lateEvents.forEach((event) => realtimeOptions.current?.onEvent?.(event));
    });
    expect(subtitleTimeline.liveKey).toBe("ai-2");
    expect(subtitleTimeline.liveCues).toEqual([]);
    expect(controls.current?.activeSubtitleIndex).toBe(1_000_000);
    expect(onRefreshSubtitles).not.toHaveBeenCalled();
    expect(onApplySubtitleTrack).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_cues",
        payload: {
          ...base,
          job_id: 2,
          track_key: "ai-2",
          done: 1,
          total: 2,
          cues: [{ start: 0, end: 3, text: "current job" }],
        },
      });
    });
    expect(subtitleTimeline.liveCues.map((cue) => cue.text)).toEqual(["current job"]);
    expect(video.play).toHaveBeenCalledOnce();
    act(() => {
      realtimeOptions.current?.onEvent?.(started(2));
    });
    expect(subtitleTimeline.liveCues.map((cue) => cue.text)).toEqual(["current job"]);
    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_failed",
        payload: { ...base, job_id: 2, track_key: "ai-2", message: "Transcription unavailable" },
      });
    });
    expect(subtitleTimeline.liveKey).toBeNull();
    expect(subtitleTimeline.liveCues).toEqual([]);
    expect(controls.current?.activeSubtitleIndex).toBeNull();
    expect(toastError).toHaveBeenCalledWith(
      "Subtitle processing failed: Transcription unavailable",
    );
  });

  it("selects the refreshed downloaded track and clears the live overlay", async () => {
    const onRefreshSubtitles = vi.fn();
    const onSubtitleChanged = vi.fn();
    const onSubtitleTrackChange = vi.fn();
    const { rerenderPlayer } = renderPlayer({
      onRefreshSubtitles,
      onSubtitleChanged,
      onSubtitleTrackChange,
    });

    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_started",
        payload: {
          session_id: "session-1",
          file_id: 7,
          job_id: 1,
          track_key: "translation-1",
          language: "es",
          label: "Spanish (AI)",
          total_cues: 2,
        },
      });
    });
    expect(onSubtitleTrackChange).not.toHaveBeenCalledWith(1_000_000, expect.any(Number));
    expect(controls.current?.activeSubtitleIndex).toBe(1_000_000);
    expect(controls.current?.subtitleTracks.some((track) => track.live)).toBe(true);

    act(() => {
      realtimeOptions.current?.onEvent?.({
        type: "event",
        session_id: "session-1",
        name: "subtitle_translation_completed",
        payload: {
          session_id: "session-1",
          file_id: 7,
          job_id: 1,
          track_key: "translation-1",
          subtitle_id: 44,
          language: "es",
          label: "Spanish (AI)",
        },
      });
    });
    expect(onRefreshSubtitles).toHaveBeenCalledOnce();
    expect(onSubtitleTrackChange).not.toHaveBeenCalledWith(1_000_000, expect.any(Number));
    expect(controls.current?.activeSubtitleIndex).toBe(1_000_000);

    const downloadedTrack: PlayerSubtitleInfo = {
      index: 4,
      media_file_id: 7,
      track_id: "downloaded:44",
      language: "es",
      codec: "srt",
      label: "Spanish (AI)",
      source: "downloaded",
      url: "/subtitles/44",
    };
    rerenderPlayer({
      plan: fixturePlanV3({
        ...directPlan,
        plan_id: "plan:2222222222222222",
        plan_attempt_key: "v3:2222222222222222",
      }),
      planRevision: 2,
      subtitleUrls: [downloadedTrack],
    });

    await waitFor(() => expect(onSubtitleChanged).toHaveBeenCalledWith(4, undefined));
    expect(onSubtitleTrackChange).toHaveBeenCalledWith(4, expect.any(Number));
    expect(controls.current?.activeSubtitleIndex).toBe(4);
    expect(controls.current?.subtitleTracks).toEqual([downloadedTrack]);
  });

  it.each(["missing", "rejecting"])(
    "falls back to webkitEnterFullscreen when requestFullscreen is %s",
    async (mode) => {
      const webkitEnterFullscreen = vi.fn();
      const webkitExitFullscreen = vi.fn();

      const { container } = renderPlayer();

      const video = container.querySelector("video") as HTMLVideoElement & {
        webkitSupportsFullscreen?: boolean;
        webkitDisplayingFullscreen?: boolean;
        webkitEnterFullscreen?: () => void;
        webkitExitFullscreen?: () => void;
      };
      video.webkitSupportsFullscreen = true;
      video.webkitEnterFullscreen = webkitEnterFullscreen;
      video.webkitExitFullscreen = webkitExitFullscreen;

      // Simulate container requestFullscreen rejecting (as WebKit on iPhone does)
      const playerContainer = container.querySelector(".player-container") as HTMLElement;
      expect(playerContainer).not.toBeNull();
      const requestFullscreen = vi.fn().mockRejectedValue(new Error("Not supported"));
      Object.defineProperty(playerContainer, "requestFullscreen", {
        value: mode === "rejecting" ? requestFullscreen : undefined,
        configurable: true,
      });

      act(() => {
        controls.current?.onFullscreenToggle?.();
      });

      await waitFor(() => expect(webkitEnterFullscreen).toHaveBeenCalledOnce());
      expect(requestFullscreen).toHaveBeenCalledTimes(mode === "rejecting" ? 1 : 0);

      video.webkitDisplayingFullscreen = true;
      act(() => {
        controls.current?.onFullscreenToggle?.();
      });
      expect(webkitExitFullscreen).toHaveBeenCalledOnce();
    },
  );

  it("makes the host's fullscreen root fullscreen instead of its own container", () => {
    const root = document.createElement("div");
    const requestRootFullscreen = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(root, "requestFullscreen", {
      value: requestRootFullscreen,
      configurable: true,
    });
    const { container } = render(createElement(VideoPlayer, playerProps()), {
      wrapper: ({ children }) =>
        wrapper({
          children: createElement(PlayerFullscreenRootContext.Provider, {
            value: { current: root },
            children,
          }),
        }),
    });
    const playerContainer = container.querySelector(".player-container") as HTMLElement;
    const requestContainerFullscreen = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(playerContainer, "requestFullscreen", {
      value: requestContainerFullscreen,
      configurable: true,
    });

    act(() => {
      controls.current?.onFullscreenToggle?.();
    });

    expect(requestRootFullscreen).toHaveBeenCalledOnce();
    expect(requestContainerFullscreen).not.toHaveBeenCalled();
  });

  it("starts in fullscreen when mounted inside a fullscreen host", () => {
    Object.defineProperty(document, "fullscreenElement", {
      value: document.body,
      configurable: true,
    });
    try {
      renderPlayer();
      expect(controls.current?.isFullscreen).toBe(true);
    } finally {
      delete (document as { fullscreenElement?: Element | null }).fullscreenElement;
    }
  });

  it("tracks WebKit fullscreen events on the video element", async () => {
    const { container } = renderPlayer();

    const video = container.querySelector("video") as HTMLVideoElement & {
      webkitDisplayingFullscreen?: boolean;
    };

    video.webkitDisplayingFullscreen = true;
    act(() => {
      video.dispatchEvent(new Event("webkitbeginfullscreen"));
    });

    expect(controls.current?.isFullscreen).toBe(true);

    video.webkitDisplayingFullscreen = false;
    act(() => {
      video.dispatchEvent(new Event("webkitendfullscreen"));
    });

    expect(controls.current?.isFullscreen).toBe(false);
  });

  it("toggles video fit and resets it for a new playback session", () => {
    const { container, rerenderPlayer } = renderPlayer();
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    expect(video).toHaveClass("object-contain");
    expect(controls.current?.videoFit).toBe("contain");

    act(() => controls.current?.onVideoFitToggle?.());

    expect(video).toHaveClass("object-cover");
    expect(controls.current?.videoFit).toBe("cover");

    rerenderPlayer({ sessionId: "session-2" });

    expect(video).toHaveClass("object-contain");
    expect(controls.current?.videoFit).toBe("contain");
  });
});

describe("VideoPlayer controls auto-hide", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    controls.current = null;
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  function renderPlaying() {
    const rendered = renderPlayer({ shouldAutoPlay: false });
    const video = rendered.container.querySelector("video")!;
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    act(() => {
      fireEvent.play(video);
    });
    const container = rendered.container.querySelector(".player-container")!;
    return { container };
  }

  it("keeps the controls up while a player menu is open", async () => {
    const { container } = renderPlaying();
    const menu = document.createElement("div");
    menu.setAttribute("role", "menu");
    container.appendChild(menu);

    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(controls.current?.visible).toBe(true);
    fireEvent.mouseLeave(container);
    expect(controls.current?.visible).toBe(true);

    menu.remove();
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    expect(controls.current?.visible).toBe(false);
  });
});

describe("VideoPlayer progressive remux after a long pause", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-13T12:00:00Z"));
    realtimeOptions.current = null;
    controls.current = null;
    hlsJS.supported = false;
    vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  // A remux reanchored at 1800s: the element clock starts at the origin.
  const remuxPlan = fixturePlanV3({
    ...directPlan,
    delivery: "server_remux_progressive",
    timeline: { ...directPlan.timeline, timeline_offset_seconds: 1_800, can_seek_anywhere: false },
  });

  function playThenPause(overrides: Partial<Parameters<typeof VideoPlayer>[0]> = {}) {
    const onReanchorSeek = vi.fn(() => true);
    const onPlaybackStateChange = vi.fn();
    const onConnectionLost = vi.fn();
    const { container, unmount, rerenderPlayer } = renderPlayer({
      plan: remuxPlan,
      onReanchorSeek,
      onPlaybackStateChange,
      onConnectionLost,
      ...overrides,
    });
    const video = container.querySelector("video")!;
    video.currentTime = 40;
    fireFrameTimeUpdate(video);
    fireEvent.pause(video);
    return {
      video,
      unmount,
      rerenderPlayer,
      onReanchorSeek,
      onPlaybackStateChange,
      onConnectionLost,
    };
  }

  it("reopens the stream at the paused position when playback resumes", () => {
    const { video, onReanchorSeek, onPlaybackStateChange, onConnectionLost } = playThenPause();

    vi.advanceTimersByTime(10 * 60_000);
    onPlaybackStateChange.mockClear();
    fireEvent.play(video);

    expect(onReanchorSeek).toHaveBeenCalledExactlyOnceWith(1_840);
    expect(onConnectionLost).not.toHaveBeenCalled();
    // The reopened stream must keep playing, so the session hears the play
    // before it can adopt the new plan.
    expect(onPlaybackStateChange).toHaveBeenCalledWith(
      expect.objectContaining({ currentTime: 1_840, playing: true }),
    );
    expect(onPlaybackStateChange.mock.invocationCallOrder[0]!).toBeLessThan(
      onReanchorSeek.mock.invocationCallOrder[0]!,
    );
  });

  it.each([
    ["reopens once a replan in flight ends without a new plan", 1, 1],
    ["leaves the stream to a replan in flight that lands a new plan", 2, 0],
  ])("%s", (_, revisionAfterReplan, reopens) => {
    const onReanchorSeek = vi.fn(() => true);
    const { container, rerenderPlayer } = renderPlayer({
      plan: remuxPlan,
      onReanchorSeek,
      replanning: true,
    });
    const video = container.querySelector("video")!;
    video.currentTime = 40;
    fireFrameTimeUpdate(video);
    fireEvent.pause(video);
    vi.advanceTimersByTime(10 * 60_000);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    fireEvent.play(video);
    expect(onReanchorSeek).not.toHaveBeenCalled();

    rerenderPlayer({ replanning: false, planRevision: revisionAfterReplan });

    expect(onReanchorSeek).toHaveBeenCalledTimes(reopens);
  });

  it("does not reconnect for a viewer who left while the reopen was pending", async () => {
    const { video, unmount, onReanchorSeek, onConnectionLost } = playThenPause();
    let settle: (ok: boolean) => void = () => {};
    onReanchorSeek.mockImplementation(
      () => new Promise<boolean>((resolve) => (settle = resolve)) as unknown as boolean,
    );

    vi.advanceTimersByTime(10 * 60_000);
    fireEvent.play(video);
    expect(onReanchorSeek).toHaveBeenCalledOnce();

    unmount();
    // Leaving ends the session's request, which then reports the reopen as not taken.
    settle(false);
    await act(() => vi.advanceTimersByTimeAsync(0));

    expect(onConnectionLost).not.toHaveBeenCalled();
  });

  it("leaves a superseded reopen to the version switch that started meanwhile", async () => {
    const { video, rerenderPlayer, onReanchorSeek, onConnectionLost } = playThenPause();
    let settle: (ok: boolean) => void = () => {};
    onReanchorSeek.mockImplementation(
      () => new Promise<boolean>((resolve) => (settle = resolve)) as unknown as boolean,
    );
    vi.advanceTimersByTime(10 * 60_000);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    fireEvent.play(video);

    // The viewer switches version; the session drops the reopen as superseded.
    rerenderPlayer({ replacing: true });
    settle(false);
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(onConnectionLost).not.toHaveBeenCalled();

    // The switch fails and leaves the idle stream in place, so the reconnect
    // reopens it.
    rerenderPlayer({ replacing: false });
    expect(onConnectionLost).toHaveBeenCalledExactlyOnceWith(1_840, true);
    expect(onReanchorSeek).toHaveBeenCalledOnce();
  });

  it("reconnects once when the reopen's own replan fails", async () => {
    const { video, rerenderPlayer, onReanchorSeek, onConnectionLost } = playThenPause();
    let settle: (ok: boolean) => void = () => {};
    onReanchorSeek.mockImplementation(
      () => new Promise<boolean>((resolve) => (settle = resolve)) as unknown as boolean,
    );
    vi.advanceTimersByTime(10 * 60_000);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    fireEvent.play(video);

    // The session shows the reopen's replan in flight, and it answers false
    // before the render that clears the flag.
    rerenderPlayer({ replanning: true });
    settle(false);
    await act(() => vi.advanceTimersByTimeAsync(0));
    rerenderPlayer({ replanning: false });

    expect(onReanchorSeek).toHaveBeenCalledOnce();
    expect(onConnectionLost).toHaveBeenCalledExactlyOnceWith(1_840, true);
  });

  it("hands a reopen the session refuses to the reconnect", async () => {
    const { video, onReanchorSeek, onConnectionLost } = playThenPause();
    // The session expired during the pause, so the replan is turned down.
    onReanchorSeek.mockImplementation(() => Promise.resolve(false) as unknown as boolean);

    vi.advanceTimersByTime(40 * 60_000);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    fireEvent.play(video);
    await act(() => vi.advanceTimersByTimeAsync(0));

    expect(onConnectionLost).toHaveBeenCalledExactlyOnceWith(1_840, true);
  });

  it.each([
    ["has opened but not shown a frame", HTMLMediaElement.HAVE_METADATA, 1],
    ["was torn down for the next stream", HTMLMediaElement.HAVE_NOTHING, 0],
  ])("counts a pause before the first frame only when the stream %s", (_, readyState, reopens) => {
    const onReanchorSeek = vi.fn(() => true);
    const { container } = renderPlayer({ plan: remuxPlan, onReanchorSeek });
    const video = container.querySelector("video")!;
    Object.defineProperty(video, "readyState", { configurable: true, value: readyState });
    fireEvent.pause(video);

    vi.advanceTimersByTime(10 * 60_000);
    fireEvent.play(video);

    expect(onReanchorSeek).toHaveBeenCalledTimes(reopens);
  });

  it.each([
    ["a short pause on a progressive remux", {}, 30_000],
    [
      "a long pause on an original file the browser can range-request",
      { plan: directPlan },
      10 * 60_000,
    ],
    [
      "a long pause in a room, which drives its own transport",
      { watchTogetherRoomId: "room-1", watchTogetherConnection: roomConnection() },
      10 * 60_000,
    ],
    ["a long pause while another replan replaces the stream", { replanning: true }, 10 * 60_000],
    ["a long pause while a version switch replaces the session", { replacing: true }, 10 * 60_000],
  ])("keeps the stream after %s", (_, overrides, pauseMs) => {
    const { video, onReanchorSeek } = playThenPause(overrides);

    vi.advanceTimersByTime(pauseMs);
    fireEvent.play(video);

    expect(onReanchorSeek).not.toHaveBeenCalled();
  });

  it("reopens a stream that settled paused and waited", async () => {
    const onReanchorSeek = vi.fn(() => true);
    const { container } = renderPlayer({ plan: remuxPlan, onReanchorSeek, shouldAutoPlay: false });
    const video = container.querySelector("video")!;
    Object.defineProperty(video, "readyState", {
      configurable: true,
      value: HTMLMediaElement.HAVE_ENOUGH_DATA,
    });
    fireEvent.canPlay(video);
    video.currentTime = 12;

    vi.advanceTimersByTime(5 * 60_000);
    fireEvent.play(video);

    expect(onReanchorSeek).toHaveBeenCalledExactlyOnceWith(1_812);
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

  it("hands a network error on a stream that played to the reconnect", () => {
    const onConnectionLost = vi.fn();
    const onPlanFailure = vi.fn();
    const { container } = renderPlayer({ onConnectionLost, onPlanFailure });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    fireFrameTimeUpdate(video);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    // The viewer seeks past the buffered range while the server is down.
    act(() => controls.current?.onSeek(1_200));
    setNetworkError(video);
    fireEvent.error(video);

    expect(onConnectionLost).toHaveBeenCalledExactlyOnceWith(1_200, true);
    expect(onPlanFailure).not.toHaveBeenCalled();
    expect(screen.queryByText("Go Back")).not.toBeInTheDocument();
  });

  it("sends a replacement transport's network error before its first frame to route recovery", () => {
    const onConnectionLost = vi.fn();
    const onPlanFailure = vi.fn();
    const { container, rerenderPlayer } = renderPlayer({ onConnectionLost, onPlanFailure });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    fireFrameTimeUpdate(video);

    // A quality change hands over a new route that never shows a frame.
    const nextPlan = fixturePlanV3({
      ...directPlan,
      plan_id: "plan:2222222222222222",
      plan_attempt_key: "v3:2222222222222222",
    });
    rerenderPlayer({ plan: nextPlan, planRevision: 2 });
    setNetworkError(video);
    fireEvent.error(video);

    expect(onConnectionLost).not.toHaveBeenCalled();
    expect(onPlanFailure).toHaveBeenCalledOnce();

    // Once that transport plays, a network error is a lost connection again.
    const thirdPlan = fixturePlanV3({
      ...directPlan,
      plan_id: "plan:3333333333333333",
      plan_attempt_key: "v3:3333333333333333",
    });
    rerenderPlayer({ plan: thirdPlan, planRevision: 3 });
    fireFrameTimeUpdate(video);
    fireEvent.error(video);
    expect(onConnectionLost).toHaveBeenCalledOnce();
    expect(onPlanFailure).toHaveBeenCalledOnce();
  });

  it("keeps a network error before the first frame on route recovery", () => {
    const onConnectionLost = vi.fn();
    const onPlanFailure = vi.fn();
    const { container } = renderPlayer({ onConnectionLost, onPlanFailure });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    setNetworkError(video);
    fireEvent.error(video);

    expect(onConnectionLost).not.toHaveBeenCalled();
    expect(onPlanFailure).toHaveBeenCalledOnce();
  });

  it("pauses behind a reconnecting notice the viewer can leave", async () => {
    const onExit = vi.fn();
    const onPlanFailure = vi.fn();
    const { container, rerenderPlayer } = renderPlayer({ onExit, onPlanFailure });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    vi.mocked(HTMLMediaElement.prototype.pause).mockClear();

    rerenderPlayer({ connectionStatus: "reconnecting" });

    expect(screen.getByRole("status", { name: "" })).toHaveTextContent("Reconnecting…");
    expect(HTMLMediaElement.prototype.pause).toHaveBeenCalled();
    expect(screen.queryByText("Try again")).not.toBeInTheDocument();
    // Route failures of the dead transport are not reported while reconnecting.
    setMediaError(video, "decoder failed");
    fireEvent.error(video);
    expect(onPlanFailure).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Go Back" }));
    await waitFor(() => expect(onExit).toHaveBeenCalled());
  });

  it("offers Try again once reconnecting gave up", () => {
    const onRetryConnection = vi.fn();
    const onPlanFailure = vi.fn();
    const { container } = renderPlayer({
      onRetryConnection,
      onPlanFailure,
      connectionStatus: "lost",
      connectionErrorTitle: "Connection lost",
      connectionError: "Silo lost its connection to the server and couldn't reconnect.",
    });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");

    expect(screen.getByRole("alert")).toHaveTextContent("Connection lost");
    expect(screen.queryByText(/could not start playback/)).not.toBeInTheDocument();
    setMediaError(video, "decoder failed");
    fireEvent.error(video);
    expect(onPlanFailure).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetryConnection).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Go Back" })).toBeInTheDocument();
  });

  it("keeps subtitles off when a reconnect had to start a new session", async () => {
    const onSubtitleTrackChange = vi.fn();
    const sidecarTrack: PlayerSubtitleInfo = {
      index: 2,
      media_file_id: 7,
      track_id: "file:7:subtitle:2",
      language: "en",
      codec: "srt",
      label: "English",
      source: "external",
      url: "/stream/session-1/subtitles/2.vtt",
    };
    const withSubtitle = fixturePlanV3({
      ...directPlan,
      selected_tracks: { ...directPlan.selected_tracks, subtitle: { id: "", index: 2 } },
    });
    const { rerenderPlayer } = renderPlayer({
      plan: withSubtitle,
      subtitleUrls: [sidecarTrack],
      subtitleMode: "always",
      preferredSubtitleLanguage: "en",
      onSubtitleTrackChange,
    });
    await waitFor(() => expect(controls.current?.activeSubtitleIndex).toBe(2));

    // The viewer turns subtitles off, and the replan for it lands.
    act(() =>
      (
        controls.current as unknown as { onSubtitleSelect: (i: number | null) => void }
      ).onSubtitleSelect(null),
    );
    await waitFor(() => expect(onSubtitleTrackChange).toHaveBeenCalledWith(null, 0));
    rerenderPlayer({ plan: directPlan, planRevision: 2 });

    // The server restarts: the reconnect starts session-2 with no subtitle.
    rerenderPlayer({ plan: directPlan, planRevision: 2, connectionStatus: "reconnecting" });
    const restarted = fixturePlanV3({
      ...directPlan,
      plan_id: "plan:restarted",
      session_id: "session-2",
    });
    rerenderPlayer({
      plan: restarted,
      planRevision: 3,
      sessionId: "session-2",
      connectionStatus: "connected",
    });
    await act(async () => Promise.resolve());

    expect(controls.current?.activeSubtitleIndex).toBeNull();
    expect(onSubtitleTrackChange).toHaveBeenCalledOnce();
  });

  it("hands a fatal hls.js network error on a stream that played to the reconnect", async () => {
    hlsJS.supported = true;
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    const onConnectionLost = vi.fn();
    const plan = fixturePlanV3({
      stream: {
        url: "/playback/transcode/session-1/master.m3u8",
        protocol: "hls",
        headers: {},
        header_refresh: "none",
      },
    });
    const { container } = renderPlayer({ plan, onConnectionLost });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    await waitFor(() => expect(hlsJS.latest).not.toBeNull());

    const fatalNetworkError = {
      fatal: true,
      type: "networkError",
      details: "fragLoadError",
    };
    // Before the first frame, hls.js keeps its startup recovery.
    act(() => hlsJS.latest?.emit("error", fatalNetworkError));
    expect(hlsJS.startLoad).toHaveBeenCalledOnce();
    expect(onConnectionLost).not.toHaveBeenCalled();

    fireFrameTimeUpdate(video);
    act(() => hlsJS.latest?.emit("error", fatalNetworkError));
    expect(onConnectionLost).toHaveBeenCalledOnce();
    expect(hlsJS.startLoad).toHaveBeenCalledOnce();
  });

  it("gives a replacement HLS transport that never played its startup recovery", async () => {
    hlsJS.supported = true;
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    const onConnectionLost = vi.fn();
    const onPlanFailure = vi.fn();
    // Direct play has been running; a quality change moves it to a transcode
    // on a node this browser cannot reach.
    const { container, rerenderPlayer } = renderPlayer({ onConnectionLost, onPlanFailure });
    const video = container.querySelector("video");
    if (!video) throw new Error("expected video element");
    fireFrameTimeUpdate(video);

    const hlsPlan = fixturePlanV3({
      plan_id: "plan:2222222222222222",
      plan_attempt_key: "v3:2222222222222222",
      stream: {
        url: "/playback/transcode/session-1/master.m3u8",
        protocol: "hls",
        headers: {},
        header_refresh: "none",
      },
    });
    rerenderPlayer({ plan: hlsPlan, planRevision: 2 });
    await waitFor(() => expect(hlsJS.latest).not.toBeNull());

    const fatalNetworkError = { fatal: true, type: "networkError", details: "fragLoadError" };
    vi.spyOn(Date, "now").mockReturnValue(10_000);
    act(() => hlsJS.latest?.emit("error", fatalNetworkError));
    expect(hlsJS.startLoad).toHaveBeenCalledOnce();
    // The startup guard gives up on a second fatal network error and reports
    // the route as failed, so the server can pick another one.
    vi.spyOn(Date, "now").mockReturnValue(20_000);
    act(() => hlsJS.latest?.emit("error", fatalNetworkError));

    expect(onConnectionLost).not.toHaveBeenCalled();
    expect(onPlanFailure).toHaveBeenCalledOnce();
    expect(onPlanFailure.mock.calls[0]?.[0]).toMatchObject({ classification: "startup_timeout" });
  });
});

describe("VideoPlayer when the server ended the session", () => {
  it("leaves the player and says why instead of showing a lost connection", async () => {
    toastInfo.mockClear();
    const onExit = vi.fn();
    renderPlayer({ onExit, connectionStatus: "ended" });
    await waitFor(() => expect(onExit).toHaveBeenCalledTimes(1), { timeout: 3_000 });
    expect(toastInfo).toHaveBeenCalledWith("An administrator ended this playback session.");
    expect(screen.queryByText("Retry")).toBeNull();
  });
});
