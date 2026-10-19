/**
 * Playback speed for the engine's audio that keeps its pitch (WSOLA: waveform
 * similarity overlap-add). Runs on the audio thread, inside the worklet.
 *
 * The worklet keeps stepping its read head through the queue at the playback
 * rate, as it does for the clock. Each time the stretcher's output runs out it
 * builds the next sequence from the PCM at the head: a stretch of the input
 * played at its own speed, so its pitch is the original's. Consecutive
 * sequences overlap and crossfade; each starts at the offset, within a short
 * search window, whose waveform best continues the previous one, so the joins
 * do not cancel or smear. Playing faster skips the input between sequences,
 * playing slower repeats some of it.
 *
 * Within a sequence the audio advances at 1x while the head, and the clock
 * the video follows, advance at the playback rate. Above 1x each sequence
 * after the first therefore starts half a sequence's skip past the head, so
 * the audio is centred on the clock rather than trailing it.
 *
 * Every buffer is allocated up front: the audio thread must not collect
 * garbage mid-quantum.
 */

/** Crossfade between consecutive sequences. */
const OVERLAP_SECONDS = 0.008;
/** How far past the read head a sequence may start, to match the waveform. */
const SEARCH_SECONDS = 0.015;
/**
 * Sequence length at and below SLOW_RATE, and at and above FAST_RATE, with a
 * straight line between. Slow speeds repeat input, so longer sequences make
 * fewer, less audible repeats; fast speeds skip input, so shorter sequences
 * keep the skipped stretches short.
 */
const SLOW_SEQUENCE_SECONDS = 0.06;
const FAST_SEQUENCE_SECONDS = 0.04;
const SLOW_RATE = 0.5;
const FAST_RATE = 1.5;
/** The fastest rate the input buffers are sized for; faster rates centre as this one. */
const MAX_RATE = 4;
/**
 * Bounds the offset search on any channel count and sample rate. Candidates
 * are first tried on a coarse grid, then one by one around the best of them,
 * and each comparison reads at most this many samples.
 */
const COARSE_CANDIDATES = 60;
const MAX_CORRELATION_SAMPLES = 256;

export class TimeStretcher {
  private readonly overlap: number;
  private readonly search: number;
  private readonly coarseStep: number;
  /** Compares every stride-th frame of the overlap. */
  private readonly stride: number;
  /** Interleaved output of the current sequence. */
  private readonly out: Float32Array;
  private outLength = 0;
  private outPosition = 0;
  /** The input that would follow the last sequence: the next one fades in from it. */
  private readonly tail: Float32Array;
  private hasTail = false;
  /** Frames of `out` and `tail` still to blend into unstretched playback. */
  private fadeRemaining = 0;
  private fadePosition = 0;

  constructor(
    private readonly channels: number,
    private readonly sampleRate: number,
  ) {
    this.overlap = Math.max(1, Math.round(OVERLAP_SECONDS * sampleRate));
    this.search = Math.max(1, Math.round(SEARCH_SECONDS * sampleRate));
    this.coarseStep = Math.max(1, Math.ceil(this.search / COARSE_CANDIDATES));
    this.stride = Math.max(1, Math.ceil((this.overlap * channels) / MAX_CORRELATION_SAMPLES));
    this.out = new Float32Array(this.sequenceFrames(0) * channels);
    this.tail = new Float32Array(this.overlap * channels);
  }

  /** Input frames past the read head that the next sequence at `rate` reads. */
  inputFrames(rate: number): number {
    return this.lead(rate) + this.search + this.sequenceFrames(rate);
  }

  /** The most input any sequence reads, for sizing the caller's buffer. */
  get maxInputFrames(): number {
    return Math.max(this.inputFrames(0), this.inputFrames(MAX_RATE));
  }

  /** Frames of the current sequence not yet played. */
  get pending(): number {
    return this.outLength - this.outPosition;
  }

  /** Forgets the current sequence, as after a seek or a gap in the audio. */
  reset(): void {
    this.outLength = 0;
    this.outPosition = 0;
    this.hasTail = false;
    this.fadeRemaining = 0;
  }

  /**
   * Builds the next sequence from `input`: interleaved PCM starting at the
   * read head, at least `inputFrames(rate)` frames of it.
   */
  build(rate: number, input: Float32Array): void {
    const channels = this.channels;
    const overlap = this.overlap;
    const frames = this.sequenceFrames(rate);
    // The first sequence continues from the head exactly, as unstretched
    // playback left off; later ones are matched to the one before.
    const start = this.hasTail ? this.bestOffset(input, this.lead(rate)) * channels : 0;
    const out = this.out;
    const tail = this.tail;
    const fade = overlap * channels;
    if (this.hasTail) {
      for (let frame = 0; frame < overlap; frame++) {
        const w = (frame + 1) / (overlap + 1);
        for (let c = 0; c < channels; c++) {
          const i = frame * channels + c;
          out[i] = tail[i]! + (input[start + i]! - tail[i]!) * w;
        }
      }
    }
    const length = (frames - overlap) * channels;
    for (let i = this.hasTail ? fade : 0; i < length; i++) out[i] = input[start + i]!;
    for (let i = 0; i < fade; i++) tail[i] = input[start + length + i]!;
    this.hasTail = true;
    this.outLength = frames - overlap;
    this.outPosition = 0;
    this.fadeRemaining = 0;
  }

  /** Writes the next frame of the current sequence to `output` at `index`. */
  take(output: Float32Array[], index: number, channels: number): void {
    const base = this.outPosition * this.channels;
    for (let c = 0; c < channels; c++) output[c]![index] = this.out[base + c]!;
    this.outPosition += 1;
  }

  /**
   * Hands over to unstretched playback: the next `blend` calls fade from what
   * the stretcher would have played into what plays instead, so the switch
   * does not click.
   */
  release(): void {
    if (!this.hasTail) return;
    this.fadeRemaining = this.overlap;
    this.fadePosition = 0;
    this.hasTail = false;
  }

  /** Fades frame `index` of `output`, already written, in from the stretcher's audio. */
  blend(output: Float32Array[], index: number, channels: number): void {
    if (this.fadeRemaining === 0) return;
    const pending = this.outLength - this.outPosition;
    const frame = this.fadePosition;
    const source = frame < pending ? this.out : this.tail;
    const base = (frame < pending ? this.outPosition + frame : frame - pending) * this.channels;
    const w = (frame + 1) / (this.overlap + 1);
    for (let c = 0; c < channels; c++) {
      const from = source[base + c]!;
      output[c]![index] = from + (output[c]![index]! - from) * w;
    }
    this.fadePosition += 1;
    this.fadeRemaining -= 1;
    if (this.fadeRemaining === 0) {
      this.outLength = 0;
      this.outPosition = 0;
    }
  }

  /** Frames past the head where a sequence's search starts, centring it on the clock. */
  private lead(rate: number): number {
    if (rate <= 1) return 0;
    const skip = this.sequenceFrames(rate) - this.overlap;
    return Math.round((skip * (Math.min(rate, MAX_RATE) - 1)) / 2);
  }

  private sequenceFrames(rate: number): number {
    const t = Math.min(1, Math.max(0, (rate - SLOW_RATE) / (FAST_RATE - SLOW_RATE)));
    const seconds = SLOW_SEQUENCE_SECONDS + (FAST_SEQUENCE_SECONDS - SLOW_SEQUENCE_SECONDS) * t;
    return Math.max(2 * this.overlap + 1, Math.round(seconds * this.sampleRate));
  }

  /**
   * The start offset, in frames past the read head and within the search
   * window from `from`, that best continues the tail.
   */
  private bestOffset(input: Float32Array, from: number): number {
    const last = from + this.search - 1;
    let best = from;
    let bestScore = -Infinity;
    for (let offset = from; offset <= last; offset += this.coarseStep) {
      const score = this.similarity(input, offset);
      if (score > bestScore) {
        bestScore = score;
        best = offset;
      }
    }
    const low = Math.max(from, best - this.coarseStep + 1);
    const high = Math.min(last, best + this.coarseStep - 1);
    for (let offset = low; offset <= high; offset++) {
      if (offset === best) continue;
      const score = this.similarity(input, offset);
      if (score > bestScore) {
        bestScore = score;
        best = offset;
      }
    }
    return best;
  }

  /**
   * Normalized cross-correlation of the tail with the input at `offset`, over
   * every channel together: a mono downmix would cancel channels that are out
   * of phase with each other.
   */
  private similarity(input: Float32Array, offset: number): number {
    const channels = this.channels;
    const tail = this.tail;
    const step = this.stride * channels;
    const end = this.overlap * channels;
    const base = offset * channels;
    let correlation = 0;
    let energy = 0;
    for (let i = 0; i < end; i += step) {
      for (let c = 0; c < channels; c++) {
        const x = input[base + i + c]!;
        correlation += x * tail[i + c]!;
        energy += x * x;
      }
    }
    return energy > 1e-12 ? correlation / Math.sqrt(energy) : 0;
  }
}
