/**
 * A Mediabunny source that reads a file as closed ranges, several at a time.
 *
 * One HTTP connection to a long, lossy link moves a fraction of what the link
 * can carry. This source keeps up to 4 two-MiB ranges in flight per source,
 * across both read workers, and keeps the read-ahead at least 32 MiB. The first
 * 16 MiB of each read worker, the warm head, comes from the plan's own URL
 * until the page seeks, while the stripes warm their connections. At most 3 other
 * ranges use the stripe URL at once; a fourth goes to the plan's own URL, whose
 * connection is already open, so no range waits on a cold fourth stripe connection. The first
 * answer other than a readable 206 that matches its request, or transport failure (fetch, body
 * read, early end, a body longer than its range, or no byte for `STRIPE_INACTIVITY_MS`), from the
 * stripe URL ends its use: the rest of that range and every later range go to the plan's own URL,
 * with no retry spent on the stripe URL. A plan URL whose first answer is a whole-file `200`
 * ignores `Range`: the source then becomes the stock `UrlSource`, which reads the file
 * sequentially. When the server names the plan's own URL as the stripe URL, as it does for a
 * proxy node and for the page's own origin, there is one URL: every range uses it, up to 4 at
 * once, with the retries of any read from the plan's URL. HTTP/2 and HTTP/3 put every request to
 * an origin on one connection, so when the browser reports that the striped URL answered over
 * either, the source also becomes the stock `UrlSource`.
 *
 * At a page seek (`endWarmHead()`) the source records the reads Mediabunny waits for, and cuts
 * each queued read to the bytes those reads wait for. When a new read then finds every read worker
 * running, each older run (one that started before the latest seek and has no read made after it
 * pending) is cut to the bytes its reads wait for, and its ranges that hold none of them are
 * cancelled. The older run that owes the fewest bytes is retired, its unmet bytes go back on the
 * queue, and the new read takes its place. Runs for the latest seek take free slots first. Trimming
 * alone is not enough: two owed 5 MiB cluster reads still held a seek's first request for 7.0 s at
 * 512 KiB/s per range. A read the replaced playback makes after the seek still counts as new. A run
 * that serves the new playback between its reads counts as older, so a later read can retire it and
 * its read-ahead is fetched again; keeping it delayed the next new read instead.
 *
 * It subclasses `UrlSource` and overrides its private `_runWorker`, so
 * Mediabunny keeps its read orchestrator, its cache, its read workers and its
 * prefetch profile. Besides the way the bytes arrive, at a seek the source changes worker
 * admission, worker targets and the queued reads. `mediabunnyInternals.ts` lists the Mediabunny
 * internals it relies on and why `mediabunny` is pinned.
 */
import type { UrlSourceOptions } from "mediabunny";
import type { createMeteredFetch, MediaRun } from "./mediaFetch";
import { InternalUrlSource, type PendingSlice, type ReadWorker } from "./mediabunnyInternals";
import { inactivitySignal, responseProtocol } from "./rangeRequest";

/** The size of one range. The last range of a stretch is cut at the worker's target. */
const RANGE_BYTES = 2 * 2 ** 20;
/** The most ranges in flight across all read workers of one source, the warm head included. */
const MAX_RANGES_IN_FLIGHT = 4;
/** The most ranges one read worker holds, delivered or not. */
const MAX_RANGES_PER_WORKER = 4;
/** Every prefetch-profile call ends no sooner than this far past the read. */
const READ_AHEAD_BYTES = 32 * 2 ** 20;
/** The warm head: what each read worker reads first from the plan's own URL. */
const WARM_HEAD_BYTES = 16 * 2 ** 20;
/** The most non-head ranges in flight on the stripe URL; the next one goes to the plan's own URL. */
const MAX_STRIPE_URL_RANGES = MAX_RANGES_IN_FLIGHT - 1;

/**
 * How long a stripe request may go without a byte, from the request until its first chunk and
 * between chunks, before it counts as failed. A cold connection on a lossy link answers within a
 * few seconds, so this leaves the plan's URL 12 s of the page's 20 s stall timeout
 * (`STALL_TIMEOUT_MS` in `EnginePlayer.ts`) to take the read over.
 */
const STRIPE_INACTIVITY_MS = 8_000;

/** How many completed responses the source asks the browser about the protocol of. */
const PROTOCOL_CHECKS = 8;

/** A `Content-Range` that names the range and the file's size. */
const CONTENT_RANGE = /^bytes (\d+)-(\d+)\/(\d+)$/;

type MeteredFetch = Pick<ReturnType<typeof createMeteredFetch>, "fetch" | "newRun">;

/** One closed range, read into `chunks` for delivery in file order. */
interface StripeRange {
  start: number;
  end: number;
  head: boolean;
  /** True when the range uses the stripe URL; false for a head range and for the plan-URL lane. */
  stripe: boolean;
  chunks: Uint8Array[];
  /**
   * Aborts this range alone, as a trim does for a range no read waits on; the worker's abort
   * aborts it too. `readRange` reads this signal, so a cancel is an abort and never counts as a
   * stripe failure.
   */
  controller: AbortController;
  done: boolean;
  failure: { error: unknown } | null;
}

/** A worker run's place in the slot queue: a run that serves a later seek goes first. */
interface SlotToken {
  /** The seek count the run serves now. */
  status: () => number;
}

/** Resolves after `ms`, or at once when `signal` aborts. */
function wait(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", done, { once: true });
  });
}

export class ParallelRangeSource extends InternalUrlSource {
  private readonly stripeUrl: string;
  /**
   * False when the stripe URL is the plan's own URL, as it is for a proxy node and for the
   * page's own origin: the ranges then have one URL, so no range is a stripe range. A transport
   * failure is retried there and a refusal fails the read, as for any read from the plan's URL.
   */
  private readonly twoOrigins: boolean;
  private readonly metered: MeteredFetch;
  /**
   * Every range, retry and head request of this source counts into one run, so the page's
   * meter sees the striped read as one long request. A stock read, which a plan URL that
   * ignores `Range` falls back to, stays a request of its own.
   */
  private readonly run: MediaRun;
  /** True while Mediabunny's `read()` runs: a `createWorker` call then is for a new read. */
  private reading = false;
  /** The ranges in flight across the workers, the workers queued for a slot, and each worker's wake function. */
  private readonly slots = { active: 0, waiting: [] as SlotToken[], wakers: new Set<() => void>() };
  private headOpen = true;
  /** The non-head ranges in flight on the stripe URL, across the workers. */
  private stripesInFlight = 0;
  /**
   * True until worker 0's warm head range ends, a second worker starts, or `endWarmHead()` is
   * called. It never turns true again.
   */
  private reserveOpen = true;
  private workersStarted = 0;
  private readonly headed = new WeakSet<ReadWorker>();
  /** The first response's size and modification time; every later response must match. */
  private firstTotal: number | null = null;
  private firstModified: string | null = null;
  /**
   * Set by the first refusal or transport failure on the stripe URL; every range then uses the
   * plan's URL.
   */
  private stripeFailed = false;
  /**
   * Counts `endWarmHead()` calls: one for each engine start after the first, which is a page seek
   * or a decoder restart. A decoder restart replaces the old generation's reads too, so the seek
   * rules below fit it.
   */
  private seeks = 0;
  /**
   * The reads Mediabunny waited for at some `endWarmHead()`, by object identity. A run's status
   * is `seeks` while a read outside this set waits on its worker, else the count when the run
   * started. Queued reads are recorded too: one that Mediabunny later gives to a worker that
   * started before the seek does not raise that run.
   */
  private readonly readsAtSeeks = new WeakSet<PendingSlice>();
  /** Each running worker's run: its status, as a slot token reads it, and how to trim it. */
  private readonly runs = new Map<ReadWorker, { status: () => number; trim: () => void }>();
  /**
   * Set when the plan's URL answered its first request with a whole-file `200`: the source is
   * the stock `UrlSource` from then on, and every worker runs the parent's `_runWorker`.
   */
  private ignoresRange = false;
  /**
   * Set when the browser reports that the striped URL answers over HTTP/2 or HTTP/3. Those put
   * every request on one connection, so the ranges gain none: the source is the stock
   * `UrlSource` from then on, as for a plan URL that ignores `Range`.
   */
  private multiplexed = false;
  private protocolChecks = 0;

  /**
   * `headUrl` is the plan's own stream URL, which carries the warm head and the
   * range that finds the stripe URL full; `stripeUrl` carries up to 3 other
   * ranges at once. When the two are the same URL, every range uses it. `metered.fetch` counts each response into the run it is given.
   */
  constructor(
    headUrl: string,
    stripeUrl: string,
    options: UrlSourceOptions,
    metered: MeteredFetch,
  ) {
    // The stock reads, which the source falls back to, are metered too.
    super(headUrl, { ...options, fetchFn: metered.fetch });
    this.stripeUrl = stripeUrl;
    this.twoOrigins = stripeUrl !== headUrl;
    this.metered = metered;
    this.run = metered.newRun();
    const orchestrator = this._orchestrator;
    const profile = orchestrator.options.prefetchProfile;
    orchestrator.options.prefetchProfile = (start, end, workers) => {
      const range = profile(start, end, workers);
      // The stock reader keeps its own read-ahead: over one connection, a 32 MiB target would
      // hold a seek back, and nothing here retires a stock worker.
      if (this.ignoresRange || this.multiplexed) return range;
      return { start: range.start, end: Math.max(range.end, start + READ_AHEAD_BYTES) };
    };
    // `read()` is synchronous and calls `createWorker` for a new read before it looks at the
    // queue. Mediabunny's only other call restarts a queued read after a worker stops, outside
    // `read()`. A new read can ask for the same hole as a queued one, so the hole cannot tell
    // the two apart.
    const read = orchestrator.read.bind(orchestrator);
    orchestrator.read = (...args) => {
      this.reading = true;
      try {
        return read(...args);
      } finally {
        this.reading = false;
      }
    };
    const createWorker = orchestrator.createWorker.bind(orchestrator);
    orchestrator.createWorker = (startPos, targetPos, strictTarget) => {
      const created = createWorker(startPos, targetPos, strictTarget);
      if (created !== null) return created;
      const older = [...this.runs].filter(([, run]) => run.status() < this.seeks);
      if (older.length === 0) return null;
      for (const [, run] of older) run.trim();
      // A queue restart is no new read: it waits its turn.
      if (!this.reading) return null;
      // A new read: retire the older worker that owes the fewest bytes and take its place.
      let victim = older[0]![0];
      for (const [worker] of older) {
        if (worker.targetPos - worker.currentPos < victim.targetPos - victim.currentPos) {
          victim = worker;
        }
      }
      this.retire(victim);
      return createWorker(startPos, targetPos, strictTarget);
    };
  }

  /** From now on no range, of any worker, is a head range, and the seek is recorded. */
  endWarmHead(): void {
    const orchestrator = this._orchestrator;
    for (const { pendingSlices } of [...orchestrator.workers, ...orchestrator.queuedReads]) {
      for (const slice of pendingSlices) this.readsAtSeeks.add(slice);
    }
    this.seeks++;
    this.trimQueuedReads();
    this.headOpen = false;
    this.reserveOpen = false;
    this.wakeWorkers();
  }

  /** Takes a slot for the worker named by `token`, or queues the worker and returns false. */
  private takeSlot(token: SlotToken): boolean {
    const slots = this.slots;
    if (slots.active >= this.slotCap() && slots.active < MAX_RANGES_IN_FLIGHT) {
      // Held back by the warm-head reserve, not by a full pool: the worker takes no place in
      // the queue, so the worker that arrives next takes the reserved slot at once.
      return false;
    }
    this.joinQueue(token);
    // A run that serves a later seek goes first. A run's status can rise while it waits, so the
    // order is taken again on each call; the sort is stable, so arrival order holds within a seek.
    slots.waiting.sort((a, b) => b.status() - a.status());
    if (slots.active < MAX_RANGES_IN_FLIGHT && slots.waiting[0] === token) {
      slots.waiting.shift();
      slots.active++;
      // The worker now at the head may be asleep while a slot is still free: wake it.
      if (slots.waiting.length > 0 && slots.active < MAX_RANGES_IN_FLIGHT) this.wakeWorkers();
      return true;
    }
    return false;
  }

  /**
   * While worker 0's warm head range is in flight and no second worker has started, worker 0
   * leaves one slot free. The MKV cue read comes from a second worker, and it can only be asked
   * for once the head's first bytes arrive, after worker 0's read-ahead would otherwise have
   * taken every slot.
   */
  private slotCap(): number {
    return this.reserveOpen ? MAX_RANGES_IN_FLIGHT - 1 : MAX_RANGES_IN_FLIGHT;
  }

  private joinQueue(token: SlotToken): void {
    if (!this.slots.waiting.includes(token)) this.slots.waiting.push(token);
  }

  private leaveQueue(token: SlotToken): void {
    const index = this.slots.waiting.indexOf(token);
    if (index >= 0) this.slots.waiting.splice(index, 1);
  }

  /** Frees a slot and wakes every worker, so the one at the head of the queue takes it. */
  private releaseSlot(): void {
    this.slots.active--;
    this.wakeWorkers();
  }

  private wakeWorkers(): void {
    for (const wake of this.slots.wakers) wake();
  }

  /**
   * The seek a run serves: the current count once a read made after the latest seek waits on the
   * run's worker, else the count when the run started.
   */
  private runStatus(worker: ReadWorker, started: number): number {
    for (const slice of worker.pendingSlices) {
      if (!this.readsAtSeeks.has(slice)) return this.seeks;
    }
    return started;
  }

  /** The end of the bytes that reads still wait for in `[start, end)`; `start` when none. */
  private static owedEnd(start: number, end: number, slices: PendingSlice[]): number {
    let bound = start;
    for (const slice of slices) {
      bound = Math.max(bound, Math.min(slice.start + slice.bytes.length, end));
    }
    return bound;
  }

  /**
   * At a page seek, cuts each queued read to the bytes its reads wait for and drops a queued read
   * that nobody waits for, so no read-ahead from before the seek starts after it.
   */
  private trimQueuedReads(): void {
    const queue = this._orchestrator.queuedReads;
    for (let i = queue.length - 1; i >= 0; i--) {
      const read = queue[i]!;
      const end = ParallelRangeSource.owedEnd(read.hole.start, read.hole.end, read.pendingSlices);
      if (end > read.hole.start) {
        read.hole.end = end;
        read.strictTarget = true;
      } else {
        queue.splice(i, 1);
      }
    }
  }

  /**
   * Retires `worker` from the orchestrator: its ranges are aborted, and the bytes its reads still
   * wait for go back on the queue, which keeps Mediabunny's invariants: sorted by `hole.start`,
   * with overlapping entries merged. The worker's reads come off it first, because
   * `signalWorkerStoppedRunning` keeps an aborted worker's reads.
   */
  private retire(worker: ReadWorker): void {
    const orchestrator = this._orchestrator;
    const slices = worker.pendingSlices.splice(0);
    const end = ParallelRangeSource.owedEnd(worker.currentPos, worker.targetPos, slices);
    worker.aborted = true;
    this.runs.delete(worker);
    orchestrator.workers.splice(orchestrator.workers.indexOf(worker), 1);
    this._abortControllers.get(worker)?.abort();
    this._abortControllers.delete(worker);
    // A run that owes nothing has nothing to queue: an empty hole would trip Mediabunny's check
    // that a worker's current position is below its target.
    if (end <= worker.currentPos) return;
    const queue = orchestrator.queuedReads;
    queue.push({
      hole: { start: worker.currentPos, end },
      strictTarget: true,
      pendingSlices: slices,
      age: orchestrator.nextAge++,
    });
    queue.sort((a, b) => a.hole.start - b.hole.start);
    for (let i = 0; i + 1 < queue.length; ) {
      const read = queue[i]!;
      const next = queue[i + 1]!;
      if (next.hole.start > read.hole.end) {
        i++;
        continue;
      }
      read.hole.end = Math.max(read.hole.end, next.hole.end);
      read.pendingSlices.push(...next.pendingSlices);
      read.strictTarget &&= next.strictTarget;
      read.age = Math.min(read.age, next.age);
      queue.splice(i + 1, 1);
    }
  }

  /**
   * Runs one read worker: keeps its ranges in flight and hands their bytes to
   * the orchestrator in file order. Called by Mediabunny's orchestrator in
   * place of `UrlSource._runWorker`.
   */
  override async _runWorker(worker: ReadWorker): Promise<void> {
    if (this.ignoresRange || this.multiplexed) return super._runWorker(worker);
    const orchestrator = this._orchestrator;
    if (worker.aborted) {
      orchestrator.signalWorkerStoppedRunning(worker);
      return;
    }
    const abortController = new AbortController();
    this._abortControllers.set(worker, abortController);
    // A worker that took a retired worker's place can cover a read still on the queue, which
    // Mediabunny would otherwise start again once a worker stops and fetch the same bytes twice.
    // Mediabunny does the same for a read it adds to a running worker.
    orchestrator.checkQueuedReadsAgainstWorker(worker);

    const ranges: StripeRange[] = []; // in file order; the first is the next to deliver
    let nextStart = worker.currentPos;
    let wake: (() => void) | null = null;
    const notify = () => {
      const resolve = wake;
      wake = null;
      resolve?.();
    };
    // A run that starts with only reads recorded at a seek restarts a read queued before that
    // seek: it serves the replaced playback, so it counts as older from its start.
    const restartsOldRead =
      worker.pendingSlices.length > 0 &&
      worker.pendingSlices.every((slice) => this.readsAtSeeks.has(slice));
    const started = restartsOldRead ? this.seeks - 1 : this.seeks;
    const status = () => this.runStatus(worker, started);
    const token: SlotToken = { status };
    this.slots.wakers.add(notify);
    // A second worker ends the reserve and takes the slot the first worker held back.
    if (++this.workersStarted > 1) this.reserveOpen = false;
    // A worker abort aborts every range it still holds, so each range reads its own signal.
    abortController.signal.addEventListener(
      "abort",
      () => {
        for (const range of ranges) range.controller.abort();
        notify();
      },
      { once: true },
    );
    let trimmed = false;
    this.runs.set(worker, {
      status,
      trim: () => {
        worker.targetPos = ParallelRangeSource.owedEnd(
          worker.currentPos,
          worker.targetPos,
          worker.pendingSlices,
        );
        trimmed = true;
        // Ranges that hold no byte a read waits for are aborted now, which frees their slots.
        for (let i = ranges.length - 1; i >= 0; i--) {
          const range = ranges[i]!;
          if (Math.max(range.start, worker.currentPos) < Math.min(range.end, worker.targetPos)) {
            continue;
          }
          range.controller.abort();
          ranges.splice(i, 1);
        }
        // The cut ranges leave no gap: the next range starts where the last kept one ends.
        nextStart = ranges[ranges.length - 1]?.end ?? worker.currentPos;
        notify();
      },
    });
    const hasRoom = () => ranges.length < MAX_RANGES_PER_WORKER && nextStart < worker.targetPos;
    // A range is wanted while this worker has room and the file size is known.
    // Until then one range per source, in flight, finds the size.
    const wants = () =>
      hasRoom() &&
      (orchestrator.fileSize !== null || (ranges.length === 0 && this.slots.active === 0));
    try {
      while (true) {
        if (this.ignoresRange || this.multiplexed) break;
        if (worker.aborted) {
          orchestrator.signalWorkerStoppedRunning(worker);
          return;
        }
        while (wants() && this.takeSlot(token)) {
          const head = this.headOpen && !this.headed.has(worker);
          if (head) this.headed.add(worker);
          // At most three ranges use the stripe URL; the next one rides the plan's own URL, whose
          // connection is already open.
          const stripe = this.twoOrigins && !head && this.stripesInFlight < MAX_STRIPE_URL_RANGES;
          if (stripe) this.stripesInFlight++;
          const range: StripeRange = {
            start: nextStart,
            end: Math.min(nextStart + (head ? WARM_HEAD_BYTES : RANGE_BYTES), worker.targetPos),
            head,
            stripe,
            chunks: [],
            controller: new AbortController(),
            done: false,
            failure: null,
          };
          nextStart = range.end;
          ranges.push(range);
          void this.readRange(range, this.run, range.controller.signal, notify);
        }
        // A worker with nothing in flight that waits for the file size keeps its place in
        // the queue, so it takes the first free slot once the size is known, ahead of the
        // read-ahead of a worker that already has a range.
        if (hasRoom() && ranges.length === 0) this.joinQueue(token);
        else if (!wants()) this.leaveQueue(token);
        const first = ranges[0];
        if (!first) {
          if (hasRoom()) {
            // Bytes remain below the target, and the other worker holds every slot or the only
            // size-finding range: wait for it.
            await new Promise<void>((resolve) => {
              wake = resolve;
            });
            continue;
          }
          // Nothing in flight and nothing left below the target.
          orchestrator.signalWorkerStoppedRunning(worker);
          return;
        }
        let delivered = false;
        while (first.chunks.length > 0 && !worker.aborted) {
          const chunk = first.chunks.shift() as Uint8Array;
          this._dispatchRead(worker.currentPos, worker.currentPos + chunk.length);
          orchestrator.supplyWorkerData(worker, chunk);
          delivered = true;
        }
        if (worker.aborted) continue;
        // A trimmed run stops once it has delivered what the reads waited for; its `finally`
        // aborts the rest of any kept range.
        if (trimmed && worker.currentPos >= worker.targetPos) {
          orchestrator.signalWorkerStoppedRunning(worker);
          return;
        }
        if (first.failure) throw first.failure.error;
        if (first.done) {
          ranges.shift();
          continue;
        }
        // The target can have grown while the chunks were delivered.
        if (delivered) continue;
        await new Promise<void>((resolve) => {
          wake = resolve;
        });
      }
    } finally {
      if (ranges.length > 0) abortController.abort();
      this.runs.delete(worker);
      this.leaveQueue(token);
      this.slots.wakers.delete(notify);
      this.wakeWorkers();
    }
    // The plan's URL ignores `Range`, or the striped URL shares one connection: the stock source
    // reads the file from here on. It gets the bytes this worker's reads still wait for, and not
    // the striped read-ahead beyond them. A worker with none left has nothing to read, and the
    // stock source would ask for the bytes after its target.
    if (!worker.aborted) {
      worker.targetPos = ParallelRangeSource.owedEnd(
        worker.currentPos,
        worker.targetPos,
        worker.pendingSlices,
      );
      if (worker.currentPos >= worker.targetPos) {
        orchestrator.signalWorkerStoppedRunning(worker);
        return;
      }
    }
    return super._runWorker(worker);
  }

  /**
   * Reads `[range.start, range.end)` into `range.chunks`. A transport failure
   * (a fetch error, a body-read error, a body that ends early) retries the rest
   * of the range through `getRetryDelay`, counted from 1 since the range last
   * received bytes, and fails the range when the delay is null. A body from the
   * plan's URL that ends early after sending bytes is asked for again with no
   * retry counted. A body-read error after the response's last expected byte ends the
   * response as complete, with no retry. An answer that is not a readable 206 matching the request, a
   * transport failure, a body longer than its range, or a stripe request silent for
   * `STRIPE_INACTIVITY_MS`, from
   * the stripe URL is no retry: it switches the source to the plan's URL, which
   * then serves the rest of the range. A response from the plan's URL that breaks
   * the byte checks fails the range at once. Any failure ends up in
   * `range.failure`; the range's slot is freed at its end.
   */
  private async readRange(
    range: StripeRange,
    run: MediaRun,
    signal: AbortSignal,
    notify: () => void,
  ): Promise<void> {
    let failures = 0;
    let position = range.start;
    try {
      while (position < range.end) {
        // Read each pass: a failure on the stripe URL moves this range's retry to the plan's URL.
        const onStripe = range.stripe && !this.stripeFailed;
        const url = onStripe ? this.stripeUrl : this._url;
        const headers = {
          ...this._requestInit.headers,
          Range: `bytes=${position}-${range.end - 1}`,
        };
        // A stripe request has its own signal, which a worker abort also aborts, so the inactivity
        // deadline can end that request alone. The plan's URL has no deadline.
        const stripe = onStripe ? inactivitySignal(signal, STRIPE_INACTIVITY_MS) : null;
        try {
          let response: Response;
          try {
            response = await this.metered.fetch(
              url,
              { ...this._requestInit, headers, signal: stripe?.signal ?? signal },
              run,
            );
          } catch (error) {
            failures = await this.failed(failures, error, url, signal, onStripe);
            continue;
          }
          // The plan's URL answered the first request of the source with the whole file: it
          // ignores `Range`, so no stripe can work either. The stock source reads it sequentially.
          if (!onStripe && response.status === 200 && this.firstTotal === null) {
            void response.body?.cancel().catch(() => {});
            this.ignoresRange = true;
            break;
          }
          let responseEnd: number;
          try {
            responseEnd = this.checkResponse(response, position, range.end) + 1;
          } catch (error) {
            // Nobody reads a rejected body: cancel it before its slot frees, whichever URL it
            // came from.
            void response.body?.cancel().catch(() => {});
            // Any answer from the stripe URL that is not a readable 206 for these bytes of this
            // file is the stripe URL failing: a refusal there says nothing about the plan's URL,
            // which serves the range and is checked the same way.
            if (!onStripe) throw error;
            this.stripeFailed = true;
            continue;
          }
          if (!response.body) {
            throw new Error(
              "Missing HTTP response body stream. The used fetch function must provide the response body as a ReadableStream.",
            );
          }
          const reader = response.body.getReader();
          // A response that delivered bytes made progress: its failure starts the count again,
          // as the stock source does, so a link that keeps dropping still finishes the range.
          let received = 0;
          let bodyFailed = false;
          while (true) {
            let result: ReadableStreamReadResult<Uint8Array>;
            try {
              result = await reader.read();
            } catch (error) {
              // Every byte of this response arrived: an error after the last one ends the
              // response, and the range, instead of failing a transport that finished its work.
              if (position >= responseEnd) break;
              failures = await this.failed(
                received > 0 ? 0 : failures,
                error,
                url,
                signal,
                onStripe,
              );
              bodyFailed = true;
              break;
            }
            if (result.done) break;
            if (position + result.value.length > responseEnd) {
              void reader.cancel().catch(() => {});
              // On the stripe URL this is the stripe failing, like any other answer that is not
              // the bytes asked for: the chunk is dropped whole and the plan's URL serves the
              // range from `position`.
              if (onStripe) {
                this.stripeFailed = true;
                bodyFailed = true;
                break;
              }
              throw new Error(
                `Response for bytes=${range.start}-${range.end - 1} is longer than its range`,
              );
            }
            stripe?.touch();
            range.chunks.push(result.value);
            position += result.value.length;
            received += result.value.length;
            notify();
          }
          if (bodyFailed) continue;
          if (position < responseEnd) {
            // The plan's URL ended its answer early after sending bytes: ask for the rest, as the
            // stock source does, without a retry or a stall report.
            if (received > 0 && !onStripe) {
              failures = 0;
              continue;
            }
            failures = await this.failed(
              failures,
              new Error(`Response for bytes=${range.start}-${range.end - 1} ended early`),
              url,
              signal,
              onStripe,
            );
            continue;
          }
          // A range that reached past the end of the file ends at its last byte.
          range.end = Math.min(range.end, responseEnd);
          if (onStripe || !this.twoOrigins) this.checkProtocol(url);
        } finally {
          stripe?.dispose();
        }
      }
    } catch (error) {
      range.failure = { error };
    }
    range.done = true;
    // Worker 0's head is the first head range; a later one only finds the reserve already over.
    if (range.head) this.reserveOpen = false;
    if (range.stripe) this.stripesInFlight--;
    this.releaseSlot();
    notify();
  }

  /**
   * Asks the browser which protocol a completed response from the striped URL came over, and
   * hands the source to the stock reader when it was HTTP/2 or HTTP/3. A cross-origin response
   * says only when its server sent `Timing-Allow-Origin`. It asks about the first
   * `PROTOCOL_CHECKS` completed responses, since a front can answer the first over HTTP/1.1 and
   * later ones over HTTP/3; without an answer the source keeps striping.
   */
  private checkProtocol(url: string | URL | Request): void {
    if (this.multiplexed || this.protocolChecks >= PROTOCOL_CHECKS) return;
    this.protocolChecks++;
    const protocol = responseProtocol(url);
    if (protocol === "h2" || protocol === "h3") {
      this.multiplexed = true;
      this.wakeWorkers();
    }
  }

  /**
   * Counts one transport failure of a range and waits out its retry delay.
   * Rethrows `error` when the retry delay is null or the read was aborted.
   * A failure on the stripe URL (`onStripe`) is no retry: it switches the
   * source to the plan's URL and returns `failures` as it was.
   */
  private async failed(
    failures: number,
    error: unknown,
    url: string | URL | Request,
    signal: AbortSignal,
    onStripe: boolean,
  ): Promise<number> {
    if (signal.aborted) throw error;
    if (onStripe) {
      this.stripeFailed = true;
      return failures;
    }
    const count = failures + 1;
    const delay = this._getRetryDelay(count, error, url);
    if (delay === null) throw error;
    if (!Number.isFinite(delay) || delay < 0) {
      throw new TypeError("Retry delay must be a non-negative finite number.");
    }
    if (delay > 0) await wait(1000 * delay, signal);
    if (signal.aborted) throw error;
    return count;
  }

  /**
   * Checks that `response` is the `206` for `[start, end)` of the same file the
   * first response described, and returns its last byte, inclusive. The
   * server ends a range that reaches past the file at the file's last byte.
   * Throws without retry on any mismatch: a replaced or re-served file must
   * not be stitched together. Supplies the file size on the first response.
   */
  private checkResponse(response: Response, start: number, end: number): number {
    if (response.status !== 206) {
      throw new Error(
        `Error fetching the stream: expected 206 Partial Content, got ${response.status}`,
      );
    }
    const match = CONTENT_RANGE.exec(response.headers.get("Content-Range") ?? "");
    if (!match) throw new Error("Error fetching the stream: no usable Content-Range");
    const [first, last, total] = [Number(match[1]), Number(match[2]), Number(match[3])];
    if (first !== start || last !== Math.min(end - 1, total - 1)) {
      throw new Error(
        `Error fetching the stream: asked for bytes ${start}-${end - 1}, got ${first}-${last} of ${total}`,
      );
    }
    const modified = response.headers.get("Last-Modified");
    if (this.firstTotal === null) {
      this.firstTotal = total;
      this.firstModified = modified;
    } else if (total !== this.firstTotal) {
      throw new Error(`The file's size changed from ${this.firstTotal} to ${total} bytes`);
    } else if (this.firstModified !== null && modified !== this.firstModified) {
      throw new Error("The file changed while it was being read");
    }
    const orchestrator = this._orchestrator;
    if (orchestrator.fileSize === null) {
      orchestrator.supplyFileSize(total);
      this.wakeWorkers();
    }
    this._fileSizeDetermined = true;
    return last;
  }
}
