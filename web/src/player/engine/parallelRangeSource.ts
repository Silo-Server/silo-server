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
 * answer other than a readable 206, or transport failure (fetch, body read, early end), from the stripe URL ends its use:
 * the rest of that range and every later range go to the plan's own URL, with no retry spent on the
 * stripe URL.
 *
 * It subclasses `UrlSource` and overrides its private `_runWorker`, so
 * Mediabunny keeps its read orchestrator, its cache, its read workers and its
 * prefetch profile, and only the way the bytes arrive changes. The Mediabunny
 * internals it relies on:
 *
 * - `UrlSource`: `_orchestrator`, `_abortControllers`, `_requestInit`, `_url`,
 *   `_getRetryDelay`, `_fileSizeDetermined`, `_dispatchRead`. The parent
 *   constructor binds `this._runWorker`, so the override runs.
 * - `ReadOrchestrator`: `fileSize`, `supplyFileSize`, `supplyWorkerData`,
 *   `signalWorkerStoppedRunning`, and `options.prefetchProfile`, which the
 *   read-ahead floor wraps.
 * - `ReadWorker`: `currentPos`, `targetPos` (which grows while the worker
 *   runs) and `aborted`.
 *
 * `mediabunny` is pinned to an exact version for that reason. Upgrading it
 * must re-run `parallelRangeSource.test.ts`, which drives the real orchestrator.
 */
import { UrlSource, type Source, type UrlSourceOptions } from "mediabunny";
import type { createMeteredFetch, MediaRun } from "./mediaFetch";

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

/** A `Content-Range` that names the range and the file's size. */
const CONTENT_RANGE = /^bytes (\d+)-(\d+)\/(\d+)$/;

type MeteredFetch = Pick<ReturnType<typeof createMeteredFetch>, "fetch" | "newRun">;

interface ReadWorker {
  currentPos: number;
  targetPos: number;
  aborted: boolean;
}

type PrefetchProfile = (
  start: number,
  end: number,
  workers: unknown,
) => { start: number; end: number };

interface ReadOrchestrator {
  fileSize: number | null;
  options: { prefetchProfile: PrefetchProfile };
  supplyFileSize(size: number): void;
  supplyWorkerData(worker: ReadWorker, bytes: Uint8Array): void;
  signalWorkerStoppedRunning(worker: ReadWorker): void;
}

/** The private surface of `UrlSource` this file reads and writes. */
interface UrlSourceInternals {
  _url: string | URL | Request;
  /** The constructor normalizes headers to a plain record. */
  _requestInit: RequestInit & { headers?: Record<string, string> };
  _getRetryDelay: (attempts: number, error: unknown, url: string | URL | Request) => number | null;
  _orchestrator: ReadOrchestrator;
  _abortControllers: Map<ReadWorker, AbortController>;
  _fileSizeDetermined: boolean;
  _dispatchRead(start: number, end: number): void;
}

const InternalUrlSource = UrlSource as unknown as new (
  url: string,
  options: UrlSourceOptions,
) => Source & UrlSourceInternals;

/** One closed range, read into `chunks` for delivery in file order. */
interface StripeRange {
  start: number;
  end: number;
  head: boolean;
  /** True when the range uses the stripe URL; false for a head range and for the plan-URL lane. */
  stripe: boolean;
  chunks: Uint8Array[];
  done: boolean;
  failure: { error: unknown } | null;
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
  private readonly metered: MeteredFetch;
  /** The ranges in flight across the workers, the workers queued for a slot, and each worker's wake function. */
  private readonly slots = { active: 0, waiting: [] as object[], wakers: new Set<() => void>() };
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
   * `headUrl` is the plan's own stream URL, which carries the warm head and the
   * range that finds the stripe URL full; `stripeUrl` carries up to 3 other
   * ranges at once. `metered.fetch` counts each response into the run it is given.
   */
  constructor(
    headUrl: string,
    stripeUrl: string,
    options: UrlSourceOptions,
    metered: MeteredFetch,
  ) {
    super(headUrl, options);
    this.stripeUrl = stripeUrl;
    this.metered = metered;
    const orchestrator = this._orchestrator;
    const profile = orchestrator.options.prefetchProfile;
    orchestrator.options.prefetchProfile = (start, end, workers) => {
      const range = profile(start, end, workers);
      return { start: range.start, end: Math.max(range.end, start + READ_AHEAD_BYTES) };
    };
  }

  /** From now on no range, of any worker, is a head range. */
  endWarmHead(): void {
    this.headOpen = false;
    this.reserveOpen = false;
    this.wakeWorkers();
  }

  /** Takes a slot for the worker named by `token`, or queues the worker and returns false. */
  private takeSlot(token: object): boolean {
    const slots = this.slots;
    if (slots.active >= this.slotCap() && slots.active < MAX_RANGES_IN_FLIGHT) {
      // Held back by the warm-head reserve, not by a full pool: the worker takes no place in
      // the queue, so the worker that arrives next takes the reserved slot at once.
      return false;
    }
    const first = slots.waiting[0];
    if (slots.active < MAX_RANGES_IN_FLIGHT && (first === undefined || first === token)) {
      if (first === token) slots.waiting.shift();
      slots.active++;
      // The worker now at the head may be asleep while a slot is still free: wake it.
      if (first === token && slots.waiting.length > 0 && slots.active < MAX_RANGES_IN_FLIGHT) {
        this.wakeWorkers();
      }
      return true;
    }
    this.joinQueue(token);
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

  private joinQueue(token: object): void {
    if (!this.slots.waiting.includes(token)) this.slots.waiting.push(token);
  }

  private leaveQueue(token: object): void {
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
   * Runs one read worker: keeps its ranges in flight and hands their bytes to
   * the orchestrator in file order. Called by Mediabunny's orchestrator in
   * place of `UrlSource._runWorker`.
   */
  async _runWorker(worker: ReadWorker): Promise<void> {
    const orchestrator = this._orchestrator;
    if (worker.aborted) {
      orchestrator.signalWorkerStoppedRunning(worker);
      return;
    }
    const abortController = new AbortController();
    this._abortControllers.set(worker, abortController);
    // One run per worker run: the page sees every range, retry and head request
    // of it as one long request.
    const run = this.metered.newRun();

    const ranges: StripeRange[] = []; // in file order; the first is the next to deliver
    let nextStart = worker.currentPos;
    let wake: (() => void) | null = null;
    const notify = () => {
      const resolve = wake;
      wake = null;
      resolve?.();
    };
    const token = {};
    this.slots.wakers.add(notify);
    // A second worker ends the reserve and takes the slot the first worker held back.
    if (++this.workersStarted > 1) this.reserveOpen = false;
    abortController.signal.addEventListener("abort", notify, { once: true });
    const hasRoom = () => ranges.length < MAX_RANGES_PER_WORKER && nextStart < worker.targetPos;
    // A range is wanted while this worker has room and the file size is known.
    // Until then one range per source, in flight, finds the size.
    const wants = () =>
      hasRoom() &&
      (orchestrator.fileSize !== null || (ranges.length === 0 && this.slots.active === 0));
    try {
      while (true) {
        if (worker.aborted) {
          orchestrator.signalWorkerStoppedRunning(worker);
          return;
        }
        while (wants() && this.takeSlot(token)) {
          const head = this.headOpen && !this.headed.has(worker);
          if (head) this.headed.add(worker);
          // At most three ranges use the stripe URL; the next one rides the plan's own URL, whose
          // connection is already open.
          const stripe = !head && this.stripesInFlight < MAX_STRIPE_URL_RANGES;
          if (stripe) this.stripesInFlight++;
          const range: StripeRange = {
            start: nextStart,
            end: Math.min(nextStart + (head ? WARM_HEAD_BYTES : RANGE_BYTES), worker.targetPos),
            head,
            stripe,
            chunks: [],
            done: false,
            failure: null,
          };
          nextStart = range.end;
          ranges.push(range);
          void this.readRange(range, run, abortController.signal, notify);
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
      this.leaveQueue(token);
      this.slots.wakers.delete(notify);
      this.wakeWorkers();
    }
  }

  /**
   * Reads `[range.start, range.end)` into `range.chunks`. A transport failure
   * (a fetch error, a body-read error, a body that ends early) retries the rest
   * of the range through `getRetryDelay`, counted from 1 since the range last
   * received bytes, and fails the range when the delay is null. A body from the
   * plan's URL that ends early after sending bytes is asked for again with no
   * retry counted. An answer other than a readable 206, or a transport failure,
   * from the stripe URL is no retry: it switches the source to the plan's URL, which
   * then serves the rest of the range. A response that breaks the byte checks
   * fails it at once. Any failure ends up in `range.failure`; the range's slot
   * is freed at its end.
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
        let response: Response;
        try {
          response = await this.metered.fetch(url, { ...this._requestInit, headers, signal }, run);
        } catch (error) {
          failures = await this.failed(failures, error, url, signal, onStripe);
          continue;
        }
        let responseEnd: number;
        try {
          responseEnd = this.checkResponse(response, position, range.end) + 1;
        } catch (error) {
          // Any answer from the stripe URL that is not a readable 206 for these bytes of this
          // file is the stripe URL failing: a refusal there says nothing about the plan's URL,
          // which serves the range and is checked the same way.
          if (!onStripe) throw error;
          void response.body?.cancel().catch(() => {});
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
            failures = await this.failed(received > 0 ? 0 : failures, error, url, signal, onStripe);
            bodyFailed = true;
            break;
          }
          if (result.done) break;
          if (position + result.value.length > responseEnd) {
            void reader.cancel().catch(() => {});
            throw new Error(
              `Response for bytes=${range.start}-${range.end - 1} is longer than its range`,
            );
          }
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
