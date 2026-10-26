import { useCallback, useMemo } from "react";
import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { useProfileDefaultWriter } from "@/hooks/queries/profileDefaults";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { parseSubtitleAppearance, type SubtitleAppearance } from "@/lib/subtitleAppearance";

/**
 * The one place subtitle appearance is read and written.
 *
 * playback.subtitle_appearance resolves profile_device → profile → default.
 * Appearance is a profile preference that follows the profile to every device,
 * so both the settings screen and the in-player panel write the profile row.
 * A device row outranks it, so the shared writer clears this browser's row on
 * every save: otherwise a row written by an older web build (which saved per
 * device) would keep winning here and the saved look would never show.
 *
 * Three surfaces render this value — the settings screen, the in-player panel,
 * and the cue renderer — and sharing one hook is what keeps them from
 * disagreeing about which scope a save lands at.
 */
const KEY = SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE;
const KEYS = [KEY];

export function useSubtitleAppearanceSetting() {
  const query = useEffectiveSettings({ keys: KEYS });
  const writer = useProfileDefaultWriter(query.data);

  const entry = query.data?.[KEY];
  // The manifest default is the same object DEFAULT_SUBTITLE_APPEARANCE spells,
  // and the effective endpoint always answers, so an unset value arrives as the
  // default rather than as nothing to parse.
  const appearance = useMemo(() => parseSubtitleAppearance(entry?.value), [entry?.value]);

  /** Whether a stored profile or device value differs from the contract default. */
  const isCustomized = entry !== undefined && entry.source !== "default";

  const { save: saveKey, reset: resetKey } = writer;
  const save = useCallback((next: SubtitleAppearance) => saveKey(KEY, next), [saveKey]);
  const reset = useCallback(() => resetKey(KEY), [resetKey]);

  return {
    appearance,
    isCustomized,
    save,
    reset,
    isSaving: writer.isSaving,
    isLoading: query.isLoading,
  };
}
