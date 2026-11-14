import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { fixturePlanV3, fixtureSubtitleInventoryItemV3 } from "../protocol-v3.fixtures";
import { resetSessionMutations } from "../session-mutations";
import { resetCodecDetectionForTests } from "../hooks/useCodecDetection";
import type { PlayerFileVersion, WatchPageProps } from "../types";
import { WatchPage } from "./WatchPage";

// The other WatchPage tests substitute the hook to pin its callers. This module
// keeps `usePlaybackSession` real so the page's two poll effects and the hook's
// identity refs and queued updates run against each other, and drives the
// deferred -> legacy -> exhausted -> late-verified sequence through them.
const videoPlayerCalls = vi.hoisted(
  () =>
    [] as Array<{
      inventoryPending?: boolean;
      inventoryFailed?: boolean;
      audioInventoryProvisional?: boolean;
      subtitleInventoryProvisional?: boolean;
      onInventoryUpdated?: (payload: unknown) => void;
    }>,
);
const startPlaybackMock = vi.hoisted(() => vi.fn());
const playbackCapabilitiesMock = vi.hoisted(() => vi.fn());
const replanMock = vi.hoisted(() => vi.fn());
const fetchWatchDetailMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());
const trickplayRefetchMock = vi.hoisted(() => vi.fn());

vi.mock("../start-v2", () => ({
  playbackCapabilitiesV2: playbackCapabilitiesMock,
  startPlaybackV2: startPlaybackMock,
  resetPlaybackCapabilitiesV2: vi.fn(),
}));
vi.mock("../lifecycle-v2", () => ({ replanV2: replanMock }));
vi.mock("./VideoPlayer", () => ({
  VideoPlayer: (props: Record<string, unknown>) => {
    videoPlayerCalls.push(props as never);
    return "Mounted video player";
  },
}));
vi.mock("@/hooks/queries/items", () => ({ fetchWatchDetail: fetchWatchDetailMock }));
vi.mock("@/hooks/queries/trickplay", () => ({
  transientTrickplayError: () => false,
  useWatchTrickplay: () => ({
    data: null,
    dataUpdatedAt: 0,
    isError: false,
    error: null,
    refetch: trickplayRefetchMock,
  }),
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ awaitAdminJob: vi.fn() }),
}));
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));
vi.mock("../hooks/useWatchTogetherRoomConnection", () => ({
  useWatchTogetherRoomConnection: () => ({ room: null }),
}));
vi.mock("@/api/v2/mediaCandidates", () => ({
  awaitVirtualCandidatesRefresh: vi.fn(),
  cancelVirtualCandidatesRefresh: vi.fn(),
}));
vi.mock("sonner", () => ({ toast: { error: toastErrorMock } }));
vi.mock("../context/PlayerConfigContext", () => ({
  usePlayerConfig: () => playerConfig,
}));

const playerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "test-device",
};

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
  // `playbackVariants` is deliberately omitted so these tests exercise the
  // component's default (and the app's `?? []`) path: the array identity must
  // stay stable across renders or the legacy poll restarts before its budget
  // can spend.
  subtitles: [],
  intro: null,
  credits: null,
  onExit: vi.fn(),
};

const lastPlayer = () => videoPlayerCalls.at(-1);

function deferredPlan() {
  return fixturePlanV3({
    session_id: "session-hook-int",
    tracks_pending: true,
    inventory_url: "/api/v2/playback/session-hook-int/inventory",
    inventory_status: "declared",
    inventory_provenance: "pending",
    effective_media_file_id: 7,
    audio_tracks: [{ codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true }],
  });
}

describe("WatchPage with the real playback session hook", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    videoPlayerCalls.length = 0;
    resetSessionMutations();
    resetCodecDetectionForTests();
    startPlaybackMock.mockReset();
    playbackCapabilitiesMock.mockReset().mockResolvedValue({
      state: "available",
      allowed: true,
      installation_id: "install-1",
      protocol_versions: [3],
      features: [],
    });
    replanMock.mockReset();
    fetchWatchDetailMock.mockReset();
    toastErrorMock.mockReset();
    trickplayRefetchMock.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("hands deferred exhaustion to the legacy retry and recovers on a late verified push", async () => {
    // The deferred `inventory_url` poll always sees "probe not landed" (304), so
    // its spent budget is the terminal deferred failure. The legacy catalog poll
    // then owns the retry and its own spent budget re-marks the failure. A late
    // verified push is the recovery.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/inventory")) return new Response(null, { status: 304 });
        if (url.endsWith("/route-events")) return new Response(null, { status: 202 });
        if (url.includes("/playback/")) return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );
    startPlaybackMock.mockResolvedValue({
      protocol_version: 3,
      server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
      outcome: "playable",
      session_id: "session-hook-int",
      playback_plan: deferredPlan(),
    });
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...version, audio_tracks: [], subtitle_tracks: [] }],
    });

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(WatchPage, { ...watchPageProps, versions: [version] }),
      ),
    );

    // Let the capability probe settle and the deferred plan adopt.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(lastPlayer()?.inventoryPending).toBe(true);
    expect(lastPlayer()?.inventoryFailed).toBe(false);

    // The deferred round spends its whole budget without a terminal answer.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    // The deferred exhaustion is a terminal failure, then the legacy fallback
    // takes the badge over while it is actually retrying.
    expect(
      videoPlayerCalls.some(
        (props) => props.inventoryPending === false && props.inventoryFailed === true,
      ),
    ).toBe(true);
    expect(lastPlayer()?.inventoryPending).toBe(false);
    expect(lastPlayer()?.inventoryFailed).toBe(false);

    // The legacy catalog poll spends its own budget and re-marks the failure.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(lastPlayer()?.inventoryPending).toBe(false);
    expect(lastPlayer()?.inventoryFailed).toBe(true);

    // The plan already declares an audio row, so the overlay shows it even after
    // the probe exhausts: it is unverified, not provisional. The subtitle menu
    // has no declared rows, so it stays provisional on the bounded path.
    const exhausted = lastPlayer();
    expect(exhausted?.audioInventoryProvisional).toBe(false);
    expect(exhausted?.subtitleInventoryProvisional).toBe(true);
    await act(async () => {
      exhausted?.onInventoryUpdated?.({
        session_id: "session-hook-int",
        inventory_status: "verified",
        inventory_revision: "rev-verified-late",
        effective_media_file_id: 7,
        audio_tracks: [
          { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
        ],
        subtitle_inventory: [fixtureSubtitleInventoryItemV3({ combined_index: 0 })],
      });
    });
    const recovered = lastPlayer();
    expect(recovered?.inventoryPending).toBe(false);
    expect(recovered?.inventoryFailed).toBe(false);
    expect(recovered?.audioInventoryProvisional).toBe(false);
    expect(recovered?.subtitleInventoryProvisional).toBe(false);
  });

  it("exhausts the legacy poll budget across unrelated re-renders in the default-prop path", async () => {
    // Regression for the default `playbackVariants`: a fresh `[]` per render
    // reaches the hook's `selectFileId` -> `loadSession` -> `replan` ->
    // `refreshSubtitles`, which is a legacy-poll dependency. That restarts the
    // effect, resets `completedAttempts`, and re-runs `markInventoryRetrying()`,
    // so the budget never spends and the failure badge never survives. The app
    // hits the same path through `item.playback_variants ?? []`, and both now
    // use a module-level empty array.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/route-events")) return new Response(null, { status: 202 });
        if (url.includes("/playback/")) return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );
    startPlaybackMock.mockResolvedValue({
      protocol_version: 3,
      server_features: ["playback_plan_v3"],
      outcome: "playable",
      session_id: "session-legacy-rerender",
      // A legacy-only plan: no `inventory_url`, no `tracks_pending`, so the
      // catalog poll (not the deferred poll) owns the discovery budget. Its
      // declared audio row is shown immediately as unverified, not provisional.
      playback_plan: fixturePlanV3({
        session_id: "session-legacy-rerender",
        inventory_status: "declared",
        inventory_provenance: "pending",
        effective_media_file_id: 7,
        audio_tracks: [
          { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
        ],
      }),
    });
    fetchWatchDetailMock.mockResolvedValue({
      versions: [{ ...version, audio_tracks: [], subtitle_tracks: [] }],
    });

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const tree = (title: string) =>
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(WatchPage, { ...watchPageProps, title }),
      );

    const { rerender } = render(tree("Test movie"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(lastPlayer()?.inventoryPending).toBe(false);
    expect(lastPlayer()?.inventoryFailed).toBe(false);
    // The plan's declared audio row is shown immediately, so the menu is not
    // provisional while the catalog poll spends its budget.
    expect(lastPlayer()?.audioInventoryProvisional).toBe(false);

    // Ordinary unrelated re-renders across the polling window. Only the title
    // changes; the inventory props keep their identity.
    for (let i = 0; i < 40; i += 1) {
      rerender(tree(`Test movie ${i}`));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
    }

    // The budget is spent and the terminal failure is up.
    expect(lastPlayer()?.inventoryPending).toBe(false);
    expect(lastPlayer()?.inventoryFailed).toBe(true);

    // It survives further unrelated re-renders and the rest of the deadline.
    for (let i = 0; i < 5; i += 1) {
      rerender(tree(`Test movie after ${i}`));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
    }
    await act(async () => {
      await vi.advanceTimersByTimeAsync(7 * 60_000);
    });
    expect(lastPlayer()?.inventoryPending).toBe(false);
    expect(lastPlayer()?.inventoryFailed).toBe(true);
  });
});
