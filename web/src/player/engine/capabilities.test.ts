import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  HDR_CHECK_GRACE_MS,
  PROBE_TIMEOUT_MS,
  probeEngineCapabilities,
  resetEngineCapabilitiesForTests,
} from "./capabilities";

const presenter = vi.hoisted(() => ({
  probe: vi.fn<(adapter: unknown) => Promise<boolean | null>>(),
}));
vi.mock("./renderers", () => ({ probePresenter: presenter.probe }));

function stubRuntime(gpu?: { requestAdapter: () => Promise<unknown> }) {
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("Worker", class {});
  vi.stubGlobal("EncodedVideoChunk", class {});
  vi.stubGlobal("AudioContext", class {});
  vi.stubGlobal("AudioWorkletNode", class {});
  vi.stubGlobal("VideoDecoder", { isConfigSupported: async () => ({ supported: true }) });
  vi.stubGlobal("navigator", { ...navigator, gpu });
}

const hardware = { info: { isFallbackAdapter: false } };

describe("probeEngineCapabilities", () => {
  beforeEach(() => {
    presenter.probe.mockReset();
  });

  afterEach(() => {
    resetEngineCapabilitiesForTests();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("gives up on a probe that never answers so playback can start", async () => {
    vi.useFakeTimers();
    stubRuntime();
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

  it("claims HDR only once the presenter has drawn through both paths", async () => {
    stubRuntime({ requestAdapter: async () => hardware });
    presenter.probe.mockResolvedValue(true);
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: true });
    expect(presenter.probe).toHaveBeenCalledWith(hardware);
  });

  it("leaves HDR off when the presenter fails its check", async () => {
    // probePresenter answers false for a shader the browser rejects, among others.
    stubRuntime({ requestAdapter: async () => hardware });
    presenter.probe.mockResolvedValue(false);
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: false });
  });

  it("does not hold a presenter chunk that failed to load against the presenter", async () => {
    stubRuntime({ requestAdapter: async () => hardware });
    presenter.probe.mockRejectedValue(new Error("Failed to fetch dynamically imported module"));
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: true });
  });

  it("leaves HDR off without WebGPU or with only a software adapter", async () => {
    stubRuntime();
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: false });
    resetEngineCapabilitiesForTests();
    stubRuntime({ requestAdapter: async () => ({ info: { isFallbackAdapter: true } }) });
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: false });
    expect(presenter.probe).not.toHaveBeenCalled();
  });

  it("trusts a hardware adapter once the rest of the check runs past its grace", async () => {
    vi.useFakeTimers();
    stubRuntime({ requestAdapter: async () => hardware });
    let finish!: (works: boolean) => void;
    presenter.probe.mockReturnValue(new Promise((resolve) => (finish = resolve)));

    const first = probeEngineCapabilities();
    let settled = false;
    void first.then(() => (settled = true));
    // Every playback start waits for this, so a slow GPU costs a second at most.
    await vi.advanceTimersByTimeAsync(HDR_CHECK_GRACE_MS - 1);
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await expect(first).resolves.toMatchObject({ hdr: true, videoCodecs: expect.any(Array) });

    // The check's own answer applies to the probes after it.
    finish(false);
    await vi.advanceTimersByTimeAsync(0);
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: false });
  });

  it("keeps no verdict from a check that ended without one", async () => {
    // A device lost during the check says nothing about the presenter.
    stubRuntime({ requestAdapter: async () => hardware });
    presenter.probe.mockResolvedValue(null);
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: true });
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: true });
  });

  it("does not claim HDR when even the adapter has not answered by the deadline", async () => {
    vi.useFakeTimers();
    stubRuntime({ requestAdapter: () => new Promise(() => {}) });
    const result = probeEngineCapabilities();
    await vi.advanceTimersByTimeAsync(PROBE_TIMEOUT_MS);
    await expect(result).resolves.toMatchObject({ hdr: false });
  });

  it("trusts an adapter that answered after the deadline from the next probe on", async () => {
    vi.useFakeTimers();
    let answer!: (adapter: unknown) => void;
    stubRuntime({ requestAdapter: () => new Promise((resolve) => (answer = resolve)) });
    // The rest of the check never finishes.
    presenter.probe.mockReturnValue(new Promise(() => {}));
    const first = probeEngineCapabilities();
    await vi.advanceTimersByTimeAsync(PROBE_TIMEOUT_MS);
    await expect(first).resolves.toMatchObject({ hdr: false });

    answer(hardware);
    await vi.advanceTimersByTimeAsync(0);
    await expect(probeEngineCapabilities()).resolves.toMatchObject({ hdr: true });
  });
});
