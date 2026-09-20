import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import type { WatchTogetherRoomConnectionResult } from "../hooks/useWatchTogetherRoomConnection";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import type {
  PlaybackRealtimeCommandEnvelope,
  PlaybackRealtimeEventEnvelope,
} from "../realtime-protocol";
import type { PlayerSubtitleInfo } from "../types";
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
    onSeek: (seconds: number) => void;
    activeSubtitleIndex: number | null;
    subtitleTracks: PlayerSubtitleInfo[];
    visible: boolean;
    onSurfaceTap?: (event: React.MouseEvent<HTMLElement>) => void;
    isFullscreen?: boolean;
    onFullscreenToggle?: () => void;
    onSubtitleJobAccepted?: (jobId: string) => void;
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
}));
const toastError = vi.hoisted(() => vi.fn());
const hlsJS = vi.hoisted(() => ({ supported: false, constructed: vi.fn() }));

vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn(), message: vi.fn() } }));

vi.mock("../hooks/usePlaybackRealtime", () => ({
  usePlaybackRealtime: vi.fn((options) => {
    realtimeOptions.current = options;
    return { connectionState: "connected" };
  }),
}));
vi.mock("../hooks/useWatchProgress", () => ({
  useWatchProgress: () => vi.fn().mockResolvedValue(undefined),
}));
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
    return [];
  },
}));
vi.mock("../hooks/useASSSubtitles", () => ({
  useASSSubtitles: (...args: unknown[]) => {
    subtitleTimeline.assOffsetSeconds = args[4] as number;
    return { isActive: false };
  },
}));
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

    constructor(config?: unknown) {
      hlsJS.constructed(config);
    }

    on() {}
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
      onSeek: (seconds: number) => void;
      activeSubtitleIndex: number | null;
      subtitleTracks: PlayerSubtitleInfo[];
      visible: boolean;
      onSurfaceTap?: (event: React.MouseEvent<HTMLElement>) => void;
      isFullscreen?: boolean;
      onFullscreenToggle?: () => void;
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
    transportCommand: null,
    serverTimeOffsetMs: 0,
    sendRoomMessage: vi.fn(() => ({ ok: true })),
    updatePolicy: vi.fn(async () => null),
    selectItem: vi.fn(async () => null),
    closeRoom: vi.fn(async () => {}),
    createSuggestion: vi.fn(async () => {}),
    deleteSuggestion: vi.fn(async () => {}),
    vote: vi.fn(async () => {}),
    unvote: vi.fn(async () => {}),
    promoteSuggestion: vi.fn(async () => null),
    ...overrides,
  };
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

  function setup(localPosition: number, timelineOffset = 0) {
    const connection = roomConnection();
    const onReanchorSeek = vi.fn();
    const rendered = renderPlayer({
      plan: fixturePlanV3({
        ...directPlan,
        delivery: "server_remux_progressive",
        timeline: {
          ...directPlan.timeline,
          timeline_offset_seconds: timelineOffset,
          can_seek_anywhere: false,
        },
      }),
      shouldAutoPlay: false,
      watchTogetherRoomId: "room-1",
      watchTogetherConnection: connection,
      onReanchorSeek,
    });
    const video = rendered.container.querySelector("video")!;
    video.currentTime = localPosition - timelineOffset;
    fireEvent.timeUpdate(video);
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

  it("keeps the actual position when a room seek cannot be sent", () => {
    const { connection, rerenderPlayer } = setup(100);
    connection.room = { ...connection.room!, self_can_manage_room: true };
    vi.mocked(connection.sendRoomMessage).mockReturnValue({ ok: false });
    rerenderPlayer({ watchTogetherConnection: connection });

    act(() => controls.current!.onSeek(1500));
    expect(controls.current!.currentTime).toBe(100);
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
      command.execute_at = new Date(Date.now() - 1_000).toISOString();
      rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
      await act(() => vi.advanceTimersByTimeAsync(0));

      // The room is now at 101, so this member must speed up, not slow down.
      expect(video.playbackRate).toBeGreaterThan(1);
      expect(onReanchorSeek).not.toHaveBeenCalled();
      expect(playerSeek).not.toHaveBeenCalled();

      vi.setSystemTime(Date.now() + 3_000);
      video.currentTime = 103.8 - timelineOffset;
      fireEvent.timeUpdate(video);
      expect(video.playbackRate).toBe(1);
    },
  );

  it("reanchors to the advancing play position when delayed beyond the catch-up band", async () => {
    const { connection, command, rerenderPlayer, onReanchorSeek } = setup(99);
    command.execute_at = new Date(Date.now() - 4_000).toISOString();
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));

    expect(onReanchorSeek).toHaveBeenCalledWith(104);
  });

  it.each([2000, 5000])(
    "recalculates catch-up after autoplay is blocked for %sms",
    async (delayMs) => {
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(101);
      vi.mocked(video.play).mockRejectedValueOnce(new Error("Autoplay blocked"));
      rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
      await act(() => vi.advanceTimersByTimeAsync(0));

      expect(video.playbackRate).toBe(1);
      vi.setSystemTime(Date.now() + delayMs);
      await act(async () => fireEvent.click(screen.getByRole("button", { name: "Join playback" })));

      if (delayMs === 2000) {
        expect(video.playbackRate).toBeGreaterThan(1);
        expect(onReanchorSeek).not.toHaveBeenCalled();
      } else {
        expect(onReanchorSeek).toHaveBeenCalledWith(105);
        expect(video.playbackRate).toBe(1);
      }
    },
  );

  it("resets catch-up when the manual autoplay retry also fails", async () => {
    const { connection, video, command, rerenderPlayer } = setup(101);
    vi.mocked(video.play).mockRejectedValue(new Error("Autoplay blocked"));
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    vi.setSystemTime(Date.now() + 2000);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Join playback" })));
    expect(video.playbackRate).toBe(1);
  });

  it("keeps the autoplay retry usable after a clock offset update", async () => {
    const { connection, video, command, rerenderPlayer } = setup(101);
    vi.mocked(video.play).mockRejectedValueOnce(new Error("Autoplay blocked"));
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
      const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(99, 80);
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
            execute_at: new Date(Date.now() - 1_000).toISOString(),
          },
        },
      });
      await act(() => vi.advanceTimersByTimeAsync(0));

      expect(playerSeek).toHaveBeenCalledWith(action === "play" ? 21 : 20);
      expect(onReanchorSeek).not.toHaveBeenCalled();
      expect(video.playbackRate).toBe(1);
    },
  );

  it("resets an active catch-up when the connection ends", async () => {
    const { connection, video, command, rerenderPlayer } = setup(99);
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
    rerenderPlayer({ watchTogetherConnection: { ...connection, transportCommand: command } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(video.playbackRate).toBeLessThan(1);

    fireEvent.pause(video);
    expect(video.playbackRate).toBe(1);
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
      const { container } = renderPlayer({ shouldAutoPlay: false });
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
      expect(playerSeek).toHaveBeenCalledWith(40);
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

    video.currentTime = 12;
    fireEvent.timeUpdate(video);
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

describe("VideoPlayer native HLS timeline", () => {
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
});
