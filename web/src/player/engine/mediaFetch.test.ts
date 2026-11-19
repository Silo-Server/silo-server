import { ReadableStream, TransformStream } from "node:stream/web";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MediaDataMeter } from "../media-data-meter";
import { createMeteredFetch } from "./mediaFetch";

const MIB = 2 ** 20;

// The VM test context has no web streams, so the test brings Node's. The fake
// response is a plain object: only its status and body are read.
function ranged(body: ReadableStream<Uint8Array>) {
  return { status: 206, body };
}

/** A response body that delivers `chunks` chunks, one every `everyMs`, starting now. */
function timedBody(chunks: number, chunkBytes: number, everyMs: number) {
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (let i = 0; i < chunks; i++) {
        setTimeout(() => {
          controller.enqueue(new Uint8Array(chunkBytes));
          if (i === chunks - 1) controller.close();
        }, i * everyMs);
      }
    },
  });
}

async function drain(
  body: ReadableStream<Uint8Array> | globalThis.ReadableStream<Uint8Array> | null,
) {
  const reader = body?.getReader();
  while (reader && !(await reader.read()).done);
}

describe("createMeteredFetch", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    vi.stubGlobal("TransformStream", TransformStream);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  // A 2 MiB range is a sub-second request on a fast link, and the meter drops
  // those, so striped playback would report no throughput and a start deadline
  // could not lower quality. One run counts every response it carries, for the
  // time at least one of them was receiving, and time several runs were
  // receiving at once counts once.
  it.each([
    {
      name: "back to back",
      responses: [
        { run: 0, at: 0, chunks: 4, every: 200 },
        { run: 0, at: 600, chunks: 4, every: 200 },
      ],
      want: Math.floor((2 * MIB * 8) / 1200),
    },
    {
      name: "an idle gap between ranges",
      responses: [
        { run: 0, at: 0, chunks: 4, every: 200 },
        { run: 0, at: 1600, chunks: 4, every: 200 },
      ],
      want: Math.floor((2 * MIB * 8) / 1200),
    },
    {
      name: "a run receiving under a second",
      responses: [
        { run: 0, at: 0, chunks: 4, every: 100 },
        { run: 0, at: 1300, chunks: 4, every: 100 },
      ],
      want: undefined,
    },
    {
      name: "overlap within one run",
      responses: [
        { run: 0, at: 0, chunks: 6, every: 200 },
        { run: 0, at: 500, chunks: 6, every: 200 },
      ],
      want: Math.floor((3 * MIB * 8) / 1500),
    },
    {
      name: "overlap across runs",
      responses: [
        { run: 0, at: 0, chunks: 6, every: 200 },
        { run: 1, at: 0, chunks: 6, every: 200 },
        { run: 0, at: 2000, chunks: 6, every: 200 },
      ],
      want: Math.floor((4.5 * MIB * 8) / 2000),
    },
  ])(
    "counts the time engine runs were receiving, overlapping time once: $name",
    async ({ responses, want }) => {
      const meter = new MediaDataMeter(0);
      const metered = createMeteredFetch((message) => {
        if (message.type === "media-data") {
          meter.received(
            message.request,
            message.bytes,
            message.first,
            message.last,
            message.spans,
          );
        }
      });
      let next = responses[0] as (typeof responses)[number];
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ranged(timedBody(next.chunks, MIB / 4, next.every))),
      );

      const runs = new Map<number, ReturnType<typeof metered.newRun>>();
      const reads: Promise<void>[] = [];
      let now = 0;
      let end = 0;
      for (const response of responses) {
        await vi.advanceTimersByTimeAsync(response.at - now);
        now = response.at;
        next = response;
        if (!runs.has(response.run)) runs.set(response.run, metered.newRun());
        const fetched = await metered.fetch(
          "https://media.example.test/s",
          undefined,
          runs.get(response.run),
        );
        reads.push(drain(fetched.body));
        end = Math.max(end, response.at + (response.chunks - 1) * response.every);
      }
      await vi.advanceTimersByTimeAsync(end - now);
      await Promise.all(reads);
      await vi.advanceTimersByTimeAsync(300);

      expect(meter.throughputKbps(performance.timeOrigin + performance.now())).toBe(want);
    },
  );
  // One span per response would grow with every range of a long play, and each report copies
  // the list. The run keeps the union instead: a chunk can join several earlier stretches.
  it("keeps a run's receiving time as merged stretches", async () => {
    let spans: [number, number][] = [];
    const metered = createMeteredFetch((message) => {
      if (message.type === "media-data") spans = message.spans;
    });
    const run = metered.newRun();
    const origin = performance.timeOrigin + performance.now();
    const reads: Promise<void>[] = [];
    // The first response's second chunk, at 1300 ms, reaches back over the two that came between.
    for (const [at, every] of [
      [0, 1300],
      [600, 100],
      [800, 100],
      [2000, 100],
    ] as const) {
      await vi.advanceTimersByTimeAsync(origin + at - (performance.timeOrigin + performance.now()));
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ranged(timedBody(2, MIB / 4, every))),
      );
      reads.push(drain((await metered.fetch("https://media.example.test/s", undefined, run)).body));
    }
    await vi.advanceTimersByTimeAsync(400);
    await Promise.all(reads);

    expect(spans.map(([first, last]) => [first - origin, last - origin])).toEqual([
      [0, 1300],
      [2000, 2100],
    ]);
  });
});
