/**
 * Whether playback is close enough to the end of the file to open Next Up.
 *
 * `promptSeconds` is the viewer's `playback.next_up_prompt_seconds`: Next Up
 * opens that many seconds before the file ends. 0 ("At end") never opens it
 * early; the player's `ended` event opens it instead. Mirrors the Apple
 * client's prompt-window policy, so a profile sees Next Up at the same moment
 * on every surface.
 */
export function isInNextUpPromptWindow(
  currentTime: number,
  duration: number,
  promptSeconds: number,
): boolean {
  if (!(promptSeconds > 0)) return false;
  if (!Number.isFinite(duration) || duration <= 0) return false;
  if (!Number.isFinite(currentTime) || currentTime <= 0) return false;
  const remaining = duration - currentTime;
  return remaining >= 0 && remaining <= promptSeconds;
}
