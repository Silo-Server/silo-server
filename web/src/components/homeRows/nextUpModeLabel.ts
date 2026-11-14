import type { MessageKey, Translate } from "@/i18n";

// The settings manifest carries the Next Up option labels; a value it doesn't
// list yet keeps the contract's English.
const NEXT_UP_MODE_LABELS: Readonly<Record<string, MessageKey>> = {
  combined: "settingsManifest.ui.next_up_mode.option.combined",
  separate: "settingsManifest.ui.next_up_mode.option.separate",
};

/** The shown label of a Next Up mode option. */
export function nextUpModeLabel(t: Translate, option: { value: string; label: string }): string {
  const key = NEXT_UP_MODE_LABELS[option.value];
  return key ? t(key) : option.label;
}
