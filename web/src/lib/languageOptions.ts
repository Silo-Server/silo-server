import { LANGUAGES } from "@/player/utils/languageNames";

/** One choice in a settings dropdown. */
export interface SettingOption {
  value: string;
  label: string;
}

/**
 * Every language a setting can name, in display order.
 *
 * Derived from the shared language list rather than enumerated here, because
 * the contract types language settings as BCP 47 rather than as an enum — the
 * manifest has no member list to render, so this is the one place a language
 * dropdown's contents come from.
 */
export const NAMED_LANGUAGE_OPTIONS: SettingOption[] = LANGUAGES.map(({ code, label }) => ({
  value: code,
  label,
}));

/**
 * The same list with a leading "no preference" entry, which is how a nullable
 * language setting spells its unset state (the contract stores null).
 */
export const LANGUAGE_OPTIONS: SettingOption[] = [
  { value: "", label: "No preference" },
  ...NAMED_LANGUAGE_OPTIONS,
];
