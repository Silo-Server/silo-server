import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { createElement, useEffect, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import { fixturePlanV3, fixtureSubtitleInventoryItemV3 } from "../protocol-v3.fixtures";
import type { PlanV3 } from "../protocol-v3";
import type { SubtitleMode } from "../types";
import { resetCodecDetectionForTests } from "../hooks/useCodecDetection";
import { usePlaybackSession, type UsePlaybackSessionResult } from "../hooks/usePlaybackSession";
import { resetSessionMutations } from "../session-mutations";
import { VideoPlayer } from "./VideoPlayer";

// The player and the session hook together, wired the way WatchPage wires
// them, so the sequences reviewers reported run through both: transport
// events in the player, requests and recovery in the hook.

const controls = vi.hoisted(() => ({
  current: null as null | { activeSubtitleIndex: number | null },
}));
const hlsJS = vi.hoisted(() => ({
  latest: null as null | { emit: (event: string, data: unknown) => void },
}));

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), message: vi.fn() } }));
vi.mock("../hooks/usePlaybackRealtime", () => ({
  usePlaybackRealtime: () => ({ connectionState: "connected" }),
}));
vi.mock("../hooks/useWatchProgress", () => ({
  useWatchProgress: () => vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../hooks/useKeyboardShortcuts", () => ({ useKeyboardShortcuts: vi.fn() }));
vi.mock("../hooks/useSubtitleTracks", () => ({ useSubtitleTracks: () => [] }));
vi.mock("../hooks/useASSSubtitles", () => ({ useASSSubtitles: () => ({ isActive: false }) }));
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
vi.mock("./PlayerControls", () => ({
  SKIP_BACK_SECONDS: 10,
  SKIP_FORWARD_SECONDS: 30,
  PlayerControls: (props: { activeSubtitleIndex: number | null }) => {
    controls.current = props;
    return null;
  },
}));
vi.mock("hls.js", () => ({
  default: class MockHls {
    static Events = {
      ERROR: "error",
      MANIFEST_PARSED: "manifestParsed",
      BUFFER_APPENDED: "bufferAppended",
    };
    static ErrorTypes = { NETWORK_ERROR: "networkError", MEDIA_ERROR: "mediaError" };
    static isSupported = () => true;

    handlers = new Map<string, (event: string, data: unknown) => void>();

    constructor() {
      hlsJS.latest = this;
    }

    on(event: string, handler: (event: string, data: unknown) => void) {
      this.handlers.set(event, handler);
    }
    emit(event: string, data: unknown) {
      this.handlers.get(event)?.(event, data);
    }
    startLoad() {}
    stopLoad() {}
    loadSource() {}
    attachMedia() {}
    destroy() {}
  },
}));
// Start and replan go to the fetch stub below, as in the session hook tests.
vi.mock("../start-v2", () => ({
  startPlaybackV2: async (config: PlayerConfig, body: unknown) => {
    const { playerFetch } = await import("../player-fetch");
    const { registerSessionMutations } = await import("../session-mutations");
    const decision = await playerFetch<import("../protocol-v3").DecisionResponseV3>(
      { ...config, apiBaseUrl: "/api/v2" },
      "/playback/start",
      { method: "POST", body: JSON.stringify(body) },
    );
    const sessionId = decision.playback_plan?.session_id ?? decision.session_id;
    if (decision.playback_plan && sessionId) registerSessionMutations(sessionId, "installation");
    return decision;
  },
}));
vi.mock("../lifecycle-v2", () => ({
  replanV2: async (config: PlayerConfig, sessionId: string, body: unknown) => {
    const { playerFetch } = await import("../player-fetch");
    return playerFetch({ ...config, apiBaseUrl: "/api/v2" }, `/playback/${sessionId}/replan`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  },
}));

const playerConfig: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "test-device",
  getProfileToken: () => null,
};

type Body = Record<string, unknown>;

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function playable(sessionId: string, plan: PlanV3) {
  return {
    protocol_version: 3,
    server_features: ["playback_plan_v3"],
    outcome: "playable",
    session_id: sessionId,
    playback_plan: plan,
  };
}

function directPlan(overrides: Partial<PlanV3> = {}): PlanV3 {
  const sessionId = overrides.session_id ?? "session-1";
  return fixturePlanV3({
    delivery: "original_http",
    stream: {
      url: `/stream/${sessionId}`,
      protocol: "http_progressive",
      headers: {},
      header_refresh: "none",
    },
    ...overrides,
  });
}

interface Requests {
  starts: Body[];
  replans: Array<{ url: string; body: Body }>;
}

/**
 * Routes start and replan requests to the test. A handler that throws stands
 * in for an unreachable server.
 */
function stubServer(handlers: {
  start: (body: Body, count: number) => Response | Promise<Response>;
  replan: (url: string, body: Body, count: number) => Response | Promise<Response>;
}): Requests {
  const requests: Requests = { starts: [], replans: [] };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Body;
        requests.starts.push(body);
        return handlers.start(body, requests.starts.length);
      }
      if (url.endsWith("/replan")) {
        const body = JSON.parse(String(init?.body)) as Body;
        requests.replans.push({ url, body });
        return handlers.replan(url, body, requests.replans.length);
      }
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      if (url.endsWith("/route-events")) return new Response(null, { status: 202 });
      return new Response(null, { status: 404 });
    }),
  );
  return requests;
}

const sessionRef: { current: UsePlaybackSessionResult | null } = { current: null };

function Harness({
  subtitleMode = "auto",
  preferredSubtitleLanguage = null,
}: {
  subtitleMode?: SubtitleMode;
  preferredSubtitleLanguage?: string | null;
}) {
  const playback = usePlaybackSession("request-1", [], [], 7, 0, false, "auto");
  // Published after commit, so the test reads the session the player rendered.
  useEffect(() => {
    sessionRef.current = playback;
  });
  if (!playback.plan || !playback.streamUrl || !playback.sessionId) return null;
  return createElement(VideoPlayer, {
    title: "Test episode",
    streamUrl: playback.streamUrl,
    plan: playback.plan,
    planRevision: playback.planRevision,
    shouldAutoPlay: playback.shouldAutoPlay,
    replanning: playback.replanning,
    replanError: playback.error,
    replanErrorTitle: playback.errorTitle,
    sessionId: playback.sessionId,
    activeFileId: playback.mediaFileId,
    subtitleUrls: playback.subtitleUrls,
    initialPosition: playback.initialPosition,
    qualityPreference: playback.qualityPreference,
    onQualitySelect: playback.changeQuality,
    onSubtitleTrackChange: playback.changeSubtitleTrack,
    onPlanFailure: playback.recoverFromFailure,
    onConnectionLost: playback.recoverConnection,
    onRetryConnection: playback.retryConnection,
    connectionStatus: playback.connectionStatus,
    connectionErrorTitle: playback.connectionErrorTitle,
    connectionError: playback.connectionError,
    onReanchorSeek: playback.reanchorSeek,
    onPlaybackStateChange: (state) =>
      playback.updatePlaybackState(state.currentTime, state.playing),
    onFirstFrame: playback.reportFirstFrame,
    // As WatchPage: a subtitle the start had to drop turns subtitles off.
    subtitleMode: playback.initialSubtitleError ? "off" : subtitleMode,
    preferredSubtitleLanguage,
    intro: null,
    credits: null,
    onExit: vi.fn(),
    seekIntervals: { back: 10, forward: 30 },
  });
}

function renderHarness(props: Parameters<typeof Harness>[0] = {}) {
  return render(createElement(Harness, props), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(PlayerConfigProvider, { config: playerConfig, children }),
  });
}

/** Fires the `timeupdate` of a source that has data for its position. */
function showFrame(video: HTMLVideoElement) {
  Object.defineProperty(video, "readyState", { configurable: true, value: 2 });
  fireEvent.timeUpdate(video);
  Reflect.deleteProperty(video, "readyState");
}

/** The viewer is watching: the element plays and shows a frame. */
async function startWatching(): Promise<HTMLVideoElement> {
  let video: HTMLVideoElement | null = null;
  await waitFor(() => {
    video = document.querySelector("video");
    expect(video?.getAttribute("src")).toBeTruthy();
  });
  const element = video as unknown as HTMLVideoElement;
  Object.defineProperty(element, "paused", { configurable: true, value: false });
  fireEvent.play(element);
  showFrame(element);
  await waitFor(() => expect(sessionRef.current?.connectionStatus).toBe("connected"));
  return element;
}

beforeEach(() => {
  sessionRef.current = null;
  controls.current = null;
  hlsJS.latest = null;
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  resetCodecDetectionForTests();
  resetSessionMutations();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("VideoPlayer with the playback session: reconnect", () => {
  it("keeps playing after a reconnect that follows a failed HLS startup", async () => {
    // HLS goes through hls.js: the browser reports no native support.
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    const hlsPlan = fixturePlanV3({
      plan_id: "plan:hls-quality",
      plan_attempt_key: "v3:hls-quality",
      stream: {
        url: "/playback/transcode/session-1/master.m3u8",
        protocol: "hls",
        headers: {},
        header_refresh: "none",
      },
    });
    let rejectRecovery: ((error: Error) => void) | undefined;
    const requests = stubServer({
      start: () => jsonResponse(playable("session-1", directPlan()), 201),
      replan: (_url, body) => {
        if (body.operation === "quality_change") {
          return jsonResponse(playable("session-1", hlsPlan));
        }
        if (body.operation === "failure_recovery") {
          // The server goes away while the recovery is in flight.
          return new Promise<Response>((_resolve, reject) => {
            rejectRecovery = reject;
          });
        }
        // The reconnect finds it back.
        return jsonResponse(playable("session-1", directPlan({ plan_id: "plan:reconnected" })));
      },
    });

    renderHarness();
    const video = await startWatching();

    // A playing direct stream switches to an HLS rung.
    act(() => sessionRef.current?.changeQuality("720p", 100));
    await waitFor(() => expect(hlsJS.latest).not.toBeNull());

    // The new transport never starts: hls.js gives up twice on the network,
    // and the startup guard reports the route as failed.
    const fatal = { fatal: true, type: "networkError", details: "manifestLoadError" };
    const now = vi.spyOn(Date, "now").mockReturnValue(10_000);
    act(() => hlsJS.latest?.emit("error", fatal));
    now.mockReturnValue(20_000);
    act(() => hlsJS.latest?.emit("error", fatal));
    now.mockRestore();
    await waitFor(() => expect(rejectRecovery).toBeDefined());

    // Tearing the failed transport down pauses the element. That pause is
    // reported, but it is not the viewer's.
    Object.defineProperty(video, "paused", { configurable: true, value: true });
    fireEvent.pause(video);
    await act(async () => Promise.resolve());

    await act(async () => {
      rejectRecovery?.(new TypeError("Failed to fetch"));
      await Promise.resolve();
    });
    await waitFor(() => expect(sessionRef.current?.connectionStatus).toBe("reconnecting"));

    await waitFor(() => expect(sessionRef.current?.plan?.plan_id).toBe("plan:reconnected"), {
      timeout: 4_000,
    });
    expect(sessionRef.current?.connectionStatus).toBe("connected");
    expect(sessionRef.current?.shouldAutoPlay).toBe(true);
    expect(requests.replans.map(({ body }) => body.operation)).toEqual([
      "quality_change",
      "failure_recovery",
      "track_change",
    ]);
  });

  it("does not re-request a burned-in subtitle the restarted session was refused", async () => {
    const burnIn = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:3",
      combined_index: 3,
      codec: "hdmv_pgs_subtitle",
      delivery: "burn_in_only",
      url: undefined,
    });
    const requests = stubServer({
      start: (body, count) => {
        if (count === 1) {
          return jsonResponse(
            playable(
              "session-1",
              directPlan({
                selected_tracks: {
                  audio: { id: "file:7:audio:0", index: 0 },
                  subtitle: { id: "file:7:subtitle:3", index: 3 },
                },
                subtitle: { mode: "burn_in", inventory: [burnIn] },
              }),
            ),
            201,
          );
        }
        if (body.subtitle_track_index === 3) {
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "terminal",
              terminal: {
                reason: "subtitle_burn_in_source_unsupported",
                message: "The selected subtitle can't be burned into this source.",
                retryable: false,
              },
            },
            201,
          );
        }
        return jsonResponse(
          playable(
            "session-2",
            directPlan({
              session_id: "session-2",
              plan_id: "plan:session-2",
              subtitle: { mode: "off", inventory: [burnIn] },
            }),
          ),
          201,
        );
      },
      replan: (url) =>
        // After the restart the burned-in track is refused on session-2 too;
        // before it, the server has lost session-1.
        url.includes("/session-2/")
          ? jsonResponse({
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "terminal",
              terminal: {
                reason: "subtitle_burn_in_source_unsupported",
                message: "The selected subtitle can't be burned into this source.",
                retryable: false,
              },
            })
          : jsonResponse({ error: "playback_session_not_found" }, 404),
    });

    renderHarness({ subtitleMode: "always", preferredSubtitleLanguage: "eng" });
    const video = await startWatching();
    await waitFor(() => expect(controls.current?.activeSubtitleIndex).toBe(3));
    expect(requests.replans).toHaveLength(0);

    // The server restarts mid-stream.
    Object.defineProperty(video, "error", {
      configurable: true,
      value: { code: 2, message: "PIPELINE_ERROR_NETWORK" },
    });
    fireEvent.error(video);
    await waitFor(() => expect(sessionRef.current?.sessionId).toBe("session-2"), {
      timeout: 4_000,
    });
    // Let every effect of the handover settle.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });

    expect(requests.starts.map((body) => body.subtitle_track_index)).toEqual([
      undefined,
      3,
      undefined,
    ]);
    expect(requests.replans.filter(({ url }) => url.includes("/session-2/"))).toEqual([]);
    expect(controls.current?.activeSubtitleIndex).toBeNull();
    expect(sessionRef.current?.connectionStatus).toBe("connected");
    expect(sessionRef.current?.error).toBeNull();
    expect(sessionRef.current?.initialSubtitleErrorTitle).toBe("That subtitle track can't be used");
  });
});
