import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fixturePlanV3 } from "../protocol-v3.fixtures";
import { derivePersistedSubtitleMode } from "../utils/subtitleMode";
import type { UsePlaybackSessionResult } from "../hooks/usePlaybackSession";
import type { PlayerFileVersion, WatchPageProps } from "../types";
import { WatchPage } from "./WatchPage";

const playbackSessionMock = vi.hoisted(() => vi.fn());
const videoPlayerMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());
const roomConnectionMock = vi.hoisted(() => vi.fn());
const playbackCapabilitiesMock = vi.hoisted(() => vi.fn());
const startPlaybackMock = vi.hoisted(() => vi.fn());
vi.mock("../start-v2", () => ({ playbackCapabilitiesV2: playbackCapabilitiesMock }));

vi.mock("../hooks/usePlaybackSession", () => ({
  usePlaybackSession: playbackSessionMock,
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
vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ fetchQuery: vi.fn() }),
}));
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
    streamUrl: "/stream/session-1",
    sessionId: "session-1",
    playbackAttemptId: "attempt-1",
    mediaFileId: 7,
    initialPosition: 0,
    audioTrackIndex: 0,
    durationSeconds: 3600,
    subtitleUrls: [],
    qualityPreference: "original",
    shouldAutoPlay: true,
    loading: false,
    replacing: false,
    replanning: false,
    errorTitle: null,
    error: null,
    initialSubtitleErrorTitle: null,
    initialSubtitleError: null,
    switchVersion: vi.fn(),
    switchAudioTrack: vi.fn(),
    changeSubtitleTrack: vi.fn(),
    changeQuality: vi.fn(),
    recoverFromFailure: vi.fn(),
    invalidatePlan: vi.fn().mockResolvedValue(true),
    reanchorSeek: vi.fn().mockResolvedValue(true),
    refreshSubtitles: vi.fn(),
    applySubtitleTrack: vi.fn(),
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
  toastErrorMock.mockReset();
});

describe("derivePersistedSubtitleMode", () => {
  it("persists an enabled mode when a subtitle track is selected", () => {
    expect(derivePersistedSubtitleMode(3)).toBe("always");
  });

  it("persists off when subtitles are disabled", () => {
    expect(derivePersistedSubtitleMode(null)).toBe("off");
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

describe("Watch Party selection changes", () => {
  it("follows another member's selection as an automatic start", async () => {
    // Nobody here pressed Play, so the controller must not time this start.
    const room = {
      room_id: "room-1",
      phase: "playing",
      selected_content_id: "content-1",
      selected_file_id: 7,
      selection_revision: 1,
      self_role: "guest",
      members: [{ is_self: true, connected: true }],
      generation: 1,
    };
    const props = {
      ...watchPageProps,
      fileId: 7,
      watchTogetherRoomId: "room-1",
      watchTogetherRoomToken: "proof",
    };
    playbackSessionMock.mockReturnValue(playbackSession());
    roomConnectionMock.mockReturnValue({ room, connectionState: "connected" });
    const view = render(createElement(WatchPage, props));
    await waitFor(() => expect(videoPlayerMock).toHaveBeenCalled());
    expect(startPlaybackMock).not.toHaveBeenCalled();

    roomConnectionMock.mockReturnValue({
      room: {
        ...room,
        selected_content_id: "content-2",
        selected_file_id: 9,
        selection_revision: 2,
      },
      connectionState: "connected",
    });
    view.rerender(createElement(WatchPage, props));

    await waitFor(() => expect(startPlaybackMock).toHaveBeenCalledOnce());
    expect(startPlaybackMock).toHaveBeenCalledWith(
      {
        contentId: "content-2",
        fileId: 9,
        libraryId: undefined,
        roomId: "room-1",
        roomToken: "proof",
        restart: true,
      },
      "automatic",
    );
    view.unmount();
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
    await waitFor(() =>
      expect(
        screen.getByText(
          "A lower-resolution source is required because 4K transcoding is disabled.",
        ),
      ).toBeTruthy(),
    );
    expect(fallbackSource).toHaveBeenCalledTimes(1);
    view.rerender(createElement(WatchPage, props));
    expect(fallbackSource).toHaveBeenCalledTimes(1);
  });
});
