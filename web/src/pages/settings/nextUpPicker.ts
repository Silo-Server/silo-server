import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";

/**
 * The Show Next Up picker presents two settings as one choice, as the contract
 * allows: Off is `playback.next_up_enabled` false, and every other choice is
 * Next Up on with that many seconds of `playback.next_up_prompt_seconds`
 * (0 is "At end"). The native apps offer the same choices.
 *
 * `offOffered` is whether the server defines `next_up_enabled` (revision 31).
 * Where it does not, Off is not on the list and the key is never written.
 */
export const NEXT_UP_OFF = "off";

const PROMPT_SECONDS_CHOICES = [0, 10, 30, 60, 120];

export function nextUpPickerChoices(offOffered: boolean): string[] {
  const seconds = PROMPT_SECONDS_CHOICES.map(String);
  return offOffered ? [NEXT_UP_OFF, ...seconds] : seconds;
}

/**
 * The picker's value; Off wins over whatever seconds are stored. Where the
 * server lacks the key, `enabled` reads as its contract default, true.
 */
export function nextUpPickerValue({
  enabled,
  seconds,
}: {
  enabled: boolean;
  seconds: number;
}): string {
  return enabled ? String(seconds) : NEXT_UP_OFF;
}

/**
 * The writes a choice makes. Off leaves the stored seconds alone, so turning
 * Next Up back on later restores them; any other choice turns Next Up on.
 */
export function nextUpPickerWrites(
  choice: string,
  offOffered: boolean,
): Array<[SettingKey, unknown]> {
  if (choice === NEXT_UP_OFF) return [[SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED, false]];
  const writes: Array<[SettingKey, unknown]> = [
    [SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS, Number(choice)],
  ];
  if (offOffered) writes.push([SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED, true]);
  return writes;
}
