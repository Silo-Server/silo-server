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
    onSeek: (seconds: number) => void;
    activeSubtitleIndex: number | null;
    subtitleTracks: PlayerSubtitleInfo[];
    visible: boolean;
    onSurfaceTap?: (event: React.MouseEvent<HTMLElement>) => void;
    isFullscreen?: boolean;
    onFullscreenToggle?: () => void;
    videoFit?: VideoFitMode;
    onVideoFitToggle?: () => void;
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

  it("pauses displaced playback and offers an explicit room rejoin", () => {
    const { connection, video, rerenderPlayer } = setup(100);
    vi.mocked(video.pause).mockClear();
    rerenderPlayer({
      watchTogetherConnection: {
        ...connection,
        connectionState: "disconnected",
        replacementReason: "This profile joined the Watch Party on another device.",
      },
    });
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
    vi.setSystemTime(Date.now() + 2_000);
    await correct("correction-2");
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);

    // The rebuild plays four seconds after it started.
    vi.setSystemTime(Date.now() + 2_000);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    video.currentTime = 100;
    fireEvent.timeUpdate(video);

    // The next rebuild waits out the backoff, then aims ahead by that startup.
    vi.setSystemTime(Date.now() + 5_000);
    await correct("correction-3", 120);
    expect(onReanchorSeek).toHaveBeenCalledTimes(1);

    vi.setSystemTime(Date.now() + 5_000);
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
    vi.setSystemTime(Date.now() + 2_000);
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
    vi.setSystemTime(Date.now() + 4_000);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    fireEvent.loadStart(video);
    fireEvent.seeking(video);
    video.currentTime = 100;
    fireEvent.timeUpdate(video);

    // Had it settled, the backoff would have allowed another reload by now.
    vi.setSystemTime(Date.now() + 11_000);
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
    vi.setSystemTime(Date.now() + 31_000);
    await correct("correction-2", 100);
    expect(onReanchorSeek).toHaveBeenCalledTimes(2);

    // The first reload's replan completes late; it must not count as the second's load.
    await act(async () => resolvers[0]!(true));
    vi.setSystemTime(Date.now() + 2_000);
    Object.defineProperty(video, "paused", { configurable: true, value: false });
    Object.defineProperty(video, "readyState", { configurable: true, value: 3 });
    video.currentTime = 100;
    fireEvent.timeUpdate(video);

    // Had the second reload landed, its 20 s backoff would have ended.
    vi.setSystemTime(Date.now() + 21_000);
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
    vi.setSystemTime(Date.now() + 2_000);
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
      expect(messages).toHaveBeenCalledWith({
        type: "state_report",
        session_id: "session-1",
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
    const { connection, video, command, rerenderPlayer, onReanchorSeek } = setup(80);
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
      position_seconds: 80,
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
      vi.mocked(video.play).mockRejectedValueOnce(
        new DOMException("Autoplay blocked", "NotAllowedError"),
      );
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
