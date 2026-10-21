import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MEDIA_ENDED_EVENT, useAutoQualityDownshift } from "./useAutoQualityDownshift";
import type { BufferingPlan } from "../buffering-quality";

const plan: BufferingPlan = {
  plan_id: "p1",
  delivered_kbps: 63000,
  quality: "auto",
  available_qualities: [{ label: "1080p", bitrate_kbps: 10000 }],
};
beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => vi.useRealTimers());

function setup({
  paused = false,
  playIntent,
}: { paused?: boolean; playIntent?: () => boolean } = {}) {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: paused, configurable: true });
  const options = {
    videoRef: { current: video },
    enabled: true,
    sessionId: "title",
    plan,
    planRevision: 1,
    mode: "always" as const,
    obscured: false,
    playIntent,
    transportTrouble: vi.fn(() => false),
    onReplan: vi.fn(),
    onNotice: vi.fn(),
    onSummary: vi.fn(),
    onKeptEstimate: vi.fn(),
    attemptId: "attempt-1" as string | null,
    suspended: false,
  };
  const hook = renderHook((props) => useAutoQualityDownshift(props), { initialProps: options });
  const send = (name: string) =>
    act(() => {
      video.dispatchEvent(new Event(name));
    });
  const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));
  if (!paused) {
    send("playing");
    advance(20_000);
  }
  return { ...hook, options, send, advance };
}

it("correlates a controller replan before showing its undo", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6_000);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
  const [request, adopted] = h.options.onReplan.mock.calls[0]!;
  expect(request.bandwidth_estimate_kbps).toBe(44100);
  act(() => adopted("p2", 2));
  h.rerender({
    ...h.options,
    planRevision: 2,
    plan: { ...plan, plan_id: "p2", delivered_kbps: 31600 },
  });
  h.advance(100);
  expect(h.result.current.pill?.kind).toBe("undo");
});

it("does not claim an unrelated plan adopted while a lowering is pending", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6_000);
  h.rerender({
    ...h.options,
    planRevision: 2,
    plan: { ...plan, plan_id: "unrelated", delivered_kbps: 10000 },
  });
  h.advance(100);
  expect(h.result.current.pill).toBeNull();
  expect(h.options.onNotice).not.toHaveBeenCalled();
});

it("excludes outages and seek waits", () => {
  const h = setup();
  h.options.transportTrouble.mockReturnValue(true);
  h.send("waiting");
  h.advance(20_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
  h.send("playing");
  h.options.transportTrouble.mockReturnValue(false);
  Object.defineProperty(h.options.videoRef.current, "seeking", { value: true });
  h.send("waiting");
  h.advance(20_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
});

it("freezes the pill timer while paused and reports the retired plan once", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6_000);
  act(() => h.options.onReplan.mock.calls[0]![1]("p2", 2));
  h.rerender({
    ...h.options,
    planRevision: 2,
    plan: { ...plan, plan_id: "p2", delivered_kbps: 31600 },
  });
  h.send("playing");
  h.advance(1_000);
  h.send("pause");
  const remaining = h.result.current.pill?.remainingMs;
  h.advance(30_000);
  expect(h.result.current.pill?.remainingMs).toBe(remaining);
  h.send("play");
  h.advance(15_000);
  expect(h.result.current.pill).toBeNull();
  expect(h.options.onSummary.mock.calls[0]![0].plan_id).toBe("p1");
  expect(h.options.onSummary.mock.calls[0]![1]).toMatchObject({
    stall_count: 1,
    stall_total_ms: 6000,
  });
  h.unmount();
  expect(h.options.onSummary).toHaveBeenCalledTimes(2);
});

it("settles a no-lower adoption even when its server plan ID repeats", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6000);
  act(() => h.options.onReplan.mock.calls[0]![1]("p1", 2));
  h.rerender({ ...h.options, planRevision: 2, plan: { ...plan } });
  expect(h.options.onNotice).toHaveBeenCalledExactlyOnceWith("cant_lower");
  h.send("playing");
  h.advance(10000);
  h.send("waiting");
  h.advance(70000);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
  h.unmount();
  expect(h.options.onSummary).toHaveBeenCalledTimes(2);
  expect(h.options.onSummary.mock.calls.map(([p]) => p.plan_id)).toEqual(["p1", "p1"]);
  expect(h.options.onSummary.mock.calls[1]![1].buffering_action).toBe("cant_lower");
});
it("does not treat an in-place update as a new adoption", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6000);
  act(() => h.options.onReplan.mock.calls[0]![1]("p2", 2));
  const next = {
    ...h.options,
    planRevision: 2,
    plan: { ...plan, plan_id: "p2", delivered_kbps: 31600 },
  };
  h.rerender(next);
  h.advance(1000);
  const deadline = h.result.current.pill?.deadlineMs;
  expect(deadline).toBe(performance.now() + 14_000);
  h.rerender({ ...next, plan: { ...next.plan } });
  h.advance(100);
  expect(h.result.current.pill?.deadlineMs).toBe(deadline);
  expect(h.options.onSummary).toHaveBeenCalledTimes(1);
});

it("reports the kept estimate from a lowering until a new attempt", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6000);
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(44100);
  h.rerender({ ...h.options, attemptId: "attempt-2" });
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(undefined);
  // Client replans are the session's to send, never the hook's.
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
});

it("drops the kept estimate when the controller stops, as in a Watch Together room", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6000);
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(44100);
  h.rerender({ ...h.options, enabled: false });
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(undefined);
});

it("does not count a wait that begins with media buffered until the buffer runs down", () => {
  const h = setup();
  const video = h.options.videoRef.current;
  let bufferedEnd = 0;
  Object.defineProperty(video, "currentTime", { value: 30, configurable: true, writable: true });
  Object.defineProperty(video, "buffered", {
    configurable: true,
    get: () => ({ length: 1, start: () => 0, end: () => bufferedEnd }),
  });
  // A decoder catching up stops playback with 5 s of media still buffered.
  bufferedEnd = 35;
  h.send("waiting");
  h.advance(10_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
  // The buffer runs down to the threshold: the stall starts now.
  bufferedEnd = 30.8;
  h.advance(5_900);
  expect(h.options.onReplan).not.toHaveBeenCalled();
  h.advance(200);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
});

it("times a plan's slow start as a wait, from adoption", () => {
  // A new plan's element starts paused and plays once enough data arrives.
  const h = setup({ paused: true, playIntent: () => true });
  const onReplan = h.options.onReplan;
  // The transport empties the element as it attaches; the start still counts.
  h.send("emptied");
  h.advance(15_900);
  expect(onReplan).not.toHaveBeenCalled();
  h.advance(200);
  expect(onReplan).toHaveBeenCalledTimes(1);
  expect(onReplan.mock.calls[0]![0]).toMatchObject({
    bandwidth_estimate_kbps: 44100,
    bandwidth_estimate_source: "observed_rebuffer",
  });
});

it("keeps timing a slow start through the waiting that follows its reload", () => {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: true, configurable: true });
  const onReplan = vi.fn();
  renderHook((props) => useAutoQualityDownshift(props), {
    initialProps: {
      videoRef: { current: video },
      enabled: true,
      sessionId: "title",
      plan,
      planRevision: 1,
      mode: "always" as const,
      obscured: false,
      playIntent: () => true,
      onReplan,
      onNotice: vi.fn(),
    },
  });
  // The transport empties the element, which then reports it is waiting.
  act(() => {
    video.dispatchEvent(new Event("emptied"));
    video.dispatchEvent(new Event("waiting"));
  });
  act(() => vi.advanceTimersByTime(16_100));
  expect(onReplan).toHaveBeenCalledTimes(1);
});

it("does not count a slow start's wait while the renderer, not the network, is behind", () => {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: true, configurable: true });
  // Five seconds are buffered from the start, but no frame has shown yet.
  Object.defineProperty(video, "buffered", {
    configurable: true,
    get: () => ({ length: 1, start: () => 0, end: () => 5 }),
  });
  const onReplan = vi.fn();
  renderHook((props) => useAutoQualityDownshift(props), {
    initialProps: {
      videoRef: { current: video },
      enabled: true,
      sessionId: "title",
      plan,
      planRevision: 1,
      mode: "always" as const,
      obscured: false,
      playIntent: () => true,
      onReplan,
      onNotice: vi.fn(),
    },
  });
  act(() => {
    video.dispatchEvent(new Event("waiting"));
  });
  act(() => vi.advanceTimersByTime(30_000));
  expect(onReplan).not.toHaveBeenCalled();
});

it("keeps timing a slow start through a seek before its first frame", () => {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: true, configurable: true });
  const onReplan = vi.fn();
  renderHook((props) => useAutoQualityDownshift(props), {
    initialProps: {
      videoRef: { current: video },
      enabled: true,
      sessionId: "title",
      plan,
      planRevision: 1,
      mode: "always" as const,
      obscured: false,
      playIntent: () => true,
      onReplan,
      onNotice: vi.fn(),
    },
  });
  // The player applies the resume position before anything has played.
  act(() => {
    video.dispatchEvent(new Event("seeking"));
    video.dispatchEvent(new Event("waiting"));
  });
  act(() => vi.advanceTimersByTime(16_100));
  expect(onReplan).toHaveBeenCalledTimes(1);
});

it("counts the wait after a seek made before a paused plan first played", () => {
  const video = document.createElement("video");
  let paused = true;
  Object.defineProperty(video, "paused", { get: () => paused, configurable: true });
  const onReplan = vi.fn();
  renderHook((props) => useAutoQualityDownshift(props), {
    initialProps: {
      videoRef: { current: video },
      enabled: true,
      sessionId: "title",
      plan,
      planRevision: 1,
      mode: "always" as const,
      obscured: false,
      playIntent: () => false,
      onReplan,
      onNotice: vi.fn(),
    },
  });
  // Opened paused: the viewer seeks, then plays, and nothing arrives.
  act(() => {
    video.dispatchEvent(new Event("seeking"));
    paused = false;
    video.dispatchEvent(new Event("play"));
    video.dispatchEvent(new Event("waiting"));
  });
  act(() => vi.advanceTimersByTime(16_100));
  expect(onReplan).toHaveBeenCalledTimes(1);
});

it("does not time a start the player will not play", () => {
  const h = setup({ paused: true, playIntent: () => false });
  h.advance(30_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
});

it("reports the playing plan when the page closes", () => {
  const h = setup();
  act(() => {
    window.dispatchEvent(new Event("pagehide"));
  });
  expect(h.options.onSummary).toHaveBeenCalledTimes(1);
  expect(h.options.onSummary.mock.calls[0]![2]).toBe(true);
  h.unmount();
  // Stopping again at unmount reports nothing more.
  expect(h.options.onSummary).toHaveBeenCalledTimes(1);
});

it("keeps a stall that hls.js steps over a hole in", () => {
  const h = setup();
  const video = h.options.videoRef.current;
  Object.defineProperty(video, "currentTime", { value: 30, configurable: true, writable: true });
  h.send("waiting");
  h.advance(2_000);
  // The next fragment leaves a 0.08 s hole; hls.js sets currentTime past it.
  video.currentTime = 30.08;
  h.send("seeking");
  h.advance(4_100);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
});

it("treats the wait after a seek made while paused as the seek's", () => {
  const h = setup();
  const video = h.options.videoRef.current;
  Object.defineProperty(video, "currentTime", { value: 30, configurable: true, writable: true });
  h.send("pause");
  video.currentTime = 600;
  h.send("seeking");
  h.send("play");
  h.send("waiting");
  h.advance(8_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
});

it("does not count the wait at the end of a stream hls.js found ended", () => {
  const h = setup();
  h.send("waiting");
  h.advance(2_000);
  h.send(MEDIA_ENDED_EVENT);
  h.advance(10_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
});

it("tracks stalls in a Watch Party room without lowering", () => {
  const h = setup();
  h.rerender({ ...h.options, suspended: true });
  h.send("waiting");
  h.advance(8_000);
  h.send("playing");
  expect(h.options.onReplan).not.toHaveBeenCalled();
  // Joining is not a plan boundary: nothing is reported until the plan ends.
  expect(h.options.onSummary).not.toHaveBeenCalled();
  h.unmount();
  expect(h.options.onSummary.mock.calls[0]![1]).toMatchObject({
    stall_count: 1,
    buffering_action: "none",
  });
});

it("does not count the wait while the browser decoder restarts", () => {
  const h = setup();
  const video = h.options.videoRef.current as HTMLVideoElement & { decoderRecovering?: boolean };
  video.decoderRecovering = true;
  h.send("waiting");
  h.advance(10_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
  // The restart is over and the wait goes on: from now it is a stall.
  video.decoderRecovering = false;
  h.advance(6_200);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
});
