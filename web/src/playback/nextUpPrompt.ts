import type { EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

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

export interface NextUpBehavior {
  /** Seconds before the end of the file Next Up opens; 0 never opens it early. */
  promptSeconds: number;
  /** What the end of a series episode or shuffled item does. */
  atEnd: "post-roll" | "close";
}

/**
 * How Next Up behaves for the item playing, from the viewer's settings.
 *
 * `playback.next_up_enabled` false turns the series Next Up off: nothing opens
 * early, and the end of the episode closes the player instead of entering
 * post-roll. A shuffle picks what plays next itself, so it keeps its post-roll
 * either way. A server that predates the key leaves it out of the read, which
 * reads as its default (on).
 *
 * Until the read settles nothing opens early, so an "At end" or Off viewer who
 * starts near the end is not shown Next Up on the defaults; a failed read
 * falls back to the contract defaults.
 */
export function resolveNextUp({
  settings,
  readFailed,
  shuffled,
}: {
  settings: EffectiveSettingsMap | undefined;
  readFailed: boolean;
  shuffled: boolean;
}): NextUpBehavior {
  if (settings === undefined && !readFailed) return { promptSeconds: 0, atEnd: "post-roll" };
  const enabled =
    settings?.[SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED]?.value ??
    SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED].defaultValue;
  if (enabled === false && !shuffled) return { promptSeconds: 0, atEnd: "close" };
  const promptSeconds = Number(
    settings?.[SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS]?.value ??
      SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS].defaultValue,
  );
  return { promptSeconds, atEnd: "post-roll" };
}
