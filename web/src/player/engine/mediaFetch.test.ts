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

  // Each 2 MiB stripe is a sub-second request, and the meter drops those, so
  // striped playback would report no throughput and a start deadline could not
  // lower quality. One run counts every response it carries.
  it("reports the bytes of every response of one run as one long request", async () => {
    const meter = new MediaDataMeter(0);
    const metered = createMeteredFetch((message) => {
      if (message.type === "media-data") {
        meter.received(message.request, message.bytes, message.first, message.last);
      }
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ranged(timedBody(4, MIB / 4, 200))),
    );

    const run = metered.newRun();
    // Response A receives 1 MiB over 600 ms; response B starts then and takes
    // 1 MiB over another 600 ms.
    const a = await metered.fetch("https://media.example.test/s", undefined, run);
    const readA = drain(a.body);
    await vi.advanceTimersByTimeAsync(600);
    const b = await metered.fetch("https://media.example.test/s", undefined, run);
    const readB = drain(b.body);
    await vi.advanceTimersByTimeAsync(600);
    await Promise.all([readA, readB]);
    await vi.advanceTimersByTimeAsync(300);

    expect(meter.throughputKbps(performance.timeOrigin + performance.now())).toBe(
      Math.floor((2 * MIB * 8) / 1200),
    );
  });
});
