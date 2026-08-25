import { describe, expect, it, vi } from "vitest";

import { resolveHLSEngineV3, selectHLSEngineV3 } from "./hlsEngine";

describe("selectHLSEngineV3", () => {
  it.each(["dolby_vision", "hdr10"])(
    "prefers native HLS for %s when both engines are available",
    (dynamicRange) => {
      expect(selectHLSEngineV3(dynamicRange, true, true)).toBe("native");
    },
  );

  it("keeps hls.js first for SDR", () => {
    expect(selectHLSEngineV3("sdr", true, true)).toBe("hlsjs");
  });

  it("falls back to native when hls.js is unavailable", () => {
    expect(selectHLSEngineV3("sdr", true, false)).toBe("native");
  });

  it("rejects an unavailable transport", () => {
    expect(selectHLSEngineV3("dolby_vision", false, false)).toBe("unsupported");
  });

  it("selects native HDR without loading hls.js", async () => {
    const loadHLSJS = vi.fn(async () => ({ isSupported: () => true }));

    await expect(resolveHLSEngineV3("dolby_vision", true, loadHLSJS)).resolves.toEqual({
      engine: "native",
    });
    expect(loadHLSJS).not.toHaveBeenCalled();
  });

  it("falls back to native HLS when loading hls.js fails", async () => {
    const loadHLSJS = vi.fn(async () => {
      throw new Error("chunk unavailable");
    });

    await expect(resolveHLSEngineV3("sdr", true, loadHLSJS)).resolves.toEqual({
      engine: "native",
    });
    expect(loadHLSJS).toHaveBeenCalledOnce();
  });

  it("returns the loaded hls.js engine when Media Source Extensions are supported", async () => {
    const hlsjs = { isSupported: () => true };

    await expect(resolveHLSEngineV3("sdr", false, async () => hlsjs)).resolves.toEqual({
      engine: "hlsjs",
      hlsjs,
    });
  });
});
