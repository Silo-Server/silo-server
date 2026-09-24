/**
 * Press-play-to-first-frame timing.
 *
 * The clock starts where the viewer asks for playback, and that is outside the
 * player: a Play button, a card, the next-episode prompt. Each of those goes
 * through the playback controller as a `viewer` start, and the controller marks
 * the intent here under the request key it is about to open. A start the app
 * makes on its own (`automatic`) leaves no mark. The playback session takes the
 * mark when it starts that request and reports `first_frame_ms` against it.
 *
 * The mark lives in memory only. The URL and history state would survive a
 * reload or come back on a Back navigation, and replay a timestamp from a
 * `performance.now()` origin that no longer applies.
 */

/**
 * A mark older than this when its request starts is not a press of Play for
 * that start: the tap reopened a request already on screen, or it never
 * reached the player, and a later load of the same request is not its answer.
 * The request starts within a few seconds of a tap even on a slow network.
 */
const MAX_INTENT_AGE_MS = 60_000;

let pending: { requestKey: string; at: number } | null = null;

/** Records that the viewer asked to play `requestKey`, replacing any earlier mark. */
export function markPlaybackIntent(requestKey: string, at = performance.now()) {
  pending = { requestKey, at };
}

/**
 * Returns when the viewer asked for `requestKey`, or null when nothing timed
 * it: a deep link, a reload, or a Back navigation. Every call consumes the
 * mark, because a load of any request means a mark for another is stale.
 */
export function takePlaybackIntent(requestKey: string): number | null {
  const mark = pending;
  pending = null;
  if (!mark || mark.requestKey !== requestKey) return null;
  return performance.now() - mark.at <= MAX_INTENT_AGE_MS ? mark.at : null;
}
