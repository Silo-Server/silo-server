import { serverText, t } from "@/i18n";
import { SETTING_KEYS, type SettingDefinition } from "@/lib/settingsContract";

/** The skip intervals, whose choices print the seconds in the screen's digits. */
const SKIP_INTERVAL_KEYS: ReadonlySet<string> = new Set([
  SETTING_KEYS.PLAYER_VIDEO_SKIP_BACK_SECONDS,
  SETTING_KEYS.PLAYER_VIDEO_SKIP_FORWARD_SECONDS,
  SETTING_KEYS.PLAYER_AUDIOBOOK_SKIP_BACK_SECONDS,
  SETTING_KEYS.PLAYER_AUDIOBOOK_SKIP_FORWARD_SECONDS,
]);

/** A manifest text's catalog key: `settingsManifest.<key>.<field>`, with "-" as "_" in segments. */
function manifestKey(definition: Pick<SettingDefinition, "key">, field: string): string {
  return `settingsManifest.${definition.key.replace(/-/g, "_")}.${field}`;
}

/**
 * The manifest's label, description and option labels in the interface
 * language: the catalog's `settingsManifest.*` translation when that language
 * has one, otherwise (and always in English) the manifest's own English. Like
 * `t()`, these read the current language; a component showing them calls
 * `useAppLanguage()` (or `useT()`) so it re-renders on a language change.
 */
export function settingLabel(definition: Pick<SettingDefinition, "key" | "label">): string {
  return serverText(manifestKey(definition, "label"), undefined, definition.label);
}

/** The manifest description in the interface language (see `settingLabel`). */
export function settingDescription(
  definition: Pick<SettingDefinition, "key" | "description">,
): string {
  return serverText(manifestKey(definition, "description"), undefined, definition.description);
}

/** The manifest's unset label in the interface language, or `fallback` when it has none. */
export function settingUnsetLabel(
  definition: Pick<SettingDefinition, "key" | "unsetLabel">,
  fallback = t("web.settings.value.unset"),
): string {
  if (!definition.unsetLabel) return fallback;
  return serverText(manifestKey(definition, "unsetLabel"), undefined, definition.unsetLabel);
}

/**
 * An enum member's label in the interface language. The manifest leaves a
 * label empty when the member is its own label (the date formats spell
 * themselves).
 */
export function settingOptionLabel(
  definition: Pick<SettingDefinition, "key">,
  member: { value: unknown; label: string },
): string {
  const value = String(member.value);
  if (!member.label) return value;
  // The manifest writes "10 seconds" as text; the catalog's message formats
  // the number, so it follows the screen's digits.
  if (SKIP_INTERVAL_KEYS.has(definition.key)) {
    return t("player.seekInterval.choice", { seconds: Number(member.value) });
  }
  return serverText(
    manifestKey(definition, `option.${value.replace(/-/g, "_")}`),
    undefined,
    member.label,
  );
}
