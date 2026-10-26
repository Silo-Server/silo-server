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
// The plan's media measured arriving below its 63000 kbps: before the attempt
// first plays, a wait lowers only with this evidence.
const slowMedia = () => 3000;
beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => vi.useRealTimers());

function setup({
  paused = false,
  playIntent,
  throughputKbps,
}: {
  paused?: boolean;
  playIntent?: () => boolean;
  throughputKbps?: () => number | undefined;
} = {}) {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: paused, configurable: true });
  const options = {
    videoRef: { current: video },
    sessionId: "title",
    plan,
    planRevision: 1,
    mode: "always" as const,
    obscured: false,
    playIntent,
    throughputKbps,
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
  act(() => adopted(2));
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

it("reports a wait an outage held until playback runs", () => {
  const h = setup();
  // Offline while playing from the buffer: nothing is frozen yet.
  h.options.transportTrouble.mockReturnValue(true);
  h.advance(100);
  expect(h.result.current.outage).toBe(false);
  h.send("waiting");
  expect(h.result.current.outage).toBe(true);
  // The network is back before playback runs again.
  h.options.transportTrouble.mockReturnValue(false);
  h.advance(1_000);
  expect(h.result.current.outage).toBe(true);
  h.send("playing");
  expect(h.result.current.outage).toBe(false);
});

it("freezes the pill timer while paused and reports the retired plan once", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6_000);
  act(() => h.options.onReplan.mock.calls[0]![1](2));
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
  act(() => h.options.onReplan.mock.calls[0]![1](2));
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
  act(() => h.options.onReplan.mock.calls[0]![1](2));
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

it.each([
  ["a new attempt", { attemptId: "attempt-2" }],
  ["a title change", { sessionId: "next-title" }],
])("reports the kept estimate from a lowering until %s", (_name, change) => {
  const h = setup();
  h.send("waiting");
  h.advance(6000);
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(44100);
  h.rerender({ ...h.options, ...change });
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(undefined);
  // Client replans are the session's to send, never the hook's.
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
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

// C12: before the attempt first plays, the wait lowers only when the plan's
// media was measured arriving more slowly than it plays.
it.each<[string, (() => number | undefined) | undefined, number]>([
  ["lowers when its media arrived slowly", slowMedia, 1],
  ["does not lower when nothing measured its media", undefined, 0],
])("times a plan's slow start as a wait, from adoption: %s", (_name, throughputKbps, lowerings) => {
  // A new plan's element starts paused and plays once enough data arrives.
  const h = setup({ paused: true, playIntent: () => true, throughputKbps });
  const onReplan = h.options.onReplan;
  // The transport empties the element as it attaches; the start still counts.
  h.send("emptied");
  h.advance(15_900);
  expect(onReplan).not.toHaveBeenCalled();
  h.advance(30_000);
  expect(onReplan).toHaveBeenCalledTimes(lowerings);
  if (lowerings)
    expect(onReplan.mock.calls[0]![0]).toMatchObject({
      bandwidth_estimate_kbps: 44100,
      bandwidth_estimate_source: "observed_rebuffer",
    });
});

// Only the attempt's first plan is a start. A slow track change on Auto
// replaces a source that played: its load is a source reload, which never
// lowers, however long it takes.
it("does not lower while a replacement plan loads slowly", () => {
  const h = setup({ paused: true, playIntent: () => true });
  h.send("playing");
  h.advance(20_000);
  h.rerender({ ...h.options, planRevision: 2, plan: { ...plan, plan_id: "p2" } });
  h.send("emptied");
  h.send("waiting");
  h.advance(30_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
});

// A plan that replaces one before any played, here a failure recovery 3 s
// in, continues the attempt's start: timed, under the start's own grace.
it("times a replacement adopted before the start played as the start's wait", () => {
  const h = setup({ paused: true, playIntent: () => true, throughputKbps: slowMedia });
  h.advance(3_000);
  h.rerender({ ...h.options, planRevision: 2, plan: { ...plan, plan_id: "p2" } });
  h.send("emptied");
  h.advance(12_900);
  expect(h.options.onReplan).not.toHaveBeenCalled();
  h.advance(200);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
});

// C12, the Apple TV cascade: a lowering adopted before the start played
// continues its wait, and lowers again only on evidence of its own.
it("does not lower a lowered start again when nothing measured its media", () => {
  let measured: number | undefined = 3000;
  const h = setup({ paused: true, playIntent: () => true, throughputKbps: () => measured });
  h.advance(16_100);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
  act(() => h.options.onReplan.mock.calls[0]![1](2));
  measured = undefined;
  h.rerender({
    ...h.options,
    planRevision: 2,
    plan: { ...plan, plan_id: "p2", delivered_kbps: 31600 },
  });
  h.advance(60_000);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
});

it("keeps timing a slow start through the waiting that follows its reload", () => {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: true, configurable: true });
  const onReplan = vi.fn();
  renderHook((props) => useAutoQualityDownshift(props), {
    initialProps: {
      videoRef: { current: video },
      sessionId: "title",
      plan,
      planRevision: 1,
      mode: "always" as const,
      obscured: false,
      playIntent: () => true,
      onReplan,
      onNotice: vi.fn(),
      throughputKbps: slowMedia,
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

// A track the device cannot render: the network delivered, nothing plays.
it("does not lower a start whose buffer fills but never plays", () => {
  const h = setup({ paused: true, playIntent: () => true });
  let bufferedEnd = 0;
  Object.defineProperty(h.options.videoRef.current, "buffered", {
    configurable: true,
    get: () => ({ length: 1, start: () => 0, end: () => bufferedEnd }),
  });
  h.advance(3_000);
  bufferedEnd = 6;
  h.advance(30_000);
  // It stays the renderer's wait even if the buffer runs down again.
  bufferedEnd = 0.5;
  h.advance(30_000);
  expect(h.options.onReplan).not.toHaveBeenCalled();
  const fail = vi.fn();
  act(() => h.result.current.startDeadlinePassed(fail));
  expect(fail).toHaveBeenCalledTimes(1);
  expect(h.options.onReplan).not.toHaveBeenCalled();
});

it("does not fail a plan that replaced one whose deadline lowering failed", () => {
  const h = setup({ throughputKbps: slowMedia });
  h.rerender({ ...h.options, planRevision: 2, plan: { ...plan, plan_id: "p2" } });
  const fail = vi.fn();
  act(() => h.result.current.startDeadlinePassed(fail));
  const [, settle] = h.options.onReplan.mock.calls[0]!;
  h.rerender({ ...h.options, planRevision: 3, plan: { ...plan, plan_id: "p3" } });
  act(() => settle(undefined));
  expect(fail).not.toHaveBeenCalled();
});

it("keeps timing a slow start through a seek before its first frame", () => {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: true, configurable: true });
  const onReplan = vi.fn();
  renderHook((props) => useAutoQualityDownshift(props), {
    initialProps: {
      videoRef: { current: video },
      sessionId: "title",
      plan,
      planRevision: 1,
      mode: "always" as const,
      obscured: false,
      playIntent: () => true,
      onReplan,
      onNotice: vi.fn(),
      throughputKbps: slowMedia,
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
      sessionId: "title",
      plan,
      planRevision: 1,
      mode: "always" as const,
      obscured: false,
      playIntent: () => false,
      onReplan,
      onNotice: vi.fn(),
      throughputKbps: slowMedia,
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

// N2: the back-forward cache.
it("adopts the playing plan again when the page comes back from the back-forward cache", () => {
  const h = setup();
  // A lowering keeps an estimate for the title.
  h.send("waiting");
  h.advance(6000);
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(44100);
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true }));
  });
  expect(h.options.onSummary).toHaveBeenCalledTimes(1);
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
  });
  // The kept estimate survives, and the plan played after the return has its
  // own adoption and summary.
  expect(h.options.onKeptEstimate).toHaveBeenLastCalledWith(44100);
  h.unmount();
  expect(h.options.onSummary).toHaveBeenCalledTimes(2);
  const [first, second] = h.options.onSummary.mock.calls.map((call) => call[0].adoption_id);
  expect(second).not.toBe(first);
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
