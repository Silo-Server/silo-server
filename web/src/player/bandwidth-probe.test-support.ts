import { afterEach, beforeEach, vi, type Mock } from "vitest";
import { fetchWithSession as clientFetch } from "@/api/client";
import { v2 } from "@/api/v2/request";
import { queryClient } from "@/lib/query-client";
import { forgetBandwidthEstimate, resetBandwidthProbesForTests } from "./bandwidth-probe";

/**
 * Fakes for the speed test suites. Each suite mocks "@/api/client" and
 * "@/api/v2/request" with plain mocks; these helpers drive them.
 */

/** The probe transfer, which each suite mocks. */
export const fetchWithSession = vi.mocked(clientFetch) as unknown as Mock;

// Node delivers BroadcastChannel messages between test workers, so another
// test file starting playback could reach a suite. This fake keeps cross-tab
// messages to the channels the suite creates.
export class FakeBroadcastChannel {
  static open: FakeBroadcastChannel[] = [];
  onmessage: ((event: MessageEvent) => void) | null = null;
  constructor(readonly name: string) {
    FakeBroadcastChannel.open.push(this);
  }
  postMessage(data: unknown): void {
    for (const peer of FakeBroadcastChannel.open) {
      if (peer !== this && peer.name === this.name) {
        queueMicrotask(() => peer.onmessage?.(new MessageEvent("message", { data })));
      }
    }
  }
  close(): void {
    FakeBroadcastChannel.open = FakeBroadcastChannel.open.filter((peer) => peer !== this);
  }
}

export const DAY = 24 * 60 * 60 * 1000;
export const PROBE_URL = "/api/v2/playback/bandwidth-probe?media_path=true";

/** A body that yields `chunks` of `chunkBytes`, advancing a fake clock `stepMs` per chunk. */
export function timedBody(chunks: number, chunkBytes: number, stepMs: number) {
  let time = 0;
  let sent = 0;
  const cancel = vi.fn(async () => undefined);
  const reader = {
    read: async () => {
      if (sent >= chunks) return { done: true, value: undefined };
      if (sent > 0) time += stepMs;
      sent++;
      return { done: false, value: new Uint8Array(chunkBytes) };
    },
    cancel,
  };
  return {
    res: { ok: true, body: { getReader: () => reader } },
    clock: () => time,
    cancel,
    sent: () => sent,
  };
}

/** A probe request that never answers until it is aborted. */
export function hangingProbe() {
  fetchWithSession.mockImplementation(
    (_url: string, init: RequestInit) =>
      new Promise((_resolve, reject) => {
        init.signal?.addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError")),
        );
      }),
  );
}

export interface ServerReading {
  probe?: { kbps: number; lower_bound: boolean; ageMs: number };
  sample?: { kbps: number; ageMs: number };
}

/**
 * Answers the capability and connection reads, with the speed test schedule;
 * a reported result becomes the probe the next connection read returns.
 */
export function server(options: {
  target?: number;
  automatic?: boolean;
  maxAgeSeconds?: number;
  reading?: ServerReading;
}) {
  let reading = options.reading ?? {};
  const posted: Record<string, unknown>[] = [];
  vi.mocked(v2).mockImplementation((async (operation: string, init?: { body?: unknown }) => {
    if (operation === "GET /api/v2/playback/capabilities")
      return {
        features: ["buffering_quality_v1", "connection_speed_v1"],
        bandwidth_probe_url: PROBE_URL,
        bandwidth_probe_target_kbps: options.target,
        bandwidth_probe_automatic: options.automatic ?? true,
        bandwidth_probe_max_age_seconds: options.maxAgeSeconds ?? 604800,
      };
    if (operation === "GET /api/v2/playback/connection") {
      const at = (ageMs: number) => new Date(Date.now() - ageMs).toISOString();
      return {
        connection: "remote",
        learned_limit: null,
        ...(reading.probe && {
          probe: {
            kbps: reading.probe.kbps,
            lower_bound: reading.probe.lower_bound,
            measured_at: at(reading.probe.ageMs),
            transport: "wifi",
          },
        }),
        ...(reading.sample && {
          playback_sample: { kbps: reading.sample.kbps, measured_at: at(reading.sample.ageMs) },
        }),
      };
    }
    if (operation === "POST /api/v2/playback/bandwidth-probe/results") {
      const body = init!.body as { kbps: number; lower_bound: boolean };
      posted.push(body as Record<string, unknown>);
      reading = { ...reading, probe: { kbps: body.kbps, lower_bound: body.lower_bound, ageMs: 0 } };
      return undefined;
    }
    throw new Error(`unexpected ${operation}`);
  }) as never);
  return { posted };
}

/** How many times the schedule has read the server's reading for this connection. */
export function connectionReads() {
  return vi
    .mocked(v2)
    .mock.calls.filter(([operation]) => operation === "GET /api/v2/playback/connection").length;
}

/** The server() mock's answer, for a test that wraps it. */
export function serverAnswer() {
  return vi.mocked(v2).getMockImplementation() as unknown as (
    operation: string,
    init?: unknown,
  ) => Promise<unknown>;
}

/**
 * Holds the next connection read's answer, taken from the server as it is
 * now, until the returned function runs.
 */
export function holdNextConnectionRead() {
  const answer = serverAnswer();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  let held = false;
  vi.mocked(v2).mockImplementation((async (operation: string, init?: unknown) => {
    if (operation !== "GET /api/v2/playback/connection" || held) return answer(operation, init);
    held = true;
    const response = await answer(operation, init);
    await gate;
    return response;
  }) as never);
  return release;
}

/** How long the schedule lets the first screen settle before it tests. */
export const TEST_DELAY_MS = 5_000;

/** Lets the schedule read the server's reading, then waits out the test delay. */
export async function readThenTest() {
  await vi.advanceTimersByTimeAsync(0);
  await vi.advanceTimersByTimeAsync(TEST_DELAY_MS);
}

export function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

/** Hides the tab for `ms`, then shows it again. */
export async function awayFor(ms: number) {
  setVisibility("hidden");
  await vi.advanceTimersByTimeAsync(ms);
  setVisibility("visible");
}

/**
 * A Web Lock manager that grants every request at once and lists what is
 * held, so tests can see this tab's media holds come and go.
 */
export function fakeLocks() {
  const held: { name: string; mode: string }[] = [];
  const locks = {
    held,
    request: vi.fn(
      async (
        name: string,
        options: { mode?: string; ifAvailable?: boolean },
        callback: (lock: unknown) => unknown,
      ) => {
        const entry = { name, mode: options.mode ?? "exclusive" };
        if (options.ifAvailable && held.some((lock) => lock.name === name)) return callback(null);
        held.push(entry);
        try {
          return await callback(entry);
        } finally {
          held.splice(held.indexOf(entry), 1);
        }
      },
    ),
    query: vi.fn(async () => ({ held: [...held], pending: [] })),
  };
  vi.stubGlobal("navigator", { ...navigator, locks });
  return locks;
}

/** A media element in the page whose playing state the test sets. */
export function pageVideo() {
  const video = document.createElement("video");
  let paused = true;
  Object.defineProperty(video, "paused", { get: () => paused, configurable: true });
  document.body.append(video);
  return {
    video,
    play() {
      paused = false;
      video.dispatchEvent(new Event("playing"));
    },
    pause() {
      paused = true;
      video.dispatchEvent(new Event("pause"));
    },
  };
}

/** A reading this device measured itself, as a server without connection_speed_v1 leaves it. */
export function keptReading(
  kbps: number,
  measuredAt: number,
  maxAgeMs: number,
  network = "unknown",
) {
  return JSON.stringify({
    readings: [{ kbps, measuredAt, maxAgeMs, network }],
    writtenAt: measuredAt,
  });
}

/** Starts each test with no schedule, no kept reading and a visible page. */
export function installProbeTestHooks(): void {
  beforeEach(() => {
    // Re-stubbed per test because tests unstub all globals afterwards.
    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
    resetBandwidthProbesForTests();
    forgetBandwidthEstimate();
    queryClient.clear();
    fetchWithSession.mockReset();
    vi.mocked(v2).mockReset();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.replaceChildren();
  });
}
