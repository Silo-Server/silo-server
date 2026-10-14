import { beforeAll, describe, expect, it, vi } from "vitest";
import type { PageToWorkletMessage, WorkerToWorkletMessage } from "./protocol";

interface Processor {
  port: { onmessage: ((event: { data: PageToWorkletMessage }) => void) | null; posted: unknown[] };
  process(inputs: Float32Array[][], outputs: Float32Array[][]): boolean;
}
type ProcessorClass = new (options: { processorOptions: { channels: number } }) => Processor;

let Processor: ProcessorClass;

beforeAll(async () => {
  // The audio thread's globals, enough to load the processor module.
  vi.stubGlobal("sampleRate", 48_000);
  vi.stubGlobal("currentTime", 0);
  vi.stubGlobal(
    "AudioWorkletProcessor",
    class {
      readonly port = {
        onmessage: null,
        posted: [] as unknown[],
        postMessage(message: unknown) {
          this.posted.push(message);
        },
      };
    },
  );
  vi.stubGlobal("registerProcessor", (_name: string, processor: ProcessorClass) => {
    Processor = processor;
  });
  await import("./audio.worklet");
});

function quantum(processor: Processor): Float32Array {
  const output = [new Float32Array(128)];
  processor.process([], [output]);
  return output[0]!;
}

describe("EngineAudioProcessor", () => {
  it("plays silence until audio that starts after the position is due", () => {
    const processor = new Processor({ processorOptions: { channels: 1 } });
    const page = (data: PageToWorkletMessage) => processor.port.onmessage!({ data });
    page({ type: "generation", generation: 1, position: 0 });
    page({ type: "playing", playing: true });
    // The track's first samples are stamped 0.1 s after the start position.
    const chunk: WorkerToWorkletMessage = {
      type: "pcm",
      generation: 1,
      timestamp: 0.1,
      samples: new Float32Array(48_000).fill(0.5),
    };
    (processor as unknown as { onWorker(message: WorkerToWorkletMessage): void }).onWorker(chunk);

    expect(Array.from(quantum(processor)).every((sample) => sample === 0)).toBe(true);
    const clock = processor.port.posted.find(
      (message) => (message as { type: string }).type === "clock",
    ) as { mediaTime: number; advancing: boolean };
    // The clock runs from the start position, not from the late chunk.
    expect(clock.mediaTime).toBe(0);
    expect(clock.advancing).toBe(true);

    // 0.1 s is 37.5 quanta at 48 kHz; by then the track's own samples play.
    for (let i = 0; i < 38; i++) quantum(processor);
    expect(quantum(processor)[127]).toBe(0.5);
  });

  it("holds a new run's audio until the page starts it", () => {
    const processor = new Processor({ processorOptions: { channels: 1 } });
    const page = (data: PageToWorkletMessage) => processor.port.onmessage!({ data });
    const worker = (data: WorkerToWorkletMessage) =>
      (processor as unknown as { onWorker(message: WorkerToWorkletMessage): void }).onWorker(data);
    page({ type: "generation", generation: 1, position: 0 });
    page({ type: "playing", playing: true });
    // A seek: the worker's first chunk for run 2 beats the page's messages.
    worker({
      type: "pcm",
      generation: 2,
      timestamp: 30,
      samples: new Float32Array(4_800).fill(0.5),
    });
    expect(Array.from(quantum(processor)).every((sample) => sample === 0)).toBe(true);
    page({ type: "generation", generation: 2, position: 30 });
    page({ type: "playing", playing: true });
    expect(quantum(processor)[0]).toBe(0.5);
  });

  it("reports running dry as a stop, without an advancing clock after it", () => {
    const processor = new Processor({ processorOptions: { channels: 1 } });
    const page = (data: PageToWorkletMessage) => processor.port.onmessage!({ data });
    page({ type: "generation", generation: 1, position: 0 });
    page({ type: "playing", playing: true });
    const onWorker = (processor as unknown as { onWorker(message: WorkerToWorkletMessage): void })
      .onWorker;
    // Four quanta so the periodic clock report falls on the quantum that runs dry.
    onWorker.call(processor, {
      type: "pcm",
      generation: 1,
      timestamp: 0,
      samples: new Float32Array(128 * 3 + 64).fill(0.5),
    });
    for (let i = 0; i < 4; i++) quantum(processor);
    const messages = processor.port.posted as Array<{ type: string; advancing?: boolean }>;
    const underrun = messages.findIndex((message) => message.type === "underrun");
    expect(underrun).toBeGreaterThanOrEqual(0);
    expect(
      messages.slice(underrun).some((message) => message.type === "clock" && message.advancing),
    ).toBe(false);
  });
});
