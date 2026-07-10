import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useTranscodeQuality } from "./useTranscodeQuality";
import { PlayerConfigProvider } from "../context/PlayerConfigContext";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import type { PlayerFileVersion, TranscodeStartRequest } from "../types";

const config: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => null,
  getProfileId: () => null,
};

const version: PlayerFileVersion = {
  file_id: 42,
  resolution: "1080p",
  codec_video: "h264",
  codec_audio: "aac",
  hdr: false,
  container: "mkv",
  file_size: 1_000_000,
  duration: 7200,
  bitrate: 8000,
};

function wrapper({ children }: { children: ReactNode }) {
  return <PlayerConfigProvider config={config}>{children}</PlayerConfigProvider>;
}

function transcodeStartResponse() {
  return {
    ok: true,
    status: 200,
    json: async () => ({
      session_id: "sess-1",
      status: "started",
      manifest_url: "/playback/transcode/sess-1/master.m3u8",
      duration_seconds: 7200,
      player_start_seconds: 0,
      timeline_offset_seconds: 0,
      can_seek_anywhere: true,
    }),
  };
}

const fetchMock = vi.fn();

function sentBodies(): TranscodeStartRequest[] {
  return fetchMock.mock.calls.map(([, init]) => JSON.parse((init as RequestInit).body as string));
}

beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockImplementation(() => Promise.resolve(transcodeStartResponse()));
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderQuality() {
  return renderHook(
    () =>
      useTranscodeQuality({
        sessionId: "sess-1",
        selectedVersion: version,
        versions: [version],
        playMethod: "remux",
        initialPosition: 0,
      }),
    { wrapper },
  );
}

describe("useTranscodeQuality", () => {
  it("coalesces same-tick restarts into a single start with the final params", async () => {
    const { result } = renderQuality();

    // Mount auto-start has fired but its dispatch is macrotask-deferred. A
    // persisted bitmap subtitle selection lands in the same tick (subtitle
    // auto-selection on session start) — the two must collapse into ONE
    // server call carrying the burn-in, instead of spawning an ffmpeg that
    // is killed milliseconds later by the second start.
    act(() => {
      result.current.setSubtitleBurnIn(3, 0, 42);
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    // Give a (wrongly) surviving first dispatch a chance to fire.
    await new Promise((r) => setTimeout(r, 20));

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const body = sentBodies()[0]!;
    expect(body.subtitle_burn_in).toBe(true);
    expect(body.subtitle_track_index).toBe(3);
    expect(body.subtitle_media_file_id).toBe(42);
    // Burn-in composites into the frames, so codec copy must be off.
    expect(body.target_codec_video).toBe("h264");
  });

  it("preserves a pending quality when burn-in is selected in the same tick", async () => {
    const { result } = renderQuality();

    act(() => {
      result.current.switchQuality("720p", 30);
      result.current.setSubtitleBurnIn(3, 30, 42);
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 20));

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const body = sentBodies()[0]!;
    expect(body.target_resolution).toBe("720p");
    expect(body.subtitle_burn_in).toBe(true);
    expect(body.subtitle_track_index).toBe(3);
    expect(body.subtitle_media_file_id).toBe(42);
  });

  it("still dispatches later restarts separately", async () => {
    const { result } = renderQuality();

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(sentBodies()[0]!.subtitle_burn_in).toBe(false);
    expect(sentBodies()[0]!.subtitle_media_file_id).toBeUndefined();

    // A user toggle in a later tick is a genuine restart, not coalesced away.
    act(() => {
      result.current.setSubtitleBurnIn(3, 120, 42);
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const second = sentBodies()[1]!;
    expect(second.subtitle_burn_in).toBe(true);
    expect(second.subtitle_track_index).toBe(3);
    expect(second.subtitle_media_file_id).toBe(42);
    expect(second.seek_seconds).toBe(120);
  });

  it("drops a deferred dispatch when the hook unmounts first", async () => {
    const { unmount } = renderQuality();

    // Auto-start is deferred; unmounting (exit → session DELETE) must cancel
    // it so no stray transcode/start resurrects the dead session.
    unmount();

    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rolls back a failed burn-in selection so the same track can be retried", async () => {
    const { result } = renderQuality();

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    fetchMock.mockRejectedValueOnce(new Error("transcode failed"));

    act(() => {
      result.current.setSubtitleBurnIn(3, 120, 42);
    });

    await waitFor(() => expect(result.current.error).toMatch(/^Couldn't switch to Original/));
    expect(result.current.burnInSubtitleIndex).toBeNull();

    fetchMock.mockResolvedValueOnce(transcodeStartResponse());
    act(() => {
      result.current.setSubtitleBurnIn(3, 120, 42);
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(sentBodies()[2]!.subtitle_track_index).toBe(3);
    expect(sentBodies()[2]!.subtitle_burn_in).toBe(true);
  });
});
