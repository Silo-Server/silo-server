export function toMediaTime(playerTimeSeconds: number, timelineOffsetSeconds = 0): number {
  return Math.max(0, playerTimeSeconds + timelineOffsetSeconds);
}

export function toPlayerTime(mediaTimeSeconds: number, timelineOffsetSeconds = 0): number {
  // Preserve negative offsets so callers can distinguish a target before the
  // current HLS window from the locally seekable position at zero.
  return mediaTimeSeconds - timelineOffsetSeconds;
}

/**
 * Where a freshly attached transport begins.
 *
 * A plan's start is not always the viewer's. A replan the client did not seek
 * for (a lowering, a re-offer of the same route) can land on the generation's
 * origin while the transport it replaces has already played past it; starting
 * there asks a live generation for segments it produced long ago, and the
 * segment layer refuses them with a 412 that ends in a terminal recovery. A
 * transport that replaces one that played therefore begins where that one had
 * got to, unless the new plan's own start is further ahead (the server moved
 * the anchor forward, and a target behind it is not producible).
 *
 * `replacedPlayheadSeconds` is on the new plan's player axis, or null when
 * nothing played before this transport (a first start, a new session).
 */
export function reloadStartSeconds(
  planStartSeconds: number,
  replacedPlayheadSeconds: number | null,
): number {
  if (replacedPlayheadSeconds === null || !Number.isFinite(replacedPlayheadSeconds)) {
    return planStartSeconds;
  }
  return replacedPlayheadSeconds > planStartSeconds ? replacedPlayheadSeconds : planStartSeconds;
}

/**
 * The playhead a replacement transport inherits, on its own player clock.
 *
 * Reading the element's clock is what makes this independent of whether the
 * outgoing transport ever reported a played frame: a transport that loaded and
 * advanced (a start that began in the middle of the file) already has a
 * position worth keeping, and a `streamPlayedRef` that is still false at the
 * moment of a rebuild used to send the replacement back to the plan's origin.
 * A transport of a different session inherits nothing — its clock belongs to
 * another source — and a zero clock has no position to carry.
 */
export function replacedTransportPlayheadSeconds(
  sameSession: boolean,
  currentTimeSeconds: number,
  timelineOffsetSeconds = 0,
): number | null {
  if (!sameSession || !Number.isFinite(currentTimeSeconds) || currentTimeSeconds <= 0) {
    return null;
  }
  return toPlayerTime(currentTimeSeconds, timelineOffsetSeconds);
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

/**
 * The position a subtitle track change reports to the server.
 *
 * A track change does not move the playhead, so the position should be where
 * the viewer is, not where the route started. The element's live position is
 * the best evidence; a seek still in flight names the target it is heading to;
 * the last position the transport reported covers a reload that has already
 * torn the element down. The fetch anchor is the last resort only: before the
 * element has media loaded it names the plan's start (the resume point), and
 * reporting it on a mid-movie switch restarts playback there.
 *
 * Candidates are accepted in order and must be finite and non-negative; a
 * missing or invalid one falls through to the next.
 */
export function subtitleChangePositionSeconds(input: {
  liveElementSeconds: number | null;
  pendingSeekSeconds: number | null;
  lastPlayedSeconds: number | null;
  fetchAnchorSeconds: number | null | undefined;
}): number {
  for (const candidate of [
    input.liveElementSeconds,
    input.pendingSeekSeconds,
    input.lastPlayedSeconds,
  ]) {
    if (candidate !== null && Number.isFinite(candidate) && candidate >= 0) {
      return candidate;
    }
  }
  return input.fetchAnchorSeconds ?? 0;
}
