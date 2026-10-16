import { afterEach, describe, expect, it, vi } from "vitest";
import type { PageToWorkerMessage, WorkerToPageMessage } from "./protocol";

/** A file whose packets start at 10 s, one key packet per second. */
const FIRST_TIMESTAMP = 10;
const packets = Array.from({ length: 12 }, (_, second) => ({
  timestamp: FIRST_TIMESTAMP + second,
  duration: 1,
  type: "key" as const,
  data: new Uint8Array(1),
}));
const keyLookups = vi.hoisted(() => [] as number[]);

vi.mock("mediabunny", () => {
  class Input {
    async getPrimaryVideoTrack() {
      return {
        getDecoderConfig: async () => ({ codec: "avc1.640028" }),
        getCodec: async () => "avc",
        getCodedWidth: async () => 320,
        getCodedHeight: async () => 180,
        getDisplayWidth: async () => 320,
        getDisplayHeight: async () => 180,
        getRotation: async () => 0,
        getFlip: async () => false,
      };
    }
    async getFirstTimestamp() {
      return FIRST_TIMESTAMP;
    }
    async computeDuration() {
      return FIRST_TIMESTAMP + packets.length;
    }
  }
  class EncodedPacketSink {
    async getKeyPacket(timestamp: number) {
      keyLookups.push(timestamp);
      return [...packets].reverse().find((packet) => packet.timestamp <= timestamp) ?? null;
    }
    async getFirstKeyPacket() {
      return packets[0];
    }
    async *packets(start: (typeof packets)[number]) {
      yield* packets.slice(packets.indexOf(start));
    }
  }
  return { ALL_FORMATS: [], Input, EncodedPacketSink, UrlSource: class {} };
});
vi.mock("./wasm/silo-audio-decoders.wasm?url", () => ({ default: "" }));

describe("engine worker", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("plays a file that starts after zero from its first packet at position 0", async () => {
    const decoded: number[] = [];
    vi.stubGlobal(
      "VideoDecoder",
      class {
        static isConfigSupported = async () => ({ supported: true });
        state = "configured";
        decodeQueueSize = 0;
        configure() {}
        addEventListener() {}
        decode(chunk: { timestamp: number }) {
          decoded.push(chunk.timestamp / 1e6);
        }
        flush = async () => {};
        close() {}
      },
    );
    vi.stubGlobal(
      "EncodedVideoChunk",
      class {
        timestamp: number;
        constructor(init: { timestamp: number }) {
          this.timestamp = init.timestamp;
        }
      },
    );
    const posted: WorkerToPageMessage[] = [];
    vi.spyOn(self, "postMessage").mockImplementation((message) =>
      posted.push(message as WorkerToPageMessage),
    );
    await import("./engine.worker");
    const send = (data: PageToWorkerMessage) =>
      (self.onmessage as (event: MessageEvent) => void)({ data } as MessageEvent);

    send({
      type: "open",
      source: {
        url: "/stream",
        headers: {},
        container: "mkv",
        durationSeconds: null,
        startSeconds: 0,
        video: { codec: "h264", width: 320, height: 180, transfer: "sdr", stripDolbyVision: false },
        audio: null,
      },
    });
    await vi.waitFor(() => expect(posted.some((m) => m.type === "ready")).toBe(true));
    // The container's end time is 22 s; the file itself lasts 12.
    expect(posted.find((m) => m.type === "ready")).toMatchObject({
      info: { durationSeconds: 12 },
    });

    send({ type: "start", generation: 0, position: 5 });
    await vi.waitFor(() => expect(posted.some((m) => m.type === "video-ended")).toBe(true));
    // Position 5 is 5 s into the file, and decoded pictures carry player time.
    expect(keyLookups).toEqual([15]);
    expect(decoded).toEqual([5, 6, 7, 8, 9, 10, 11]);
  });
});
