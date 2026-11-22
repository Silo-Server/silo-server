/**
 * The private members of Mediabunny's `UrlSource` and read orchestrator that
 * `parallelRangeSource.ts` reads and writes, typed here. The Mediabunny internals it relies on:
 *
 * - `UrlSource`: `_orchestrator`, `_abortControllers`, `_requestInit`, `_url`,
 *   `_getRetryDelay`, `_fileSizeDetermined`, `_dispatchRead`, and `_runWorker`,
 *   which a plan URL that ignores `Range` hands each worker to. The parent
 *   constructor binds `this._runWorker`, so the override runs.
 * - `ReadOrchestrator`: `fileSize`, `supplyFileSize`, `supplyWorkerData`,
 *   `signalWorkerStoppedRunning`, and `options.prefetchProfile`, which the
 *   read-ahead floor wraps. `signalWorkerStoppedRunning` clears a stopped worker's reads unless
 *   the worker is aborted, so a trim must cover the bytes those reads wait for.
 *   - `read`, which the source wraps: it is synchronous, and calls `createWorker` for a new
 *     read before it looks at the queue.
 *   - `createWorker`, which the source wraps: its name, its `null` while every worker runs, and
 *     that `read()` and the queue restart call it through `this`. The restart runs after a
 *     worker stops, outside `read()`.
 *   - `checkQueuedReadsAgainstWorker`, which moves the queued reads a worker covers onto it.
 *   - `workers`, which a retired worker leaves.
 *   - `queuedReads`, sorted by `hole.start` with no overlaps, and each entry's `hole`,
 *     `strictTarget`, `pendingSlices` and `age`.
 *   - `nextAge`, the orchestrator's one age counter for workers, cache entries and queued reads,
 *     which a requeued read takes its age from.
 * - `ReadWorker`: `currentPos`, `targetPos`, `aborted`, and `pendingSlices`, whose entries are
 *   each read's identity, `start` and `bytes.length`.
 *
 * `mediabunny` is pinned to an exact version for that reason. Upgrading it
 * must re-run `parallelRangeSource.test.ts` and `parallelRangeSource.seek.test.ts`, which drive the
 * real orchestrator.
 */
import { UrlSource, type Source, type UrlSourceOptions } from "mediabunny";

/** One read Mediabunny waits on: the bytes `[start, start + bytes.length)`. */
export interface PendingSlice {
  start: number;
  bytes: Uint8Array;
}

export interface ReadWorker {
  currentPos: number;
  targetPos: number;
  aborted: boolean;
  pendingSlices: PendingSlice[];
}

/** A read that found every worker running, waiting for one to stop. */
interface QueuedRead {
  hole: { start: number; end: number };
  strictTarget: boolean;
  pendingSlices: PendingSlice[];
  age: number;
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
  read(...args: unknown[]): unknown;
  createWorker(startPos: number, targetPos: number, strictTarget: boolean): ReadWorker | null;
  checkQueuedReadsAgainstWorker(worker: ReadWorker): void;
  workers: ReadWorker[];
  /** Sorted by `hole.start`, holes never overlapping. */
  queuedReads: QueuedRead[];
  nextAge: number;
}

/** The private surface of `UrlSource` this file reads and writes. */
interface UrlSourceInternals {
  _url: string | URL | Request;
  /** The engine passes headers as a plain record; the constructor copies them only to drop a `Range`. */
  _requestInit: RequestInit & { headers?: Record<string, string> };
  _getRetryDelay: (attempts: number, error: unknown, url: string | URL | Request) => number | null;
  _orchestrator: ReadOrchestrator;
  _abortControllers: Map<ReadWorker, AbortController>;
  _fileSizeDetermined: boolean;
  _dispatchRead(start: number, end: number): void;
  _runWorker(worker: ReadWorker): Promise<void>;
}

export const InternalUrlSource = UrlSource as unknown as new (
  url: string,
  options: UrlSourceOptions,
) => Source & UrlSourceInternals;
