/**
 * Bounds how long a delivered segment may yield no video frames before the
 * player recovers.
 *
 * A segment can arrive as a complete, successful response and still be
 * useless: the packaging decodes to no picture (an audio-only fragment, or a
 * fragment the decoder drops), so the element stays on its last presented
 * frame while hls.js happily buffers every later fragment. Nothing in the load
 * ladder sees a failure — the load *succeeded* — so playback waits with no
 * deadline and no recovery. This is the deadline for exactly that state: a
 * fragment was delivered, the player means to play, and no video frame has
 * been presented for the window.
 *
 * The window is short on purpose. A normal 2 s segment presents its first frame
 * within a few hundred milliseconds of its bytes arriving, so a few seconds
 * without one is not slow encoding, it is a fragment that produced nothing.
 * Recovery is bounded: the caller re-recovers the media source at most
 * MAX_DELIVERED_NO_FRAMES_RECOVERIES times per transport, and after that the
 * stream is left to the ordinary route failure paths instead of being torn down
 * in a loop.
 *
 * `now` is injected so the state machine is exercised without timers or sleeps.
 */

/** How long a delivered segment may go without presenting a frame. */
export const DELIVERED_NO_FRAMES_MS = 4_000;

/** Recovery attempts per transport before the deadline gives up. */
export const MAX_DELIVERED_NO_FRAMES_RECOVERIES = 2;

export interface DeliveredNoFramesExpiry {
  /** How long the delivered segment went without a presented frame. */
  waitedMs: number;
  /** Which recovery this is, counting from one. */
  recoveries: number;
}

export interface DeliveredNoFramesHooks {
  now: () => number;
  /** Called once per armed window when it expires; run the recovery here. */
  onExpire: (expiry: DeliveredNoFramesExpiry) => void;
}

export class DeliveredNoFramesDeadline {
  private deliveredAt: number | undefined;
  private recoveries = 0;
  private exhausted = false;
  private playing = true;

  constructor(
    private readonly hooks: DeliveredNoFramesHooks,
    private readonly windowMs: number = DELIVERED_NO_FRAMES_MS,
  ) {}

  /**
   * A media fragment's bytes were delivered. The first delivery of a stall arms
   * the window; later deliveries of the same stall keep it, so a stream that
   * keeps buffering frameless fragments still expires.
   */
  delivered(): void {
    if (this.deliveredAt !== undefined || this.exhausted || !this.playing) return;
    this.deliveredAt = this.hooks.now();
  }

  /** A video frame was presented, so the window closes. */
  framed(): void {
    this.deliveredAt = undefined;
  }

  /** The viewer paused (or the element ended): silence is expected. */
  setPlaying(playing: boolean): void {
    this.playing = playing;
    if (!playing) this.deliveredAt = undefined;
  }

  /** A new transport starts with a fresh budget. */
  reset(): void {
    this.deliveredAt = undefined;
    this.recoveries = 0;
    this.exhausted = false;
  }

  /** True while a delivered segment is waiting for its first frame. */
  get armed(): boolean {
    return this.deliveredAt !== undefined;
  }

  /** True once the recovery budget is spent for this transport. */
  get spent(): boolean {
    return this.exhausted;
  }

  get recoveryCount(): number {
    return this.recoveries;
  }

  /**
   * Advances the deadline. Returns true when it expired on this call, which is
   * also when `onExpire` ran.
   */
  poll(): boolean {
    if (this.deliveredAt === undefined) return false;
    const waitedMs = this.hooks.now() - this.deliveredAt;
    if (waitedMs < this.windowMs) return false;
    this.deliveredAt = undefined;
    this.recoveries += 1;
    if (this.recoveries >= MAX_DELIVERED_NO_FRAMES_RECOVERIES) this.exhausted = true;
    this.hooks.onExpire({ waitedMs, recoveries: this.recoveries });
    return true;
  }
}
