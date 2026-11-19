import { ReadableStream } from "node:stream/web";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  disposeSources,
  FakeServer,
  HEAD_URL,
  KIB,
  makeSource,
  MIB,
  pattern,
  read,
  same,
  SIZE,
  STRIPE_URL,
  tick,
  type Fault,
} from "./parallelRangeSource.test-support";

afterEach(disposeSources);

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
    // The source passes its one run to every request of every worker, the head included, so
    // the page's meter counts the striped read as one request.
    expect(runs()).toBe(1);
    expect(new Set(server.requests.map((r) => r.run)).size).toBe(1);
    expect(server.requests[0]?.run).toBeDefined();
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

  // HTTP/2 and HTTP/3 put every request to an origin on one connection, so parallel ranges there
  // add requests and no connections. The browser says which protocol a completed response used.
  it.each([
    ["h2", false],
    ["h3", false],
    ["http/1.1", true],
    ["", true],
  ])("after the browser reports protocol %j, keeps striping: %s", async (protocol, stripes) => {
    vi.stubGlobal("ReadableStream", ReadableStream);
    // The VM test context's `performance` has no resource timing.
    const timing = performance as { getEntriesByName?: unknown };
    timing.getEntriesByName = () => (protocol === "" ? [] : [{ nextHopProtocol: protocol }]);
    try {
      const server = new FakeServer(pattern(SIZE));
      const { source } = makeSource(server);
      expect(same(await read(source, 0, 20 * MIB), server.bytes.subarray(0, 20 * MIB))).toBe(true);
      await vi.waitFor(() => expect(server.inFlight).toBe(0));
      const stripeBefore = server.requests.filter((r) => r.url === STRIPE_URL).length;
      expect(stripeBefore).toBeGreaterThan(0);
      source.endWarmHead();
      expect(
        same(await read(source, 60 * MIB, 61 * MIB), server.bytes.subarray(60 * MIB, 61 * MIB)),
      ).toBe(true);
      const stripeAfter = server.requests.filter((r) => r.url === STRIPE_URL).length;
      expect(stripeAfter > stripeBefore).toBe(stripes);
      // Over one connection the stock reader keeps its own, shorter read-ahead.
      const targets = (
        source as unknown as {
          _orchestrator: { workers: { startPos: number; targetPos: number }[] };
        }
      )._orchestrator.workers.map((w) => w.targetPos - w.startPos);
      expect(targets.some((bytes) => bytes >= 32 * MIB)).toBe(stripes);
    } finally {
      delete timing.getEntriesByName;
      vi.unstubAllGlobals();
    }
  });

  // The range that tells the source its URL is multiplexed can also be the one that takes its
  // worker to the end of the file. That worker has nothing left for the stock reader.
  it("asks for no bytes past the end of the file when it hands over at a worker's target", async () => {
    vi.stubGlobal("ReadableStream", ReadableStream);
    const timing = performance as { getEntriesByName?: unknown };
    timing.getEntriesByName = () => [{ nextHopProtocol: "h2" }];
    try {
      const server = new FakeServer(pattern(SIZE));
      const { source } = makeSource(server, {}, HEAD_URL);
      expect(
        same(await read(source, SIZE - MIB, SIZE), server.bytes.subarray(SIZE - MIB, SIZE)),
      ).toBe(true);
      await vi.waitFor(() => expect(server.inFlight).toBe(0));
      expect(server.requests.some((r) => r.start >= SIZE)).toBe(false);
    } finally {
      delete timing.getEntriesByName;
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

  // A proxy node and the page's own origin have one URL: the server names the plan's own URL as
  // the stripe URL. There is then no second origin to give up on, so a transport failure is
  // retried on that URL like any read's, and the ranges stay parallel.
  it("reads every range from the one URL and retries a transport failure there", async () => {
    const server = new FakeServer(pattern(SIZE));
    server.fault = (request, index) =>
      index === 3 && request.start >= FIRST_STRIPE ? { kind: "fetchError" } : undefined;
    const getRetryDelay = vi.fn(() => 0);
    const { source } = makeSource(server, { getRetryDelay }, HEAD_URL);
    expect(same(await read(source, 0, 30 * MIB), server.bytes.subarray(0, 30 * MIB))).toBe(true);
    await vi.waitFor(() => expect(server.inFlight).toBe(0));
    expect(server.requests.every((r) => r.url === HEAD_URL)).toBe(true);
    expect(getRetryDelay).toHaveBeenCalledTimes(1);
    expect(server.maxInFlight).toBe(4);
    // Still 2 MiB ranges after the failure: the failed one is asked again, and later ones follow.
    const failed = server.requests[3]!;
    expect(server.requests.filter((r) => r.start === failed.start).length).toBe(2);
    expect(server.requests.some((r) => r.start > failed.start && r.last - r.start < 2 * MIB)).toBe(
      true,
    );
  });

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
