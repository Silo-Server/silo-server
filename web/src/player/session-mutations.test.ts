import { afterEach, describe, expect, it, vi } from "vitest";
import {
  registerSessionMutations,
  resetSessionMutations,
  observeSessionProgress,
  captureSessionProgress,
  sendSessionProgress,
  stopSequencedSession,
  sessionInstallation,
} from "./session-mutations";
import type { PlayerConfig } from "./context/PlayerConfigContext";

const config: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile",
  getDeviceId: () => "device",
};
const sample = { position: 30, is_paused: false };
const receipt = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const stopped = () => receipt({ outcome: "stopped", stop_id: "ignored" });
const applied = () => receipt({ outcome: "applied" });
const problem = (status: number, type: string) =>
  receipt({ type: `https://silo.example.test/problems/${type}`, detail: type }, status);

afterEach(() => {
  resetSessionMutations();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("sequenced playback mutations", () => {
  it("posts v2 bodies with the installation and a sequence per sample", async () => {
    const fetcher = vi.fn().mockImplementation(async () => applied());
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("seq", "install");
    expect(sessionInstallation("seq")).toBe("install");
    await sendSessionProgress(config, "seq", sample);
    await sendSessionProgress(config, "seq", { ...sample, position: 10 });
    expect(fetcher.mock.calls[0]![0]).toBe("/api/v2/playback/seq/progress");
    expect(JSON.parse(fetcher.mock.calls[0]![1].body)).toEqual({
      ...sample,
      sequence: 1,
      installation_id: "install",
    });
    expect(JSON.parse(fetcher.mock.calls[1]![1].body)).toMatchObject({ sequence: 2, position: 10 });
  });

  it("allocates once per sample and preserves the body after a lost reply", async () => {
    vi.useFakeTimers();
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("lost reply"))
      .mockImplementation(async () => applied());
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("progress-retry", "install");
    const first = sendSessionProgress(config, "progress-retry", sample);
    await vi.advanceTimersByTimeAsync(250);
    await first;
    await sendSessionProgress(config, "progress-retry", { ...sample, position: 10 });
    const bodies = fetcher.mock.calls.map((call) => call[1].body);
    expect(bodies[0]).toBe(bodies[1]);
    expect(JSON.parse(bodies[0]).sequence).toBe(1);
    expect(JSON.parse(bodies[2])).toMatchObject({ ...sample, position: 10, sequence: 2 });
  });

  it("retries a 5xx progress reply but not a 4xx", async () => {
    vi.useFakeTimers();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(problem(503, "dependency_unavailable"))
      .mockResolvedValueOnce(applied())
      .mockResolvedValueOnce(problem(409, "progress_conflict"));
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("progress-5xx", "install");
    const first = sendSessionProgress(config, "progress-5xx", sample);
    await vi.advanceTimersByTimeAsync(250);
    await expect(first).resolves.toBeUndefined();
    await expect(sendSessionProgress(config, "progress-5xx", sample)).rejects.toMatchObject({
      status: 409,
      code: "progress_conflict",
    });
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it("sends one stop identity and treats stopped and replayed as final", async () => {
    vi.useFakeTimers();
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("lost reply"))
      .mockResolvedValueOnce(receipt({ outcome: "replayed", stop_id: "other" }));
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("stop-retry", "install");
    const stop = stopSequencedSession(config, "stop-retry");
    await vi.advanceTimersByTimeAsync(500);
    await expect(stop).resolves.toBeUndefined();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[0]![0]).toBe("/api/v2/playback/stop-retry");
    expect(fetcher.mock.calls[0]![1].method).toBe("DELETE");
    expect(fetcher.mock.calls[0]![1].body).toBe(fetcher.mock.calls[1]![1].body);
    const body = JSON.parse(fetcher.mock.calls[0]![1].body);
    expect(body.stop_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(body.installation_id).toBe("install");
    // A later stop is a no-op once the receipt arrived.
    await stopSequencedSession(config, "stop-retry");
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("rejects a stopped receipt for a different stop id", async () => {
    const fetcher = vi.fn().mockResolvedValue(stopped());
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("stop-mismatch", "install");
    await expect(stopSequencedSession(config, "stop-mismatch")).rejects.toThrow("another receipt");
  });

  it("gives up after the budget and retains the stop ID for retry", async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn().mockImplementation(async () => problem(503, "dependency_unavailable"));
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("stop-timeout", "install");
    const stop = stopSequencedSession(config, "stop-timeout").catch((error) => error);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(await stop).toBeInstanceOf(Error);
    const original = fetcher.mock.calls[0]![1].body;
    fetcher.mockImplementation(async () =>
      receipt({ outcome: "stopped", stop_id: JSON.parse(original).stop_id }),
    );
    await stopSequencedSession(config, "stop-timeout");
    expect(fetcher.mock.calls[fetcher.mock.calls.length - 1]![1].body).toBe(original);
  });

  it("starts the stop budget after waiting for queued progress", async () => {
    vi.useFakeTimers();
    const fetcher = vi
      .fn()
      // The queued progress request never answers; the stop waits the full
      // 30s for it, then its first DELETE fails transiently.
      .mockImplementationOnce(() => new Promise<Response>(() => {}))
      .mockResolvedValueOnce(problem(503, "dependency_unavailable"))
      .mockImplementation(async () => receipt({ outcome: "replayed" }));
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("stop-budget", "install");
    void sendSessionProgress(config, "stop-budget", sample).catch(() => {});
    const stop = stopSequencedSession(config, "stop-budget");
    await vi.advanceTimersByTimeAsync(30_000);
    expect(fetcher).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(500);
    await expect(stop).resolves.toBeUndefined();
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(fetcher.mock.calls[2]![1].body).toBe(fetcher.mock.calls[1]![1].body);
  });

  it("treats a 404 stop as already over", async () => {
    const fetcher = vi.fn().mockResolvedValue(problem(404, "not_found"));
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("stop-gone", "install");
    await expect(stopSequencedSession(config, "stop-gone")).resolves.toBeUndefined();
    await stopSequencedSession(config, "stop-gone");
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("orders a stop after accepted progress and suppresses later progress", async () => {
    let accept!: (value: Response) => void;
    const fetcher = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            accept = resolve;
          }),
      )
      .mockImplementation(async () => stopped());
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("stop-order", "install");
    const progress = sendSessionProgress(config, "stop-order", sample);
    const stop = stopSequencedSession(config, "stop-order");
    await Promise.resolve();
    await Promise.resolve();
    expect(fetcher).toHaveBeenCalledTimes(1);
    accept(applied());
    await progress;
    await stop.catch(() => {});
    await sendSessionProgress(config, "stop-order", { ...sample, position: 40 });
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1]![1].method).toBe("DELETE");
  });

  it("keeps v1 bodies for a session started on the bridge", async () => {
    const fetcher = vi.fn().mockImplementation(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetcher);
    await sendSessionProgress(config, "legacy-mutations", sample);
    await stopSequencedSession(config, "legacy-mutations");
    expect(fetcher.mock.calls[0]![0]).toBe("/api/v1/playback/legacy-mutations/progress");
    expect(JSON.parse(fetcher.mock.calls[0]![1].body)).toEqual(sample);
    expect(fetcher.mock.calls[1]![0]).toBe("/api/v1/playback/legacy-mutations");
    expect(fetcher.mock.calls[1]![1].body).toBeUndefined();
  });

  it("captures the final snapshot once and keeps it stable across retries", async () => {
    vi.useFakeTimers();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(problem(503, "dependency_unavailable"))
      .mockImplementation(async () => receipt({ outcome: "replayed" }));
    vi.stubGlobal("fetch", fetcher);
    registerSessionMutations("final-snapshot", "install");
    captureSessionProgress("final-snapshot", { position: 20, is_paused: false });
    let current = { position: 47, is_paused: true };
    const forget = observeSessionProgress("final-snapshot", () => current);
    const stop = stopSequencedSession(config, "final-snapshot");
    await vi.advanceTimersByTimeAsync(0);
    current = { position: 99, is_paused: false };
    forget();
    await vi.advanceTimersByTimeAsync(500);
    await stop;
    const first = fetcher.mock.calls[0]![1].body;
    expect(JSON.parse(first)).toMatchObject({ position: 47, is_paused: true, sequence: 1 });
    expect(fetcher.mock.calls[1]![1].body).toBe(first);
  });
});
