import { render, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { itemKeys } from "@/hooks/queries/keys";
import { expect, it, vi } from "vitest";

import { fixturePlanV3 } from "../protocol-v3.fixtures";
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

it("suppresses a cached seek-preview manifest when the active file becomes unavailable", async () => {
  roomConnectionMock.mockReturnValue({ room: null });
  playbackSessionMock.mockReturnValue(playbackSession());
  const client = new QueryClient();
  const key = itemKeys.watchTrickplay(watchPageProps.contentId, version.file_id);
  const cached = {
    intervalMs: 10000,
    width: 300,
    height: 168,
    columns: 10,
    rows: 10,
    count: 100,
    sheets: ["https://example.com/signed-sheet.jpg"],
    expiresAt: Date.now() + 3600000,
  };
  client.setQueryData(key, cached);
  const page = (available: boolean) =>
    createElement(
      QueryClientProvider,
      { client },
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [{ ...version, trickplay_available: available }],
      }),
    );
  const view = render(page(true));
  expect(videoPlayerMock.mock.calls.at(-1)?.[0].trickplay).toEqual(cached);
  view.rerender(page(false));
  await waitFor(() => expect(videoPlayerMock.mock.calls.at(-1)?.[0].trickplay).toBeNull());
  expect(client.getQueryData(key)).toEqual(cached);
  view.rerender(page(true));
  await waitFor(() => expect(videoPlayerMock.mock.calls.at(-1)?.[0].trickplay).toEqual(cached));
  view.unmount();
  client.clear();
});
