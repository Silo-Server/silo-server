/**
 * How a watch-together client applies a room transport command's position.
 *
 * Small drift between local playback and the room position is absorbed with a
 * playbackRate nudge instead of a seek. On copy-remux deliveries a seek
 * outside the already-downloaded window cannot run inside the current stream:
 * it forces a seek-reanchor replan, which rebuilds the whole stream. Rate
 * convergence keeps the room synced without rebuilding anything. Local files
 * and seek-anywhere routes are unaffected: every position the element can
 * reach without a rebuild stays a seek, exactly as before.
 */

/** Drift within this band is corrected by rate instead of rebuilding the stream. */
export const roomCatchupBandSeconds = 2;
/** Local playback already matching the room within this bound needs no correction. */
export const roomCatchupDeadbandSeconds = 0.35;
/** Bounds for the convergence nudge. */
export const roomCatchupMinRate = 0.9;
export const roomCatchupMaxRate = 1.25;
/**
 * A 1s deficit plays at ~1.125x and converges in ~8s; the band edge (2s)
 * reaches the cap. Gentle on purpose: this runs during normal playback.
 */
const roomCatchupDivisorSeconds = 8;

export type RoomCatchupDecision =
  | { kind: "seek" }
  | { kind: "rate"; rate: number }
  | { kind: "none" };

/**
 * The room position a rate catch-up is converging toward. The room keeps
 * advancing at 1x from the command's execution, so convergence has to track
 * that moving position: against a static one, a member behind the room ends
 * the nudge early, and a member slowed ahead of it never ends it at all.
 */
export interface RoomCatchupTarget {
  positionSeconds: number;
  /** Local wall-clock time (ms) at which `positionSeconds` starts advancing. */
  executeAtMs: number;
}

/** The room's expected position at `nowMs`, advancing at 1x from execution. */
export function roomCatchupExpectedPosition(target: RoomCatchupTarget, nowMs: number): number {
  return Math.max(0, target.positionSeconds + Math.max(0, (nowMs - target.executeAtMs) / 1000));
}

/** Whether local playback has reached the advancing room position. */
export function roomCatchupConverged(
  target: RoomCatchupTarget,
  localPositionSeconds: number,
  nowMs: number,
): boolean {
  return (
    Math.abs(roomCatchupExpectedPosition(target, nowMs) - localPositionSeconds) <=
    roomCatchupDeadbandSeconds
  );
}

export interface RoomCatchupInput {
  action: "play" | "pause" | "seek";
  /** The command's position in media time. */
  targetPositionSeconds: number;
  /** The local playback position in media time. */
  localPositionSeconds: number;
  /**
   * Whether the element can take the target without a stream rebuild: the
   * plan seeks anywhere, or the target lies inside the seekable ranges.
   */
  targetLocallySeekable: boolean;
}

export function decideRoomCatchup(input: RoomCatchupInput): RoomCatchupDecision {
  if (input.action === "seek") return { kind: "seek" };
  const delta = input.targetPositionSeconds - input.localPositionSeconds;
  const drift = Math.abs(delta);
  if (drift <= roomCatchupDeadbandSeconds) return { kind: "none" };
  // Everything the element can reach without a rebuild stays a seek, exactly
  // as before the rate path existed.
  if (input.targetLocallySeekable) return { kind: "seek" };
  if (input.action === "pause") {
    // Pausing is cheap and the room's next play command realigns; never
    // rebuild a stream to park a paused member at the anchor.
    return { kind: "none" };
  }
  if (drift > roomCatchupBandSeconds) return { kind: "seek" };
  const rate = 1 + delta / roomCatchupDivisorSeconds;
  return {
    kind: "rate",
    rate: Math.min(roomCatchupMaxRate, Math.max(roomCatchupMinRate, rate)),
  };
}

/** Whether `nativeSeconds` falls inside one of the element's seekable ranges. */
export function isNativePositionSeekable(seekable: TimeRanges, nativeSeconds: number): boolean {
  for (let i = 0; i < seekable.length; i++) {
    if (nativeSeconds >= seekable.start(i) && nativeSeconds <= seekable.end(i)) {
      return true;
    }
  }
  return false;
}
