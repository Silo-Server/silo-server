import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import { useScreenWakeLock } from "./useScreenWakeLock";

class FakeSentinel extends EventTarget {
  released = false;
  async release() {
    if (this.released) return;
    this.released = true;
    this.dispatchEvent(new Event("release"));
  }
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { configurable: true, value: state });
  document.dispatchEvent(new Event("visibilitychange"));
}

afterEach(() => {
  Reflect.deleteProperty(navigator, "wakeLock");
  Reflect.deleteProperty(document, "visibilityState");
});

// Browsers release a screen wake lock whenever the page is hidden, and the
// release event can arrive before or after the page is visible again.
it.each([
  ["before", false],
  ["after", true],
])("requests a new lock when the release arrives %s the page is shown", async (_, late) => {
  const sentinels: FakeSentinel[] = [];
  const request = vi.fn(async () => {
    const sentinel = new FakeSentinel();
    sentinels.push(sentinel);
    return sentinel;
  });
  Object.defineProperty(navigator, "wakeLock", { configurable: true, value: { request } });

  renderHook(() => useScreenWakeLock(true));
  await waitFor(() => expect(request).toHaveBeenCalledTimes(1));

  await act(async () => {
    setVisibility("hidden");
    if (late) setVisibility("visible");
    await sentinels[0]?.release();
    if (!late) setVisibility("visible");
  });

  await waitFor(() => expect(request).toHaveBeenCalledTimes(2));
});
