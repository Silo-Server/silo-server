/**
 * Orders the writes behind a viewer's track choices, and lets a playback start
 * wait for them.
 *
 * A choice is a request the page does not wait for, so two quick choices for
 * the same series (a pick, then Auto, then another pick) would otherwise race
 * and the server could keep whichever arrived last rather than the viewer's
 * last choice. Writes that share a key run one after another in the order they
 * were made. Different keys stay independent.
 */

export type TrackPreferenceDimension = "audio" | "subtitle";

const pending = new Map<string, Promise<void>>();

function writeKey(profileId: string, dimension: TrackPreferenceDimension, targetId: string) {
  return `${profileId}\u0000${dimension}\u0000${targetId}`;
}

/**
 * Runs `write` after every earlier write for the same profile, target, and
 * dimension has settled. A failed earlier write does not cancel later ones.
 */
export function runTrackPreferenceWrite<T>(
  target: { profileId: string; dimension: TrackPreferenceDimension; targetId: string },
  write: () => Promise<T>,
): Promise<T> {
  const key = writeKey(target.profileId, target.dimension, target.targetId);
  const result = (pending.get(key) ?? Promise.resolve()).then(write);
  const settled = result.then(
    () => undefined,
    () => undefined,
  );
  pending.set(key, settled);
  void settled.then(() => {
    if (pending.get(key) === settled) pending.delete(key);
  });
  return result;
}

/**
 * Resolves once every track preference write made so far has settled, or after
 * `timeoutMs`, whichever comes first. A playback start awaits this so the
 * server resolves tracks from the viewer's latest choice; the bound keeps a
 * hung write from holding playback back.
 */
export function settleTrackPreferenceWrites(timeoutMs = 2000): Promise<void> {
  if (pending.size === 0) return Promise.resolve();
  let timer: ReturnType<typeof setTimeout> | undefined;
  return Promise.race([
    Promise.all(pending.values()).then(() => undefined),
    new Promise<void>((resolve) => {
      timer = setTimeout(resolve, timeoutMs);
    }),
  ]).finally(() => clearTimeout(timer));
}
