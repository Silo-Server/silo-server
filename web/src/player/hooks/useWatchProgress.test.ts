import { act, cleanup, renderHook } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import { PlayerFetchError } from "../player-fetch";
import { useWatchProgress } from "./useWatchProgress";

const mocks = vi.hoisted(() => ({ send: vi.fn() }));
vi.mock("../session-mutations", () => ({
  sendSessionProgress: mocks.send,
  observeSessionProgress: () => () => {},
  captureSessionProgress: () => {},
}));

const config: PlayerConfig = {
  apiBaseUrl: "https://silo.example.test/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile",
  getDeviceHeaders: () => ({}),
};
const wrapper = ({ children }: { children: React.ReactNode }) =>
  createElement(PlayerConfigProvider, { config, children });

// A video element with metadata loaded, at 42 s, whose pause state the test sets.
function loadedVideo() {
  const video = document.createElement("video");
  let paused = false;
  Object.defineProperty(video, "readyState", { value: HTMLMediaElement.HAVE_METADATA });
  Object.defineProperty(video, "currentTime", { value: 42 });
  Object.defineProperty(video, "paused", { get: () => paused });
  const fire = (event: "play" | "pause") => {
    paused = event === "pause";
    video.dispatchEvent(new Event(event));
  };
  return { video, fire };
}

const sentPauseStates = () =>
  mocks.send.mock.calls.map(([, , sample]) => (sample as { is_paused: boolean }).is_paused);

beforeEach(() => {
  vi.useFakeTimers();
  mocks.send.mockReset();
  mocks.send.mockResolvedValue(undefined);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

// The server's paused timeout must hear of a resume when it happens, not up
// to a report interval later.
it("reports a pause and a resume at once, once each, and keeps the interval", async () => {
  const { video, fire } = loadedVideo();
  renderHook(() => useWatchProgress("session", { current: video }), { wrapper });

  act(() => fire("pause"));
  expect(sentPauseStates()).toEqual([true]);
  act(() => fire("play"));
  expect(sentPauseStates()).toEqual([true, false]);

  await act(async () => {
    await vi.advanceTimersByTimeAsync(10_000);
  });
  expect(sentPauseStates()).toEqual([true, false, false]);
});

it("ends the session when a pause report learns it stayed paused too long", async () => {
  const { video, fire } = loadedVideo();
  mocks.send.mockRejectedValue(
    new PlayerFetchError(410, "Gone", "playback_session_paused_timeout"),
  );
  const onSessionEnded = vi.fn();
  renderHook(() => useWatchProgress("session", { current: video }, undefined, onSessionEnded), {
    wrapper,
  });

  await act(async () => {
    fire("pause");
    await Promise.resolve();
  });
  expect(onSessionEnded).toHaveBeenCalledExactlyOnceWith("paused_timeout");
});
