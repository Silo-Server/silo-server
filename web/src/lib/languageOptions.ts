import { PSEUDO_LOCALES_ENABLED } from "@/i18n";
import { pickerLanguages } from "@/lib/appLocale";
import {
  SETTING_DEFINITIONS,
  SETTING_KEYS,
  SETTING_OPTION_SETS,
  SETTINGS_REVISION,
  type SettingKey,
} from "@/lib/settingsContract";
import { getLanguageName, languageIdentity } from "@/lib/languageNames";
import { settingUnsetLabel } from "@/lib/settingsText";

/** One choice in a settings dropdown. */
export interface SettingOption {
  value: string;
  label: string;
}

/** Preserve an exact open value while replacing any semantic ISO alias already shown. */
export function withCurrentLanguageOption(
  options: readonly SettingOption[],
  currentValue: string,
): SettingOption[] {
  const currentIdentity = languageIdentity(currentValue);
  const current = { value: currentValue, label: getLanguageName(currentValue) };
  const existing = options.findIndex(
    (option) => languageIdentity(option.value) === currentIdentity,
  );
  if (existing < 0) return [current, ...options];

  const merged = [...options];
  merged[existing] = current;
  return merged;
}

function suggestedValues(key: SettingKey, revision: number): string[] {
  // The app language's contract set is only an advisory floor: like the
  // profile's own picker, offer the languages this client ships.
  if (key === SETTING_KEYS.UI_APP_LANGUAGE) {
    return pickerLanguages(PSEUDO_LOCALES_ENABLED).map((language) => language.tag);
  }
  const setId = SETTING_DEFINITIONS[key].suggestedOptions;
  if (!setId) return [];
  return SETTING_OPTION_SETS[setId].options
    .filter((option) => option.introducedIn <= revision)
    .map((option) => option.value);
}

/**
 * Suggested values for one open language setting.
 *
 * The generated contract list is the stable floor, except for the app
 * language, which lists the languages this client ships. The server adds observed
 * original languages for catalog.metadata_language only (the audio/subtitle
 * track scans were too expensive; those pickers offer free entry via
 * LanguageSelect's "Other…" instead), and the current stored tag is always
 * synthesized into the list so an open value such as `pt-BR` never leaves a
 * select with no matching row. True language aliases are de-duplicated, with
 * the exact current wire value winning so the control remains selected.
 */
export function namedLanguageOptionsFor(
  key: SettingKey,
  currentValue?: string | null,
  runtimeValues: readonly string[] = [],
  revision = SETTINGS_REVISION,
): SettingOption[] {
  const contractValues = suggestedValues(key, revision);

  const values: string[] = [];
  const indexByLanguage = new Map<string, number>();
  const add = (value: string, replace: boolean) => {
    const trimmed = value.trim();
    if (!trimmed) return;
    const identity = languageIdentity(trimmed);
    const existing = indexByLanguage.get(identity);
    if (existing !== undefined) {
      if (replace) values[existing] = trimmed;
      return;
    }
    indexByLanguage.set(identity, values.length);
    values.push(trimmed);
  };

  contractValues.forEach((value) => add(value, false));
  runtimeValues.forEach((value) => add(value, false));
  if (currentValue) add(currentValue, true);

  return values.map((value) => ({ value, label: getLanguageName(value) }));
}

/** The nullable list, using the contract's context-specific unset copy. */
export function languageOptionsFor(
  key: SettingKey,
  currentValue?: string | null,
  runtimeValues: readonly string[] = [],
  revision = SETTINGS_REVISION,
): SettingOption[] {
  const named = namedLanguageOptionsFor(key, currentValue, runtimeValues, revision);
  const definition = SETTING_DEFINITIONS[key];
  if (!definition.nullable) return named;
  return [{ value: "", label: settingUnsetLabel(definition) }, ...named];
}
