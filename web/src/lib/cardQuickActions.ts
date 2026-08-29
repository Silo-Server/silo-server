import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

// Hand-written only because the generated definition types its enum members as
// unknown, which cannot produce the literal union the card props need; the test
// pins this list to the contract.
export const CARD_QUICK_ACTION_MODES = ["both", "favorites", "watched"] as const;

export type EnabledCardQuickActionMode = (typeof CARD_QUICK_ACTION_MODES)[number];
export type CardQuickActionMode = EnabledCardQuickActionMode | "none";

export const CARD_QUICK_ACTION_OPTIONS: ReadonlyArray<{
  value: EnabledCardQuickActionMode;
  label: string;
}> = (SETTING_DEFINITIONS[SETTING_KEYS.UI_CARD_QUICK_ACTIONS].values ?? []).map((member) => ({
  value: member.value as EnabledCardQuickActionMode,
  label: member.label,
}));

export function normalizeCardQuickActionMode(
  value: unknown,
  fallback: EnabledCardQuickActionMode = "both",
): EnabledCardQuickActionMode {
  return typeof value === "string" &&
    CARD_QUICK_ACTION_MODES.includes(value as EnabledCardQuickActionMode)
    ? (value as EnabledCardQuickActionMode)
    : fallback;
}

export function showsFavoriteQuickAction(mode: CardQuickActionMode): boolean {
  return mode === "both" || mode === "favorites";
}

export function showsWatchedQuickAction(mode: CardQuickActionMode): boolean {
  return mode === "both" || mode === "watched";
}
