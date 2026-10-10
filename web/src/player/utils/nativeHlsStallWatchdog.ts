/**
 * Detects native HLS playback that says it is playing but has stopped.
 *
 * Safari's native HLS can stop requesting segments partway through a copy
 * remux while the element stays `paused === false` at HAVE_ENOUGH_DATA, with
 * no `waiting`, `stalled` or `error` event. Its `duration` drops to the end of
 * what it has buffered, `seekable` goes empty, and nothing moves until the
 * source is reloaded (#1466). Reloading the same source and seeking back to
 * the frozen position resumes playback.
 *
 * The watchdog reports that state: the element is playing and not seeking,
 * is not loading anything, has not reached the end of the media, and its
 * position has not moved for `STALL_TIMEOUT_MS`. A slow segment keeps the
 * element in NETWORK_LOADING, so waiting on the server is not a stall.
 *
 * It is for native HLS only. With hls.js the element is attached to a
 * MediaSource and reports NETWORK_LOADING throughout, and hls.js recovers its
 * own stalls.
 */

/** How long a playing element's position may stay still before it is a stall. */
export const STALL_TIMEOUT_MS = 8_000;

/** Reloads attempted per attached source before the watchdog gives up. */
export const MAX_STALL_RECOVERIES = 3;

/** The media element state the watchdog reads. */
export interface StallWatchdogMedia {
  readonly paused: boolean;
  readonly seeking: boolean;
  readonly ended: boolean;
  readonly readyState: number;
  readonly networkState: number;
  readonly currentTime: number;
}

export interface NativeHlsStallWatchdogOptions {
  media: StallWatchdogMedia;
  /** Whether the position is at the end of the media, where stopping is expected. */
  atEnd: () => boolean;
  /** Called with the frozen position when a stall is found and a recovery is left. */
  onStall: (position: number) => void;
  /** Called once when a stall is found after the recoveries ran out. */
  onGiveUp?: () => void;
  now?: () => number;
}

const HAVE_METADATA = 1;
const NETWORK_LOADING = 2;

export class NativeHlsStallWatchdog {
  private stillSince: { position: number; at: number } | null = null;
  private recoveries = 0;
  private gaveUp = false;
  private readonly now: () => number;

  constructor(private readonly options: NativeHlsStallWatchdogOptions) {
    this.now = options.now ?? (() => Date.now());
  }

  /** Samples the element; call it on a regular timer. */
  check() {
    const { media } = this.options;
    if (
      media.paused ||
      media.seeking ||
      media.ended ||
      media.readyState < HAVE_METADATA ||
      media.networkState === NETWORK_LOADING ||
      this.options.atEnd()
    ) {
      this.stillSince = null;
      return;
    }

    const position = media.currentTime;
    const now = this.now();
    if (!this.stillSince || this.stillSince.position !== position) {
      this.stillSince = { position, at: now };
      return;
    }
    if (now - this.stillSince.at < STALL_TIMEOUT_MS) return;

    this.stillSince = null;
    if (this.recoveries < MAX_STALL_RECOVERIES) {
      this.recoveries++;
      this.options.onStall(position);
    } else if (!this.gaveUp) {
      this.gaveUp = true;
      this.options.onGiveUp?.();
    }
  }
}
