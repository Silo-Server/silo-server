/**
 * The engine's audio output and master clock, running on the audio thread.
 *
 * PCM arrives from the decode worker over a MessagePort as interleaved chunks
 * already mixed to the output layout. Each render quantum copies the next 128
 * frames out and, every few quanta, tells the page which media time was
 * rendered at which context time; the page turns that into the playback clock
 * the video follows. Playing slower or faster than 1x steps through the queue
 * at that rate, and the samples come from a time stretcher that keeps the
 * audio's pitch.
 */
import { TimeStretcher } from "./timeStretch";
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
/**
 * A chunk that starts later than this past what has been played is a real gap
 * (audio starting after the video, or missing packets), filled with silence so
 * the clock keeps the track's timing. Smaller differences are timestamp
 * rounding and play back to back.
 */
const GAP_TOLERANCE_SECONDS = 0.02;

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
  /** The rate in the last clock report; a change is reported at once. */
  private reportedRate = 1;
  /**
   * Media time just past the last sample rendered in this generation, or the
   * generation's start position before anything has played.
   */
  private renderedUntil: number | null = null;
  private worker: MessagePort | null = null;
  private readonly stretcher: TimeStretcher;
  /** The PCM at the read head that the stretcher builds its next sequence from. */
  private readonly stretchInput: Float32Array;
  /**
   * Less than a sequence is left before the end of the stream: it plays
   * unstretched, so its samples fill the media time left instead of a
   * sequence padded with silence.
   */
  private finishing = false;

  constructor(options: { processorOptions: { channels: number } }) {
    super(options);
    this.channels = options.processorOptions.channels;
    this.stretcher = new TimeStretcher(this.channels, sampleRate);
    this.stretchInput = new Float32Array(this.stretcher.maxInputFrames * this.channels);
    this.port.onmessage = (event: MessageEvent<PageToWorkletMessage>) => this.onPage(event.data);
  }

  private adopt(generation: number): void {
    if (generation <= this.generation) return;
    this.generation = generation;
    // The worker's first chunk can arrive before the page's messages for this
    // run, which travel on another port. Output waits for the page to say the
    // new position is ready.
    this.playing = false;
    this.queue = [];
    this.offset = 0;
    this.ended = false;
    this.drainedReported = false;
    this.underrunReported = false;
    this.consumedFrames = 0;
    this.renderedUntil = null;
    this.finishing = false;
    this.stretcher.reset();
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
        // The worker's first chunk can arrive before this message and adopt the
        // generation without its position.
        if (message.generation === this.generation && this.renderedUntil === null) {
          this.renderedUntil = message.position;
        }
        return;
      case "playing":
        this.playing = message.playing;
        return;
      case "rate": {
        const rate = message.rate > 0 ? message.rate : 1;
        if (rate === 1 && this.rate !== 1) this.stretcher.release();
        else if (rate !== 1 && this.rate === 1) this.stretcher.reset();
        this.rate = rate;
        return;
      }
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
      const chunk = { samples: message.samples, frames, timestamp: message.timestamp };
      // The queue ran dry with the head past the end of the last chunk.
      if (this.queue.length === 0) this.carryIntoGap(chunk);
      this.queue.push(chunk);
      this.underrunReported = false;
    }
  }

  process(_inputs: Float32Array[][], outputs: Float32Array[][]): boolean {
    const output = outputs[0];
    if (!output || output.length === 0) return true;
    const length = output[0]!.length;
    const head = this.queue[0];
    let mediaTime: number | null = null;
    if (head) {
      mediaTime = this.inGap(head) ? this.renderedUntil : head.timestamp + this.offset / sampleRate;
    }
    let rendered = 0;
    // The stretcher had too little PCM queued to build its next sequence.
    let starved = false;

    if (this.playing) {
      const channels = Math.min(this.channels, output.length);
      const step = this.rate;
      const stretched = step !== 1;
      for (; rendered < length && this.queue.length > 0; rendered++) {
        const chunk = this.queue[0]!;
        if (this.inGap(chunk)) {
          for (let c = 0; c < channels; c++) output[c]![rendered] = 0;
          this.renderedUntil! += step / sampleRate;
          this.stretcher.reset();
          continue;
        }
        if (stretched && !this.finishing && this.stretcher.pending === 0) {
          if (this.peek(this.stretcher.inputFrames(step))) {
            this.stretcher.build(step, this.stretchInput);
          } else if (this.ended) {
            this.finishing = true;
            this.stretcher.release();
          } else {
            starved = true;
            break;
          }
        }
        if (stretched && !this.finishing) {
          this.stretcher.take(output, rendered, channels);
        } else {
          const index = Math.floor(this.offset);
          const fraction = this.offset - index;
          const next = index + 1 < chunk.frames ? chunk : this.queue[1];
          const nextIndex = index + 1 < chunk.frames ? index + 1 : 0;
          for (let c = 0; c < channels; c++) {
            const a = chunk.samples[index * this.channels + c]!;
            const b = fraction > 0 && next ? next.samples[nextIndex * this.channels + c]! : a;
            // Interpolating between two samples never leaves their range, so the
            // mix's limiting holds here.
            output[c]![rendered] = a + (b - a) * fraction;
          }
          this.stretcher.blend(output, rendered, channels);
        }
        this.offset += step;
        this.consumedFrames += step;
        while (this.queue.length > 0 && this.offset >= this.queue[0]!.frames) {
          const finished = this.queue.shift()!;
          this.offset -= finished.frames;
          this.renderedUntil = finished.timestamp + finished.frames / sampleRate;
          if (this.queue.length > 0) this.carryIntoGap(this.queue[0]!);
        }
      }
      if (rendered > 0 && this.queue[0] && !this.inGap(this.queue[0])) {
        this.renderedUntil = this.queue[0].timestamp + this.offset / sampleRate;
      }
    }
    for (let c = 0; c < output.length; c++) output[c]!.fill(0, rendered);

    const ranDry =
      this.playing && (this.queue.length === 0 || starved) && !this.ended && !this.underrunReported;
    // When the queue ran dry inside this quantum, the underrun is the report:
    // an advancing clock for the samples before it would carry the clock on
    // through the silence that follows.
    const advancing = rendered > 0 && !ranDry;
    if (ranDry) {
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
      !ranDry &&
      mediaTime !== null &&
      (advancing !== this.lastAdvancing ||
        this.rate !== this.reportedRate ||
        this.quanta % CLOCK_REPORT_QUANTA === 0)
    ) {
      this.reportedRate = this.rate;
      this.postPage({
        type: "clock",
        generation: this.generation,
        contextTime: currentTime,
        mediaTime,
        advancing,
        rate: this.rate,
      });
    }
    this.lastAdvancing = advancing;
    // Audio flowing again ends a dry spell even without new PCM, as when a
    // speed change lets the stretcher build from what is already queued.
    if (advancing) this.underrunReported = false;
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

  /** True while `chunk` is not due yet: silence plays until it is. */
  private inGap(chunk: Chunk): boolean {
    return (
      this.offset < 1 && this.renderedUntil !== null && this.startsLate(chunk, this.renderedUntil)
    );
  }

  /**
   * Above 1x the head can step past the end of a chunk. When `next` starts
   * after a gap, those frames play the gap's silence instead of skipping
   * into it.
   */
  private carryIntoGap(next: Chunk): void {
    if (
      this.offset > 0 &&
      this.renderedUntil !== null &&
      this.startsLate(next, this.renderedUntil)
    ) {
      this.renderedUntil += this.offset / sampleRate;
      this.offset = 0;
    }
  }

  private startsLate(chunk: Chunk, after: number): boolean {
    return chunk.timestamp - after > GAP_TOLERANCE_SECONDS;
  }

  /**
   * Copies `frames` frames of the stream from the read head into
   * stretchInput, with silence where the queue's timestamps leave a gap.
   * False when less than that is queued.
   */
  private peek(frames: number): boolean {
    const channels = this.channels;
    const into = this.stretchInput;
    let written = 0;
    let index = Math.floor(this.offset);
    for (let i = 0; i < this.queue.length && written < frames; i++) {
      const chunk = this.queue[i]!;
      if (i > 0) {
        const previous = this.queue[i - 1]!;
        const end = previous.timestamp + previous.frames / sampleRate;
        if (this.startsLate(chunk, end)) {
          const silent = Math.min(
            frames - written,
            Math.round((chunk.timestamp - end) * sampleRate),
          );
          into.fill(0, written * channels, (written + silent) * channels);
          written += silent;
        }
        index = 0;
      }
      const count = Math.min(chunk.frames - index, frames - written);
      const from = index * channels;
      const to = written * channels;
      for (let s = 0; s < count * channels; s++) into[to + s] = chunk.samples[from + s]!;
      written += count;
    }
    return written >= frames;
  }

  private postPage(message: WorkletToPageMessage): void {
    this.port.postMessage(message);
  }
}

registerProcessor("silo-engine-audio", EngineAudioProcessor);
