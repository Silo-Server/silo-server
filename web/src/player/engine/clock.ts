/**
 * The engine's playback clock. With audio, the clock is the audio itself:
 * the worklet reports which media time it rendered at which context time, and
 * the clock reads that mapping at the context time reaching the speakers now.
 * Without audio it runs from performance.now().
 */

export interface AudioAnchor {
  contextTime: number;
  mediaTime: number;
  advancing: boolean;
}

export class EngineClock {
  /** Null while running from wall time: no audio, or audio that has ended. */
  private audible: (() => number) | null;
  private anchor: AudioAnchor | null = null;
  /** First anchor of the current advancing run; nothing before it is audible. */
  private runStart: AudioAnchor | null = null;
  private lastReading = 0;
  private wallBase = 0;
  private wallStartedAt: number | null = null;
  private rate = 1;

  constructor(
    private readonly audioSource: (() => number) | null,
    private origin: number,
  ) {
    this.audible = audioSource;
    this.lastReading = origin;
    this.wallBase = origin;
  }

  /** Restarts the clock at a new position (a seek), paused. */
  reset(position: number): void {
    this.audible = this.audioSource;
    this.origin = position;
    this.anchor = null;
    this.runStart = null;
    this.lastReading = position;
    this.wallBase = position;
    this.wallStartedAt = null;
  }

  setRate(rate: number): void {
    if (this.wallStartedAt !== null) {
      this.wallBase = this.now();
      this.wallStartedAt = performance.now();
    }
    this.rate = rate;
  }

  onAnchor(anchor: AudioAnchor): void {
    if (anchor.advancing && !this.anchor?.advancing) this.runStart = anchor;
    if (!anchor.advancing) this.runStart = null;
    this.anchor = anchor;
  }

  /**
   * Continues from wall time once the audio has played out, so video that
   * runs past the end of the audio keeps time and still stops on pause.
   */
  detachAudio(running: boolean): void {
    if (!this.audible) return;
    this.wallBase = this.now();
    this.audible = null;
    this.wallStartedAt = running ? performance.now() : null;
  }

  /** Wall-clock mode only: runs or holds the clock. */
  setRunning(running: boolean): void {
    if (this.audible) return;
    if (running && this.wallStartedAt === null) {
      this.wallStartedAt = performance.now();
    } else if (!running && this.wallStartedAt !== null) {
      this.wallBase = this.now();
      this.wallStartedAt = null;
    }
  }

  now(): number {
    if (!this.audible) {
      if (this.wallStartedAt === null) return this.wallBase;
      return this.wallBase + ((performance.now() - this.wallStartedAt) / 1000) * this.rate;
    }
    const anchor = this.anchor;
    if (!anchor) return this.origin;
    if (!anchor.advancing) {
      this.lastReading = anchor.mediaTime;
      return anchor.mediaTime;
    }
    const audible = this.audible();
    if (this.runStart && audible < this.runStart.contextTime) {
      return Math.max(this.lastReading, this.runStart.mediaTime);
    }
    const reading = anchor.mediaTime + (audible - anchor.contextTime) * this.rate;
    // Anchors arrive every few milliseconds with independent jitter; never
    // let the picture step backwards between them.
    this.lastReading = Math.max(this.lastReading, reading);
    return this.lastReading;
  }
}
