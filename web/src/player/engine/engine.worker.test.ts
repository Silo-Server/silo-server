import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PageToWorkerMessage, WorkerToPageMessage } from "./protocol";

interface Packet {
  timestamp: number;
  duration: number;
  type: "key" | "delta";
  data: Uint8Array;
  /** An IDR, the only H.264 key Mediabunny verifies on Chromium. */
  idr?: boolean;
}

/** A file whose packets start at 10 s, one key packet per second. */
const FIRST_TIMESTAMP = 10;
const closedGop: Packet[] = Array.from({ length: 12 }, (_, second) => ({
  timestamp: FIRST_TIMESTAMP + second,
  duration: 1,
  type: "key" as const,
  data: new Uint8Array(1),
}));
const file = vi.hoisted(() => ({
  packets: [] as Packet[],
  description: undefined as Uint8Array | undefined,
  keyLookups: [] as number[],
}));

/** One length-prefixed (4-byte) access unit from NAL units. */
function accessUnit(...nals: number[][]): Uint8Array {
  const bytes: number[] = [];
  for (const nal of nals) bytes.push(0, 0, 0, nal.length, ...nal);
  return new Uint8Array(bytes);
}
const IDR_SLICE = [0x65, 0x88];
const NON_IDR_SLICE = [0x41, 0x9a];
/** SEI recovery point: recovery_frame_cnt 0, exact match, no broken link. */
const RECOVERY_POINT_SEI = [0x06, 0x06, 0x01, 0xc4, 0x80];
/** avcC record with 4-byte NAL lengths. */
const AVCC = new Uint8Array([1, 0x64, 0, 0x28, 0xff, 0xe0, 0]);

/**
 * A Blu-ray-style open-GOP stream: an IDR only at the start, then I-frames
 * with a recovery point that the container marks as keys. The pictures right
 * after each I-frame in decode order show before it and reference the GOP
 * before it.
 */
const openGop: Packet[] = (() => {
  const out: Packet[] = [];
  const add = (seconds: number, type: Packet["type"], data: Uint8Array, idr = false) =>
    out.push({ timestamp: FIRST_TIMESTAMP + seconds, duration: 0.5, type, data, idr });
  add(0, "key", accessUnit(IDR_SLICE), true);
  for (let t = 0.5; t < 3; t += 0.5) add(t, "delta", accessUnit(NON_IDR_SLICE));
  for (const gop of [3, 6]) {
    add(gop, "key", accessUnit(RECOVERY_POINT_SEI, NON_IDR_SLICE));
    add(gop - 0.5, "delta", accessUnit(NON_IDR_SLICE));
    for (let t = gop + 0.5; t < gop + 3; t += 0.5) add(t, "delta", accessUnit(NON_IDR_SLICE));
  }
  return out;
})();

vi.mock("mediabunny", () => {
  class Input {
    async getPrimaryVideoTrack() {
      return {
        getDecoderConfig: async () => ({ codec: "avc1.640028", description: file.description }),
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
      return FIRST_TIMESTAMP + 12;
    }
  }
  class EncodedPacketSink {
    async getKeyPacket(timestamp: number, options?: { verifyKeyPackets?: boolean }) {
      file.keyLookups.push(timestamp);
      // On Chromium, Mediabunny verifies only IDR slices as H.264 keys.
      const verified = options?.verifyKeyPackets === true && file.description !== undefined;
      return (
        [...file.packets]
          .reverse()
          .find(
            (packet) =>
              packet.type === "key" &&
              packet.timestamp <= timestamp &&
              (!verified || packet.idr === true),
          ) ?? null
      );
    }
    async getFirstKeyPacket() {
      return file.packets[0];
    }
    async *packets(start: Packet) {
      yield* file.packets.slice(file.packets.indexOf(start));
    }
  }
  return { ALL_FORMATS: [], Input, EncodedPacketSink, UrlSource: class {} };
});
vi.mock("./wasm/silo-audio-decoders.wasm?url", () => ({ default: "" }));

describe("engine worker", () => {
  beforeEach(() => {
    file.packets = closedGop;
    file.description = undefined;
    file.keyLookups = [];
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  /**
   * Records the player time of each chunk decoded, one list per decoder.
   * A decoder whose first chunk is at `rejectFrom` fails on its second, as
   * Chrome's does when a picture references the GOP before the start; with
   * `outputFirst` it decodes the first picture before failing.
   */
  function stubDecoder(rejectFrom?: number, outputFirst = false): number[][] {
    const decoded: number[][] = [];
    vi.stubGlobal(
      "VideoDecoder",
      class {
        static isConfigSupported = async () => ({ supported: true });
        state = "configured";
        decodeQueueSize = 0;
        private chunks: number[] = [];
        constructor(
          private init: {
            output: (frame: { timestamp: number; duration: number; close(): void }) => void;
            error: (error: Error) => void;
          },
        ) {
          decoded.push(this.chunks);
        }
        configure() {}
        addEventListener() {}
        decode(chunk: { timestamp: number }) {
          this.chunks.push(chunk.timestamp / 1e6);
          if (this.chunks.length === 2 && this.chunks[0] === rejectFrom) {
            if (outputFirst) {
              this.init.output({ timestamp: this.chunks[0]! * 1e6, duration: 0, close() {} });
            }
            this.state = "closed";
            this.init.error(new Error("Decoding error."));
          }
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
    return decoded;
  }

  async function openWorker() {
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
    return { posted, send };
  }

  it("plays a file that starts after zero from its first packet at position 0", async () => {
    const decoded = stubDecoder();
    const { posted, send } = await openWorker();
    // The container's end time is 22 s; the file itself lasts 12.
    expect(posted.find((m) => m.type === "ready")).toMatchObject({
      info: { durationSeconds: 12 },
    });

    send({ type: "start", generation: 0, position: 5 });
    await vi.waitFor(() => expect(posted.some((m) => m.type === "video-ended")).toBe(true));
    // Position 5 is 5 s into the file, and decoded pictures carry player time.
    expect(file.keyLookups).toEqual([15]);
    expect(decoded).toEqual([[5, 6, 7, 8, 9, 10, 11]]);
  });

  it("starts an open-GOP H.264 stream at the recovery point before the position", async () => {
    file.packets = openGop;
    file.description = AVCC;
    const decoded = stubDecoder();
    const { posted, send } = await openWorker();

    send({ type: "start", generation: 1, position: 4 });
    await vi.waitFor(() =>
      expect(posted.some((m) => m.type === "video-ended" && m.generation === 1)).toBe(true),
    );
    // Not walked back to the IDR at 0: the I-frame at 3 s is where decoding
    // starts. The picture at 2.5 s that follows it references the GOP before
    // and is never shown, so it is not decoded; the one at 5.5 s belongs to a
    // GOP that is being decoded and is.
    expect(decoded).toEqual([[3, 3.5, 4, 4.5, 5, 5.5, 6, 5.5, 6.5, 7, 7.5, 8, 8.5]]);
  });

  it("starts at the recovery point before when the decoder rejects the stream from one", async () => {
    file.packets = openGop;
    file.description = AVCC;
    const decoded = stubDecoder(6);
    const { posted, send } = await openWorker();

    send({ type: "start", generation: 2, position: 7 });
    await vi.waitFor(() =>
      expect(posted.some((m) => m.type === "video-ended" && m.generation === 2)).toBe(true),
    );
    expect(posted.some((m) => m.type === "error")).toBe(false);
    expect(decoded).toEqual([
      [6, 6.5],
      [3, 3.5, 4, 4.5, 5, 5.5, 6, 5.5, 6.5, 7, 7.5, 8, 8.5],
    ]);
  });

  it("fails over when no key packet before the position starts an H.264 stream", async () => {
    // I-frames marked as keys, with no IDR and no recovery point anywhere.
    file.packets = Array.from({ length: 120 }, (_, second) => ({
      timestamp: FIRST_TIMESTAMP + second * 0.5,
      duration: 0.5,
      type: "key" as const,
      data: accessUnit(NON_IDR_SLICE),
    }));
    file.description = AVCC;
    const decoded = stubDecoder();
    const { posted, send } = await openWorker();

    send({ type: "start", generation: 3, position: 59 });
    await vi.waitFor(() =>
      expect(posted.find((m) => m.type === "error" && m.generation === 3)).toMatchObject({
        classification: "browser_engine_video_unsupported",
      }),
    );
    expect(decoded).toEqual([]);
    // Not back to the start of the file: 20 s behind the position, the keys
    // from 59 s back to 39 s and the one before that ends the search.
    expect(file.keyLookups.length).toBe(42);
  });

  it("fails a recovery-point start whose decoder decoded a picture before failing", async () => {
    file.packets = openGop;
    file.description = AVCC;
    const decoded = stubDecoder(6, true);
    const { posted, send } = await openWorker();

    send({ type: "start", generation: 4, position: 7 });
    // A decoder that worked and then failed is a decode failure, not a start
    // to step back from.
    await vi.waitFor(() =>
      expect(posted.find((m) => m.type === "error" && m.generation === 4)).toMatchObject({
        classification: "browser_engine_video_decode_failed",
      }),
    );
    expect(decoded).toEqual([[6, 6.5]]);
  });

  it("steps past packets a container marks as keys to the IDR before the position", async () => {
    // Every frame marked as a key, as in an MP4 without a sync sample table;
    // an IDR every 10 s.
    file.packets = Array.from({ length: 40 }, (_, frame) => ({
      timestamp: FIRST_TIMESTAMP + frame * 0.5,
      duration: 0.5,
      type: "key" as const,
      data: accessUnit(frame % 20 === 0 ? IDR_SLICE : NON_IDR_SLICE),
    }));
    file.description = AVCC;
    const decoded = stubDecoder();
    const { posted, send } = await openWorker();

    send({ type: "start", generation: 5, position: 19.5 });
    await vi.waitFor(() =>
      expect(posted.some((m) => m.type === "video-ended" && m.generation === 5)).toBe(true),
    );
    expect(decoded[0]?.[0]).toBe(10);
  });

  it("decodes from an IDR far behind the position", async () => {
    // A long-keyint closed-GOP stream: one IDR, then 30 s of P-frames.
    file.packets = Array.from({ length: 61 }, (_, frame) => ({
      timestamp: FIRST_TIMESTAMP + frame * 0.5,
      duration: 0.5,
      type: frame === 0 ? ("key" as const) : ("delta" as const),
      data: accessUnit(frame === 0 ? IDR_SLICE : NON_IDR_SLICE),
    }));
    file.description = AVCC;
    const decoded = stubDecoder();
    const { posted, send } = await openWorker();

    send({ type: "start", generation: 6, position: 25 });
    await vi.waitFor(() =>
      expect(posted.some((m) => m.type === "video-ended" && m.generation === 6)).toBe(true),
    );
    expect(decoded[0]?.[0]).toBe(0);
  });
});
