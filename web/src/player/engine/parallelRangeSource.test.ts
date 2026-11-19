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
  /** The request's own first and last byte, with `*` where the file's size goes. */
  | { kind: "unknownSize" }
  | { kind: "total"; total: number }
  | { kind: "lastModified"; value: string }
  | { kind: "fetchError" }
  | { kind: "bodyError"; afterBytes: number }
  | { kind: "short"; bytes: number }
  /** One chunk with the range's bytes and the `extra` bytes of the file that follow them. */
  | { kind: "long"; extra: number }
  | { kind: "ignoreRange" }
  | { kind: "holdBody" }
  | { kind: "holdBodyUntilAbort" }
  | { kind: "slowBody"; intervalMs: number }
  | { kind: "holdHeaders" };

/** The parts of a `Response` the reader reads. */
interface FakeReply {
  ok: boolean;
  status: number;
  statusText: string;
  type: "basic";
  redirected: false;
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
    const asked = /^bytes=(\d+)-(\d*)$/.exec(headers.Range ?? "");
    if (!asked) throw new Error(`unexpected Range ${headers.Range}`);
    const request: Served = {
      url: String(input),
      start: Number(asked[1]),
      // An open-ended range, which only the stock source sends, asks for the rest of the file.
      last: asked[2] === "" ? Number.POSITIVE_INFINITY : Number(asked[2]),
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
    // A server that ignores Range sends the whole file from byte 0, with its length.
    if (fault?.kind === "ignoreRange") payload = this.bytes;
    if (fault?.kind === "short") payload = payload.subarray(0, fault.bytes);
    if (fault?.kind === "long")
      payload = this.bytes.subarray(request.start, last + 1 + fault.extra);
    const total = fault?.kind === "total" ? fault.total : size;
    const modified = fault?.kind === "lastModified" ? fault.value : MODIFIED;
    const contentRange =
      fault?.kind === "contentRange"
        ? fault.value
        : `bytes ${request.start}-${last}/${fault?.kind === "unknownSize" ? "*" : total}`;
    const status =
      fault?.kind === "status" ? fault.status : fault?.kind === "ignoreRange" ? 200 : 206;
    const responseHeaders = new Map([["last-modified", modified]]);
    if (fault?.kind === "ignoreRange") responseHeaders.set("content-length", String(size));
    else responseHeaders.set("content-range", contentRange);
    let sent = 0;
    const body = new ReadableStream<Uint8Array>({
      pull: async (controller) => {
        if (fault?.kind === "holdBody") await this.held;
        if (fault?.kind === "slowBody") {
          // Like a real body, the wait ends when the request aborts.
          await new Promise<void>((resolve) => {
            const timer = setTimeout(resolve, fault.intervalMs);
            request.signal.addEventListener(
              "abort",
              () => {
                clearTimeout(timer);
                resolve();
              },
              { once: true },
            );
          });
        }
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
        const chunk = payload.slice(sent, sent + (fault?.kind === "long" ? Infinity : 512 * KIB));
        sent += chunk.length;
        controller.enqueue(chunk);
      },
      cancel: () => finish(),
    });
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: "",
      type: "basic",
      redirected: false,
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
    newRun: (): MediaRun => ({
      request: runs++,
      bytes: 0,
      first: 0,
      last: 0,
      spans: [],
      report: null,
    }),
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
    ["a body longer than its range", { kind: "long", extra: 1 }],
  ] satisfies Array<[string, Fault]>)("fails the source on %s", async (_name, fault) => {
    const server = new FakeServer(pattern(SIZE));
    server.fault = (request) => (request.start === FIRST_STRIPE ? fault : undefined);
    const getRetryDelay = vi.fn(() => 0);
    const { source } = makeSource(server, { getRetryDelay });
    await expect(read(source, 0, 20 * MIB)).rejects.toThrow();
    // A mismatch is not a transport failure: it is never retried.
    expect(getRetryDelay).not.toHaveBeenCalled();
  });

  // A server can reset the connection after the last byte of the file. The range already holds
  // every byte it will get, so the error is the end of the response, not a transport failure.
  it("completes a read past the end of the file whose body errors after its last byte", async () => {
    const size = 5 * MIB + 100;
    const server = new FakeServer(pattern(size));
    server.fault = (request): Fault | undefined => {
      if (request.start >= size) return { kind: "status", status: 416 };
      return request.start === 0 && request.url === HEAD_URL
        ? { kind: "bodyError", afterBytes: size }
        : undefined;
    };
    const getRetryDelay = vi.fn(() => 0);
    const handleUnhandledError = vi.fn();
    const { source } = makeSource(server, { getRetryDelay, handleUnhandledError });
    expect(same(await read(source, 0, size), server.bytes)).toBe(true);

    // The read resolves with its bytes, before the server's terminal error: wait for the worker's
    // run to end, which is when a wrong retry would have shown.
    const { _orchestrator } = source as unknown as {
      _orchestrator: { workers: { running: boolean }[] };
    };
    await vi.waitFor(() => {
      expect(_orchestrator.workers.every((worker) => !worker.running)).toBe(true);
      expect(server.inFlight).toBe(0);
    });
    expect(server.requests.some((request) => request.start >= size)).toBe(false);
    expect(getRetryDelay).not.toHaveBeenCalled();
    expect(handleUnhandledError).not.toHaveBeenCalled();
  });

  // The stock source reads the file from one response and keeps it sequential, with a warning
  // that Mediabunny logs once per origin.
  it("plays a plan URL that ignores Range sequentially", async () => {
    // The VM test context has no web streams, and the stock source wraps the body in one.
    vi.stubGlobal("ReadableStream", ReadableStream);
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = (request) => (request.url === HEAD_URL ? { kind: "ignoreRange" } : undefined);
      const getRetryDelay = vi.fn(() => 0);
      const { source } = makeSource(server, { getRetryDelay });
      expect(same(await read(source, 0, 20 * MIB), server.bytes.subarray(0, 20 * MIB))).toBe(true);
      expect(
        same(await read(source, 40 * MIB, 41 * MIB), server.bytes.subarray(40 * MIB, 41 * MIB)),
      ).toBe(true);
      expect(server.requests.some((request) => request.url === STRIPE_URL)).toBe(false);
      expect(getRetryDelay).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllGlobals();
    }
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
    ["a Content-Range without the file's size", { kind: "unknownSize" }],
    [
      "a Content-Range that names other bytes",
      { kind: "contentRange", value: `bytes 1-2/${SIZE}` },
    ],
    ["a Content-Range that names another file size", { kind: "total", total: SIZE + 1 }],
    ["a network error", { kind: "fetchError" }],
    ["a body-read error", { kind: "bodyError", afterBytes: 512 * KIB }],
    ["a short body", { kind: "short", bytes: 256 * KIB }],
    ["a body longer than its range", { kind: "long", extra: 1 }],
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

  // A stripe whose body stops without closing would hold the in-order delivery until the page's
  // 20 s stall timeout. Fake timers stand in for the wait.
  it("sends the range to the head URL when a stripe body goes silent", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = (request) =>
        request.url === STRIPE_URL ? { kind: "holdBodyUntilAbort" } : undefined;
      const getRetryDelay = vi.fn(() => 0);
      const { source } = makeSource(server, { getRetryDelay });
      let bytes: Uint8Array | undefined;
      const reading = read(source, 0, 20 * MIB).then((read) => (bytes = read));
      await vi.advanceTimersByTimeAsync(10_000);
      expect(bytes, "the read finished within 10 s of stripe silence").toBeDefined();
      await reading;
      expect(same(bytes as Uint8Array, server.bytes.subarray(0, 20 * MIB))).toBe(true);
      expect(getRetryDelay).not.toHaveBeenCalled();
      // Only the silent stripe request is aborted; the plan's URL requests are not.
      const stripes = server.requests.filter((r) => r.url === STRIPE_URL);
      expect(stripes.length).toBeGreaterThan(0);
      expect(stripes.every((r) => r.signal.aborted)).toBe(true);
      expect(server.requests.filter((r) => r.url === HEAD_URL).some((r) => r.signal.aborted)).toBe(
        false,
      );
    } finally {
      vi.useRealTimers();
    }
  });

  // The deadline is for silence, not for the whole answer: a stripe that keeps sending chunks
  // finishes on the stripe URL, however long that takes in total.
  it("keeps a stripe body that sends a chunk before each deadline on the stripe URL", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      // 4 chunks of 512 KiB, 5 s apart: 20 s in total, past the 8 s deadline, never silent for it.
      server.fault = (request) =>
        request.url === STRIPE_URL ? { kind: "slowBody", intervalMs: 5_000 } : undefined;
      const getRetryDelay = vi.fn(() => 0);
      const { source } = makeSource(server, { getRetryDelay });
      let bytes: Uint8Array | undefined;
      const reading = read(source, 0, 20 * MIB).then((read) => (bytes = read));
      await vi.advanceTimersByTimeAsync(30_000);
      expect(bytes, "the read finished within 30 s").toBeDefined();
      await reading;
      expect(same(bytes as Uint8Array, server.bytes.subarray(0, 20 * MIB))).toBe(true);
      expect(getRetryDelay).not.toHaveBeenCalled();
      // The read's two stripe ranges, 16-20 MiB, stayed on the stripe URL: none went to the plan's.
      const inRead = server.requests.filter((r) => r.start >= FIRST_STRIPE && r.start < 20 * MIB);
      expect(inRead.map((r) => r.url)).toEqual([STRIPE_URL, STRIPE_URL]);
    } finally {
      vi.useRealTimers();
    }
  });

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

describe("seek while both read workers are busy", () => {
  type Worker = {
    currentPos: number;
    targetPos: number;
    running: boolean;
    pendingSlices: { start: number }[];
  };
  const orchestratorOf = (source: ParallelRangeSource) =>
    (source as unknown as { _orchestrator: { workers: Worker[]; queuedReads: unknown[] } })
      ._orchestrator;
  const stripeFailed = (source: ParallelRangeSource) =>
    (source as unknown as { stripeFailed: boolean }).stripeFailed;
  /** The first request from `index` on that covers byte `at`. */
  const requestFor = (server: FakeServer, at: number, index = 0) =>
    server.requests.slice(index).find((r) => r.start <= at && r.last >= at);
  /** The worker that holds a pending read starting at `start`, if any. */
  const pendingAt = (source: ParallelRangeSource, start: number) =>
    orchestratorOf(source).workers.find((w) => w.pendingSlices.some((s) => s.start === start));

  /** Advances fake time in `step` ms until `done()` holds, and returns the time that took. */
  async function advanceUntil(done: () => boolean, limit: number, step = 100): Promise<number> {
    let waited = 0;
    while (!done()) {
      expect(waited, `still waiting after ${limit} ms`).toBeLessThan(limit);
      await vi.advanceTimersByTimeAsync(step);
      waited += step;
    }
    return waited;
  }

  /** Advances fake time until `promise` settles, and returns it. */
  async function settled<T>(promise: Promise<T>, limit = 120_000): Promise<T> {
    let done = false;
    promise.then(
      () => (done = true),
      () => (done = true),
    );
    await advanceUntil(() => done, limit, 1_000);
    return promise;
  }

  // A Matroska cluster as ffmpeg muxes it is 5 MiB, and Mediabunny reads it whole: more than one
  // range, so each old worker owes bytes for two range times.
  it("sends the seek's first range at once when each old read still waits for a cluster", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      // 512 KiB a second: one 2 MiB range takes 4 s.
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      source.endWarmHead(); // An earlier seek: the old reads use ordinary ranges.
      const a = read(source, 0, 5 * MIB);
      const b = read(source, 34 * MIB, 39 * MIB);
      await advanceUntil(
        () => orchestratorOf(source).workers.length === 2 && server.inFlight === 4,
        900,
      );
      const old = [...orchestratorOf(source).workers];
      const targets = old.map((w) => w.targetPos);
      expect(server.inFlight).toBe(4);
      source.endWarmHead();
      const seek = read(source, 68 * MIB, 68 * MIB + KIB);
      // Before the first chunk of any old range arrives.
      await advanceUntil(() => requestFor(server, 68 * MIB) !== undefined, 1_000);
      old.forEach((w, i) => expect(w.currentPos).toBeLessThan(targets[i]!));
      // One old worker is retired; the other keeps its ranges.
      const aborted = server.requests.filter((r) => r.last < 68 * MIB && r.signal.aborted);
      expect(aborted.length).toBeGreaterThan(0);
      expect(new Set(aborted.map((r) => r.start < 33 * MIB)).size).toBe(1);
      expect(stripeFailed(source)).toBe(false);
      expect(same(await settled(seek), server.bytes.subarray(68 * MIB, 68 * MIB + KIB))).toBe(true);
      expect(same(await settled(a), server.bytes.subarray(0, 5 * MIB))).toBe(true);
      expect(same(await settled(b), server.bytes.subarray(34 * MIB, 39 * MIB))).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves the seek's ranges alone when a read from the replaced playback contends", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = () => ({ kind: "slowBody", intervalMs: 5_000 });
      const { source } = makeSource(server);
      const a = read(source, 0, 1536 * KIB);
      const b = read(source, 34 * MIB, 34 * MIB + 3 * MIB);
      // Both old workers are past their first chunk, and both reads still wait.
      await advanceUntil(() => {
        const workers = orchestratorOf(source).workers;
        return (
          workers.length === 2 &&
          workers.some((w) => w.currentPos > 0 && w.currentPos < 33 * MIB) &&
          workers.some((w) => w.currentPos > 34 * MIB)
        );
      }, 9_000);
      source.endWarmHead();
      const seek = read(source, 68 * MIB, 68 * MIB + KIB);
      await advanceUntil(() => requestFor(server, 68 * MIB) !== undefined, 1_000);
      const seekWorker = orchestratorOf(source).workers.find((w) => w.targetPos > 67 * MIB)!;
      const seekTarget = seekWorker.targetPos;
      expect(orchestratorOf(source).workers.filter((w) => w.running).length).toBe(2);
      // A read the replaced playback still makes, far from both running workers.
      const stale = read(source, 20 * MIB, 20 * MIB + KIB);
      // The stale read has a worker: the contention has run.
      await advanceUntil(
        () =>
          orchestratorOf(source).workers.some(
            (w) => w.currentPos <= 20 * MIB && w.targetPos > 20 * MIB,
          ),
        1_000,
      );
      expect(seekWorker.targetPos).toBe(seekTarget);
      expect(server.requests.filter((r) => r.last >= 67 * MIB).some((r) => r.signal.aborted)).toBe(
        false,
      );
      expect(same(await settled(seek), server.bytes.subarray(68 * MIB, 68 * MIB + KIB))).toBe(true);
      expect(same(await settled(stale), server.bytes.subarray(20 * MIB, 20 * MIB + KIB))).toBe(
        true,
      );
      expect(same(await settled(a), server.bytes.subarray(0, 1536 * KIB))).toBe(true);
      expect(same(await settled(b), server.bytes.subarray(34 * MIB, 37 * MIB))).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("gives the seek the one slot a retired worker frees", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      source.endWarmHead();
      // The worker near the file's end holds one range; the other holds three and owes more.
      const tail = read(source, SIZE - 1536 * KIB, SIZE);
      const head = read(source, 0, 10 * MIB);
      await advanceUntil(
        () => orchestratorOf(source).workers.length === 2 && server.inFlight === 4,
        900,
      );
      expect(server.inFlight).toBe(4);
      source.endWarmHead();
      const seek = read(source, 40 * MIB, 40 * MIB + KIB);
      // The tail's worker owes fewer bytes, so it is retired; its one slot goes to the seek.
      await advanceUntil(() => requestFor(server, 40 * MIB) !== undefined, 1_000);
      expect(same(await settled(seek), server.bytes.subarray(40 * MIB, 40 * MIB + KIB))).toBe(true);
      expect(same(await settled(head), server.bytes.subarray(0, 10 * MIB))).toBe(true);
      expect(same(await settled(tail), server.bytes.subarray(SIZE - 1536 * KIB, SIZE))).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("frees a slot for the seek from an old worker's ranges that hold no awaited byte", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      source.endWarmHead();
      // One worker holds all 4 slots and owes 1 MiB of its 8; the other owes 1 KiB and holds none.
      const a = read(source, 0, MIB);
      await advanceUntil(() => server.requests.length === 4, 400);
      const b = read(source, 34 * MIB, 34 * MIB + KIB);
      await advanceUntil(() => pendingAt(source, 34 * MIB) !== undefined, 400);
      expect(server.requests.map((r) => r.start / MIB)).toEqual([0, 2, 4, 6]);
      source.endWarmHead();
      const seek = read(source, 68 * MIB, 68 * MIB + KIB);
      // The idle worker is retired, which frees no slot; the ranges past the 1 MiB free three.
      await advanceUntil(() => requestFor(server, 68 * MIB) !== undefined, 1_000);
      expect(server.requests.find((r) => r.start === 6 * MIB)?.signal.aborted).toBe(true);
      const worker = orchestratorOf(source).workers.find((w) => w.targetPos === MIB)!;
      expect(same(await settled(seek), server.bytes.subarray(68 * MIB, 68 * MIB + KIB))).toBe(true);
      expect(same(await settled(a), server.bytes.subarray(0, MIB))).toBe(true);
      // The kept range 0-2 MiB is cut once the 1 MiB arrives.
      await advanceUntil(() => !worker.running, 5_000);
      expect(worker.currentPos).toBe(MIB);
      expect(same(await settled(b), server.bytes.subarray(34 * MIB, 34 * MIB + KIB))).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps an old worker that a read after the seek now waits on", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      source.endWarmHead();
      const a = read(source, 0, KIB);
      const b = read(source, 34 * MIB, 44 * MIB);
      // Both reads wait on two different workers.
      await advanceUntil(
        () =>
          pendingAt(source, 0) !== undefined &&
          pendingAt(source, 34 * MIB) !== undefined &&
          pendingAt(source, 0) !== pendingAt(source, 34 * MIB),
        400,
      );
      source.endWarmHead();
      // A short skip lands ahead of the first old worker, inside its region: that worker serves it.
      const skip = read(source, 3 * MIB, 3 * MIB + KIB);
      await advanceUntil(
        () =>
          pendingAt(source, 3 * MIB) !== undefined &&
          pendingAt(source, 3 * MIB) === pendingAt(source, 0),
        400,
      );
      // A second read after the seek needs a worker. The first old worker owes fewer bytes than the
      // second, but the skip waits on it, so the second is retired.
      const other = read(source, 68 * MIB, 68 * MIB + KIB);
      await advanceUntil(() => requestFor(server, 68 * MIB) !== undefined, 1_000);
      expect(server.requests.filter((r) => r.last < 33 * MIB).some((r) => r.signal.aborted)).toBe(
        false,
      );
      // The skip's bytes are in the first old worker's second range: 8 s at 512 KiB a second.
      expect(same(await settled(skip, 10_000), server.bytes.subarray(3 * MIB, 3 * MIB + KIB))).toBe(
        true,
      );
      expect(same(await settled(other), server.bytes.subarray(68 * MIB, 68 * MIB + KIB))).toBe(
        true,
      );
      expect(same(await settled(a), server.bytes.subarray(0, KIB))).toBe(true);
      expect(same(await settled(b), server.bytes.subarray(34 * MIB, 44 * MIB))).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("sends the seek's first range at once and starts no read-ahead for reads queued before it", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(200 * MIB));
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      await settled(read(source, 0, KIB));
      await settled(read(source, 34 * MIB, 34 * MIB + KIB));
      const old = [...orchestratorOf(source).workers];
      const targets = old.map((w) => w.targetPos);
      expect(old.length === 2 && old.every((w) => w.running)).toBe(true);
      // Lookups of the replaced playback queue while both workers run.
      const stale = [
        read(source, 108 * MIB, 108 * MIB + KIB),
        read(source, 148 * MIB, 148 * MIB + KIB),
      ];
      expect(orchestratorOf(source).queuedReads.length).toBe(2);
      source.endWarmHead();
      const index = server.requests.length;
      const seek = read(source, 68 * MIB, 68 * MIB + KIB);
      // Within 100 ms a request covers the seek, and no read waits on either old worker: both
      // stop, their read-ahead dropped.
      await advanceUntil(
        () => requestFor(server, 68 * MIB, index) !== undefined && old.every((w) => !w.running),
        100,
      );
      old.forEach((w, i) => expect(w.currentPos).toBeLessThan(targets[i]!));
      expect(stripeFailed(source)).toBe(false);
      expect(same(await settled(seek), server.bytes.subarray(68 * MIB, 68 * MIB + KIB))).toBe(true);
      for (const [i, at] of [108 * MIB, 148 * MIB].entries()) {
        expect(same(await settled(stale[i]!), server.bytes.subarray(at, at + KIB))).toBe(true);
        // The queued read gets the bytes it waits for, and none of its read-ahead.
        const near = server.requests.filter((r) => r.start >= at - MIB && r.start < at + 32 * MIB);
        expect(Math.max(...near.map((r) => r.last))).toBeLessThan(at + KIB);
      }
    } finally {
      vi.useRealTimers();
    }
  });

  it("delivers the right bytes when a read grows a trimmed run past its cut ranges", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      source.endWarmHead();
      const a = read(source, 0, MIB);
      await advanceUntil(() => server.requests.length === 4, 400);
      const b = read(source, 34 * MIB, 34 * MIB + KIB);
      await advanceUntil(() => pendingAt(source, 34 * MIB) !== undefined, 400);
      expect(server.requests.map((r) => r.start / MIB)).toEqual([0, 2, 4, 6]);
      source.endWarmHead();
      const seek = read(source, 68 * MIB, 68 * MIB + KIB);
      await advanceUntil(() => requestFor(server, 68 * MIB) !== undefined, 1_000);
      // A read after the seek lands on the trimmed run, just past its owed end.
      const next = read(source, MIB, 3 * MIB);
      expect(same(await settled(next), server.bytes.subarray(MIB, 3 * MIB))).toBe(true);
      expect(same(await settled(seek), server.bytes.subarray(68 * MIB, 68 * MIB + KIB))).toBe(true);
      expect(same(await settled(a), server.bytes.subarray(0, MIB))).toBe(true);
      expect(same(await settled(b), server.bytes.subarray(34 * MIB, 34 * MIB + KIB))).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
