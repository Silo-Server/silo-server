import { createContext, useContext } from "react";
import type { ThemeId } from "@/themes/theme";

/**
 * The colour theme's context, kept apart from the provider component so that
 * file exports a component and nothing else — the same split the rest of the
 * app uses to keep fast refresh working.
 *
 * Separate from `hooks/useTheme`, which owns the readability preferences (text
 * scale, text weight, high contrast) and reads them from the settings API. This
 * one is device-local and has no server side.
 */
export interface AppThemeContextValue {
  theme: ThemeId;
  setTheme: (theme: ThemeId) => void;
}

export const AppThemeContext = createContext<AppThemeContextValue | null>(null);

export function useAppTheme(): AppThemeContextValue {
  const ctx = useContext(AppThemeContext);
  if (!ctx) throw new Error("useAppTheme must be used within AppThemeProvider");
  return ctx;
}
