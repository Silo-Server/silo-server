import { describe, expect, it, vi } from "vitest";

import {
  MAX_STALL_RECOVERIES,
  NativeHlsStallWatchdog,
  STALL_TIMEOUT_MS,
  type StallWatchdogMedia,
} from "./nativeHlsStallWatchdog";

// The frozen state from #1466: playing, HAVE_ENOUGH_DATA, NETWORK_IDLE.
function frozenMedia(overrides: Partial<StallWatchdogMedia> = {}): StallWatchdogMedia {
  return {
    paused: false,
    seeking: false,
    ended: false,
    readyState: 4,
    networkState: 1,
    currentTime: 304.14,
    ...overrides,
  };
}

function setup(media: StallWatchdogMedia, atEnd = false) {
  let now = 0;
  const onStall = vi.fn();
  const onGiveUp = vi.fn();
  const watchdog = new NativeHlsStallWatchdog({
    media,
    atEnd: () => atEnd,
    onStall,
    onGiveUp,
    now: () => now,
  });
  const advance = (ms: number) => {
    now += ms;
    watchdog.check();
  };
  watchdog.check();
  return { watchdog, onStall, onGiveUp, advance };
}

describe("NativeHlsStallWatchdog", () => {
  it("reports a playing element whose position stops moving", () => {
    const { onStall, advance } = setup(frozenMedia());

    advance(STALL_TIMEOUT_MS - 1);
    expect(onStall).not.toHaveBeenCalled();

    advance(1);
    expect(onStall).toHaveBeenCalledExactlyOnceWith(304.14);
  });

  it("does not report a position that keeps moving", () => {
    const media = frozenMedia();
    const { onStall, advance } = setup(media);

    for (let i = 0; i < 20; i++) {
      (media as { currentTime: number }).currentTime += 1;
      advance(1_000);
    }
    expect(onStall).not.toHaveBeenCalled();
  });

  it.each([
    ["paused", { paused: true }],
    ["seeking", { seeking: true }],
    ["ended", { ended: true }],
    ["without metadata", { readyState: 0 }],
    // Waiting on a slow segment is not a stall.
    ["loading", { networkState: 2 }],
  ])("ignores an element that is %s", (_label, overrides) => {
    const { onStall, advance } = setup(frozenMedia(overrides));

    advance(STALL_TIMEOUT_MS * 3);
    expect(onStall).not.toHaveBeenCalled();
  });

  it("ignores a position at the end of the media", () => {
    const { onStall, advance } = setup(frozenMedia(), true);

    advance(STALL_TIMEOUT_MS * 3);
    expect(onStall).not.toHaveBeenCalled();
  });

  it("restarts the timer when the element pauses and resumes", () => {
    const media = frozenMedia();
    const { onStall, advance } = setup(media);

    advance(STALL_TIMEOUT_MS - 1_000);
    (media as { paused: boolean }).paused = true;
    advance(1_000);
    (media as { paused: boolean }).paused = false;
    // The first sample after resuming starts a new timer.
    advance(1_000);
    advance(STALL_TIMEOUT_MS - 1);
    expect(onStall).not.toHaveBeenCalled();
    advance(1);
    expect(onStall).toHaveBeenCalledOnce();
  });

  it("gives up once after its recoveries run out", () => {
    const { onStall, onGiveUp, advance } = setup(frozenMedia());

    for (let i = 0; i < (MAX_STALL_RECOVERIES + 3) * 2; i++) {
      advance(STALL_TIMEOUT_MS);
    }
    expect(onStall).toHaveBeenCalledTimes(MAX_STALL_RECOVERIES);
    expect(onGiveUp).toHaveBeenCalledOnce();
  });
});
