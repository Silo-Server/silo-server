export interface PendingSeekResolution {
  currentTime: number;
  pendingSeekTime: number | null;
}

const DEFAULT_SEEK_SETTLE_TOLERANCE_SECONDS = 1;

export function resolvePendingSeekTime(
  actualTime: number,
  pendingSeekTime: number | null,
  toleranceSeconds = DEFAULT_SEEK_SETTLE_TOLERANCE_SECONDS,
): PendingSeekResolution {
  if (pendingSeekTime == null) {
    return { currentTime: actualTime, pendingSeekTime: null };
  }

  if (Math.abs(actualTime - pendingSeekTime) <= toleranceSeconds) {
    return { currentTime: actualTime, pendingSeekTime: null };
  }

  return { currentTime: pendingSeekTime, pendingSeekTime };
}

/**
 * A room seek this close to the viewer's held request answers it. The server
 * folds a repeat seek this close to its waiting seek into that seek.
 */
export const ROOM_HELD_SEEK_MATCH_SECONDS = 1;
/** A held request the room never runs stops holding the timeline after this. */
export const ROOM_HELD_SEEK_TIMEOUT_MS = 5_000;

/**
 * The viewer's room seek request still held after the room runs a seek to
 * `commanded`. In a burst of skips the room's seeks for the earlier skips land
 * first: they move the element but leave the newest request on the timeline,
 * so the next skip still builds on it. Only the room's seek to it releases it.
 */
export function remainingRoomHeldSeek(held: number | null, commanded: number): number | null {
  if (held === null || !Number.isFinite(commanded)) return null;
  return Math.abs(held - commanded) <= ROOM_HELD_SEEK_MATCH_SECONDS ? null : held;
}
