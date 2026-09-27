import { afterEach, describe, expect, it, vi } from "vitest";
import { markPlaybackIntent, takePlaybackIntent } from "./first-frame";

describe("playback intent", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    takePlaybackIntent("");
  });

  it("hands the mark to the request it was made for, once", () => {
    markPlaybackIntent("request-1", 1_000);
    vi.spyOn(performance, "now").mockReturnValue(3_000);

    expect(takePlaybackIntent("request-1")).toBe(1_000);
    expect(takePlaybackIntent("request-1")).toBeNull();
  });

  it("drops a mark made for another request", () => {
    markPlaybackIntent("request-1", 1_000);
    vi.spyOn(performance, "now").mockReturnValue(3_000);

    expect(takePlaybackIntent("request-2")).toBeNull();
    expect(takePlaybackIntent("request-1")).toBeNull();
  });

  it("drops a mark too old to have started this request", () => {
    markPlaybackIntent("request-1", 1_000);
    vi.spyOn(performance, "now").mockReturnValue(61_001);

    expect(takePlaybackIntent("request-1")).toBeNull();
  });
});
