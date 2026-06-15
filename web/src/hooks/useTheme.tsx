import { createContext, useContext, useEffect, useState, useCallback } from "react";
import type { ReactNode } from "react";
import type { ThemeId } from "@/lib/themes";
import { useSettings, useSetSetting } from "@/hooks/queries/settings";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useBranding } from "@/hooks/useBranding";
import { storage } from "@/utils/storage";
import {
  getInitialTheme,
  isValidTheme,
  parseHighContrast,
  parseTextScale,
  parseTextWeight,
  shouldLoadApiTheme,
} from "@/hooks/themePreferences";
import type { TextScale, TextWeight } from "@/hooks/themePreferences";

interface ThemeContextValue {
  theme: ThemeId;
  setTheme: (theme: ThemeId) => void;
  previewTheme: (theme: ThemeId) => void;
  resetPreviewTheme: () => void;
  textScale: TextScale;
  setTextScale: (value: TextScale) => void;
  textWeight: TextWeight;
  setTextWeight: (value: TextWeight) => void;
  highContrast: boolean;
  setHighContrast: (value: boolean) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

function applyThemeToDOM(theme: ThemeId): void {
  document.documentElement.setAttribute("data-theme", theme);
}

function applyTextScaleToDOM(scale: TextScale): void {
  document.documentElement.setAttribute("data-text-scale", scale);
}

function applyTextWeightToDOM(weight: TextWeight): void {
  document.documentElement.setAttribute("data-text-weight", weight);
}

function applyHighContrastToDOM(value: boolean): void {
  document.documentElement.setAttribute("data-high-contrast", value ? "true" : "false");
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [themePreference, setThemePreference] = useState<ThemeId>(getInitialTheme);
  const [previewThemeState, setPreviewThemeState] = useState<ThemeId | null>(null);
  const [textScalePreference, setTextScalePreference] = useState<TextScale>(() =>
    parseTextScale(storage.get(storage.KEYS.UI_TEXT_SCALE)),
  );
  const [textWeightPreference, setTextWeightPreference] = useState<TextWeight>(() =>
    parseTextWeight(storage.get(storage.KEYS.UI_TEXT_WEIGHT)),
  );
  const [highContrastPreference, setHighContrastPreference] = useState<boolean>(() =>
    parseHighContrast(storage.get(storage.KEYS.UI_HIGH_CONTRAST)),
  );
  const auth = useOptionalAuth();
  const loadApiTheme = shouldLoadApiTheme({
    loading: auth?.loading ?? false,
    user: auth?.user ? { id: auth.user.id } : null,
  });

  // Load persisted setting from API (profile-scoped)
  const { data: apiSettings } = useSettings({ enabled: loadApiTheme });
  const apiTheme = apiSettings?.ui_theme;
  const apiTextScale = apiSettings?.ui_text_scale;
  const apiTextWeight = apiSettings?.ui_text_weight;
  const apiHighContrast = apiSettings?.ui_high_contrast;
  const settingMutation = useSetSetting();

  // Admin-set server default theme applies only when the user has expressed no
  // preference of their own (no stored local choice and no profile ui_theme).
  // A user's explicit choice always wins, preserving the per-user layering.
  const { defaultTheme: adminDefaultTheme } = useBranding();
  const hasStoredThemeChoice = storage.get(storage.KEYS.THEME) != null;
  const fallbackTheme: ThemeId =
    !hasStoredThemeChoice && isValidTheme(adminDefaultTheme) ? adminDefaultTheme : themePreference;

  const theme =
    loadApiTheme && apiTheme ? getInitialThemeFromApi(apiTheme, fallbackTheme) : fallbackTheme;
  const textScale = loadApiTheme
    ? parseTextScale(apiTextScale ?? textScalePreference)
    : textScalePreference;
  const textWeight = loadApiTheme
    ? parseTextWeight(apiTextWeight ?? textWeightPreference)
    : textWeightPreference;
  const highContrast = loadApiTheme
    ? parseHighContrast(apiHighContrast ?? String(highContrastPreference))
    : highContrastPreference;

  useEffect(() => {
    applyThemeToDOM(previewThemeState ?? theme);
  }, [previewThemeState, theme]);

  useEffect(() => {
    applyTextScaleToDOM(textScale);
  }, [textScale]);

  useEffect(() => {
    applyTextWeightToDOM(textWeight);
  }, [textWeight]);

  useEffect(() => {
    applyHighContrastToDOM(highContrast);
  }, [highContrast]);

  const setTheme = useCallback(
    (newTheme: ThemeId) => {
      setPreviewThemeState(null);
      setThemePreference(newTheme);
      applyThemeToDOM(newTheme);
      storage.set(storage.KEYS.THEME, newTheme);
      settingMutation.mutate({ key: "ui_theme", value: newTheme });
    },
    [settingMutation],
  );

  const previewTheme = useCallback((newTheme: ThemeId) => {
    setPreviewThemeState(newTheme);
  }, []);

  const resetPreviewTheme = useCallback(() => {
    setPreviewThemeState(null);
  }, []);

  const setTextScale = useCallback(
    (value: TextScale) => {
      setTextScalePreference(value);
      applyTextScaleToDOM(value);
      storage.set(storage.KEYS.UI_TEXT_SCALE, value);
      settingMutation.mutate({ key: "ui_text_scale", value });
    },
    [settingMutation],
  );

  const setTextWeight = useCallback(
    (value: TextWeight) => {
      setTextWeightPreference(value);
      applyTextWeightToDOM(value);
      storage.set(storage.KEYS.UI_TEXT_WEIGHT, value);
      settingMutation.mutate({ key: "ui_text_weight", value });
    },
    [settingMutation],
  );

  const setHighContrast = useCallback(
    (value: boolean) => {
      setHighContrastPreference(value);
      applyHighContrastToDOM(value);
      storage.set(storage.KEYS.UI_HIGH_CONTRAST, String(value));
      settingMutation.mutate({ key: "ui_high_contrast", value: String(value) });
    },
    [settingMutation],
  );

  return (
    <ThemeContext
      value={{
        theme,
        setTheme,
        previewTheme,
        resetPreviewTheme,
        textScale,
        setTextScale,
        textWeight,
        setTextWeight,
        highContrast,
        setHighContrast,
      }}
    >
      {children}
    </ThemeContext>
  );
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useTheme must be used within ThemeProvider");
  return ctx;
}

function getInitialThemeFromApi(apiTheme: string | null, fallback: ThemeId): ThemeId {
  if (!apiTheme || !isValidTheme(apiTheme)) return fallback;
  return storage.get(storage.KEYS.THEME) !== apiTheme ? apiTheme : fallback;
}
