import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { itemKeys } from "@/hooks/queries/keys";
import { expect, it, vi } from "vitest";

import { fixturePlanV3 } from "../protocol-v3.fixtures";
import type { UsePlaybackSessionResult } from "../hooks/usePlaybackSession";
import type { PlayerFileVersion, WatchPageProps } from "../types";
import { WatchPage } from "./WatchPage";
import { SeekBar } from "./SeekBar";
import { trickplayFromV2 } from "@/api/v2/trickplay";
import manifest from "../../../../contracts/api/v2/fixtures/get_watch_trickplay_ok.json";

const playbackSessionMock = vi.hoisted(() => vi.fn());
const videoPlayerMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());
const roomConnectionMock = vi.hoisted(() => vi.fn());
const playbackCapabilitiesMock = vi.hoisted(() => vi.fn());
const startPlaybackMock = vi.hoisted(() => vi.fn());
const trickplayOverride = vi.hoisted(() => vi.fn());
const showSeekBar = vi.hoisted(() => ({ value: false }));
vi.mock("@/hooks/queries/trickplay", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/hooks/queries/trickplay")>();
  return {
    ...original,
    useWatchTrickplay: (...args: Parameters<typeof original.useWatchTrickplay>) =>
      trickplayOverride(...args) ?? original.useWatchTrickplay(...args),
  };
});
vi.mock("../start-v2", () => ({ playbackCapabilitiesV2: playbackCapabilitiesMock }));

vi.mock("../hooks/usePlaybackSession", () => ({
  usePlaybackSession: playbackSessionMock,
}));
vi.mock("./VideoPlayer", () => ({
  VideoPlayer: (props: Parameters<typeof SeekBar>[0]) => {
    videoPlayerMock(props);
    return showSeekBar.value
      ? createElement(SeekBar, {
          ...props,
          currentTime: 0,
          duration: 3600,
          buffered: null,
          onSeek: () => {},
        })
      : "Mounted video player";
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
    connectionStatus: "connected",
    connectionErrorTitle: null,
    connectionError: null,
    switchVersion: vi.fn(),
    switchAudioTrack: vi.fn(),
    changeSubtitleTrack: vi.fn(),
    changeQuality: vi.fn(),
    recoverFromFailure: vi.fn(),
    recoverConnection: vi.fn(),
    retryConnection: vi.fn(),
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

it.each([
  { status: 404, serveCached: false },
  { status: 503, serveCached: true },
])(
  "keeps cached previews=$serveCached after a manifest refetch returns $status",
  async ({ status, serveCached }) => {
    const raw = {
      ...manifest,
      expires_at: new Date(Date.now() + 3600_000).toISOString(),
    };
    const request = vi.fn(
      async () =>
        new Response(JSON.stringify(raw), { headers: { "Content-Type": "application/json" } }),
    );
    vi.stubGlobal("fetch", request);
    roomConnectionMock.mockReturnValue({ room: null });
    playbackSessionMock.mockReturnValue(playbackSession());
    const client = new QueryClient();
    const key = itemKeys.watchTrickplay(watchPageProps.contentId, version.file_id);
    const view = render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(WatchPage, {
          ...watchPageProps,
          versions: [{ ...version, trickplay_available: true }],
        }),
      ),
    );
    try {
      await waitFor(() =>
        expect(videoPlayerMock.mock.calls.at(-1)?.[0].trickplay).toEqual(trickplayFromV2(raw)),
      );
      const cached = client.getQueryData(key);
      request.mockImplementation(
        async () =>
          new Response(
            JSON.stringify({
              type: `https://siloserver.org/docs/api/v2/problems/${status === 404 ? "not_found" : "dependency_unavailable"}`,
              title: status === 404 ? "Not found" : "Service unavailable",
              status,
              detail: "Seek previews are unavailable.",
              instance: "/api/v2/watch/content-1/trickplay",
            }),
            { status, headers: { "Content-Type": "application/problem+json" } },
          ),
      );
      await act(async () => {
        await client.refetchQueries({ queryKey: key });
      });
      expect(client.getQueryState(key)?.status).toBe("error");
      expect(client.getQueryData(key)).toBe(cached);
      await waitFor(() =>
        expect(videoPlayerMock.mock.calls.at(-1)?.[0].trickplay).toBe(serveCached ? cached : null),
      );
    } finally {
      view.unmount();
      client.clear();
      vi.unstubAllGlobals();
    }
  },
);

it("defers a throttled sheet failure and cancels recovery when the file changes", async () => {
  vi.useFakeTimers();
  vi.setSystemTime(1000);
  const refetch = vi.fn().mockResolvedValue({});
  trickplayOverride.mockReturnValue({ data: null, refetch });
  roomConnectionMock.mockReturnValue({ room: null });
  playbackSessionMock.mockReturnValue(playbackSession());
  const client = new QueryClient();
  const page = () =>
    createElement(
      QueryClientProvider,
      { client },
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [{ ...version, trickplay_available: true }],
      }),
    );
  const view = render(page());
  try {
    act(() => videoPlayerMock.mock.calls.at(-1)?.[0].onTrickplayError());
    expect(refetch).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(1000));
    act(() => {
      videoPlayerMock.mock.calls.at(-1)?.[0].onTrickplayError();
      videoPlayerMock.mock.calls.at(-1)?.[0].onTrickplayError();
    });
    await act(() => vi.advanceTimersByTimeAsync(59_000));
    expect(refetch).toHaveBeenCalledTimes(2);
    act(() => videoPlayerMock.mock.calls.at(-1)?.[0].onTrickplayError());
    playbackSessionMock.mockReturnValue(playbackSession({ mediaFileId: 8 }));
    view.rerender(page());
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(refetch).toHaveBeenCalledTimes(2);
  } finally {
    view.unmount();
    client.clear();
    trickplayOverride.mockReset();
    vi.useRealTimers();
  }
});

it("reloads a failed sheet after an identical manifest refetch without moving the pointer", async () => {
  let fails = true;
  const requests: string[] = [];
  vi.stubGlobal(
    "Image",
    class {
      onload: (() => void) | null = null;
      onerror: (() => void) | null = null;
      set src(url: string) {
        requests.push(url);
        queueMicrotask(() => (fails ? this.onerror : this.onload)?.());
      }
    },
  );
  const raw = {
    ...manifest,
    expires_at: new Date(Date.now() + 3600_000).toISOString(),
    sheets: [manifest.sheets[0]!],
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(raw), { headers: { "Content-Type": "application/json" } }),
    ),
  );
  roomConnectionMock.mockReturnValue({ room: null });
  playbackSessionMock.mockReturnValue(playbackSession());
  showSeekBar.value = true;
  const client = new QueryClient();
  const key = itemKeys.watchTrickplay(watchPageProps.contentId, version.file_id);
  client.setQueryData(key, trickplayFromV2(raw), { updatedAt: Date.now() - 1000 });
  const cached = client.getQueryData(key);
  const view = render(
    createElement(
      QueryClientProvider,
      { client },
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [{ ...version, trickplay_available: true }],
      }),
    ),
  );
  try {
    const slider = screen.getByRole("slider");
    slider.getBoundingClientRect = () =>
      ({ left: 0, width: 1000, top: 0, height: 10, right: 1000, bottom: 10 }) as DOMRect;
    fireEvent.mouseMove(slider, { clientX: 100 });
    await waitFor(() => expect(requests.length).toBeGreaterThan(0));
    await waitFor(() => expect(videoPlayerMock.mock.calls.at(-1)?.[0].trickplay).toBeTruthy());
    // The query shares the same manifest object after successful refresh.
    await waitFor(() => expect(client.getQueryState(key)?.fetchStatus).toBe("idle"));
    expect(client.getQueryData(key)).toBe(cached);
    expect(screen.queryByTestId("seek-preview-image")).toBeNull();
    fails = false;
    // This is the scheduled recovery path: refetch the same wire manifest.
    await act(async () => {
      await client.refetchQueries({ queryKey: key });
    });
    expect(client.getQueryData(key)).toBe(cached);
    expect(await screen.findByTestId("seek-preview-image")).toBeTruthy();
  } finally {
    view.unmount();
    client.clear();
    showSeekBar.value = false;
    vi.unstubAllGlobals();
  }
});

it("keeps sheet recovery inside the manifest Retry-After delay", async () => {
  vi.useFakeTimers();
  vi.setSystemTime(1000);
  vi.stubGlobal(
    "Image",
    class {
      onload: (() => void) | null = null;
      onerror: (() => void) | null = null;
      set src(_url: string) {
        queueMicrotask(() => this.onerror?.());
      }
    },
  );
  const raw = {
    ...manifest,
    expires_at: new Date(Date.now() + 3600_000).toISOString(),
  };
  const request = vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          type: "https://siloserver.org/problems/rate_limited",
          title: "Rate limited",
          status: 429,
          detail: "Try later",
          instance: "/api/v2/watch/content-1/trickplay",
        }),
        {
          status: 429,
          headers: { "Content-Type": "application/problem+json", "Retry-After": "90" },
        },
      ),
  );
  vi.stubGlobal("fetch", request);
  roomConnectionMock.mockReturnValue({ room: null });
  playbackSessionMock.mockReturnValue(playbackSession());
  showSeekBar.value = true;
  const client = new QueryClient();
  const key = itemKeys.watchTrickplay(watchPageProps.contentId, version.file_id);
  client.setQueryData(key, trickplayFromV2(raw));
  const view = render(
    createElement(
      QueryClientProvider,
      { client },
      createElement(WatchPage, {
        ...watchPageProps,
        versions: [{ ...version, trickplay_available: true }],
      }),
    ),
  );
  try {
    const slider = screen.getByRole("slider");
    slider.getBoundingClientRect = () =>
      ({ left: 0, width: 1000, top: 0, height: 10, right: 1000, bottom: 10 }) as DOMRect;
    fireEvent.mouseMove(slider, { clientX: 100 });
    await act(() => vi.advanceTimersByTimeAsync(20));
    expect(request).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(request).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(request).toHaveBeenCalledTimes(2);
    // More failed sheets can leave a recovery timer pending when retries end.
    await act(() => vi.advanceTimersByTimeAsync(50_000));
    fireEvent.mouseMove(slider, { clientX: 900 });
    await act(() => vi.advanceTimersByTimeAsync(40_000));
    expect(request).toHaveBeenCalledTimes(3);
    expect(client.getQueryState(key)?.status).toBe("error");
    // Another failed sheet after the retries must leave recovery to polling.
    fireEvent.mouseMove(slider, { clientX: 500 });
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(request).toHaveBeenCalledTimes(3);
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(request).toHaveBeenCalledTimes(4);
  } finally {
    view.unmount();
    client.clear();
    showSeekBar.value = false;
    vi.unstubAllGlobals();
    vi.useRealTimers();
  }
});
