import { Buffer } from "node:buffer";
import { ReadableStream } from "node:stream/web";
import type { UrlSourceOptions } from "mediabunny";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MediaRun } from "./mediaFetch";
import { ParallelRangeSource } from "./parallelRangeSource";

const MIB = 2 ** 20;
const KIB = 2 ** 10;
const HEAD_URL = "https://app.example.test/api/v2/stream/s?st=x";
const STRIPE_URL = "https://media.example.test/api/v2/stream/s?st=x&token=t";
const MODIFIED = "Mon, 09 Nov 2026 10:00:00 GMT";
const SIZE = 70 * MIB + 12345;

const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

function pattern(size: number): Uint8Array {
  const bytes = new Uint8Array(size);
  // The higher terms keep 512 KiB chunks and 2 MiB ranges from repeating one another.
  for (let i = 0; i < size; i++) {
    bytes[i] = (i * 31 + (i >>> 8) + (i >>> 16) * 7 + (i >>> 24) * 13) & 0xff;
  }
  return bytes;
}

function same(a: Uint8Array, b: Uint8Array): boolean {
  return Buffer.from(a.buffer, a.byteOffset, a.length).equals(
    Buffer.from(b.buffer, b.byteOffset, b.length),
  );
}

interface Served {
  url: string;
  start: number;
  /** Last byte asked for, inclusive. */
  last: number;
  run: MediaRun | undefined;
  signal: AbortSignal;
  index: number;
}

type Fault =
  | { kind: "status"; status: number }
  | { kind: "contentRange"; value: string }
  | { kind: "total"; total: number }
  | { kind: "lastModified"; value: string }
  | { kind: "fetchError" }
  | { kind: "bodyError"; afterBytes: number }
  | { kind: "short"; bytes: number }
  | { kind: "holdBody" }
  | { kind: "holdBodyUntilAbort" }
  | { kind: "holdHeaders" };

/** The parts of a `Response` the reader reads. */
interface FakeReply {
  ok: boolean;
  status: number;
  statusText: string;
  headers: { get(name: string): string | null; has(name: string): boolean };
  body: ReadableStream<Uint8Array>;
}

type MeteredFetch = ConstructorParameters<typeof ParallelRangeSource>[3];

/** A ranged server over an in-memory file, standing in for the metered fetch. */
class FakeServer {
  readonly requests: Served[] = [];
  inFlight = 0;
  maxInFlight = 0;
  private stripeInFlight = 0;
  maxStripeInFlight = 0;
  completed = 0;
  fault: ((request: Served, attempt: number) => Fault | undefined) | undefined;
  private perRun = new Map<MediaRun, number>();
  maxPerRun = 0;
  private release: (() => void) | null = null;
  private held = new Promise<void>((resolve) => (this.release = resolve));

  constructor(readonly bytes: Uint8Array) {}

  releaseHeld() {
    this.release?.();
  }

  fetch = async (
    input: RequestInfo | URL,
    init?: RequestInit,
    run?: MediaRun,
  ): Promise<FakeReply> => {
    const headers = init?.headers as Record<string, string>;
    const asked = /^bytes=(\d+)-(\d+)$/.exec(headers.Range ?? "");
    if (!asked) throw new Error(`unexpected Range ${headers.Range}`);
    const request: Served = {
      url: String(input),
      start: Number(asked[1]),
      last: Number(asked[2]),
      run,
      signal: init?.signal as AbortSignal,
      index: this.requests.length,
    };
    this.requests.push(request);
    this.inFlight++;
    this.maxInFlight = Math.max(this.maxInFlight, this.inFlight);
    const onStripeUrl = request.url === STRIPE_URL;
    if (onStripeUrl) {
      this.stripeInFlight++;
      this.maxStripeInFlight = Math.max(this.maxStripeInFlight, this.stripeInFlight);
    }
    if (run) {
      this.perRun.set(run, (this.perRun.get(run) ?? 0) + 1);
      this.maxPerRun = Math.max(this.maxPerRun, this.perRun.get(run) ?? 0);
    }
    let finished = false;
    const finish = () => {
      if (finished) return;
      finished = true;
      this.inFlight--;
      if (onStripeUrl) this.stripeInFlight--;
      this.completed++;
      if (run) this.perRun.set(run, (this.perRun.get(run) ?? 1) - 1);
    };
    request.signal.addEventListener("abort", finish, { once: true });

    await tick();
    const fault = this.fault?.(request, request.index);
    if (fault?.kind === "fetchError") {
      finish();
      throw new TypeError("network error");
    }
    if (fault?.kind === "holdHeaders") await this.held;
    const size = this.bytes.length;
    const last = Math.min(request.last, size - 1);
    let payload = this.bytes.subarray(request.start, last + 1);
    if (fault?.kind === "short") payload = payload.subarray(0, fault.bytes);
    const total = fault?.kind === "total" ? fault.total : size;
    const modified = fault?.kind === "lastModified" ? fault.value : MODIFIED;
    const contentRange =
      fault?.kind === "contentRange" ? fault.value : `bytes ${request.start}-${last}/${total}`;
    const status = fault?.kind === "status" ? fault.status : 206;
    const responseHeaders = new Map([
      ["content-range", contentRange],
      ["last-modified", modified],
    ]);
    let sent = 0;
    const body = new ReadableStream<Uint8Array>({
      pull: async (controller) => {
        if (fault?.kind === "holdBody") await this.held;
        if (fault?.kind === "holdBodyUntilAbort" && !request.signal.aborted) {
          await new Promise<void>((resolve) =>
            request.signal.addEventListener("abort", () => resolve(), { once: true }),
          );
        }
        await tick();
        if (request.signal.aborted) {
          controller.error(new Error("aborted"));
          return;
        }
        if (fault?.kind === "bodyError" && sent >= fault.afterBytes) {
          finish();
          controller.error(new Error("connection reset"));
          return;
        }
        if (sent >= payload.length) {
          finish();
          controller.close();
          return;
        }
        const chunk = payload.slice(sent, sent + 512 * KIB);
        sent += chunk.length;
        controller.enqueue(chunk);
      },
      cancel: () => finish(),
    });
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: "",
      headers: {
        get: (name: string) => responseHeaders.get(name.toLowerCase()) ?? null,
        has: (name: string) => responseHeaders.has(name.toLowerCase()),
      },
      body,
    };
  };
}

interface Readable {
  _read(
    start: number,
    end: number,
    min: number,
    max: number,
  ): Promise<{ bytes: Uint8Array; offset: number } | null>;
}

async function read(source: ParallelRangeSource, start: number, end: number): Promise<Uint8Array> {
  const result = await (source as unknown as Readable)._read(start, end, 0, Infinity);
  if (!result) throw new Error("read past the end");
  return result.bytes.subarray(start - result.offset, end - result.offset);
}

const sources: ParallelRangeSource[] = [];
afterEach(() => {
  for (const source of sources.splice(0)) {
    try {
      source.ref().free();
    } catch {
      // Already disposed by the test.
    }
  }
});

function makeSource(server: FakeServer, options: UrlSourceOptions = {}) {
  let runs = 0;
  const metered = {
    fetch: server.fetch as unknown as MeteredFetch["fetch"],
    newRun: (): MediaRun => ({ request: runs++, bytes: 0, first: 0, last: 0, report: null }),
  };
  const source = new ParallelRangeSource(
    HEAD_URL,
    STRIPE_URL,
    { getRetryDelay: () => 0, ...options },
    metered,
  );
  sources.push(source);
  return { source, runs: () => runs };
}

describe("ParallelRangeSource reads", () => {
  it("returns the file's bytes in order across the head and the stripes", async () => {
    const server = new FakeServer(pattern(SIZE));
    const { source } = makeSource(server);
    const events: Array<[number, number]> = [];
    source.on("read", ({ start, end }) => events.push([start, end]));

    for (let pos = 0; pos < SIZE; pos += MIB) {
      const end = Math.min(pos + MIB, SIZE);
      expect(same(await read(source, pos, end), server.bytes.subarray(pos, end))).toBe(true);
    }
    // One worker, so what it hands over must be contiguous.
    let next = 0;
    for (const [start, end] of events) {
      expect(start).toBe(next);
      next = end;
    }
    expect(next).toBe(SIZE);
  });

  it("stays inside the worker's target, with a head and 2 MiB stripes", async () => {
    const server = new FakeServer(pattern(SIZE));
    const { source } = makeSource(server);
    await read(source, 0, KIB);
    // The 32 MiB floor makes the target 32 MiB; the worker fills it and stops.
    await vi.waitFor(() => {
      expect(server.inFlight).toBe(0);
      expect(server.requests.reduce((sum, r) => sum + (r.last - r.start + 1), 0)).toBe(32 * MIB);
    });
    expect(Math.max(...server.requests.map((r) => r.last))).toBe(32 * MIB - 1);
    const [head, ...stripes] = server.requests;
    expect([head?.url, head?.start, head?.last]).toEqual([HEAD_URL, 0, 16 * MIB - 1]);
    // The first three stripes find the stripe URL free; a later one can be the fourth lane.
    expect(stripes.slice(0, 3).every((stripe) => stripe.url === STRIPE_URL)).toBe(true);
    expect(server.maxStripeInFlight).toBeLessThanOrEqual(3);
    for (const stripe of stripes) {
      expect(stripe.last - stripe.start + 1).toBe(2 * MIB);
    }
  });

  it("keeps at most 4 ranges in flight across two workers, and 4 per worker", async () => {
    const server = new FakeServer(pattern(SIZE));
    const { source, runs } = makeSource(server);
    await Promise.all([read(source, 0, KIB), read(source, 40 * MIB, 40 * MIB + KIB)]);
    await vi.waitFor(() => {
      expect(server.inFlight).toBe(0);
      expect(server.requests.length).toBeGreaterThan(16);
    });
    expect(server.maxInFlight).toBe(4);
    expect(server.maxPerRun).toBeLessThanOrEqual(4);
    // Each worker run passes its one run to every request, the head included.
    expect(runs()).toBe(2);
    const byRun = new Map<MediaRun | undefined, number>();
    for (const r of server.requests) byRun.set(r.run, (byRun.get(r.run) ?? 0) + 1);
    expect(byRun.size).toBe(2);
    expect([...byRun.keys()].every((run) => run !== undefined)).toBe(true);
  });

  it("keeps one range in flight across two workers until the file size is known", async () => {
    const server = new FakeServer(pattern(SIZE));
    server.fault = (_request, index) => (index === 0 ? { kind: "holdHeaders" } : undefined);
    const { source } = makeSource(server);
    const reading = Promise.all([read(source, 0, KIB), read(source, 40 * MIB, 40 * MIB + KIB)]);
    reading.catch(() => undefined);
    try {
      await vi.waitFor(() => expect(server.requests.length).toBeGreaterThanOrEqual(1));
      // Both workers have started; the first response, which carries the size, is held.
      for (let i = 0; i < 5; i++) await tick();
      expect(server.requests.length).toBe(1);
      server.releaseHeld();
      const [first, second] = await reading;
      expect(same(first, server.bytes.subarray(0, KIB))).toBe(true);
      expect(same(second, server.bytes.subarray(40 * MIB, 40 * MIB + KIB))).toBe(true);
    } finally {
      server.releaseHeld();
      await reading.catch(() => undefined);
    }
  });

  it("gives a worker that waited for the file size the next slot, before the other worker's read-ahead", async () => {
    const server = new FakeServer(pattern(SIZE));
    // The first response carries the size; its body stays held, so the head keeps its slot.
    // The second worker's body is held too, so no body chunk wakes the first worker: only
    // the slot hand-off can.
    server.fault = (_request, index) => (index <= 1 ? { kind: "holdBody" } : undefined);
    const { source } = makeSource(server);
    const reading = Promise.all([read(source, 0, KIB), read(source, 40 * MIB, 40 * MIB + KIB)]);
    reading.catch(() => undefined);
    try {
      // The size arrives and 3 slots are free: the waiting worker takes the first,
      // ahead of the other worker's stripes.
      await vi.waitFor(() => expect(server.requests.length).toBeGreaterThanOrEqual(4));
      // The first worker's read-ahead ends at 32 MiB; the second worker's range lies past it.
      expect(server.requests[1]?.start).toBeGreaterThanOrEqual(32 * MIB);
      server.releaseHeld();
      const [first, second] = await reading;
      expect(same(first, server.bytes.subarray(0, KIB))).toBe(true);
      expect(same(second, server.bytes.subarray(40 * MIB, 40 * MIB + KIB))).toBe(true);
    } finally {
      server.releaseHeld();
      await reading.catch(() => undefined);
    }
  });

  it("holds one slot back from a lone worker while the warm head is open, for a second worker", async () => {
    const server = new FakeServer(pattern(SIZE));
    // The head's response carries the size, but no body byte arrives, and the stripes stay in flight.
    server.fault = (_request, index) => (index <= 2 ? { kind: "holdBody" } : undefined);
    const { source } = makeSource(server);
    const reading = read(source, 0, 20 * MIB);
    reading.catch(() => undefined);
    let second: Promise<Uint8Array> | undefined;
    try {
      await vi.waitFor(() => expect(server.requests.length).toBe(3));
      for (let i = 0; i < 5; i++) await tick();
      // The head and two stripes: the lone worker leaves the fourth slot free.
      expect(server.requests.length).toBe(3);
      expect(server.maxInFlight).toBe(3);

      // The second worker (the cue read) is requested at once, before any range completes.
      second = read(source, 40 * MIB, 40 * MIB + KIB);
      second.catch(() => undefined);
      await vi.waitFor(() => expect(server.requests.length).toBe(4));
      expect(server.requests[3]?.start).toBeGreaterThanOrEqual(32 * MIB);
      expect(server.completed).toBe(0);

      server.releaseHeld();
      expect(same(await reading, server.bytes.subarray(0, 20 * MIB))).toBe(true);
      expect(same(await second, server.bytes.subarray(40 * MIB, 40 * MIB + KIB))).toBe(true);
    } finally {
      server.releaseHeld();
      await reading.catch(() => undefined);
      await second?.catch(() => undefined);
    }
  });

  it("gives a lone worker all 4 slots once its warm head range has completed", async () => {
    const server = new FakeServer(pattern(SIZE));
    // The head completes; every stripe stays in flight, so only the slot limit can stop a fourth.
    server.fault = (request) => (request.start > 0 ? { kind: "holdBody" } : undefined);
    const { source } = makeSource(server);
    const reading = read(source, 0, 20 * MIB);
    reading.catch(() => undefined);
    try {
      // No seek and no second worker: the head URL is still open, but the reserve is over.
      await vi.waitFor(() => expect(server.inFlight).toBe(4));
      expect(server.requests.length).toBe(5);
      // The three held stripes fill the stripe URL's lanes; the head's slot, freed now, goes to
      // the head URL.
      expect(server.requests.filter((r) => r.url === STRIPE_URL).length).toBe(3);
      expect(server.requests[4]?.url).toBe(HEAD_URL);
      expect(server.requests[4]?.start).toBe(22 * MIB);
      server.releaseHeld();
      expect(same(await reading, server.bytes.subarray(0, 20 * MIB))).toBe(true);
    } finally {
      server.releaseHeld();
      await reading.catch(() => undefined);
    }
  });

  it("gives the first worker all 4 slots once a second worker has come and gone", async () => {
    const server = new FakeServer(pattern(SIZE));
    // The first worker's ranges, the head included, stay in flight until released; the second
    // worker's range stays in flight until its worker is aborted.
    server.fault = (request) =>
      request.start < 32 * MIB ? { kind: "holdBody" } : { kind: "holdBodyUntilAbort" };
    const { source } = makeSource(server);
    const reading = read(source, 0, 20 * MIB);
    reading.catch(() => undefined);
    try {
      await vi.waitFor(() => expect(server.requests.length).toBe(3));
      // The aborted worker's read never settles; the source's disposal after the test rejects it.
      read(source, 40 * MIB, 40 * MIB + KIB).catch(() => undefined);
      await vi.waitFor(() => expect(server.requests.length).toBe(4));
      expect(server.inFlight).toBe(4);

      // Mediabunny aborts a worker like this when it retires its workers. The second worker
      // leaves while its range is in flight; that range frees its slot afterwards.
      const { _orchestrator, _abortControllers } = source as unknown as {
        _orchestrator: { workers: { currentPos: number; aborted: boolean }[] };
        _abortControllers: Map<object, AbortController>;
      };
      const worker = _orchestrator.workers.find(
        (candidate) => candidate.currentPos === server.requests[3]?.start,
      );
      expect(worker).toBeDefined();
      if (!worker) return;
      worker.aborted = true;
      _abortControllers.get(worker)?.abort();

      // The head is still in flight and the page has not seeked, but the reserve ended when the
      // second worker started.
      await vi.waitFor(() => {
        expect(server.requests.some((request) => request.start === 20 * MIB)).toBe(true);
        expect(server.inFlight).toBe(4);
      });
      server.releaseHeld();
      expect(same(await reading, server.bytes.subarray(0, 20 * MIB))).toBe(true);
    } finally {
      server.releaseHeld();
      await reading.catch(() => undefined);
    }
  });

  it("gives a lone worker all 4 slots once the warm head has ended", async () => {
    const server = new FakeServer(pattern(SIZE));
    // The first four ranges stay in flight, so only the slot limit can stop a fifth or a fourth.
    server.fault = (_request, index) => (index <= 3 ? { kind: "holdBody" } : undefined);
    const { source } = makeSource(server);
    const reading = read(source, 0, 20 * MIB);
    reading.catch(() => undefined);
    try {
      // The engine ends the warm head on a seek, after the open's reads have started.
      await vi.waitFor(() => expect(server.requests.length).toBe(3));
      for (let i = 0; i < 5; i++) await tick();
      expect(server.requests.length).toBe(3);
      source.endWarmHead();
      await vi.waitFor(() => expect(server.requests.length).toBe(4));
      expect(server.maxInFlight).toBe(4);
      server.releaseHeld();
      expect(same(await reading, server.bytes.subarray(0, 20 * MIB))).toBe(true);
    } finally {
      server.releaseHeld();
      await reading.catch(() => undefined);
    }
  });

  it("holds at most 4 ranges per worker, delivered or not", async () => {
    const server = new FakeServer(pattern(SIZE));
    server.fault = (_request, index) => (index === 0 ? { kind: "holdBody" } : undefined);
    const { source } = makeSource(server);
    const reading = read(source, 0, 20 * MIB);
    reading.catch(() => undefined);
    try {
      // The head's size answer lets stripes start; its body is held, so nothing
      // after the first four ranges (the head and three stripes) may start.
      await vi.waitFor(() => expect(server.completed).toBe(3));
      expect(server.requests.length).toBe(4);
      server.releaseHeld();
      expect(same(await reading, server.bytes.subarray(0, 20 * MIB))).toBe(true);
    } finally {
      server.releaseHeld();
      await reading.catch(() => undefined);
    }
  });

  it("sends the first range of a worker to the head URL until the head ends", async () => {
    const server = new FakeServer(pattern(SIZE));
    const { source } = makeSource(server);
    await read(source, 0, KIB);
    await vi.waitFor(() => expect(server.inFlight).toBe(0));
    expect(server.requests[0]?.url).toBe(HEAD_URL);
    expect(server.requests.slice(1, 4).every((r) => r.url === STRIPE_URL)).toBe(true);
    expect(server.maxStripeInFlight).toBeLessThanOrEqual(3);

    source.endWarmHead();
    const before = server.requests.length;
    await read(source, 40 * MIB, 40 * MIB + KIB);
    await vi.waitFor(() => expect(server.inFlight).toBe(0));
    expect(server.requests.length).toBeGreaterThan(before);
    // Not a head range: 2 MiB, and the first three go to the stripe URL. The fourth finds it full.
    const [first] = server.requests.slice(before);
    expect((first?.last ?? 0) - (first?.start ?? 0) + 1).toBe(2 * MIB);
    expect(server.requests.slice(before, before + 3).every((r) => r.url === STRIPE_URL)).toBe(true);
    expect(server.requests[before + 3]?.url).toBe(HEAD_URL);
  });

  it("completes a read whose range reaches past the end of the file", async () => {
    const small = 5 * MIB + 100;
    const server = new FakeServer(pattern(small));
    const { source } = makeSource(server);
    expect(same(await read(source, 0, small), server.bytes)).toBe(true);
    // The head asked for 16 MiB and the server ended its 206 at the last byte.
    expect(server.requests[0]?.last).toBe(16 * MIB - 1);
  });

  it("cancels every in-flight range of a worker when the source is disposed", async () => {
    const server = new FakeServer(pattern(SIZE));
    server.fault = (_request, index) => (index === 0 ? { kind: "holdBody" } : undefined);
    const { source } = makeSource(server);
    const reading = read(source, 0, 20 * MIB).catch(() => undefined);
    await vi.waitFor(() => expect(server.requests.length).toBe(4));
    source.ref().free();
    await reading;
    await vi.waitFor(() => expect(server.inFlight).toBe(0));
    expect(server.requests.every((r) => r.signal.aborted)).toBe(true);
  });
});

describe("ParallelRangeSource failures", () => {
  const FIRST_STRIPE = 16 * MIB;

  it.each([
    ["a 200", { kind: "status", status: 200 }],
    [
      "a Content-Range that starts elsewhere",
      {
        kind: "contentRange",
        value: `bytes ${FIRST_STRIPE + 1}-${FIRST_STRIPE + 2 * MIB}/${SIZE}`,
      },
    ],
    ["a changed total", { kind: "total", total: SIZE + 1 }],
    ["a changed Last-Modified", { kind: "lastModified", value: "Tue, 10 Nov 2026 10:00:00 GMT" }],
  ] satisfies Array<[string, Fault]>)("fails the source on %s", async (_name, fault) => {
    const server = new FakeServer(pattern(SIZE));
    server.fault = (request) => (request.start === FIRST_STRIPE ? fault : undefined);
    const getRetryDelay = vi.fn(() => 0);
    const { source } = makeSource(server, { getRetryDelay });
    await expect(read(source, 0, 20 * MIB)).rejects.toThrow();
    // A mismatch is not a transport failure: it is never retried.
    expect(getRetryDelay).not.toHaveBeenCalled();
  });

  it("fails a range at its fourth failure in a row that delivered nothing", async () => {
    const server = new FakeServer(pattern(SIZE));
    const failures: Fault[] = [
      // The stripe URL's fetch error moves the range to the head URL and counts no failure.
      { kind: "fetchError" },
      { kind: "fetchError" },
      { kind: "bodyError", afterBytes: 0 },
      { kind: "short", bytes: 0 },
      { kind: "fetchError" },
    ];
    let seen = 0;
    server.fault = (request) =>
      request.start >= FIRST_STRIPE && request.start < FIRST_STRIPE + 2 * MIB
        ? failures[seen++]
        : undefined;
    const getRetryDelay = vi.fn((attempts: number) => (attempts < 4 ? 0 : null));
    const { source } = makeSource(server, { getRetryDelay });
    await expect(read(source, 0, 20 * MIB)).rejects.toThrow();
    expect(getRetryDelay.mock.calls.map(([attempts]) => attempts)).toEqual([1, 2, 3, 4]);
  });

  // A lossy link drops a body again and again; each drop that delivered bytes is progress.
  it("finishes a range whose every answer fails after delivering bytes", async () => {
    const server = new FakeServer(pattern(SIZE));
    let seen = 0;
    server.fault = (request): Fault | undefined => {
      if (request.url !== HEAD_URL || request.start >= FIRST_STRIPE || seen >= 8) return undefined;
      return seen++ % 2 === 0
        ? { kind: "bodyError", afterBytes: MIB }
        : { kind: "short", bytes: MIB };
    };
    const getRetryDelay = vi.fn((attempts: number) => (attempts < 4 ? 0 : null));
    const { source } = makeSource(server, { getRetryDelay });
    expect(same(await read(source, 0, 20 * MIB), server.bytes.subarray(0, 20 * MIB))).toBe(true);
    // A body error restarts the count; a short body is asked for again with no retry.
    expect(getRetryDelay.mock.calls.map(([attempts]) => attempts)).toEqual([1, 1, 1, 1]);
  });

  it("starts the retry count again after a short body that delivered bytes", async () => {
    const server = new FakeServer(pattern(SIZE));
    const faults: Fault[] = [
      { kind: "fetchError" },
      { kind: "fetchError" },
      { kind: "short", bytes: 512 * KIB },
      { kind: "fetchError" },
      { kind: "fetchError" },
    ];
    let seen = 0;
    server.fault = (request) =>
      request.url === HEAD_URL && request.start < FIRST_STRIPE ? faults[seen++] : undefined;
    const getRetryDelay = vi.fn((attempts: number) => (attempts < 4 ? 0 : null));
    const { source } = makeSource(server, { getRetryDelay });
    expect(same(await read(source, 0, 20 * MIB), server.bytes.subarray(0, 20 * MIB))).toBe(true);
    expect(getRetryDelay.mock.calls.map(([attempts]) => attempts)).toEqual([1, 2, 1, 2]);
  });

  it.each([
    ["a 502", { kind: "status", status: 502 }],
    ["a 404", { kind: "status", status: 404 }],
    ["a Content-Range without the file's size", { kind: "contentRange", value: "bytes 0-1/*" }],
    [
      "a Content-Range that names other bytes",
      { kind: "contentRange", value: `bytes 1-2/${SIZE}` },
    ],
    ["a Content-Range that names another file size", { kind: "total", total: SIZE + 1 }],
    ["a network error", { kind: "fetchError" }],
    ["a body-read error", { kind: "bodyError", afterBytes: 512 * KIB }],
    ["a short body", { kind: "short", bytes: 256 * KIB }],
  ] satisfies Array<[string, Fault]>)(
    "sends every range to the head URL after the stripe URL fails with %s",
    async (_name, fault) => {
      const server = new FakeServer(pattern(SIZE));
      server.fault = (request) => (request.url === STRIPE_URL ? fault : undefined);
      const getRetryDelay = vi.fn(() => 0);
      const { source } = makeSource(server, { getRetryDelay });
      expect(same(await read(source, 0, 20 * MIB), server.bytes.subarray(0, 20 * MIB))).toBe(true);
      // The rest of the failed range is asked again from the head URL, with no retry spent on the
      // stripe URL.
      expect(getRetryDelay).not.toHaveBeenCalled();
      expect(
        server.requests.some(
          (r) => r.url === HEAD_URL && r.start >= FIRST_STRIPE && r.start < FIRST_STRIPE + 2 * MIB,
        ),
      ).toBe(true);

      await vi.waitFor(() => expect(server.inFlight).toBe(0));
      source.endWarmHead();
      const before = server.requests.length;
      const stripeBefore = server.requests.filter((r) => r.url === STRIPE_URL).length;
      expect(
        same(await read(source, 40 * MIB, 41 * MIB), server.bytes.subarray(40 * MIB, 41 * MIB)),
      ).toBe(true);
      expect(server.requests.length).toBeGreaterThan(before);
      expect(server.requests.filter((r) => r.url === STRIPE_URL).length).toBe(stripeBefore);
    },
  );

  it("retries the rest of a range after a body failure", async () => {
    const server = new FakeServer(pattern(SIZE));
    let failed = false;
    server.fault = (request) => {
      if (failed || request.start !== FIRST_STRIPE) return undefined;
      failed = true;
      return { kind: "bodyError", afterBytes: 512 * KIB };
    };
    const { source } = makeSource(server);
    expect(same(await read(source, 0, 20 * MIB), server.bytes.subarray(0, 20 * MIB))).toBe(true);
    expect(server.requests.some((r) => r.start === FIRST_STRIPE + 512 * KIB)).toBe(true);
  });
});
