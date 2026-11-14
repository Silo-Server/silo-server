import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient } from "@tanstack/react-query";
import { createElement, useCallback, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { itemKeys } from "@/hooks/queries/keys";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import { derivePersistedSubtitleMode } from "../utils/subtitleMode";
import type { UsePlaybackSessionResult } from "../hooks/usePlaybackSession";
import type { PlaybackSourceCommittedPayload } from "../realtime-protocol";
import type {
  PlayerAudioTrack,
  PlayerFileVersion,
  PlayerSubtitleInfo,
  WatchPageProps,
} from "../types";
import {
  INVENTORY_REFRESH_DEADLINE_MS,
  INVENTORY_REFRESH_INTERVAL_MS,
  inventoryPollDelayMs,
  mergeResolvedLiveVersion,
  WatchPage,
} from "./WatchPage";

const playbackSessionMock = vi.hoisted(() => vi.fn());
const videoPlayerMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());
const fetchWatchDetailMock = vi.hoisted(() => vi.fn());
const awaitVirtualCandidatesRefreshMock = vi.hoisted(() => vi.fn());
const awaitAdminJobMock = vi.hoisted(() => vi.fn());
const fetchQueryMock = vi.hoisted(() => vi.fn());
// When set, `useQueryClient` hands back this real client instead of the
// pass-through fake so a test can exercise the react-query cache itself.
const queryClientOverride = vi.hoisted(() => ({ current: null as unknown }));
const roomConnectionMock = vi.hoisted(() => vi.fn());
const playbackCapabilitiesMock = vi.hoisted(() => vi.fn());
const startPlaybackMock = vi.hoisted(() => vi.fn());
const trickplayRefetchMock = vi.hoisted(() => vi.fn());
vi.mock("../start-v2", () => ({ playbackCapabilitiesV2: playbackCapabilitiesMock }));

vi.mock("../hooks/usePlaybackSession", () => ({
  usePlaybackSession: playbackSessionMock,
}));
vi.mock("@/hooks/queries/items", () => ({
  fetchWatchDetail: fetchWatchDetailMock,
}));
vi.mock("@/api/v2/mediaCandidates", () => ({
  awaitVirtualCandidatesRefresh: awaitVirtualCandidatesRefreshMock,
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ awaitAdminJob: awaitAdminJobMock }),
}));
vi.mock("./VideoPlayer", () => ({
  VideoPlayer: (props: unknown) => {
    videoPlayerMock(props);
    return "Mounted video player";
  },
}));
const playerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "test-device",
};
vi.mock("../context/PlayerConfigContext", () => ({
  usePlayerConfig: () => playerConfig,
}));
vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQueryClient: () => queryClientOverride.current ?? { fetchQuery: fetchQueryMock },
    useQuery: () => ({ data: undefined, refetch: trickplayRefetchMock }),
  };
});
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: startPlaybackMock }),
}));
vi.mock("../hooks/useWatchTogetherRoomConnection", () => ({
  useWatchTogetherRoomConnection: roomConnectionMock,
}));
vi.mock("sonner", () => ({
  toast: { error: toastErrorMock },
}));

const version: PlayerFileVersion = {
  file_id: 7,
  resolution: "1080p",
  codec_video: "h264",
  codec_audio: "aac",
  hdr: false,
  container: "mp4",
  file_size: 1,
  duration: 3600,
  bitrate: 1,
  chapters: [{ index: 0, title: "Chapter", start_seconds: 0, end_seconds: 3600, source: "test" }],
};

const watchPageProps: WatchPageProps = {
  seekIntervals: { back: 10, forward: 30 },
  contentId: "content-1",
  title: "Test movie",
  versions: [version],
  subtitles: [],
  intro: null,
  credits: null,
  onExit: vi.fn(),
};

function playbackSession(
  overrides: Partial<UsePlaybackSessionResult> = {},
): UsePlaybackSessionResult {
  return {
    plan: fixturePlanV3(),
    planRevision: 1,
    transportRevision: 1,
    replayRevision: 0,
    streamUrl: "/stream/session-1",
    sessionId: "session-1",
    playbackAttemptId: "attempt-1",
    mediaFileId: 7,
    effectiveVirtualUri: null,
    inventoryRevision: null,
    inventoryUrl: null,
    inventoryPending: false,
    inventoryFailed: false,
    initialPosition: 0,
    audioTrackIndex: 0,
    durationSeconds: 3600,
    subtitleUrls: [],
    planAudioTracks: [],
    audioInventoryProvisional: false,
    subtitleInventoryProvisional: false,
    qualityPreference: "original",
    shouldAutoPlay: true,
    loading: false,
    replacing: false,
    replanning: false,
    autoFallback: false,
    replanningQuality: false,
    pendingSwitchFileId: null,
    errorTitle: null,
    error: null,
    errorReason: null,
    errorRetryable: false,
    retrying: false,
    initialSubtitleErrorTitle: null,
    initialSubtitleError: null,
    trackSelectionNotice: null,
    connectionStatus: "connected",
    connectionErrorTitle: null,
    connectionError: null,
    browserEngine: null,
    nativeOriginalSupport: { containers: [], videoCodecs: [], audioCodecs: [] },
    switchVersion: vi.fn(),
    selectAutoVersion: vi.fn(),
    retryStart: vi.fn(),
    switchAudioTrack: vi.fn(),
    changeSubtitleTrack: vi.fn(),
    changeQuality: vi.fn(),
    recoverFromFailure: vi.fn(),
    recoverConnection: vi.fn(),
    retryConnection: vi.fn(),
    expediteReconnect: vi.fn(),
    reportSessionLost: vi.fn(),
    markSessionGone: vi.fn(),
    restartPlayback: vi.fn(),
    invalidatePlan: vi.fn().mockResolvedValue(true),
    reanchorSeek: vi.fn().mockResolvedValue(true),
    refreshSubtitles: vi.fn(),
    pollInventory: vi.fn().mockResolvedValue(true),
    markInventoryExhausted: vi.fn(),
    markInventoryRetrying: vi.fn(),
    applySubtitleTrack: vi.fn(),
    applyAudioInventory: vi.fn(),
    applyCommittedSource: vi.fn(),
    applyInventoryUpdate: vi.fn(),
    updatePlaybackState: vi.fn(),
    reportFirstFrame: vi.fn(),
    reportEvent: vi.fn(),
    ...overrides,
  };
}

beforeEach(() => {
  roomConnectionMock.mockReset().mockReturnValue({ room: null });
  playbackCapabilitiesMock.mockReset().mockResolvedValue({
    features: [
      "watch_party_coordinator_v1",
      "fixed_media_file_v1",
      "watch_party_source_fallback_v1",
    ],
  });
  startPlaybackMock.mockReset();
  playbackSessionMock.mockReset();
  videoPlayerMock.mockReset();
  trickplayRefetchMock.mockReset();
  toastErrorMock.mockReset();
  fetchWatchDetailMock.mockReset();
  awaitVirtualCandidatesRefreshMock.mockReset();
  awaitAdminJobMock.mockReset().mockResolvedValue({ id: "job-1", status: "completed" });
  // The component reads watch detail through the shared react-query cache. The
  // fake client passes straight through to the queryFn so these tests keep
  // exercising the poll's attempt/deadline logic; the cache dedupe itself is
  // covered in items.test.ts.
  fetchQueryMock.mockReset();
  fetchQueryMock.mockImplementation((options: { queryFn: () => unknown }) => options.queryFn());
  queryClientOverride.current = null;
});

describe("derivePersistedSubtitleMode", () => {
  it("persists an enabled mode when a subtitle track is selected", () => {
    expect(derivePersistedSubtitleMode(3)).toBe("always");
  });

  it("persists off when subtitles are disabled", () => {
    expect(derivePersistedSubtitleMode(null)).toBe("off");
  });
});

describe("inventoryPollDelayMs", () => {
  it("runs the first attempts on a short early cadence", () => {
    expect(inventoryPollDelayMs(0, false)).toBe(2_000);
    expect(inventoryPollDelayMs(1, false)).toBe(4_000);
    expect(inventoryPollDelayMs(2, false)).toBe(8_000);
  });

  it("settles into the steady interval after the early attempts", () => {
    expect(inventoryPollDelayMs(3, false)).toBe(INVENTORY_REFRESH_INTERVAL_MS);
    expect(inventoryPollDelayMs(10, false)).toBe(INVENTORY_REFRESH_INTERVAL_MS);
  });

  it("returns zero once the inventory is found so polling stops", () => {
    expect(inventoryPollDelayMs(0, true)).toBe(0);
  });
});

describe("WatchPage playback errors", () => {
  it("requires the coordinator before opening room playback", async () => {
    playbackCapabilitiesMock.mockResolvedValue({ features: ["fixed_media_file_v1"] });
    playbackSessionMock.mockReturnValue(playbackSession());
    render(
      createElement(WatchPage, {
        ...watchPageProps,
        watchTogetherRoomId: "room-1",
        watchTogetherRoomToken: "proof",
      }),
    );
    expect(playbackSessionMock).not.toHaveBeenCalled();
    expect(roomConnectionMock).not.toHaveBeenCalled();
    expect(
      await screen.findByText("This server needs an update to support Watch Party."),
    ).toBeInTheDocument();
    expect(videoPlayerMock).not.toHaveBeenCalled();
  });
  it("waits for capability confirmation before mounting the room player", async () => {
    let finish!: (value: { features: string[] }) => void;
    playbackCapabilitiesMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    playbackSessionMock.mockReturnValue(playbackSession());
    render(
      createElement(WatchPage, {
        ...watchPageProps,
        watchTogetherRoomId: "room-1",
        watchTogetherRoomToken: "proof",
      }),
    );
    expect(screen.getByText("Checking Watch Party support...")).toBeInTheDocument();
    expect(playbackSessionMock).not.toHaveBeenCalled();
    expect(roomConnectionMock).not.toHaveBeenCalled();
    await act(async () =>
      finish({ features: ["watch_party_coordinator_v1", "fixed_media_file_v1"] }),
    );
    expect(screen.getByText("Mounted video player")).toBeInTheDocument();
  });
  it("retries a failed capability check without starting playback early", async () => {
    playbackCapabilitiesMock.mockRejectedValueOnce(new Error("Network unavailable"));
    playbackSessionMock.mockReturnValue(playbackSession());
    render(
      createElement(WatchPage, {
        ...watchPageProps,
        watchTogetherRoomId: "room-1",
        watchTogetherRoomToken: "proof",
      }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Try Again" }));
    expect(playbackSessionMock).not.toHaveBeenCalled();
    expect(roomConnectionMock).not.toHaveBeenCalled();
    expect(await screen.findByText("Mounted video player")).toBeInTheDocument();
    expect(playbackCapabilitiesMock).toHaveBeenCalledTimes(2);
  });
  it("ignores capability confirmation after leaving the player", async () => {
    let finish!: (value: { features: string[] }) => void;
    playbackCapabilitiesMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const view = render(
      createElement(WatchPage, {
        ...watchPageProps,
        watchTogetherRoomId: "room-1",
        watchTogetherRoomToken: "proof",
      }),
    );
    view.unmount();
    await act(async () =>
      finish({ features: ["watch_party_coordinator_v1", "fixed_media_file_v1"] }),
    );
    expect(playbackSessionMock).not.toHaveBeenCalled();
    expect(roomConnectionMock).not.toHaveBeenCalled();
  });
  it("pins the room file and disables version changes while keeping quality controls", async () => {
    playbackSessionMock.mockReturnValue(playbackSession());
    render(
      createElement(WatchPage, {
        ...watchPageProps,
        fileId: 7,
        watchTogetherRoomId: "room-1",
        watchTogetherRoomToken: "room-token",
      }),
    );

    await waitFor(() => expect(videoPlayerMock).toHaveBeenCalled());
    expect(videoPlayerMock.mock.calls.at(-1)?.[0].onSwitchVersion).toBeUndefined();
    expect(videoPlayerMock.mock.calls.at(-1)?.[0].onQualitySelect).toBeTypeOf("function");
    expect(playbackSessionMock.mock.calls.at(-1)?.[12]).toBe(false);
  });

  it("keeps the player mounted when a replan fails with an active plan", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ errorTitle: "Quality change failed", error: "Temporary server error" }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByText("Mounted video player")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Go Back" })).not.toBeInTheDocument();
    expect(playbackCapabilitiesMock).not.toHaveBeenCalled();
  });

  it("shows the fatal error screen when startup fails without a plan", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        errorTitle: "Playback unavailable",
        error: "Failed to start playback",
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByText("Failed to start playback")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go Back" })).toBeInTheDocument();
    expect(screen.queryByText("Mounted video player")).not.toBeInTheDocument();
  });

  it("offers Try again for a retryable virtual-source terminal and keeps Go Back", () => {
    const retryStart = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        errorTitle: "Playback unavailable",
        error: "The virtual source could not be resolved for playback.",
        errorReason: "virtual_source_unavailable",
        errorRetryable: true,
        retryStart,
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(
      screen.getByText("The virtual source could not be resolved for playback."),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(retryStart).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Go Back" })).toBeInTheDocument();
  });

  it("offers Try again for a retryable transcode start failure such as a failed seek anchor", () => {
    const retryStart = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        errorTitle: "Playback unavailable",
        error: "Failed to resolve remux seek position.",
        errorReason: "transcode_start_failed",
        errorRetryable: true,
        retryStart,
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByText("Failed to resolve remux seek position.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(retryStart).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Go Back" })).toBeInTheDocument();
  });

  it("offers Try again for an adaptation refusal the server marked non-retryable", () => {
    const retryStart = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        errorTitle: "No playable version found",
        error: "Vio couldn't find a way to play this file on this device.",
        errorReason: "adaptation_unavailable",
        errorRetryable: false,
        retryStart,
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByText("No playable version found")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(retryStart).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Go Back" })).toBeInTheDocument();
  });

  it("offers Restart playback for a session the server answered gone", () => {
    const restartPlayback = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        errorTitle: null,
        error: null,
        connectionStatus: "gone",
        connectionErrorTitle: "Playback session expired",
        connectionError:
          "This playback session is no longer active. Start it again to keep watching.",
        restartPlayback,
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByText("Playback session expired")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Restart playback" }));
    expect(restartPlayback).toHaveBeenCalledTimes(1);
  });

  it("keeps a non-retryable terminal a Go Back-only dead-end", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        errorTitle: "This video is no longer available",
        error: "The file needed to play it can't be found right now.",
        errorReason: "source_unavailable",
        errorRetryable: false,
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go Back" })).toBeInTheDocument();
  });

  it("keeps a refused initial bitmap subtitle off without treating it as a playback error", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        initialSubtitleErrorTitle: "That subtitle track can't be used",
        initialSubtitleError: "Enable HDR transcoding to use this subtitle.",
      }),
    );

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        subtitleMode: "always",
        showForcedSubtitles: true,
      }),
    );

    const props = videoPlayerMock.mock.calls[0]?.[0] as {
      subtitleMode?: string;
      showForcedSubtitles?: boolean;
      replanError?: string | null;
    };
    expect(props.subtitleMode).toBe("off");
    expect(props.showForcedSubtitles).toBe(false);
    expect(props.replanError).toBeNull();
    expect(toastErrorMock).toHaveBeenCalledWith("That subtitle track can't be used", {
      description: "Enable HDR transcoding to use this subtitle.",
    });
  });
});

describe("WatchPage playback state", () => {
  it("keeps the session resume anchor current while forwarding state", () => {
    const updatePlaybackState = vi.fn();
    const onPlaybackStateChange = vi.fn();
    playbackSessionMock.mockReturnValue(playbackSession({ updatePlaybackState }));

    render(createElement(WatchPage, { ...watchPageProps, onPlaybackStateChange }));

    const props = videoPlayerMock.mock.calls[0]?.[0] as {
      onPlaybackStateChange?: (state: {
        currentTime: number;
        duration: number;
        playing: boolean;
      }) => void;
    };
    const state = { currentTime: 321, duration: 3600, playing: true };
    props.onPlaybackStateChange?.(state);

    expect(updatePlaybackState).toHaveBeenCalledWith(321, true);
    expect(onPlaybackStateChange).toHaveBeenCalledWith(state);
  });
});

describe("WatchPage exit selection", () => {
  it("carries the requested file beside the effective one on end", async () => {
    const onEnded = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({ requested_media_file_id: 7, effective_media_file_id: 8 }),
        mediaFileId: 8,
      }),
    );
    render(
      createElement(WatchPage, {
        ...watchPageProps,
        fileId: 7,
        onEnded,
      }),
    );
    await waitFor(() => expect(videoPlayerMock).toHaveBeenCalled());
    const props = videoPlayerMock.mock.calls.at(-1)?.[0] as { onEnded?: () => void };
    act(() => props.onEnded?.());

    expect(onEnded).toHaveBeenCalledWith(
      expect.objectContaining({
        lastFileId: 8,
        selectedFileId: 7,
      }),
    );
  });

  it("reports a committed requested version change to the host", async () => {
    const onRequestedVersionChange = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({ plan: fixturePlanV3({ requested_media_file_id: 7 }) }),
    );
    const { rerender } = render(
      createElement(WatchPage, {
        ...watchPageProps,
        onRequestedVersionChange,
      }),
    );
    await waitFor(() => expect(videoPlayerMock).toHaveBeenCalled());
    // Mounting on the current row is not a switch.
    expect(onRequestedVersionChange).not.toHaveBeenCalled();

    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({
          plan_id: "plan:version-switch",
          requested_media_file_id: 8,
          effective_media_file_id: 8,
        }),
        mediaFileId: 8,
      }),
    );
    rerender(
      createElement(WatchPage, {
        ...watchPageProps,
        onRequestedVersionChange,
      }),
    );

    // A row the session committed outside Auto is the viewer's pick, and the
    // host records it as one so a later Play keeps pinning it.
    await waitFor(() => expect(onRequestedVersionChange).toHaveBeenCalledWith(8, true));
  });

  it("reports the row Auto chose as a hint, not a pin", async () => {
    const onRequestedVersionChange = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({ requested_media_file_id: 7 }),
        autoFallback: true,
      }),
    );
    const { rerender } = render(
      createElement(WatchPage, {
        ...watchPageProps,
        onRequestedVersionChange,
      }),
    );
    await waitFor(() => expect(videoPlayerMock).toHaveBeenCalled());

    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({
          plan_id: "plan:auto-pick",
          requested_media_file_id: 9,
          effective_media_file_id: 9,
        }),
        mediaFileId: 9,
        autoFallback: true,
      }),
    );
    rerender(
      createElement(WatchPage, {
        ...watchPageProps,
        onRequestedVersionChange,
      }),
    );

    await waitFor(() => expect(onRequestedVersionChange).toHaveBeenCalledWith(9, false));
  });
});

describe("WatchPage audio menu", () => {
  it("prefers the plan's audio inventory over item metadata", () => {
    const planAudioTracks = [
      { language: "eng", codec: "aac", channels: 2, default: true },
      { language: "spa", codec: "ac3", channels: 6, default: false },
    ];
    playbackSessionMock.mockReturnValue(playbackSession({ planAudioTracks }));

    render(createElement(WatchPage, watchPageProps));

    const props = videoPlayerMock.mock.calls[0]?.[0] as { audioTracks?: unknown[] };
    expect(props.audioTracks).toEqual(planAudioTracks);
  });

  it("adopts a committed source's declared inventory and re-keys on a real change", () => {
    const applyCommittedSource = vi.fn();
    const refreshSubtitles = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        applyCommittedSource,
        refreshSubtitles,
        mediaFileId: 7,
        effectiveVirtualUri: "virtual://movie/x?result=A",
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    const props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onSourceCommitted?: (payload: PlaybackSourceCommittedPayload) => void;
    };
    props.onSourceCommitted?.({
      session_id: "session-1",
      effective_media_file_id: 8,
      effective_virtual_uri: "virtual://movie/x?result=B",
      inventory_status: "declared",
      audio_tracks: [{ language: "deu", codec: "eac3", channels: 6, default: true }],
    });

    expect(applyCommittedSource).toHaveBeenCalledWith(
      {
        effectiveMediaFileId: 8,
        effectiveVirtualUri: "virtual://movie/x?result=B",
        inventoryStatus: "declared",
      },
      [{ language: "deu", codec: "eac3", channels: 6, default: true }],
    );
    // The source actually changed, so the subtitle inventory is re-read.
    expect(refreshSubtitles).toHaveBeenCalled();
  });

  it("does not fall back to another release's tracks when the plan names an effective source", () => {
    const otherRelease: PlayerFileVersion = {
      ...version,
      file_id: 9,
      file_path: "virtual://movie/x?result=OTHER",
      audio_tracks: [{ language: "eng", codec: "aac", channels: 2, default: true }],
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [],
        mediaFileId: 7,
        effectiveVirtualUri: "virtual://movie/x?result=B",
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [otherRelease] }));

    const props = videoPlayerMock.mock.calls.at(-1)?.[0] as { audioTracks?: unknown[] };
    // No row names the effective source; the menu must stay empty rather than
    // show the unrelated release's tracks.
    expect(props.audioTracks).toEqual([]);
  });

  it("falls back to the version's item metadata when the plan publishes no inventory", () => {
    const versionWithTracks: PlayerFileVersion = {
      ...version,
      audio_tracks: [{ language: "eng", codec: "aac", channels: 2, default: true }],
    };
    playbackSessionMock.mockReturnValue(playbackSession({ planAudioTracks: [] }));

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [versionWithTracks],
      }),
    );

    const props = videoPlayerMock.mock.calls[0]?.[0] as { audioTracks?: unknown[] };
    expect(props.audioTracks).toEqual(versionWithTracks.audio_tracks);
  });

  it("forwards each inventory's provisional state to the player", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        audioInventoryProvisional: true,
        subtitleInventoryProvisional: false,
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    const props = videoPlayerMock.mock.calls[0]?.[0] as {
      audioInventoryProvisional?: boolean;
      subtitleInventoryProvisional?: boolean;
    };
    expect(props.audioInventoryProvisional).toBe(true);
    expect(props.subtitleInventoryProvisional).toBe(false);
  });
});

describe("WatchPage overlay inventory provenance", () => {
  const catalogSubtitle: PlayerSubtitleInfo = {
    index: 4,
    language: "eng",
    codec: "subrip",
    label: "English",
    source: "embedded",
    url: "/stream/session-1/subtitles/4.vtt",
  };
  const liveSubtitle: PlayerSubtitleInfo = {
    index: 6,
    language: "fra",
    codec: "subrip",
    label: "French",
    source: "embedded",
    url: "/stream/session-1/subtitles/6.vtt",
  };
  const catalogAudio = { language: "eng", codec: "aac", channels: 2, default: true };
  const liveAudio = { language: "fra", codec: "ac3", channels: 6, default: true };

  it("shows the playing version's declared subtitles immediately, then swaps in probe evidence", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ subtitleUrls: [], subtitleInventoryProvisional: false }),
    );
    const view = render(
      createElement(WatchPage, { ...watchPageProps, subtitles: [catalogSubtitle] }),
    );

    let props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      subtitleUrls?: PlayerSubtitleInfo[];
      subtitleInventoryProvisional?: boolean;
      shouldAutoPlay?: boolean;
      streamUrl?: string | null;
    };
    // The playing version's own declaration is inventory: show it now rather
    // than sitting on "Discovering" until the probe lands.
    expect(props.subtitleUrls).toEqual([catalogSubtitle]);
    expect(props.subtitleInventoryProvisional).toBe(false);
    // Playback starts unblocked either way.
    expect(props.shouldAutoPlay).toBe(true);
    expect(props.streamUrl).toBe("/stream/session-1");

    playbackSessionMock.mockReturnValue(
      playbackSession({
        subtitleUrls: [liveSubtitle],
        subtitleInventoryProvisional: false,
      }),
    );
    view.rerender(createElement(WatchPage, { ...watchPageProps, subtitles: [catalogSubtitle] }));

    props = videoPlayerMock.mock.calls.at(-1)?.[0] as typeof props;
    expect(props.subtitleUrls).toEqual([liveSubtitle]);
    expect(props.subtitleInventoryProvisional).toBe(false);
  });

  it("shows the plan's declared audio inventory immediately, then swaps in probe evidence", () => {
    const versionWithCatalogAudio: PlayerFileVersion = {
      ...version,
      audio_tracks: [catalogAudio],
    };
    const declaredPlanAudio = { language: "eng", codec: "aac", channels: 2 };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [declaredPlanAudio],
        audioInventoryProvisional: true,
      }),
    );
    const view = render(
      createElement(WatchPage, { ...watchPageProps, versions: [versionWithCatalogAudio] }),
    );

    let props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      audioTracks?: unknown[];
      audioInventoryProvisional?: boolean;
      shouldAutoPlay?: boolean;
    };
    // The plan's declared rows are shown immediately and the menu labels them
    // unverified; they are not withheld behind a discovery state.
    expect(props.audioTracks).toEqual([declaredPlanAudio]);
    expect(props.audioInventoryProvisional).toBe(false);
    expect(props.shouldAutoPlay).toBe(true);

    playbackSessionMock.mockReturnValue(
      playbackSession({ planAudioTracks: [liveAudio], audioInventoryProvisional: false }),
    );
    view.rerender(
      createElement(WatchPage, { ...watchPageProps, versions: [versionWithCatalogAudio] }),
    );

    props = videoPlayerMock.mock.calls.at(-1)?.[0] as typeof props;
    expect(props.audioTracks).toEqual([liveAudio]);
    expect(props.audioInventoryProvisional).toBe(false);
  });

  it("falls back to the playing version's declared audio when the plan publishes none", () => {
    const versionWithCatalogAudio: PlayerFileVersion = {
      ...version,
      audio_tracks: [catalogAudio],
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({ planAudioTracks: [], audioInventoryProvisional: true }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [versionWithCatalogAudio] }));

    const props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      audioTracks?: unknown[];
      audioInventoryProvisional?: boolean;
    };
    expect(props.audioTracks).toEqual([catalogAudio]);
    expect(props.audioInventoryProvisional).toBe(false);
  });

  it("keeps an empty verified inventory final instead of waiting on a probe", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ subtitleUrls: [], subtitleInventoryProvisional: false }),
    );

    render(createElement(WatchPage, { ...watchPageProps, subtitles: [] }));

    const props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      subtitleInventoryProvisional?: boolean;
    };
    expect(props.subtitleInventoryProvisional).toBe(false);
  });
});

describe("WatchPage version list refresh", () => {
  const firstVersion: PlayerFileVersion = { ...version, file_id: 7 };
  const secondVersion: PlayerFileVersion = { ...version, file_id: 8 };

  it("waits on the refresh job then re-reads the server's list", async () => {
    const refreshed: PlayerFileVersion = { ...version, file_id: 9, resolution: "720p" };
    awaitVirtualCandidatesRefreshMock.mockResolvedValueOnce(undefined);
    fetchWatchDetailMock.mockResolvedValueOnce({
      content_id: "content-1",
      versions: [refreshed],
      indexer_releases: [],
    });
    playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 7 }));

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [firstVersion, secondVersion],
      }),
    );

    const before = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRefreshVersions?: () => Promise<void>;
    };
    expect(awaitVirtualCandidatesRefreshMock).not.toHaveBeenCalled();

    await act(async () => {
      await before.onRefreshVersions?.();
    });

    // The async flow is awaited with the realtime job helper, then the list is
    // re-read from the watch detail. The live session's own row is re-keyed in
    // because the refreshed list does not carry it.
    expect(awaitVirtualCandidatesRefreshMock).toHaveBeenCalledTimes(1);
    expect(awaitVirtualCandidatesRefreshMock).toHaveBeenCalledWith("content-1", awaitAdminJobMock);
    const after = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      versions?: PlayerFileVersion[];
    };
    expect(after.versions).toEqual([refreshed, firstVersion]);
  });

  it("re-keys the committed source into the refreshed list", async () => {
    const refreshed: PlayerFileVersion = { ...version, file_id: 9, resolution: "720p" };
    awaitVirtualCandidatesRefreshMock.mockResolvedValueOnce(undefined);
    fetchWatchDetailMock.mockResolvedValueOnce({
      content_id: "content-1",
      versions: [refreshed],
      indexer_releases: [],
    });
    // The live session is on a virtual candidate (file 7) the refreshed list
    // does not name; only the mount-time candidates carry its row.
    playbackSessionMock.mockReturnValue(
      playbackSession({ mediaFileId: 7, effectiveVirtualUri: "virtual://movie/x?result=A" }),
    );

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [firstVersion, secondVersion],
      }),
    );

    const before = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRefreshVersions?: () => Promise<void>;
    };
    await act(async () => {
      await before.onRefreshVersions?.();
    });

    const after = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      versions?: PlayerFileVersion[];
      activeFileId?: number | null;
    };
    // The committed row stays present so the menus keep resolving it.
    expect(after.versions).toEqual([refreshed, firstVersion]);
    expect(after.activeFileId).toBe(7);
  });

  it("forwards the refreshed indexer releases to the menu", async () => {
    awaitVirtualCandidatesRefreshMock.mockResolvedValueOnce(undefined);
    fetchWatchDetailMock.mockResolvedValueOnce({
      content_id: "content-1",
      versions: [firstVersion],
      indexer_releases: [
        { release_id: "rel-1", title: "Movie 2026 2160p", download_state: "not_downloaded" },
      ],
    });
    playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 7 }));

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [firstVersion, secondVersion],
      }),
    );

    const before = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRefreshVersions?: () => Promise<void>;
    };
    await act(async () => {
      await before.onRefreshVersions?.();
    });

    const after = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      indexerReleases?: unknown[];
    };
    expect(after.indexerReleases).toEqual([
      { release_id: "rel-1", title: "Movie 2026 2160p", download_state: "not_downloaded" },
    ]);
  });

  it("keeps the known candidates when the refresh fails", async () => {
    awaitVirtualCandidatesRefreshMock.mockRejectedValueOnce(new Error("network"));
    playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 7 }));

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [firstVersion, secondVersion],
      }),
    );

    const props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRefreshVersions?: () => Promise<void>;
    };

    await act(async () => {
      await expect(props.onRefreshVersions?.()).rejects.toThrow("network");
    });

    // The re-read never runs behind a failed job; the known rows stay.
    expect(fetchWatchDetailMock).not.toHaveBeenCalled();
    const after = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      versions?: PlayerFileVersion[];
    };
    expect(after.versions).toEqual([firstVersion, secondVersion]);
  });

  it("re-stamps the parent's liveness verdicts onto the refreshed rows", async () => {
    const refreshed: PlayerFileVersion = {
      ...version,
      file_id: 9,
      resolution: "720p",
      available: undefined,
    };
    awaitVirtualCandidatesRefreshMock.mockResolvedValueOnce(undefined);
    fetchWatchDetailMock.mockResolvedValueOnce({
      content_id: "content-1",
      versions: [refreshed],
      indexer_releases: [],
    });
    playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 7 }));

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [firstVersion, secondVersion],
        versionLiveness: new Map([[9, { available: false }]]),
      }),
    );

    const before = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRefreshVersions?: () => Promise<void>;
    };
    await act(async () => {
      await before.onRefreshVersions?.();
    });

    // The server's refreshed row carries no verdict; the parent checked it as
    // dead, so the badge has to survive the manual refresh.
    const after = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      versions?: PlayerFileVersion[];
    };
    expect(after.versions?.[0]).toMatchObject({ file_id: 9, available: false });
  });
});

describe("WatchPage version track coupling", () => {
  const versionA: PlayerFileVersion = { ...version, file_id: 7 };
  const versionB: PlayerFileVersion = { ...version, file_id: 8 };
  const audioA: PlayerAudioTrack[] = [
    { codec: "aac", channels: 2, language: "eng", default: true },
  ];
  const audioB: PlayerAudioTrack[] = [
    { codec: "eac3", channels: 6, layout: "5.1", language: "spa", default: true },
  ];
  const subtitleA = {
    index: 0,
    language: "en",
    codec: "srt",
    label: "English",
    source: "embedded" as const,
    url: "/subs/a.vtt",
  };
  const subtitleB = {
    index: 0,
    language: "fr",
    codec: "srt",
    label: "French",
    source: "embedded" as const,
    url: "/subs/b.vtt",
  };

  it("updates the audio and subtitle lists when the viewer switches version", () => {
    // A stateful harness so the switch actually moves the session to the other
    // file, the way usePlaybackSession's replan does in production: after the
    // switch the plan publishes the new candidate's tracks, not the old one's.
    function Harness() {
      const [mediaFileId, setMediaFileId] = useState(7);
      playbackSessionMock.mockReturnValue(
        playbackSession({
          mediaFileId,
          planAudioTracks: mediaFileId === 8 ? audioB : audioA,
          subtitleUrls: [mediaFileId === 8 ? subtitleB : subtitleA],
          switchVersion: (nextFileId: number) => setMediaFileId(nextFileId),
        }),
      );
      return createElement(WatchPage, {
        ...watchPageProps,
        versions: [versionA, versionB],
      });
    }

    render(createElement(Harness));

    const before = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      audioTracks?: PlayerAudioTrack[];
      subtitleUrls?: unknown[];
      onSwitchVersion?: (fileId: number) => void;
    };
    expect(before.audioTracks).toEqual(audioA);
    expect(before.subtitleUrls).toEqual([subtitleA]);

    act(() => {
      before.onSwitchVersion?.(8);
    });

    const after = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      audioTracks?: PlayerAudioTrack[];
      subtitleUrls?: unknown[];
    };
    expect(after.audioTracks).toEqual(audioB);
    expect(after.subtitleUrls).toEqual([subtitleB]);
  });
});

describe("WatchPage version switch feedback", () => {
  it("shows a non-blocking switching indicator while replacing with an active plan", () => {
    playbackSessionMock.mockReturnValue(playbackSession({ replacing: true }));

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByRole("status", { name: "Switching version" })).toBeInTheDocument();
    expect(screen.getByText("Switching version…")).toBeInTheDocument();
    // The old stream keeps playing: the player stays mounted.
    expect(screen.getByText("Mounted video player")).toBeInTheDocument();
  });

  it("keeps the full-screen loading overlay for the no-plan case", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        loading: true,
        replacing: true,
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByText("Loading player...")).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "Switching version" })).not.toBeInTheDocument();
    expect(screen.queryByText("Mounted video player")).not.toBeInTheDocument();
  });

  it("forwards the quality-replan and pending-switch flags to the player", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ replanningQuality: true, pendingSwitchFileId: 99 }),
    );

    render(createElement(WatchPage, watchPageProps));

    const props = videoPlayerMock.mock.calls[0]?.[0] as {
      replanningQuality?: boolean;
      pendingSwitchFileId?: number | null;
    };
    expect(props.replanningQuality).toBe(true);
    expect(props.pendingSwitchFileId).toBe(99);
  });

  it("forwards the replace and replan state so the track menus can gate", () => {
    playbackSessionMock.mockReturnValue(playbackSession({ replacing: true, replanning: true }));

    render(createElement(WatchPage, watchPageProps));

    const props = videoPlayerMock.mock.calls[0]?.[0] as {
      replacing?: boolean;
      replanning?: boolean;
    };
    expect(props.replacing).toBe(true);
    expect(props.replanning).toBe(true);
  });

  it("shows a dismissible notice when the server played a different version than auto-selected", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({ requested_media_file_id: 7, effective_media_file_id: 8 }),
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(
      screen.getByText(
        "Playing a different version than selected — the requested version isn't playable on this device.",
      ),
    ).toBeInTheDocument();

    // Dismissing hides the notice.
    fireEvent.click(screen.getByRole("button", { name: "Dismiss version notice" }));
    expect(
      screen.queryByText(
        "Playing a different version than selected — the requested version isn't playable on this device.",
      ),
    ).not.toBeInTheDocument();
  });

  it("shows the version-swap notice when an explicit selection was substituted", () => {
    // The original release was explicitly chosen and then replaced (a dead
    // release, an undecodable one). The viewer must still be told.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({ requested_media_file_id: 7, effective_media_file_id: 8 }),
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, explicitFileSelection: true }));

    expect(
      screen.getByText(
        "Playing a different version than selected — the requested version isn't playable on this device.",
      ),
    ).toBeInTheDocument();
  });

  it("does not show the version-swap notice when the plan kept the requested file", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({ requested_media_file_id: 7, effective_media_file_id: 7 }),
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(
      screen.queryByText(
        "Playing a different version than selected — the requested version isn't playable on this device.",
      ),
    ).not.toBeInTheDocument();
  });
});

describe("WatchPage virtual version substitution notice", () => {
  const genericNotice =
    "Playing a different version than selected — the requested version isn't playable on this device.";
  const labeledNotice =
    "The selected version wasn't available, so Vio is playing 1080p H264 instead.";
  const virtualRow: PlayerFileVersion = {
    ...version,
    file_id: 100,
    container: "virtual",
    file_path: "virtual://movie/tt1?result=all",
  };
  const candidateRow: PlayerFileVersion = {
    ...version,
    file_id: 7,
    file_path: "/media/Movies/Example (2024)/Example.1080p.mkv",
  };

  it("names the effective version when the resolved virtual candidate is known", () => {
    const effectiveVirtualUri = candidateRow.file_path;
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 100,
        effectiveVirtualUri: effectiveVirtualUri ?? null,
        plan: fixturePlanV3({
          requested_media_file_id: 100,
          effective_media_file_id: 100,
          effective_virtual_uri: effectiveVirtualUri,
        }),
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualRow, candidateRow] }));

    expect(screen.getByText(labeledNotice)).toBeInTheDocument();
    expect(screen.queryByText(genericNotice)).not.toBeInTheDocument();
  });

  it("falls back to generic copy when the effective virtual candidate is unknown", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 100,
        effectiveVirtualUri: "/media/Movies/Example (2024)/Uncatalogued.mkv",
        plan: fixturePlanV3({
          requested_media_file_id: 100,
          effective_media_file_id: 100,
          effective_virtual_uri: "/media/Movies/Example (2024)/Uncatalogued.mkv",
        }),
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualRow, candidateRow] }));

    expect(screen.getByText(genericNotice)).toBeInTheDocument();
    expect(screen.queryByText(labeledNotice)).not.toBeInTheDocument();
  });

  it("stays quiet when the requested row is the effective virtual candidate", () => {
    const effectiveVirtualUri = candidateRow.file_path;
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: effectiveVirtualUri ?? null,
        plan: fixturePlanV3({
          requested_media_file_id: 7,
          effective_media_file_id: 7,
          effective_virtual_uri: effectiveVirtualUri,
        }),
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualRow, candidateRow] }));

    expect(screen.queryByText(labeledNotice)).not.toBeInTheDocument();
    expect(screen.queryByText(genericNotice)).not.toBeInTheDocument();
  });

  it("still surfaces the substitution for an explicit selection", () => {
    // An explicit pick can still be substituted (a dead release, a device that
    // cannot play it). The viewer chose that release, so honesty matters more:
    // the notice must still render.
    const effectiveVirtualUri = candidateRow.file_path;
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 100,
        effectiveVirtualUri: effectiveVirtualUri ?? null,
        plan: fixturePlanV3({
          requested_media_file_id: 100,
          effective_media_file_id: 100,
          effective_virtual_uri: effectiveVirtualUri,
        }),
      }),
    );

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [virtualRow, candidateRow],
        explicitFileSelection: true,
      }),
    );

    expect(screen.getByText(labeledNotice)).toBeInTheDocument();
  });

  it("names the server's substitution reason when it is published", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({
          requested_media_file_id: 7,
          effective_media_file_id: 8,
          substituted_from_file_id: 7,
          substitution_reason: "transport_failed",
        }),
      }),
    );

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [version, { ...version, file_id: 8 }],
      }),
    );

    expect(
      screen.getByText(
        "The selected version wouldn't start, so Vio is playing 1080p H264 instead.",
      ),
    ).toBeInTheDocument();
  });

  it.each([
    {
      reason: "version_not_playable",
      expected: "The chosen version could not be played, so Vio is playing 1080p H264 instead.",
    },
    {
      // A reason this build does not know must keep the generic copy rather
      // than render a cause the server never named.
      reason: "some_future_reason",
      expected: "The selected version wasn't available, so Vio is playing 1080p H264 instead.",
    },
  ])("renders the substitution notice for reason $reason", ({ reason, expected }) => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: fixturePlanV3({
          requested_media_file_id: 7,
          effective_media_file_id: 8,
          substituted_from_file_id: 7,
          substitution_reason: reason,
        }),
      }),
    );

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [version, { ...version, file_id: 8 }],
      }),
    );

    expect(screen.getByText(expected)).toBeInTheDocument();
  });
});

describe("WatchPage effective virtual version", () => {
  it("selects the path-matched candidate when the session's id is the VIRTUAL row", () => {
    const virtualRow: PlayerFileVersion = {
      ...version,
      file_id: 100,
      container: "virtual",
      file_path: "virtual://movie/tt1?result=all",
    };
    const candidateRow: PlayerFileVersion = {
      ...version,
      file_id: 7,
      file_path: "/media/Movies/Example (2024)/Example.1080p.mkv",
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 100,
        effectiveVirtualUri: candidateRow.file_path ?? null,
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualRow, candidateRow] }));

    const props = videoPlayerMock.mock.calls[0]?.[0] as { selectedVersion?: PlayerFileVersion };
    expect(props.selectedVersion?.file_id).toBe(7);
  });

  it("keeps file_id matching when the plan publishes no effective virtual URI", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ mediaFileId: 7, effectiveVirtualUri: null }),
    );

    render(createElement(WatchPage, watchPageProps));

    const props = videoPlayerMock.mock.calls[0]?.[0] as { selectedVersion?: PlayerFileVersion };
    expect(props.selectedVersion?.file_id).toBe(7);
  });
});

describe("mergeResolvedLiveVersion", () => {
  const candidate: PlayerFileVersion = {
    ...version,
    file_id: 8,
    container: "virtual",
    file_path: "/media/Movies/Example (2024)/Example.1080p.mkv",
  };

  it("re-keys a path-resolved candidate row under the live session file id", () => {
    const merged = mergeResolvedLiveVersion(
      [candidate],
      7,
      { mediaFileId: 7, effectiveVirtualUri: candidate.file_path ?? null },
      [candidate],
    );

    expect(merged.map((row) => row.file_id)).toEqual([8, 7]);
    expect(merged.find((row) => row.file_id === 7)).toMatchObject({
      file_path: candidate.file_path,
      container: "virtual",
    });
  });

  it("restores a live row dropped from the list using the version list prop", () => {
    const merged = mergeResolvedLiveVersion([], 7, { mediaFileId: 7, effectiveVirtualUri: null }, [
      version,
    ]);

    expect(merged).toEqual([version]);
  });

  it("leaves the list untouched when the live row is already present", () => {
    const list = [candidate];
    const merged = mergeResolvedLiveVersion(
      list,
      8,
      { mediaFileId: 8, effectiveVirtualUri: null },
      [candidate],
    );

    expect(merged).toBe(list);
  });

  it("leaves the list untouched when the live file cannot be resolved", () => {
    const list = [candidate];
    const merged = mergeResolvedLiveVersion(
      list,
      7,
      { mediaFileId: 7, effectiveVirtualUri: null },
      [candidate],
    );

    expect(merged).toBe(list);
  });

  it("replaces the live row when a same-file rotation changes its URI", () => {
    const sourceA = "virtual://movie/x?result=A";
    const sourceB = "virtual://movie/x?result=B";
    const staleLiveRow: PlayerFileVersion = {
      ...version,
      file_id: 7,
      file_path: sourceA,
      duration: 3600,
    };
    const committedRow: PlayerFileVersion = {
      ...candidate,
      file_id: 7,
      file_path: sourceB,
      container: "virtual",
      duration: 7200,
    };

    // The live file id is unchanged; only the committed source moved from A to
    // B, so the row still carries A's path and duration.
    const merged = mergeResolvedLiveVersion(
      [staleLiveRow],
      7,
      { mediaFileId: 7, effectiveVirtualUri: sourceB },
      [committedRow],
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({ file_id: 7, file_path: sourceB, duration: 7200 });
  });

  it("keeps the live row when the committed source is a different catalog row", () => {
    const liveRow: PlayerFileVersion = {
      ...version,
      file_id: 7,
      container: "virtual",
      file_path: "virtual://movie/tt1?result=all",
    };
    const committedRow: PlayerFileVersion = {
      ...candidate,
      file_id: 8,
      file_path: "/media/Movies/Example (2024)/Example.1080p.mkv",
    };
    const list = [liveRow, committedRow];

    // A virtual collapsed row keeps its identity; the concrete candidate is
    // already in the list and resolved by URI.
    const merged = mergeResolvedLiveVersion(
      list,
      7,
      { mediaFileId: 7, effectiveVirtualUri: committedRow.file_path ?? null },
      list,
    );

    expect(merged).toBe(list);
  });

  it("appends a URI-matched candidate omitted from the refreshed list", () => {
    const liveRow: PlayerFileVersion = {
      ...version,
      file_id: 7,
      container: "virtual",
      file_path: "virtual://movie/tt1?result=all",
    };
    const committedRow: PlayerFileVersion = {
      ...candidate,
      file_id: 8,
      file_path: "/media/Movies/Example (2024)/Example.1080p.mkv",
    };

    // The refreshed list collapsed the live row but no longer carries the
    // concrete candidate. The URI match lands on the candidate in `candidates`,
    // so the live row keeps its provenance while the candidate is appended
    // under its own id instead of being discarded.
    const merged = mergeResolvedLiveVersion(
      [liveRow],
      7,
      { mediaFileId: 7, effectiveVirtualUri: committedRow.file_path ?? null },
      [committedRow],
    );

    expect(merged.map((row) => row.file_id)).toEqual([7, 8]);
    expect(merged.find((row) => row.file_id === 7)).toBe(liveRow);
    expect(merged.find((row) => row.file_id === 8)).toMatchObject({
      file_path: committedRow.file_path,
    });
  });
});

describe("WatchPage live session version re-keying", () => {
  it("re-keys the resolved live row so duration and chapters use the playing file", async () => {
    const candidateRow: PlayerFileVersion = {
      ...version,
      file_id: 8,
      container: "virtual",
      file_path: "/media/Movies/Example (2024)/Example.1080p.mkv",
      duration: 7200,
      chapters: [
        { index: 0, title: "Live chapter", start_seconds: 0, end_seconds: 7200, source: "test" },
      ],
      audio_tracks: richerAudioTracks,
    };
    // The session plays file 7, which the version list does not carry; the plan
    // publishes the candidate's path, so the live row resolves to row 8.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: candidateRow.file_path ?? null,
        durationSeconds: null,
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [planSubtitle],
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [candidateRow] }));

    await act(async () => {
      await Promise.resolve();
    });

    const props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      versions?: PlayerFileVersion[];
      duration?: number;
      chapters?: Array<{ title?: string }>;
    };
    // The live file is keyed into the list, so the version-keyed fallbacks
    // resolve to the playing file instead of another release's first row.
    expect(props.versions?.find((row) => row.file_id === 7)?.duration).toBe(7200);
    expect(props.duration).toBe(7200);
    expect(props.chapters?.map((chapter) => chapter.title)).toEqual(["Live chapter"]);
  });
});

const planSubtitle = {
  index: 0,
  language: "en",
  codec: "srt",
  label: "English",
  source: "embedded" as const,
  url: "/api/v1/stream/session-1/subtitles/0.vtt",
};

const richerAudioTracks: PlayerAudioTrack[] = [
  { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
  { codec: "ac3", channels: 6, layout: "5.1", language: "spa", index: 9 },
];

const virtualVersion: PlayerFileVersion = { ...version, container: "virtual" };

describe("WatchPage live inventory refresh", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    fetchWatchDetailMock.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("polls only an incomplete virtual audio inventory and fills it in", async () => {
    const applyAudioInventory = vi.fn();
    const switchVersion = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
        switchVersion,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, audio_tracks: richerAudioTracks }],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    // Nothing is fetched before the first interval.
    expect(fetchWatchDetailMock).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });

    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
    expect(applyAudioInventory).toHaveBeenCalledWith(richerAudioTracks, 7);
    // Menu data only: no restart or stream swap.
    expect(switchVersion).not.toHaveBeenCalled();
    const playerCalls = videoPlayerMock.mock.calls;
    const playerProps = playerCalls[playerCalls.length - 1]?.[0] as { streamUrl?: string };
    expect(playerProps.streamUrl).toBe("/stream/session-1");
  });

  it("polls a provisional audio inventory even for a local file", async () => {
    const applyAudioInventory = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        audioInventoryProvisional: true,
        applyAudioInventory,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...version, audio_tracks: richerAudioTracks }],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [version] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    // The declared list is the reason to look, so a local (non-virtual) file
    // must still reach the probe-persisted tracks.
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
    expect(applyAudioInventory).toHaveBeenCalledWith(richerAudioTracks, 7);
  });

  it("requests a subtitle replan for a provisional inventory even when entries are selectable", async () => {
    const refreshSubtitles = vi.fn().mockResolvedValue(true);
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [planSubtitle],
        subtitleInventoryProvisional: true,
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...virtualVersion,
          subtitle_tracks: [{ index: 13, language: "en", codec: "ass", title: "English" }],
        },
      ],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    // A selectable-but-declared entry is not proof the probe landed; the menu
    // must keep looking until a verified plan clears the badge.
    expect(refreshSubtitles).toHaveBeenCalledTimes(1);
  });

  it("aborts an in-flight inventory read when the page unmounts", async () => {
    const applyAudioInventory = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );
    let capturedSignal: AbortSignal | undefined;
    fetchWatchDetailMock.mockImplementation(
      (_id: string, _fileId?: number, _libraryId?: number, options?: RequestInit) => {
        capturedSignal = options?.signal ?? undefined;
        // Never settles: the read is still in flight when the page unmounts.
        return new Promise(() => {});
      },
    );

    const { unmount } = render(
      createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
    expect(capturedSignal?.aborted).toBe(false);

    unmount();

    // Teardown aborts the read so it cannot land in the shared cache after the
    // player has moved on.
    expect(capturedSignal?.aborted).toBe(true);
  });

  it("reads the effective virtual candidate's inventory when the id names the collapsed row", async () => {
    const applyAudioInventory = vi.fn();
    const refreshSubtitles = vi.fn();
    // The session targets the collapsed VIRTUAL row (id 7); probes persist to
    // the resolved candidate row (id 8), which the plan identifies by path.
    const collapsedVirtualVersion: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/tt1?result=all",
    };
    const candidateVersion: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 8,
      file_path: "/media/Movies/Example (2024)/Example.1080p.mkv",
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: candidateVersion.file_path ?? null,
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [],
        applyAudioInventory,
        refreshSubtitles,
      }),
    );
    // The collapsed row carries no probe inventory; only the candidate does.
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        collapsedVirtualVersion,
        {
          ...candidateVersion,
          audio_tracks: richerAudioTracks,
          subtitle_tracks: [{ index: 13, language: "en", codec: "pgs", title: "English" }],
        },
      ],
    });

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [collapsedVirtualVersion, candidateVersion],
      }),
    );

    // The first attempt runs on the early cadence; one poll is enough to
    // observe the candidate row's inventory.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    // The candidate's probed inventory is adopted, and its path re-keys the
    // live identity so the version menu follows the same source.
    expect(applyAudioInventory).toHaveBeenCalledWith(
      richerAudioTracks,
      8,
      "/media/Movies/Example (2024)/Example.1080p.mkv",
    );
    expect(refreshSubtitles).toHaveBeenCalledTimes(1);
  });

  it("re-keys a same-file candidate whose URI moved even when the list is unchanged", async () => {
    const applyAudioInventory = vi.fn();
    const refreshSubtitles = vi.fn();
    // The session already plays candidate A of virtual row 7 and carries a
    // verified single-track list. The catalog read now resolves candidate B of
    // the same row with the same list length; only the path moved. A single
    // track keeps the poll's virtual-file completeness gate open.
    const singleTrack = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const versionA: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/x?result=A",
      audio_tracks: singleTrack,
    };
    const versionB: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/x?result=B",
      audio_tracks: singleTrack,
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: versionA.file_path ?? null,
        planAudioTracks: singleTrack,
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({ versions: [versionB] });

    render(createElement(WatchPage, { ...watchPageProps, versions: [versionA, versionB] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    // The verified list is the same length, but the candidate moved, so the
    // poll must still re-key the identity to the row that is now playing.
    expect(applyAudioInventory).toHaveBeenCalledWith(singleTrack, 7, versionB.file_path);
  });

  it("re-keys a moved same-file candidate even when its probed list is empty", async () => {
    const applyAudioInventory = vi.fn();
    const versionA: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/x?result=A",
    };
    const versionB: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/x?result=B",
      audio_tracks: [],
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: versionA.file_path ?? null,
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [planSubtitle],
        audioInventoryProvisional: true,
        applyAudioInventory,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({ versions: [versionB] });

    render(createElement(WatchPage, { ...watchPageProps, versions: [versionA, versionB] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    // An empty probed list is not evidence, but the identity still moves so the
    // version menu follows the source actually playing.
    expect(applyAudioInventory).toHaveBeenCalledWith([], 7, versionB.file_path);
  });

  it("falls back to the collapsed id when the plan publishes no effective virtual URI", async () => {
    const applyAudioInventory = vi.fn();
    const collapsedVersion: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 7,
      audio_tracks: richerAudioTracks,
    };
    const otherVersion: PlayerFileVersion = {
      ...virtualVersion,
      file_id: 8,
      file_path: "/media/Movies/Example (2024)/Example.2160p.mkv",
      audio_tracks: [],
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: null,
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({ versions: [collapsedVersion, otherVersion] });

    render(
      createElement(WatchPage, { ...watchPageProps, versions: [collapsedVersion, otherVersion] }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS);
    });

    expect(applyAudioInventory).toHaveBeenCalledWith(richerAudioTracks, 7);
  });

  it("does not poll a local file or a complete inventory", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
      }),
    );

    render(createElement(WatchPage, watchPageProps));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });

    expect(fetchWatchDetailMock).not.toHaveBeenCalled();
  });

  it("requests a subtitle replan when the catalog gains tracks the plan lacks", async () => {
    const refreshSubtitles = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [],
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...virtualVersion,
          subtitle_tracks: [{ index: 13, language: "en", codec: "pgs", title: "English" }],
        },
      ],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    // The first attempt runs on the early cadence, not the 20 s steady
    // interval, so a probe that landed within seconds is seen immediately.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    expect(refreshSubtitles).toHaveBeenCalledTimes(1);
  });

  it("keeps polling after a failed subtitle replan and stops once one succeeds", async () => {
    const refreshSubtitles = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [],
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...virtualVersion,
          subtitle_tracks: [{ index: 13, language: "en", codec: "pgs", title: "English" }],
        },
      ],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS * 4);
    });

    // The first replan failed, so the inventory is not complete and the poll
    // runs again; the second adopts a plan and completes the loop early.
    expect(refreshSubtitles).toHaveBeenCalledTimes(2);
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(2);
  });

  it("stops after the attempt cap when the inventory never fills in", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [],
      }),
    );
    // The catalog never grows past the plan's single track.
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...virtualVersion,
          audio_tracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        },
      ],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000 * 10);
    });

    // One attempt per interval, capped at five.
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(5);
  });

  it("retries transient fetch errors without spending the attempt budget", async () => {
    const applyAudioInventory = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );
    fetchWatchDetailMock
      .mockRejectedValueOnce(new Error("network"))
      .mockRejectedValueOnce(new Error("network"))
      .mockRejectedValueOnce(new Error("network"))
      .mockResolvedValue({
        versions: [{ ...virtualVersion, audio_tracks: richerAudioTracks }],
      });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS * 4);
    });

    // The three failed fetches do not count against the cap, so the fourth
    // (successful) request still runs and fills the inventory in.
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(4);
    expect(applyAudioInventory).toHaveBeenCalledWith(richerAudioTracks, 7);
  });

  it("stops polling at the elapsed deadline when every request fails", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [],
      }),
    );
    // Every request fails, so the completed-attempt cap never trips.
    fetchWatchDetailMock.mockRejectedValue(new Error("network"));

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_DEADLINE_MS * 2);
    });

    // One request per scheduled delay (2 s, 4 s, 8 s, then the steady 20 s
    // interval) until the five-minute deadline, then none. 18 attempts land by
    // the time the deadline check stops scheduling.
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(18);
  });

  it("discards a slow response that lands after the session switched files", async () => {
    const applyAudioInventory = vi.fn();
    let resolveFetch: (value: { versions: PlayerFileVersion[] }) => void = () => {};
    fetchWatchDetailMock.mockImplementation(
      () =>
        new Promise<{ versions: PlayerFileVersion[] }>((resolve) => {
          resolveFetch = resolve;
        }),
    );
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        sessionId: "session-1",
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );

    const { rerender } = render(
      createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);

    // The session switches to file 8 while file 7's request is in flight.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 8,
        sessionId: "session-2",
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );
    rerender(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    // File 7's response resolves after the switch.
    await act(async () => {
      resolveFetch({ versions: [{ ...virtualVersion, audio_tracks: richerAudioTracks }] });
      await Promise.resolve();
    });

    expect(applyAudioInventory).not.toHaveBeenCalled();
  });

  it("discards a slow response that lands after a same-file source rotation", async () => {
    const applyAudioInventory = vi.fn();
    let resolveFetch: (value: { versions: PlayerFileVersion[] }) => void = () => {};
    fetchWatchDetailMock.mockImplementation(
      () =>
        new Promise<{ versions: PlayerFileVersion[] }>((resolve) => {
          resolveFetch = resolve;
        }),
    );
    const sourceA = "virtual://movie/x?result=A";
    const sourceB = "virtual://movie/x?result=B";
    const versionA: PlayerFileVersion = { ...virtualVersion, file_id: 7, file_path: sourceA };
    const versionB: PlayerFileVersion = { ...virtualVersion, file_id: 8, file_path: sourceB };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        sessionId: "session-1",
        effectiveVirtualUri: sourceA,
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );

    const { rerender } = render(
      createElement(WatchPage, { ...watchPageProps, versions: [versionA, versionB] }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);

    // The transport rotates to sibling B while the collapsed file id and the
    // session id stay the same; only the effective virtual source moves.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        sessionId: "session-1",
        effectiveVirtualUri: sourceB,
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );
    rerender(createElement(WatchPage, { ...watchPageProps, versions: [versionA, versionB] }));

    // Source A's response resolves after the rotation. It must not be applied
    // as source B's inventory even though its row resolves.
    await act(async () => {
      resolveFetch({ versions: [{ ...versionB, audio_tracks: richerAudioTracks }] });
      await Promise.resolve();
    });

    expect(applyAudioInventory).not.toHaveBeenCalled();
  });

  it("re-keys the poll to the live session file after a version switch", async () => {
    const richerForNewFile = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng" },
      { codec: "ac3", channels: 2, layout: "stereo", language: "spa", index: 9 },
    ];
    const virtualVersion8: PlayerFileVersion = { ...virtualVersion, file_id: 8 };
    const applyAudioInventory = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion8, audio_tracks: richerForNewFile }],
    });

    const { rerender } = render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [virtualVersion, virtualVersion8],
      }),
    );

    // The session switches to file 8 while no poll has run yet.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 8,
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
      }),
    );
    rerender(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [virtualVersion, virtualVersion8],
      }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    // The request is keyed on the live file, not the mount-time one.
    const lastQuery = fetchQueryMock.mock.calls.at(-1)?.[0] as { queryKey?: unknown[] };
    expect(lastQuery?.queryKey).toEqual(itemKeys.watchDetail("content-1", 8, undefined));
    expect(applyAudioInventory).toHaveBeenCalledWith(richerForNewFile, 8);
  });

  it("polls the catalog on the first early attempt, not after 20 s", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ planAudioTracks: richerAudioTracks, subtitleUrls: [] }),
    );
    fetchWatchDetailMock.mockResolvedValue({ versions: [{ ...virtualVersion }] });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
  });

  it("uses a 2 s then 4 s cadence before settling into the steady interval", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ planAudioTracks: richerAudioTracks, subtitleUrls: [] }),
    );
    fetchWatchDetailMock.mockResolvedValue({ versions: [{ ...virtualVersion }] });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    // First attempt at 2 s.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);

    // The next attempt is 4 s later (at 6 s), not 20 s.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_999);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(2);
  });

  it("stops polling once the first attempt fills the inventory", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        subtitleUrls: [planSubtitle],
        applyAudioInventory: vi.fn(),
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, audio_tracks: richerAudioTracks }],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
  });

  it("forces a real fetch on the first attempt even when the cache is fresh", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ planAudioTracks: richerAudioTracks, subtitleUrls: [] }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClientOverride.current = client;
    // A payload cached at page mount (< staleTime old) without the probed
    // inventory; a cache-first read would return it and waste the attempt.
    client.setQueryData(itemKeys.watchDetail("content-1", undefined, undefined), {
      versions: [{ ...virtualVersion }],
    });
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...virtualVersion,
          subtitle_tracks: [{ index: 13, language: "en", codec: "pgs", title: "English" }],
        },
      ],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
  });

  it("still polls when the plan's only subtitle entry is not selectable", async () => {
    const refreshSubtitles = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: richerAudioTracks,
        // No URL and not burn-in only: the menu renders nothing from it, so the
        // poll must keep looking for the probe's real inventory.
        subtitleUrls: [{ ...planSubtitle, url: "" }],
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...virtualVersion,
          subtitle_tracks: [{ index: 13, language: "en", codec: "pgs", title: "English" }],
        },
      ],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
    expect(refreshSubtitles).toHaveBeenCalledTimes(1);
  });

  it("requests a subtitle replan when only the resolved candidate row carries the probed tracks", async () => {
    const refreshSubtitles = vi.fn();
    // A first-play plan has not learned the effective candidate, so the resolved
    // version is the collapsed virtual row with no probed tracks. The probe
    // persisted the embedded tracks to the candidate row instead; the seed must
    // fall back to it so the no-op replan can pull the inventory in.
    const collapsedVirtualVersion = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/tt1",
      subtitle_tracks: [],
    };
    const candidateVersion = {
      ...virtualVersion,
      file_id: 8,
      file_path: "virtual://movie/tt1?result=all",
      subtitle_tracks: [{ index: 13, language: "en", codec: "ass", title: "English" }],
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: null,
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [],
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [collapsedVirtualVersion, candidateVersion],
    });

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [collapsedVirtualVersion, candidateVersion],
      }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    // The candidate's probed tracks must trigger the no-op replan even though
    // the resolved-version snapshot stays empty.
    expect(refreshSubtitles).toHaveBeenCalledTimes(1);
  });

  it("seeds the subtitle replan from the live mediaFileId row's probed tracks", async () => {
    const refreshSubtitles = vi.fn();
    // The probed tracks landed on the live row (id 7); an unrelated release
    // row (id 8) must not be consulted to decide whether to replan.
    const liveVersion = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/tt1",
      subtitle_tracks: [{ index: 13, language: "en", codec: "ass", title: "English" }],
    };
    const otherReleaseVersion = {
      ...virtualVersion,
      file_id: 8,
      file_path: "virtual://movie/tt1?result=alternate",
      subtitle_tracks: [],
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: null,
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [],
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [liveVersion, otherReleaseVersion],
    });

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [liveVersion, otherReleaseVersion],
      }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    expect(refreshSubtitles).toHaveBeenCalledTimes(1);
  });

  it("exhausts the attempt budget without replanning when no row has probed tracks", async () => {
    const refreshSubtitles = vi.fn();
    const collapsed = {
      ...virtualVersion,
      file_id: 7,
      file_path: "virtual://movie/tt1",
      subtitle_tracks: [],
    };
    const candidate = {
      ...virtualVersion,
      file_id: 8,
      file_path: "virtual://movie/tt1?result=all",
      subtitle_tracks: [],
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        effectiveVirtualUri: null,
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [],
        refreshSubtitles,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({ versions: [collapsed, candidate] });

    render(createElement(WatchPage, { ...watchPageProps, versions: [collapsed, candidate] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS * 10);
    });

    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(5);
    expect(refreshSubtitles).not.toHaveBeenCalled();
  });

  it("does not poll when the plan already has a selectable subtitle entry", async () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ planAudioTracks: richerAudioTracks, subtitleUrls: [planSubtitle] }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });

    expect(fetchWatchDetailMock).not.toHaveBeenCalled();
  });

  it("marks the legacy catalog poll exhausted when its budget runs out", async () => {
    const markInventoryExhausted = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        planAudioTracks: richerAudioTracks,
        audioInventoryProvisional: true,
        subtitleUrls: [],
        subtitleInventoryProvisional: true,
        inventoryUrl: null,
        inventoryPending: false,
        markInventoryExhausted,
      }),
    );
    // The catalog never gains probe evidence, so the legacy poll spends its
    // whole attempt budget looking. No `inventory_url` means the deferred poll
    // never runs; this is the older-server path.
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, audio_tracks: [], subtitle_tracks: [] }],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    expect(markInventoryExhausted).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS * 10);
    });

    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(5);
    // The exhausted budget must read as a terminal failure rather than leaving
    // the menus on an indefinite "Discovering…".
    expect(markInventoryExhausted).toHaveBeenCalled();
  });

  it("does not mark the legacy poll failed once a verified list is present", async () => {
    const markInventoryExhausted = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [planSubtitle],
        inventoryUrl: null,
        inventoryPending: false,
        markInventoryExhausted,
      }),
    );

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });

    // A confident list never starts the legacy poll, so nothing is exhausted.
    expect(fetchWatchDetailMock).not.toHaveBeenCalled();
    expect(markInventoryExhausted).not.toHaveBeenCalled();
  });

  it("hands the discovery badge to the legacy fallback after the deferred poll exhausts", async () => {
    // A plan with `inventory_url` defers its enumeration, and the deferred poll
    // exhausts without a terminal answer. The legacy catalog poll then takes
    // over: the terminal "discovery did not complete" marker is handed back to
    // it while it is actually retrying, and its own exhausted budget re-marks
    // the failure.
    const events: string[] = [];
    // Stable identities: the poll effects key on these, so a fresh closure per
    // render would restart the attempt budget for the wrong reason.
    const stableSession = {
      applyAudioInventory: vi.fn(),
      applyInventoryUpdate: vi.fn(),
      refreshSubtitles: vi.fn(),
      pollInventory: vi.fn().mockResolvedValue(false),
    };
    queryClientOverride.current = { fetchQuery: fetchQueryMock };
    function Harness() {
      const [inventoryPending, setPending] = useState(true);
      const [inventoryFailed, setFailed] = useState(false);
      const markInventoryExhausted = useCallback(() => {
        events.push("exhausted");
        setPending(false);
        setFailed(true);
      }, []);
      const markInventoryRetrying = useCallback(() => {
        events.push("retrying");
        setFailed(false);
      }, []);
      playbackSessionMock.mockReturnValue(
        playbackSession({
          ...stableSession,
          mediaFileId: 7,
          sessionId: "session-deferred-exhaust",
          inventoryUrl: "/api/v2/playback/session-deferred-exhaust/inventory",
          inventoryPending,
          inventoryFailed,
          audioInventoryProvisional: true,
          planAudioTracks: richerAudioTracks,
          subtitleUrls: [],
          subtitleInventoryProvisional: true,
          markInventoryExhausted,
          markInventoryRetrying,
        }),
      );
      return createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] });
    }
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, audio_tracks: [], subtitle_tracks: [] }],
    });

    render(createElement(Harness));

    // Up to the end of the deferred budget (2 s + 4 s + 8 s + 20 s + 20 s) plus
    // the fallback's first attempt: the terminal marker is already cleared and
    // the badge is owned by the retry.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    const retryingProps = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      inventoryPending?: boolean;
      inventoryFailed?: boolean;
    };
    expect(events[0]).toBe("exhausted");
    expect(events).toContain("retrying");
    expect(retryingProps.inventoryPending).toBe(false);
    expect(retryingProps.inventoryFailed).toBe(false);

    // The fallback spends its own budget and marks the terminal failure.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    const exhaustedProps = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      inventoryFailed?: boolean;
    };
    expect(events.filter((event) => event === "exhausted").length).toBeGreaterThanOrEqual(2);
    expect(exhaustedProps.inventoryFailed).toBe(true);
  });

  it("arms legacy exhaustion with the live session identity across a replacement", async () => {
    const markFirst = vi.fn();
    const markSecond = vi.fn();
    const provisionalLegacy = {
      planAudioTracks: richerAudioTracks,
      audioInventoryProvisional: true,
      subtitleUrls: [],
      subtitleInventoryProvisional: true,
      inventoryUrl: null,
      inventoryPending: false,
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        sessionId: "session-1",
        markInventoryExhausted: markFirst,
        ...provisionalLegacy,
      }),
    );
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, audio_tracks: [], subtitle_tracks: [] }],
    });

    const { rerender } = render(
      createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }),
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);

    // The player replaces the session while the legacy timer is armed.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 8,
        sessionId: "session-2",
        markInventoryExhausted: markSecond,
        ...provisionalLegacy,
      }),
    );
    rerender(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS * 10);
    });

    // The replaced session is never marked, and the fresh session's exhaustion
    // names its live identity so the shared guard can drop a stale timer.
    expect(markFirst).not.toHaveBeenCalled();
    expect(markSecond).toHaveBeenCalled();
    expect(markSecond.mock.calls.at(-1)?.[0]).toEqual({
      sessionId: "session-2",
      mediaFileId: 8,
    });
  });

  it("drops a deferred exhaustion when only the file moved on the live session", async () => {
    const markFirst = vi.fn();
    const markSecond = vi.fn();
    // One stable session object: mutating it below mirrors the player adopting a
    // replacement into the live ref before the armed round's effect restarts.
    const stableSession = playbackSession({
      mediaFileId: 7,
      sessionId: "session-1",
      inventoryUrl: "/api/v2/playback/session-1/inventory",
      planAudioTracks: richerAudioTracks,
      audioInventoryProvisional: true,
      subtitleUrls: [],
      subtitleInventoryProvisional: true,
      inventoryPending: true,
      pollInventory: vi.fn().mockResolvedValue(false),
      markInventoryExhausted: markFirst,
    });
    playbackSessionMock.mockReturnValue(stableSession);

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    // Spend most of the deferred budget, leaving the round's last poll armed.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(53_000);
    });
    expect(markFirst).not.toHaveBeenCalled();

    // A serve-layer rotation moves only the effective file: the session and the
    // deferred `inventory_url` stay put, so only the round's own stored file
    // identity can recognize it as stale. The live ref now names file 8 while
    // the armed round still holds file 7's closure.
    stableSession.mediaFileId = 8;
    stableSession.markInventoryExhausted = markSecond;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(markFirst).not.toHaveBeenCalled();
    expect(markSecond).not.toHaveBeenCalled();
  });

  it("retains a still-declared badge when the poll's reads stay empty", async () => {
    const applyInventoryUpdate = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        planAudioTracks: richerAudioTracks,
        audioInventoryProvisional: true,
        subtitleUrls: [],
        subtitleInventoryProvisional: true,
        applyInventoryUpdate,
      }),
    );
    // The catalog never gains tracks: the resolved row stays empty on every
    // read. That is not proof the probe completed — an unprobed, slow, or
    // failed probe also serves empty tracks — so the client must not promote
    // the declared inventory to verified by itself.
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, audio_tracks: [], subtitle_tracks: [] }],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS * 10);
    });

    // The exhausted budget leaves the declared state and its badges untouched
    // rather than manufacturing a verified empty inventory server-side.
    expect(applyInventoryUpdate).not.toHaveBeenCalled();
  });

  it("does not clear a declared badge on a server payload that is not verified", async () => {
    const applyInventoryUpdate = vi.fn();
    playbackSessionMock.mockReturnValue(
      playbackSession({
        mediaFileId: 7,
        planAudioTracks: richerAudioTracks,
        audioInventoryProvisional: true,
        subtitleUrls: [],
        subtitleInventoryProvisional: true,
        applyInventoryUpdate,
      }),
    );
    // A declared (or still-probing) read is empty, but carries no verified
    // inventory_status. Only the server can certify the probe landed, so the
    // poll must not read this as the probe's answer.
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...virtualVersion,
          audio_tracks: [],
          subtitle_tracks: [],
          inventory_status: "declared",
          inventory_provenance: "declared",
        },
      ],
    });

    render(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(INVENTORY_REFRESH_INTERVAL_MS * 10);
    });

    expect(applyInventoryUpdate).not.toHaveBeenCalled();
  });

  it("restarts the poll when a declared push re-marks a verified inventory", async () => {
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, audio_tracks: richerAudioTracks }],
    });
    // Stable callbacks across renders: a fresh mock identity would restart the
    // effect for the wrong reason and hide whether the flag transition does.
    const applyAudioInventory = vi.fn();
    const applyInventoryUpdate = vi.fn();
    const refreshSubtitles = vi.fn();
    // Starts verified: nothing to poll for.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [planSubtitle],
        applyAudioInventory,
        applyInventoryUpdate,
        refreshSubtitles,
      }),
    );

    const view = render(
      createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(fetchWatchDetailMock).not.toHaveBeenCalled();

    // A declared push after the verified inventory re-marks the audio menu; the
    // poll has to come back and look again rather than staying retired.
    playbackSessionMock.mockReturnValue(
      playbackSession({
        planAudioTracks: richerAudioTracks,
        subtitleUrls: [planSubtitle],
        audioInventoryProvisional: true,
        applyAudioInventory,
        applyInventoryUpdate,
        refreshSubtitles,
      }),
    );
    view.rerender(createElement(WatchPage, { ...watchPageProps, versions: [virtualVersion] }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
  });
});

describe("WatchPage realtime reconnect reconcile", () => {
  it("re-applies the parent's liveness verdicts to the fresh detail rows", async () => {
    playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 7 }));
    // Stable client: the default mock hands back a fresh object per render, which
    // would re-fire the effect and mask the single reconcile read.
    queryClientOverride.current = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    // The reconnect read returns the server rows as-is: no liveness on them.
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, available: undefined }],
    });

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [{ ...virtualVersion, available: false }],
        versionLiveness: new Map([[7, { available: false }]]),
      }),
    );

    // The reconcile is for a reconnection. The first live socket is the one
    // the starting player opened, one read after the mounted detail query.
    const connProps = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRealtimeConnectionStateChange?: (state: "connected" | "connecting") => void;
    };
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connected");
    });
    expect(fetchWatchDetailMock).not.toHaveBeenCalled();

    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connecting");
    });
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connected");
    });

    await waitFor(() => expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(
        (videoPlayerMock.mock.calls.at(-1)?.[0] as { versions?: PlayerFileVersion[] })
          .versions?.[0],
      ).toMatchObject({ file_id: 7, available: false }),
    );
  });

  it("re-keys a serve-layer rotation the fresh list omitted", async () => {
    const mountedLiveRow: PlayerFileVersion = { ...virtualVersion, file_id: 7 };
    // The socket moved the effective source during the disconnect; the session
    // still reports the collapsed file id but a new virtual URI.
    playbackSessionMock.mockReturnValue(
      playbackSession({ mediaFileId: 7, effectiveVirtualUri: "virtual://movie/x?result=A" }),
    );
    queryClientOverride.current = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    // The reconcile read no longer carries the live row: only the rotated
    // candidate the menus have to follow.
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...virtualVersion, file_id: 9, file_path: "virtual://movie/x?result=A" }],
    });

    render(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [mountedLiveRow],
      }),
    );

    await waitFor(() => expect(fetchWatchDetailMock).toHaveBeenCalledTimes(0));

    const connProps = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRealtimeConnectionStateChange?: (state: "connected" | "connecting") => void;
    };
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connected");
    });
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connecting");
    });
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connected");
    });

    // The fresh list dropped the live row again; only the reconcile's re-key
    // puts it back so the menus keep resolving the committed source.
    await waitFor(() => {
      const props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
        versions?: PlayerFileVersion[];
        activeFileId?: number | null;
      };
      expect(props.activeFileId).toBe(7);
      expect(props.versions?.some((row) => row.file_id === 7)).toBe(true);
    });
  });

  it("keeps a reconcile that is in flight while the version list and liveness change", async () => {
    playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 7 }));
    queryClientOverride.current = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let resolveDetail!: (detail: { versions: PlayerFileVersion[] }) => void;
    fetchWatchDetailMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveDetail = resolve;
        }),
    );

    const firstVersion: PlayerFileVersion = { ...version, file_id: 7, resolution: "1080p" };
    const { rerender } = render(
      createElement(WatchPage, { ...watchPageProps, versions: [firstVersion] }),
    );

    const connProps = videoPlayerMock.mock.calls.at(-1)?.[0] as {
      onRealtimeConnectionStateChange?: (state: "connected" | "connecting") => void;
    };
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connected");
    });
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connecting");
    });
    act(() => {
      connProps.onRealtimeConnectionStateChange?.("connected");
    });
    await waitFor(() => expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1));

    // The parent's version list and liveness verdicts move while the reconcile
    // read is still in flight. The reconnect epoch is already claimed, so a
    // cancelled response would be lost for this reconnect for good.
    rerender(
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [{ ...firstVersion, resolution: "2160p" }],
        versionLiveness: new Map([[9, { available: false }]]),
      }),
    );

    await act(async () => {
      resolveDetail({
        versions: [{ ...version, file_id: 9, resolution: "720p" }],
      });
      await Promise.resolve();
    });

    // The fresh row lands, stamped with the liveness the parent holds now.
    await waitFor(() => {
      const props = videoPlayerMock.mock.calls.at(-1)?.[0] as {
        versions?: PlayerFileVersion[];
      };
      expect(props.versions?.find((row) => row.file_id === 9)).toMatchObject({
        available: false,
      });
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
  });
});

describe("WatchPage chapter refresh", () => {
  it("fetches past a fresh chapterless cache entry and only then spends the repair attempt", async () => {
    playbackSessionMock.mockReturnValue(playbackSession({ subtitleUrls: [planSubtitle] }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClientOverride.current = client;
    const chapterlessVersion: PlayerFileVersion = { ...version, chapters: [] };
    // The mounted query already holds a fresh payload without chapters; a
    // cache-first read would consume the single attempt without a request.
    client.setQueryData(itemKeys.watchDetail("content-1", undefined, undefined), {
      versions: [chapterlessVersion],
    });
    fetchWatchDetailMock.mockResolvedValue({
      versions: [
        {
          ...version,
          chapters: [
            { index: 0, title: "Chapter", start_seconds: 0, end_seconds: 3600, source: "test" },
          ],
        },
      ],
    });

    const { rerender } = render(
      createElement(WatchPage, { ...watchPageProps, versions: [chapterlessVersion] }),
    );

    await waitFor(() => expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1));

    // The attempt was spent on the real fetch: re-running the effect for the
    // same file must not issue a second request.
    rerender(
      createElement(WatchPage, { ...watchPageProps, versions: [{ ...chapterlessVersion }] }),
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(fetchWatchDetailMock).toHaveBeenCalledTimes(1);
  });
});

describe("Watch Party source fallback", () => {
  function refusedRoom(selfRole = "host") {
    const room = {
      room_id: "room-1",
      phase: "playing",
      selected_content_id: "content-1",
      selected_file_id: 7,
      selection_revision: 1,
      self_role: selfRole,
      members: [{ is_self: true, connected: true }],
      generation: 1,
    };
    playbackSessionMock.mockReturnValue(
      playbackSession({
        plan: null,
        streamUrl: null,
        sessionId: null,
        mediaFileId: null,
        errorReason: "no_alternate_version",
        errorTitle: "Playback unavailable",
        error: "A lower-resolution source is required because 4K transcoding is disabled.",
      }),
    );
    return room;
  }
  const props = {
    ...watchPageProps,
    fileId: 7,
    watchTogetherRoomId: "room-1",
    watchTogetherRoomToken: "proof",
  };

  it("ignores a changed selection from a late room read while this connection is replaced", async () => {
    const room = refusedRoom();
    playbackSessionMock.mockReturnValue(playbackSession());
    const fallbackSource = vi.fn();
    roomConnectionMock.mockReturnValue({ room, connectionState: "connected", fallbackSource });
    const view = render(createElement(WatchPage, props));
    await waitFor(() => expect(videoPlayerMock).toHaveBeenCalled());
    roomConnectionMock.mockReturnValue({
      room: {
        ...room,
        selected_content_id: "another-title",
        selected_file_id: 8,
        selection_revision: 2,
      },
      connectionState: "disconnected",
      replacementReason: "This profile joined the Watch Party on another device.",
      fallbackSource,
    });
    view.rerender(createElement(WatchPage, props));
    expect(startPlaybackMock).not.toHaveBeenCalled();
    expect(fallbackSource).not.toHaveBeenCalled();
  });

  it.each(["host", "guest"])(
    "automatically requests one shared fallback for a %s",
    async (role) => {
      const room = refusedRoom(role);
      let finish!: () => void;
      const fallbackSource = vi.fn(
        () =>
          new Promise<void>((resolve) => {
            finish = resolve;
          }),
      );
      roomConnectionMock.mockReturnValue({ room, connectionState: "connected", fallbackSource });
      const view = render(createElement(WatchPage, props));
      await waitFor(() =>
        expect(fallbackSource).toHaveBeenCalledWith({
          selectionRevision: 1,
          failedFileId: 7,
          reason: "no_alternate_version",
        }),
      );
      expect(screen.getByText("Finding a compatible version for everyone...")).toBeTruthy();
      view.rerender(createElement(WatchPage, props));
      expect(fallbackSource).toHaveBeenCalledTimes(1);
      roomConnectionMock.mockReturnValue({
        room: { ...room, selected_file_id: 8, selection_revision: 2, generation: 2 },
        connectionState: "connected",
        fallbackSource,
      });
      view.rerender(createElement(WatchPage, props));
      await waitFor(() =>
        expect(startPlaybackMock).toHaveBeenCalledWith(
          expect.objectContaining({ fileId: 8, roomId: "room-1", restart: true }),
          "automatic",
        ),
      );
      finish();
      view.unmount();
    },
  );

  it("waits for confirmed membership before reporting the refusal", async () => {
    const room = refusedRoom();
    const fallbackSource = vi.fn().mockResolvedValue(null);
    roomConnectionMock.mockReturnValue({
      room: { ...room, members: [] },
      connectionState: "connected",
      fallbackSource,
    });
    const view = render(createElement(WatchPage, props));
    expect(fallbackSource).not.toHaveBeenCalled();
    roomConnectionMock.mockReturnValue({ room, connectionState: "connected", fallbackSource });
    view.rerender(createElement(WatchPage, props));
    await waitFor(() => expect(fallbackSource).toHaveBeenCalledTimes(1));
  });

  it("does not apply an old file's refusal to a newer room selection", async () => {
    const room = refusedRoom();
    const fallbackSource = vi.fn();
    roomConnectionMock.mockReturnValue({
      room: { ...room, selected_file_id: 8, selection_revision: 2 },
      connectionState: "connected",
      fallbackSource,
    });
    render(createElement(WatchPage, props));
    await waitFor(() => expect(startPlaybackMock).toHaveBeenCalled());
    expect(fallbackSource).not.toHaveBeenCalled();
    expect(playbackCapabilitiesMock).toHaveBeenCalledTimes(1);
  });

  it("retries the same refusal once after the room proof is renewed", async () => {
    const room = refusedRoom();
    const fallbackSource = vi.fn().mockRejectedValue(new Error("Expired room proof"));
    roomConnectionMock.mockReturnValue({ room, connectionState: "connected", fallbackSource });
    const view = render(createElement(WatchPage, props));
    await waitFor(() => expect(fallbackSource).toHaveBeenCalledTimes(1));
    view.rerender(createElement(WatchPage, { ...props, watchTogetherRoomToken: "renewed-proof" }));
    await waitFor(() => expect(fallbackSource).toHaveBeenCalledTimes(2));
    view.rerender(createElement(WatchPage, { ...props, watchTogetherRoomToken: "renewed-proof" }));
    expect(fallbackSource).toHaveBeenCalledTimes(2);
  });

  it("retains the refusal without looping when no shared fallback exists", async () => {
    const room = refusedRoom();
    const fallbackSource = vi.fn().mockRejectedValue(new Error("No alternative room source"));
    roomConnectionMock.mockReturnValue({ room, connectionState: "connected", fallbackSource });
    const view = render(createElement(WatchPage, props));
    await waitFor(() => {
      expect(
        screen.getByText(
          "A lower-resolution source is required because 4K transcoding is disabled.",
        ),
      ).toBeTruthy();
      expect(fallbackSource).toHaveBeenCalledTimes(1);
    });
    view.rerender(createElement(WatchPage, props));
    expect(fallbackSource).toHaveBeenCalledTimes(1);
  });
});

it("bounds sheet error refreshes and lets a changed file refresh independently", () => {
  playbackSessionMock.mockReturnValue(playbackSession());
  const view = render(createElement(WatchPage, watchPageProps));
  const refresh = () => videoPlayerMock.mock.calls.at(-1)?.[0].onTrickplayError();
  refresh();
  for (let attempt = 0; attempt < 20; attempt++) refresh();
  expect(trickplayRefetchMock).toHaveBeenCalledTimes(1);
  playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 8 }));
  view.rerender(createElement(WatchPage, watchPageProps));
  refresh();
  expect(trickplayRefetchMock).toHaveBeenCalledTimes(2);
  const clock = vi.spyOn(Date, "now").mockReturnValue(Date.now() + 60_001);
  refresh();
  expect(trickplayRefetchMock).toHaveBeenCalledTimes(3);
  clock.mockRestore();
});

it("merges marker images only into the active session's matching occurrence", () => {
  playbackSessionMock.mockReturnValue(playbackSession());
  const props = {
    ...watchPageProps,
    versions: [
      {
        ...version,
        marker_segments: [{ kind: "intro" as const, start_seconds: 6, end_seconds: 9 }],
      },
    ],
  };
  const view = render(createElement(WatchPage, props));
  const event = {
    type: "event",
    session_id: "session-1",
    name: "marker_thumbnail_ready",
    payload: {
      session_id: "session-1",
      file_id: 7,
      kind: "intro",
      start_seconds: 6,
      end_seconds: 9,
      thumbnail_url: "/yellow.webp",
      thumbnail_capture_seconds: 6,
    },
  };
  const receive = (message: unknown) =>
    act(() => videoPlayerMock.mock.calls.at(-1)?.[0].onRealtimeEvent(message));
  receive({ ...event, payload: { ...event.payload, end_seconds: 10 } });
  expect(videoPlayerMock.mock.calls.at(-1)?.[0].markerSegments[0].thumbnail_url).toBeUndefined();
  playbackSessionMock.mockReturnValue(playbackSession({ sessionId: "session-2" }));
  view.rerender(createElement(WatchPage, props));
  receive(event);
  expect(videoPlayerMock.mock.calls.at(-1)?.[0].markerSegments[0].thumbnail_url).toBeUndefined();
  receive({
    ...event,
    session_id: "session-2",
    payload: { ...event.payload, session_id: "session-2" },
  });
  expect(videoPlayerMock.mock.calls.at(-1)?.[0].markerSegments[0].thumbnail_url).toBe(
    "/yellow.webp",
  );
});
