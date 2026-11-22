import { afterEach, describe, expect, it, vi } from "vitest";
import type { ParallelRangeSource } from "./parallelRangeSource";
import {
  disposeSources,
  FakeServer,
  KIB,
  makeSource,
  MIB,
  pattern,
  read,
  same,
  SIZE,
} from "./parallelRangeSource.test-support";

afterEach(disposeSources);

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

  // Mediabunny aligns a read's start down to 64 KiB and caps its end at the file's size, so a
  // seek near the end of the file asks for the very hole a read queued before the seek waits on.
  it("retires an old worker for a seek whose first read asks for a queued read's hole", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      source.endWarmHead();
      const a = read(source, 0, 5 * MIB);
      const b = read(source, 34 * MIB, 39 * MIB);
      await advanceUntil(
        () => orchestratorOf(source).workers.length === 2 && server.inFlight === 4,
        900,
      );
      const tail = SIZE - MIB;
      const queued = read(source, tail, SIZE);
      expect(orchestratorOf(source).queuedReads.length).toBe(1);
      const before = server.requests.length;
      source.endWarmHead();
      const seek = read(source, tail + 4 * KIB, tail + 5 * KIB);
      await advanceUntil(() => requestFor(server, tail + 4 * KIB, before) !== undefined, 1_000);
      expect(same(await settled(seek), server.bytes.subarray(tail + 4 * KIB, tail + 5 * KIB))).toBe(
        true,
      );
      expect(same(await settled(queued), server.bytes.subarray(tail, SIZE))).toBe(true);
      expect(same(await settled(a), server.bytes.subarray(0, 5 * MIB))).toBe(true);
      expect(same(await settled(b), server.bytes.subarray(34 * MIB, 39 * MIB))).toBe(true);
      // The seek's worker took the queued read with it: the file's last byte is fetched once.
      expect(server.requests.filter((r) => r.last === SIZE - 1).length).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  // A read queued before a seek starts on a new worker once an old one stops. That worker serves
  // the replaced playback, so the seek's next read retires it and does not wait for it.
  it("retires a worker that restarted a read queued before the seek", async () => {
    vi.useFakeTimers();
    try {
      const server = new FakeServer(pattern(SIZE));
      server.fault = () => ({ kind: "slowBody", intervalMs: 1_000 });
      const { source } = makeSource(server);
      source.endWarmHead();
      const a = read(source, 0, 5 * MIB);
      const b = read(source, 34 * MIB, 39 * MIB);
      await advanceUntil(
        () => orchestratorOf(source).workers.length === 2 && server.inFlight === 4,
        900,
      );
      const queued = read(source, 67 * MIB, SIZE);
      expect(orchestratorOf(source).queuedReads.length).toBe(1);
      source.endWarmHead();
      const first = read(source, 33 * MIB, 33 * MIB + KIB);
      // The old worker that was kept finishes what it owes, and the queued read takes its place.
      await advanceUntil(() => requestFor(server, 67 * MIB) !== undefined, 60_000);
      const before = server.requests.length;
      const second = read(source, 10 * MIB, 10 * MIB + KIB);
      await advanceUntil(() => requestFor(server, 10 * MIB, before) !== undefined, 1_000);
      expect(same(await settled(second), server.bytes.subarray(10 * MIB, 10 * MIB + KIB))).toBe(
        true,
      );
      expect(same(await settled(first), server.bytes.subarray(33 * MIB, 33 * MIB + KIB))).toBe(
        true,
      );
      expect(same(await settled(queued), server.bytes.subarray(67 * MIB, SIZE))).toBe(true);
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
