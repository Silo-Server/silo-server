/**
 * The engine's audio output and master clock, running on the audio thread.
 *
 * PCM arrives from the decode worker over a MessagePort as interleaved chunks
 * already mixed to the output layout. Each render quantum copies the next 128
 * frames out and, every few quanta, tells the page which media time was
 * rendered at which context time; the page turns that into the playback clock
 * the video follows. Playing slower or faster than 1x steps through the queue
 * at that rate, which shifts pitch: it exists for Watch Together's brief
 * catch-up nudges, not for listening at speed.
 */
import type {
  PageToWorkletMessage,
  WorkerToWorkletMessage,
  WorkletToPageMessage,
  WorkletToWorkerMessage,
} from "./protocol";

declare const sampleRate: number;
declare const currentTime: number;
declare class AudioWorkletProcessor {
  readonly port: MessagePort;
  constructor(options?: { processorOptions?: unknown });
}
declare function registerProcessor(
  name: string,
  processor: new (options: { processorOptions: { channels: number } }) => AudioWorkletProcessor,
): void;

interface Chunk {
  samples: Float32Array;
  frames: number;
  timestamp: number;
}

const CLOCK_REPORT_QUANTA = 4;
const CONSUMED_REPORT_QUANTA = 8;
const LIMITER_KNEE = 0.9;

/** Soft-clips above the knee so a hot downmix never wraps or hard-clips. */
function limit(sample: number): number {
  const magnitude = Math.abs(sample);
  if (magnitude <= LIMITER_KNEE) return sample;
  const over = (magnitude - LIMITER_KNEE) / (1 - LIMITER_KNEE);
  return Math.sign(sample) * (LIMITER_KNEE + (1 - LIMITER_KNEE) * Math.tanh(over));
}

class EngineAudioProcessor extends AudioWorkletProcessor {
  private readonly channels: number;
  private queue: Chunk[] = [];
  /** Fractional frame position inside queue[0]. */
  private offset = 0;
  private generation = -1;
  private playing = false;
  private rate = 1;
  private ended = false;
  private drainedReported = false;
  private underrunReported = false;
  private consumedFrames = 0;
  private quanta = 0;
  private lastAdvancing = false;
  /** Media time just past the last sample rendered in this generation. */
  private renderedUntil: number | null = null;
  private worker: MessagePort | null = null;

  constructor(options: { processorOptions: { channels: number } }) {
    super(options);
    this.channels = options.processorOptions.channels;
    this.port.onmessage = (event: MessageEvent<PageToWorkletMessage>) => this.onPage(event.data);
  }

  private adopt(generation: number): void {
    if (generation <= this.generation) return;
    this.generation = generation;
    this.queue = [];
    this.offset = 0;
    this.ended = false;
    this.drainedReported = false;
    this.underrunReported = false;
    this.consumedFrames = 0;
    this.renderedUntil = null;
  }

  private onPage(message: PageToWorkletMessage): void {
    switch (message.type) {
      case "connect":
        this.worker = message.port;
        this.worker.onmessage = (event: MessageEvent<WorkerToWorkletMessage>) =>
          this.onWorker(event.data);
        return;
      case "generation":
        this.adopt(message.generation);
        return;
      case "playing":
        this.playing = message.playing;
        return;
      case "rate":
        this.rate = message.rate > 0 ? message.rate : 1;
        return;
    }
  }

  private onWorker(message: WorkerToWorkletMessage): void {
    if (message.generation < this.generation) return;
    this.adopt(message.generation);
    if (message.type === "end") {
      this.ended = true;
      return;
    }
    const frames = Math.floor(message.samples.length / this.channels);
    if (frames > 0) {
      this.queue.push({ samples: message.samples, frames, timestamp: message.timestamp });
      this.underrunReported = false;
    }
  }

  process(_inputs: Float32Array[][], outputs: Float32Array[][]): boolean {
    const output = outputs[0];
    if (!output || output.length === 0) return true;
    const length = output[0]!.length;
    const head = this.queue[0];
    const mediaTime = head ? head.timestamp + this.offset / sampleRate : null;
    let rendered = 0;

    if (this.playing) {
      const channels = Math.min(this.channels, output.length);
      const step = this.rate;
      for (; rendered < length && this.queue.length > 0; rendered++) {
        const chunk = this.queue[0]!;
        const index = Math.floor(this.offset);
        const fraction = this.offset - index;
        const next = index + 1 < chunk.frames ? chunk : this.queue[1];
        const nextIndex = index + 1 < chunk.frames ? index + 1 : 0;
        for (let c = 0; c < channels; c++) {
          const a = chunk.samples[index * this.channels + c]!;
          const b = fraction > 0 && next ? next.samples[nextIndex * this.channels + c]! : a;
          output[c]![rendered] = limit(a + (b - a) * fraction);
        }
        this.offset += step;
        this.consumedFrames += step;
        while (this.queue.length > 0 && this.offset >= this.queue[0]!.frames) {
          const finished = this.queue.shift()!;
          this.offset -= finished.frames;
          this.renderedUntil = finished.timestamp + finished.frames / sampleRate;
        }
      }
      if (rendered > 0 && this.queue[0]) {
        this.renderedUntil = this.queue[0].timestamp + this.offset / sampleRate;
      }
    }
    for (let c = 0; c < output.length; c++) output[c]!.fill(0, rendered);

    const advancing = rendered > 0;
    if (this.playing && this.queue.length === 0 && !this.ended && !this.underrunReported) {
      this.underrunReported = true;
      this.postPage({
        type: "underrun",
        generation: this.generation,
        contextTime: currentTime + rendered / sampleRate,
        mediaTime: this.renderedUntil,
      });
    }
    if (this.ended && this.queue.length === 0 && !this.drainedReported) {
      this.drainedReported = true;
      this.postPage({ type: "drained", generation: this.generation });
    }

    this.quanta += 1;
    if (
      mediaTime !== null &&
      (advancing !== this.lastAdvancing || this.quanta % CLOCK_REPORT_QUANTA === 0)
    ) {
      this.postPage({
        type: "clock",
        generation: this.generation,
        contextTime: currentTime,
        mediaTime,
        advancing,
      });
    }
    this.lastAdvancing = advancing;
    if (this.worker && this.quanta % CONSUMED_REPORT_QUANTA === 0) {
      const report: WorkletToWorkerMessage = {
        type: "consumed",
        generation: this.generation,
        consumedSeconds: this.consumedFrames / sampleRate,
      };
      this.worker.postMessage(report);
    }
    return true;
  }

  private postPage(message: WorkletToPageMessage): void {
    this.port.postMessage(message);
  }
}

registerProcessor("silo-engine-audio", EngineAudioProcessor);
