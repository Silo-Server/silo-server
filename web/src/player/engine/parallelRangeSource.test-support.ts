/**
 * Shared fakes for the `parallelRangeSource` tests: a ranged in-memory server, a source wired to
 * it, and the helpers that read and compare bytes. Not a test file itself; imported by
 * `parallelRangeSource.test.ts` and `parallelRangeSource.seek.test.ts`.
 */
import { Buffer } from "node:buffer";
import { ReadableStream } from "node:stream/web";
import type { UrlSourceOptions } from "mediabunny";
import type { MediaRun } from "./mediaFetch";
import { ParallelRangeSource } from "./parallelRangeSource";

export const MIB = 2 ** 20;
export const KIB = 2 ** 10;
export const HEAD_URL = "https://app.example.test/api/v2/stream/s?st=x";
export const STRIPE_URL = "https://media.example.test/api/v2/stream/s?st=x&token=t";
const MODIFIED = "Mon, 09 Nov 2026 10:00:00 GMT";
export const SIZE = 70 * MIB + 12345;

export const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

export function pattern(size: number): Uint8Array {
  const bytes = new Uint8Array(size);
  // The higher terms keep 512 KiB chunks and 2 MiB ranges from repeating one another.
  for (let i = 0; i < size; i++) {
    bytes[i] = (i * 31 + (i >>> 8) + (i >>> 16) * 7 + (i >>> 24) * 13) & 0xff;
  }
  return bytes;
}

export function same(a: Uint8Array, b: Uint8Array): boolean {
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

export type Fault =
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
export class FakeServer {
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

export async function read(
  source: ParallelRangeSource,
  start: number,
  end: number,
): Promise<Uint8Array> {
  const result = await (source as unknown as Readable)._read(start, end, 0, Infinity);
  if (!result) throw new Error("read past the end");
  return result.bytes.subarray(start - result.offset, end - result.offset);
}

const sources: ParallelRangeSource[] = [];

/** Frees every source `makeSource` created. Each test file calls it from its own `afterEach`. */
export function disposeSources() {
  for (const source of sources.splice(0)) {
    try {
      source.ref().free();
    } catch {
      // Already disposed by the test.
    }
  }
}

export function makeSource(
  server: FakeServer,
  options: UrlSourceOptions = {},
  stripeUrl = STRIPE_URL,
) {
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
    stripeUrl,
    { getRetryDelay: () => 0, ...options },
    metered,
  );
  sources.push(source);
  return { source, runs: () => runs };
}
