import type { ThemeToken } from "@/lib/themeTokens";

/**
 * Theme tokens driven by the brand accent quick-picker. Setting one accent
 * color recolors the primary action color, the focus ring, and the sidebar's
 * primary accent so the brand color shows across the prominent UI surfaces.
 * Admins who want finer control still have the full TokenEditor in Theming.
 */
export const ACCENT_TOKENS: readonly ThemeToken[] = ["primary", "ring", "sidebar-primary"];

/** Maps a chosen accent hex to the token overrides that recolor the UI accent. */
export function accentColorToTokens(hex: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const token of ACCENT_TOKENS) {
    out[token] = hex;
  }
  return out;
}
