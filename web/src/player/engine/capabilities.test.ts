import { afterEach, describe, expect, it, vi } from "vitest";
import {
  PROBE_TIMEOUT_MS,
  probeEngineCapabilities,
  resetEngineCapabilitiesForTests,
} from "./capabilities";

describe("probeEngineCapabilities", () => {
  afterEach(() => {
    resetEngineCapabilitiesForTests();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("gives up on a probe that never answers so playback can start", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("isSecureContext", true);
    vi.stubGlobal("Worker", class {});
    vi.stubGlobal("EncodedVideoChunk", class {});
    vi.stubGlobal("AudioContext", class {});
    vi.stubGlobal("AudioWorkletNode", class {});
    vi.stubGlobal("VideoDecoder", { isConfigSupported: () => new Promise(() => {}) });

    const result = probeEngineCapabilities();
    let settled = false;
    void result.then(() => (settled = true));
    // The decoder query hangs, so only the timeout can settle the probe.
    await vi.advanceTimersByTimeAsync(PROBE_TIMEOUT_MS - 1);
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await expect(result).resolves.toBeNull();
  });
});
