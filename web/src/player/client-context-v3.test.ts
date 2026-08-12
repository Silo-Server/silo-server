import { afterEach, describe, expect, it, vi } from "vitest";

import {
  buildClientCapabilitiesV3,
  buildClientPlaybackContextV3,
  buildDeliveriesV3,
  detectHLSSupport,
  type WebCapabilityProbe,
} from "./client-context-v3";

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
      hdrDetails: {
        hdr10: false,
        hdr10_plus: false,
        hlg: false,
        dolby_vision_profiles: [],
      },
      hls: true,
    });

    for (const delivery of Object.values(deliveries)) {
      expect(delivery.subtitles.embedded_text).toBe(true);
      expect(delivery.subtitles.sidecar_text).toBe(true);
    }
  });
});

describe("structured HDR capabilities", () => {
  const probe: WebCapabilityProbe = {
    containers: ["mp4"],
    codecsVideo: ["hevc"],
    codecsAudio: ["eac3"],
    maxResolution: "2160p",
    hdr: true,
    hdrDetails: {
      hdr10: true,
      hdr10_plus: false,
      hlg: true,
      dolby_vision_profiles: [8],
    },
    hls: true,
  };

  it("publishes the structured formats in both device and active-output contexts", () => {
    expect(buildClientCapabilitiesV3(probe).hdr_details).toEqual(probe.hdrDetails);
    expect(buildClientPlaybackContextV3(probe).output.hdr_details).toEqual(probe.hdrDetails);
  });

  it("scopes the same output formats to every web delivery path", () => {
    for (const delivery of Object.values(buildDeliveriesV3(probe))) {
      expect(delivery.hdr_details).toEqual(probe.hdrDetails);
    }
  });
});
