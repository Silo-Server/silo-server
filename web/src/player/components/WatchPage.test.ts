import { render, screen } from "@testing-library/react";
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

vi.mock("../hooks/usePlaybackSession", () => ({
  usePlaybackSession: playbackSessionMock,
}));
vi.mock("./VideoPlayer", () => ({
  VideoPlayer: (props: unknown) => {
    videoPlayerMock(props);
    return "Mounted video player";
  },
}));
vi.mock("../context/PlayerConfigContext", () => ({
  usePlayerConfig: () => ({
    apiBaseUrl: "/api/v1",
    getAccessToken: () => "token",
    getProfileId: () => "profile-1",
    getDeviceId: () => "test-device",
  }),
}));
vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ fetchQuery: vi.fn() }),
}));
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));
vi.mock("../hooks/useWatchTogetherRoomConnection", () => ({
  useWatchTogetherRoomConnection: () => ({ room: null }),
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
    reanchorSeek: vi.fn(),
    refreshSubtitles: vi.fn(),
    applySubtitleTrack: vi.fn(),
    updatePlaybackState: vi.fn(),
    reportEvent: vi.fn(),
    ...overrides,
  };
}

beforeEach(() => {
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
  it("keeps the player mounted when a replan fails with an active plan", () => {
    playbackSessionMock.mockReturnValue(
      playbackSession({ errorTitle: "Quality change failed", error: "Temporary server error" }),
    );

    render(createElement(WatchPage, watchPageProps));

    expect(screen.getByText("Mounted video player")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Go Back" })).not.toBeInTheDocument();
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
