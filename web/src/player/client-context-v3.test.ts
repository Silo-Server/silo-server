import { afterEach, describe, expect, it, vi } from "vitest";

import { buildDeliveriesV3, detectHLSSupport } from "./client-context-v3";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("detectHLSSupport", () => {
  it("accepts native HLS when Media Source Extensions are unavailable", () => {
    vi.stubGlobal("MediaSource", undefined);
    vi.stubGlobal("document", {
      createElement: () => ({ canPlayType: () => "maybe" }),
    });

    expect(detectHLSSupport()).toBe(true);
  });

  it("falls back to the hls.js Media Source Extensions probe", () => {
    vi.stubGlobal("document", {
      createElement: () => ({ canPlayType: () => "" }),
    });
    vi.stubGlobal("MediaSource", { isTypeSupported: () => true });

    expect(detectHLSSupport()).toBe(true);
  });
});

describe("buildDeliveriesV3", () => {
  it("advertises the embedded text artifacts rendered by the web player", () => {
    const deliveries = buildDeliveriesV3({
      containers: ["mp4"],
      codecsVideo: ["h264"],
      codecsAudio: ["aac"],
      maxResolution: "1080p",
      hdr: false,
      hls: true,
    });

    for (const delivery of Object.values(deliveries)) {
      expect(delivery.subtitles.embedded_text).toBe(true);
      expect(delivery.subtitles.sidecar_text).toBe(true);
    }
  });
});
