export function toMediaTime(playerTimeSeconds: number, timelineOffsetSeconds = 0): number {
  return Math.max(0, playerTimeSeconds + timelineOffsetSeconds);
}

export function toPlayerTime(mediaTimeSeconds: number, timelineOffsetSeconds = 0): number {
  // Preserve negative offsets so callers can distinguish a target before the
  // current HLS window from the locally seekable position at zero.
  return mediaTimeSeconds - timelineOffsetSeconds;
}

// hls.js's default `maxBufferHole`.
const BUFFER_HOLE_SECONDS = 0.1;

/**
 * Seconds of media buffered from `position` on; 0 when `position` is not
 * buffered. Holes small enough for hls.js to step over don't end the buffer.
 */
export function bufferedAheadSeconds(ranges: TimeRanges, position: number): number {
  let end: number | null = null;
  for (let i = 0; i < ranges.length; i++) {
    const start = ranges.start(i) - BUFFER_HOLE_SECONDS;
    if (end === null) {
      if (position >= start && position < ranges.end(i)) end = ranges.end(i);
    } else if (start <= end) {
      end = ranges.end(i);
    } else {
      break;
    }
  }
  return end === null ? 0 : end - position;
}

export function subtitleStartPositionSeconds(
  readyState: number,
  currentTimeSeconds: number,
  fetchAnchorSeconds: number | null | undefined,
): number {
  return readyState > 0 ? currentTimeSeconds : (fetchAnchorSeconds ?? 0);
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
