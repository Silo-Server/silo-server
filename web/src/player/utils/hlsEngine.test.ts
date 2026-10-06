// @vitest-environment node

import { describe, expect, it, vi } from "vitest";

import { isSafariBrowserV3, resolveHLSEngineV3, selectHLSEngineV3 } from "./hlsEngine";

describe("isSafariBrowserV3", () => {
  it.each(["CriOS/151.0 Mobile/15E148 Safari/604.1", "FxiOS/151.0 Mobile/15E148 Safari/605.1.15"])(
    "does not classify an iOS alternative browser as Safari: %s",
    (userAgent) => expect(isSafariBrowserV3(userAgent)).toBe(false),
  );
});

describe("selectHLSEngineV3", () => {
  it("falls back to native when hls.js is unavailable", () => {
    expect(selectHLSEngineV3("sdr", true, false)).toBe("native");
  });

  it("rejects an unavailable transport", () => {
    expect(selectHLSEngineV3("dolby_vision", false, false)).toBe("unsupported");
  });

  it("selects native Safari HLS without loading hls.js", async () => {
    const loadHLSJS = vi.fn(async () => ({ isSupported: () => true }));

    await expect(resolveHLSEngineV3("sdr", true, loadHLSJS, undefined, true)).resolves.toEqual({
      engine: "native",
    });
    expect(loadHLSJS).not.toHaveBeenCalled();
  });

  it("loads hls.js for Chromium HDR even when native HLS is advertised", async () => {
    const hlsjs = { isSupported: () => true };
    const loadHLSJS = vi.fn(async () => hlsjs);

    await expect(
      resolveHLSEngineV3("dolby_vision", true, loadHLSJS, undefined, false),
    ).resolves.toEqual({ engine: "hlsjs", hlsjs });
    expect(loadHLSJS).toHaveBeenCalledOnce();
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
