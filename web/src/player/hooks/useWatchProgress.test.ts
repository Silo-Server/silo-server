import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError } from "../player-fetch";

const mocks = vi.hoisted(() => ({ send: vi.fn() }));

vi.mock("../session-mutations", () => ({
  sendSessionProgress: (...args: unknown[]) => mocks.send(...args),
  observeSessionProgress: () => () => {},
  captureSessionProgress: () => {},
}));

import { useWatchProgress } from "./useWatchProgress";

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

const endedProblem = JSON.stringify({
  type: "https://siloserver.org/docs/api/v2/problems/playback_session_ended",
  title: "Playback session ended",
  status: 410,
  detail: "An administrator ended this playback session",
});

async function reportOnce(error: Error) {
  mocks.send.mockReset();
  mocks.send.mockRejectedValue(error);
  const onSessionEnded = vi.fn();
  const video = document.createElement("video");
  Object.defineProperty(video, "currentTime", { value: 30 });
  Object.defineProperty(video, "readyState", { value: HTMLMediaElement.HAVE_METADATA });
  const videoRef = { current: video };
  vi.useFakeTimers();
  const rendered = renderHook(
    () => useWatchProgress("session-1", videoRef, undefined, onSessionEnded),
    { wrapper },
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10_000);
  });
  expect(mocks.send).toHaveBeenCalled();
  mocks.send.mockResolvedValue(undefined);
  rendered.unmount();
  return onSessionEnded;
}

describe("useWatchProgress", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("reports when the server ended the session for good", async () => {
    const onSessionEnded = await reportOnce(
      new PlayerFetchError(410, "Playback session ended", undefined, endedProblem),
    );
    expect(onSessionEnded).toHaveBeenCalledTimes(1);
  });

  // An element without metadata still reads currentTime 0. Reporting that
  // when the viewer leaves during startup would replace their resume point.
  it("reports nothing before the element has metadata", async () => {
    mocks.send.mockReset();
    mocks.send.mockResolvedValue(undefined);
    const video = document.createElement("video");
    const videoRef = { current: video };
    vi.useFakeTimers();
    const rendered = renderHook(() => useWatchProgress("session-1", videoRef), { wrapper });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    rendered.unmount();
    expect(mocks.send).not.toHaveBeenCalled();
  });

  it("keeps playing through other progress failures", async () => {
    for (const error of [
      new PlayerFetchError(404, "Playback session not found", "session_not_found"),
      new PlayerFetchError(503, "Unavailable"),
      new TypeError("Failed to fetch"),
    ]) {
      const onSessionEnded = await reportOnce(error);
      expect(onSessionEnded).not.toHaveBeenCalled();
      vi.useRealTimers();
    }
  });

  it("ignores an ended answer that arrives after the session changed", async () => {
    let rejectReport: (error: Error) => void = () => {};
    mocks.send.mockReset();
    mocks.send.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          rejectReport = reject;
        }),
    );
    mocks.send.mockResolvedValue(undefined);
    const onSessionEnded = vi.fn();
    const video = document.createElement("video");
    Object.defineProperty(video, "currentTime", { value: 30 });
    Object.defineProperty(video, "readyState", { value: HTMLMediaElement.HAVE_METADATA });
    const videoRef = { current: video };
    vi.useFakeTimers();
    const rendered = renderHook(
      ({ sessionId }) => useWatchProgress(sessionId, videoRef, undefined, onSessionEnded),
      { wrapper, initialProps: { sessionId: "session-1" } },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    rendered.rerender({ sessionId: "session-2" });
    await act(async () => {
      rejectReport(new PlayerFetchError(410, "Playback session ended", undefined, endedProblem));
      await Promise.resolve();
    });
    expect(onSessionEnded).not.toHaveBeenCalled();
    rendered.unmount();
  });
});
