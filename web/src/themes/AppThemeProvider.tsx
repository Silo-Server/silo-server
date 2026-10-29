import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { applyThemeToDOM, readStoredTheme, storeTheme, THEME_STORAGE_KEY } from "@/themes/theme";
import type { ThemeId } from "@/themes/theme";
import { AppThemeContext } from "@/themes/appThemeContext";

/**
 * Owns the theme choice. `themeBoot.js` has already written the stored value to
 * `<html>` before first paint, so the state below starts from the same read and
 * the attribute is never briefly wrong. The effect keeps it in step afterwards,
 * including for a value changed in another tab.
 */
export function AppThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemeId>(readStoredTheme);

  useEffect(() => {
    applyThemeToDOM(theme);
  }, [theme]);

  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== null && event.key !== THEME_STORAGE_KEY) return;
      setThemeState(readStoredTheme());
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const setTheme = useCallback((next: ThemeId) => {
    // The attribute is written here rather than only in the effect so the
    // repaint is synchronous with the click that chose it.
    applyThemeToDOM(next);
    storeTheme(next);
    setThemeState(next);
  }, []);

  return <AppThemeContext value={{ theme, setTheme }}>{children}</AppThemeContext>;
}
