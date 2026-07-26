export function toMediaTime(playerTimeSeconds: number, streamOriginSeconds = 0): number {
  return Math.max(0, playerTimeSeconds + streamOriginSeconds);
}

export function toPlayerTime(mediaTimeSeconds: number, streamOriginSeconds = 0): number {
  return Math.max(0, mediaTimeSeconds - streamOriginSeconds);
}

/**
 * Picks the runtime that belongs alongside a media-time position.
 *
 * The server's runtime is authoritative and already in media time. The
 * element's duration is player-local and, on a remux or transcode stream,
 * covers only the window produced so far — pairing it with a media-time
 * position makes a resumed movie look finished within seconds of starting,
 * which latches the item watched and clears its resume point.
 *
 * The element duration is therefore only a last resort, for the case where the
 * server supplied no runtime at all. Returns undefined when neither is known,
 * so callers can omit the value instead of publishing a zero.
 */
export function mediaDurationSeconds(
  backendDurationSeconds: number | null | undefined,
  elementDurationSeconds: number | null | undefined,
): number | undefined {
  if (backendDurationSeconds != null && backendDurationSeconds > 0) {
    return backendDurationSeconds;
  }
  if (elementDurationSeconds != null && elementDurationSeconds > 0) {
    return elementDurationSeconds;
  }
  return undefined;
}
