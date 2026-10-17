import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useAutoQualityDownshift } from "./useAutoQualityDownshift";
import type { BufferingPlan } from "../buffering-quality";

const plan: BufferingPlan = {
  plan_id: "p1",
  delivered_kbps: 63000,
  quality: "auto",
  available_qualities: [{ label: "1080p", bitrate_kbps: 10000 }],
};
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});
afterEach(() => vi.useRealTimers());

function setup() {
  const video = document.createElement("video");
  Object.defineProperty(video, "paused", { value: false, configurable: true });
  const options = {
    videoRef: { current: video },
    enabled: true,
    sessionId: "title",
    plan,
    mode: "always" as const,
    obscured: false,
    transportTrouble: vi.fn(() => false),
    onReplan: vi.fn(),
    onNotice: vi.fn(),
    onSummary: vi.fn(),
  };
  const hook = renderHook((props) => useAutoQualityDownshift(props), { initialProps: options });
  const send = (name: string) =>
    act(() => {
      video.dispatchEvent(new Event(name));
    });
  const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));
  send("playing");
  advance(20_000);
  return { ...hook, options, send, advance };
}

it("correlates a controller replan before showing its undo", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6_000);
  expect(h.options.onReplan).toHaveBeenCalledTimes(1);
  const [request, adopted] = h.options.onReplan.mock.calls[0]!;
  expect(request.bandwidth_estimate_kbps).toBe(44100);
  act(() => adopted("p2"));
  h.rerender({ ...h.options, plan: { ...plan, plan_id: "p2", delivered_kbps: 31600 } });
  h.advance(100);
  expect(h.result.current.pill?.kind).toBe("undo");
});

it("does not claim an unrelated plan adopted while a lowering is pending", () => {
  const h = setup();
  h.send("waiting");
  h.advance(6_000);
  h.rerender({ ...h.options, plan: { ...plan, plan_id: "unrelated", delivered_kbps: 10000 } });
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
  act(() => h.options.onReplan.mock.calls[0]![1]("p2"));
  h.rerender({ ...h.options, plan: { ...plan, plan_id: "p2", delivered_kbps: 31600 } });
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
