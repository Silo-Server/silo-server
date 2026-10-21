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

  describe("at a speed other than 1x", () => {
    const RATE = 48_000;

    function setup(channels: number) {
      const processor = new Processor({ processorOptions: { channels } });
      const page = (data: PageToWorkletMessage) => processor.port.onmessage!({ data });
      const worker = (data: WorkerToWorkletMessage) =>
        (processor as unknown as { onWorker(message: WorkerToWorkletMessage): void }).onWorker(
          data,
        );
      page({ type: "generation", generation: 1, position: 0 });
      page({ type: "playing", playing: true });
      const render = (quanta: number) => {
        const out = Array.from({ length: channels }, () => new Float32Array(quanta * 128));
        for (let q = 0; q < quanta; q++) {
          const output = Array.from({ length: channels }, () => new Float32Array(128));
          processor.process([], [output]);
          for (let c = 0; c < channels; c++) out[c]!.set(output[c]!, q * 128);
        }
        return out;
      };
      const lastClock = () =>
        (processor.port.posted as Array<{ type: string; mediaTime: number; advancing: boolean }>)
          .filter((message) => message.type === "clock")
          .pop()!;
      return { processor, page, worker, render, lastClock };
    }

    function sine(frequency: number, seconds: number, channels: number, polarity = [1, -1]) {
      const frames = Math.round(seconds * RATE);
      const samples = new Float32Array(frames * channels);
      for (let i = 0; i < frames; i++) {
        const x = 0.5 * Math.sin((2 * Math.PI * frequency * i) / RATE);
        for (let c = 0; c < channels; c++) samples[i * channels + c] = x * polarity[c % 2]!;
      }
      return samples;
    }

    /** Upward zero crossings per second. */
    function frequencyOf(signal: Float32Array): number {
      let crossings = 0;
      for (let i = 1; i < signal.length; i++) {
        if (signal[i - 1]! < 0 && signal[i]! >= 0) crossings += 1;
      }
      return crossings / (signal.length / RATE);
    }

    it.each([0.5, 1.5, 2, 3])("keeps the pitch and advances the clock %sx as fast", (speed) => {
      const { page, worker, render, lastClock } = setup(1);
      worker({ type: "pcm", generation: 1, timestamp: 0, samples: sine(440, 4, 1) });
      page({ type: "rate", rate: speed });
      // 0.8 s of output: 300 quanta.
      const [out] = render(300);
      expect(frequencyOf(out!.subarray(4_800))).toBeGreaterThan(440 * 0.97);
      expect(frequencyOf(out!.subarray(4_800))).toBeLessThan(440 * 1.03);
      expect(lastClock().mediaTime).toBeCloseTo((299 * 128 * speed) / RATE, 2);
    });

    it("keeps out-of-phase channels at full level", () => {
      const { page, worker, render } = setup(2);
      worker({ type: "pcm", generation: 1, timestamp: 0, samples: sine(440, 4, 2) });
      page({ type: "rate", rate: 1.5 });
      const [left, right] = render(300);
      // RMS over each 10 ms window: a join that cancels shows as a dip.
      let lowest = Infinity;
      for (let start = 4_800; start + 480 <= left!.length; start += 480) {
        let sum = 0;
        for (let i = start; i < start + 480; i++) sum += left![i]! * left![i]!;
        lowest = Math.min(lowest, Math.sqrt(sum / 480));
      }
      expect(lowest).toBeGreaterThan(0.3);
      for (let i = 4_800; i < left!.length; i++) expect(right![i]).toBeCloseTo(-left![i]!, 6);
    });

    it("plays a gap in the audio as silence instead of skipping it", () => {
      const { page, worker, render, lastClock } = setup(1);
      worker({
        type: "pcm",
        generation: 1,
        timestamp: 0,
        samples: new Float32Array(1_024).fill(0.5),
      });
      // The next chunk starts 100 ms after the first one ends.
      const next = 1_024 / RATE + 0.1;
      worker({
        type: "pcm",
        generation: 1,
        timestamp: next,
        samples: new Float32Array(48_000).fill(0.25),
      });
      worker({ type: "end", generation: 1 });
      page({ type: "rate", rate: 3 });
      const [out] = render(40);
      let silent = 0;
      for (const sample of out!) if (sample === 0) silent += 1;
      // 100 ms of media at 3x is about 1,600 frames, less the 20 ms tolerance.
      expect(silent).toBeGreaterThan(1_200);
      // The clock keeps the gap's time, snapping onto the next chunk's own
      // timestamp within the tolerance, as at 1x.
      const media = lastClock().mediaTime;
      expect(media).toBeGreaterThanOrEqual((39 * 128 * 3) / RATE);
      expect(media).toBeLessThanOrEqual((39 * 128 * 3) / RATE + 0.021);
    });

    it("keeps a gap that starts after the queue ran dry", () => {
      const { page, worker, render, lastClock } = setup(1);
      for (let i = 0; i < 9; i++) {
        worker({
          type: "pcm",
          generation: 1,
          timestamp: (i * 1_024) / RATE,
          samples: new Float32Array(1_024).fill(0.5),
        });
      }
      render(1);
      page({ type: "rate", rate: 3 });
      render(25);
      // Audio resumes 100 ms after the old audio ends.
      const end = (9 * 1_024) / RATE;
      worker({
        type: "pcm",
        generation: 1,
        timestamp: end + 0.1,
        samples: new Float32Array(48_000).fill(0.25),
      });
      const [out] = render(1);
      expect(out!.every((sample) => sample === 0)).toBe(true);
      expect(lastClock().mediaTime).toBeLessThan(end + 0.01);
    });

    it("waits for more audio instead of playing silence it does not have", () => {
      const { processor, page, worker, render } = setup(1);
      page({ type: "rate", rate: 2 });
      // Less than one sequence needs.
      worker({
        type: "pcm",
        generation: 1,
        timestamp: 0,
        samples: new Float32Array(1_000).fill(0.5),
      });
      const [starved] = render(1);
      expect(starved!.every((sample) => sample === 0)).toBe(true);
      expect(
        (processor.port.posted as Array<{ type: string }>).some(
          (message) => message.type === "underrun",
        ),
      ).toBe(true);
      worker({
        type: "pcm",
        generation: 1,
        timestamp: 1_000 / RATE,
        samples: new Float32Array(48_000).fill(0.5),
      });
      const [resumed] = render(1);
      expect(resumed![0]).toBe(0.5);
    });

    it("drains the last of the stream at speed", () => {
      const { processor, page, worker, render } = setup(1);
      page({ type: "rate", rate: 2 });
      worker({
        type: "pcm",
        generation: 1,
        timestamp: 0,
        samples: new Float32Array(1_000).fill(0.5),
      });
      worker({ type: "end", generation: 1 });
      const [out] = render(8);
      expect(out![0]).toBe(0.5);
      expect(
        (processor.port.posted as Array<{ type: string }>).some(
          (message) => message.type === "drained",
        ),
      ).toBe(true);
    });

    it("keeps the audio it plays near the clock", () => {
      const { page, worker, render } = setup(1);
      // Each sample holds its own frame index, so the output shows which input plays.
      const ramp = new Float32Array(4 * RATE);
      for (let i = 0; i < ramp.length; i++) ramp[i] = i / 1e6;
      worker({ type: "pcm", generation: 1, timestamp: 0, samples: ramp });
      page({ type: "rate", rate: 3 });
      const [out] = render(300);
      let total = 0;
      let worst = 0;
      for (let k = 4_800; k < out!.length; k++) {
        const lag = Math.abs(out![k]! * 1e6 - k * 3) / RATE;
        total += lag;
        worst = Math.max(worst, lag);
      }
      // Without centring the audio trails the clock by up to 64 ms at 3x.
      expect(total / (out!.length - 4_800)).toBeLessThan(0.02);
      expect(worst).toBeLessThan(0.05);
    });

    it("reports running dry again after a speed change ends a starved spell", () => {
      const { processor, page, worker, render } = setup(1);
      page({ type: "rate", rate: 2 });
      worker({
        type: "pcm",
        generation: 1,
        timestamp: 0,
        samples: new Float32Array(1_000).fill(0.5),
      });
      render(1);
      const underruns = () =>
        (processor.port.posted as Array<{ type: string }>).filter((m) => m.type === "underrun")
          .length;
      expect(underruns()).toBe(1);
      // At 1x what is queued plays, then the queue runs dry for real.
      page({ type: "rate", rate: 1 });
      render(12);
      expect(underruns()).toBe(2);
    });

    it("plays the last of the stream through its whole media time when slowed", () => {
      const { processor, page, worker, render } = setup(1);
      page({ type: "rate", rate: 0.5 });
      worker({
        type: "pcm",
        generation: 1,
        timestamp: 0,
        samples: new Float32Array(1_000).fill(0.5),
      });
      worker({ type: "end", generation: 1 });
      const [out] = render(20);
      // 1,000 frames at 0.5x last 2,000 output frames, all of them sound.
      expect(out!.subarray(0, 1_990).every((sample) => sample === 0.5)).toBe(true);
      expect(
        (processor.port.posted as Array<{ type: string }>).some(
          (message) => message.type === "drained",
        ),
      ).toBe(true);
    });

    it("returns to 1x without a jump in the waveform", () => {
      const { page, worker, render } = setup(1);
      worker({ type: "pcm", generation: 1, timestamp: 0, samples: sine(440, 4, 1) });
      page({ type: "rate", rate: 2 });
      const [before] = render(50);
      page({ type: "rate", rate: 1 });
      const [after] = render(10);
      // A 440 Hz sine at 0.5 amplitude moves at most 0.029 per sample.
      expect(Math.abs(after![0]! - before![before!.length - 1]!)).toBeLessThan(0.06);
      for (let i = 1; i < after!.length; i++) {
        expect(Math.abs(after![i]! - after![i - 1]!)).toBeLessThan(0.06);
      }
    });
  });
});
