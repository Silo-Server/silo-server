import type { DecodeErrorCarrier } from "../decode-failure";

// The server keeps a plan's session for at least 90s so the next fallback can
// still be requested (docs/architecture/playback-protocol-v3.md §2.5).
export const STARTUP_TIMEOUT_MS = 60_000;

const MAX_FATAL_NETWORK_RECOVERIES = 1;

type StartupState = "starting" | "playable" | "failed" | "disposed";

export class StartupGuard {
  private state: StartupState = "starting";
  private fatalNetworkRecoveries = 0;
  private timeoutId: ReturnType<typeof setTimeout> | null;

  /** `onFailure` hears whether the deadline passed or fatal network errors ran out. */
  constructor(private readonly onFailure: (cause: "deadline" | "network") => void) {
    this.timeoutId = setTimeout(() => this.fail("deadline"), STARTUP_TIMEOUT_MS);
  }

  /**
   * A plan adoption installed a new transport. Only a transport that already
   * answered gets a fresh startup window: a start that has not shown a frame
   * yet keeps the deadline it began with, so a session that keeps replanning
   * still reaches its own failure instead of pushing the deadline out forever.
   * A start that already failed gets a new window too, because the failure was
   * reported (the Auto ladder lowers and retries on it), and its retry is a
   * genuinely new attempt.
   */
  armForReplacementTransport() {
    if (this.state === "starting" || this.state === "disposed") return;

    this.state = "starting";
    this.fatalNetworkRecoveries = 0;
    this.clearTimeout();
    this.timeoutId = setTimeout(() => this.fail("deadline"), STARTUP_TIMEOUT_MS);
  }

  handleFatalNetworkError(): boolean {
    if (this.state === "playable") return true;
    if (this.state !== "starting") return false;

    if (this.fatalNetworkRecoveries < MAX_FATAL_NETWORK_RECOVERIES) {
      this.fatalNetworkRecoveries++;
      return true;
    }

    this.fail("network");
    return false;
  }

  markPlaybackStarted() {
    if (this.state !== "starting") return;

    this.state = "playable";
    this.clearTimeout();
  }

  hasFailed() {
    return this.state === "failed";
  }

  /** True only while initial startup is still in flight (not yet playable). */
  isStarting() {
    return this.state === "starting";
  }

  dispose() {
    this.state = "disposed";
    this.clearTimeout();
  }

  private fail(cause: "deadline" | "network") {
    if (this.state !== "starting") return;

    this.state = "failed";
    this.clearTimeout();
    this.onFailure(cause);
  }

  private clearTimeout() {
    if (this.timeoutId === null) return;

    clearTimeout(this.timeoutId);
    this.timeoutId = null;
  }
}

/**
 * HTTP status the server returns for a segment URL whose opaque `sgen`
 * generation token names a replaced FFmpeg incarnation (the playback segment
 * handler's `stale_generation`). Retrying that exact URL can never succeed: the
 * bytes are fenced to the generation that produced the playlist, and the
 * session has moved on.
 */
export const STALE_GENERATION_STATUS = 412;

/**
 * Cap on consecutive playlist refetches a single transport may perform for the
 * stale-generation fence. Each refetch rebuilds the segment URLs with the
 * current `sgen`; if the server keeps answering 412 the session is genuinely
 * moving faster than the client can adopt it, so the player stops refetching
 * and lets the ordinary failure path replan instead of spinning.
 */
export const MAX_STALE_GENERATION_RECOVERIES = 2;

/**
 * Reports whether an hls.js error is the server's stale-generation fence. The
 * HTTP status is the authority: the segment route uses 412 for this verdict
 * only, and the v2 delivery adapter rewrites the v1 `stale_generation` body
 * code into the generic `precondition_failed` problem type, so the body cannot
 * be relied on.
 */
export function isStaleGenerationError(carrier: DecodeErrorCarrier | null | undefined): boolean {
  if (!carrier) return false;
  const fromResponse = carrier.response?.code;
  if (typeof fromResponse === "number") {
    return fromResponse === STALE_GENERATION_STATUS;
  }
  const details = carrier.networkDetails as { status?: unknown } | null | undefined;
  return details?.status === STALE_GENERATION_STATUS;
}

/**
 * The hls.js surface the stale-generation recovery drives. It is structural so
 * the recovery can be unit-tested without loading hls.js.
 */
export interface StaleGenerationRecoveryHost {
  /** The playlist URL the instance last loaded, or null before any load. */
  readonly url: string | null;
  /** hls.js's merged config; `startPosition` is read when the refetch autostarts. */
  config: { startPosition?: number };
  loadSource(url: string): void;
}

/**
 * Recovers hls.js from the server's stale-generation fence on a segment URL.
 *
 * The failing segment URI carried an `sgen` token minted against an FFmpeg
 * incarnation the server has since replaced. hls.js keeps no record of the
 * playlist's own segment URLs — it stores parsed fragments, not their source
 * text — so the only correct recovery is to invalidate the cached playlist and
 * fetch it again: the fresh manifest rebuilds every segment URL with the
 * current `sgen`. Retrying the dead URL would loop on a URL the server will
 * keep refusing. The refetch resumes at the current element position, and the
 * caller is expected to have bounded the number of refetches so a pathological
 * session still reaches the ordinary failure path.
 *
 * Returns false when there is no playlist URL to refetch, so the caller can fall
 * through to the ordinary failure recovery instead of stalling.
 */
export function recoverFromStaleGeneration(
  host: StaleGenerationRecoveryHost,
  resumePositionSeconds: number,
): boolean {
  const playlistUrl = host.url;
  if (!playlistUrl) return false;
  if (Number.isFinite(resumePositionSeconds) && resumePositionSeconds > 0) {
    host.config.startPosition = resumePositionSeconds;
  }
  host.loadSource(playlistUrl);
  return true;
}
