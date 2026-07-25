import { createContext, useContext, useEffect, useState, useCallback } from "react";
import type { ReactNode } from "react";
import type { ThemeId } from "@/lib/themes";
import { DEFAULT_THEME } from "@/lib/themes";
import { useSettings, useSetSetting } from "@/hooks/queries/settings";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useBranding } from "@/hooks/useBranding";
import { appearanceCache, storage } from "@/utils/storage";
import {
  appearanceCacheOwner,
  getInitialTheme,
  isValidTheme,
  parseHighContrast,
  parseTextScale,
  parseTextWeight,
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
  const auth = useOptionalAuth();
  // The account that owns the localStorage warm start. Null while auth is
  // bootstrapping or nobody is signed in, which still trusts the cache so the
  // app paints in the last look this device used.
  const cacheOwner = appearanceCacheOwner({
    loading: auth?.loading ?? false,
    user: auth?.user ? { id: auth.user.id } : null,
  });
  const loadApiTheme = cacheOwner !== null;
  // Read once per render, before any effect below re-stamps the cache, so every
  // ThemeProvider in this render pass agrees on whether the cache is ours.
  const cacheTrusted = appearanceCache.isTrusted(cacheOwner);

  const [themePreference, setThemePreference] = useState<ThemeId>(() =>
    getInitialTheme(cacheOwner),
  );
  const [previewThemeState, setPreviewThemeState] = useState<ThemeId | null>(null);
  const [textScalePreference, setTextScalePreference] = useState<TextScale>(() =>
    parseTextScale(appearanceCache.get(storage.KEYS.UI_TEXT_SCALE, cacheOwner)),
  );
  const [textWeightPreference, setTextWeightPreference] = useState<TextWeight>(() =>
    parseTextWeight(appearanceCache.get(storage.KEYS.UI_TEXT_WEIGHT, cacheOwner)),
  );
  const [highContrastPreference, setHighContrastPreference] = useState<boolean>(() =>
    parseHighContrast(appearanceCache.get(storage.KEYS.UI_HIGH_CONTRAST, cacheOwner)),
  );

  // Another account's appearance is never a valid starting point: drop it (and
  // take ownership of the now-empty cache) as soon as we know who is signed in.
  useEffect(() => {
    if (cacheOwner === null || cacheTrusted) return;
    appearanceCache.clear(cacheOwner);
    setThemePreference(DEFAULT_THEME);
    setTextScalePreference("default");
    setTextWeightPreference("default");
    setHighContrastPreference(false);
  }, [cacheOwner, cacheTrusted]);

  // Load persisted setting from API (user-scoped)
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
  // Values cached by another account must not stand in for the signed-in
  // account's missing preferences — that would both show them someone else's
  // appearance and suppress the admin default theme they should be getting.
  const localTheme = cacheTrusted ? themePreference : DEFAULT_THEME;
  const localTextScale = cacheTrusted ? textScalePreference : "default";
  const localTextWeight = cacheTrusted ? textWeightPreference : "default";
  const localHighContrast = cacheTrusted ? highContrastPreference : false;
  const hasStoredThemeChoice = appearanceCache.get(storage.KEYS.THEME, cacheOwner) != null;
  const fallbackTheme: ThemeId =
    !hasStoredThemeChoice && isValidTheme(adminDefaultTheme) ? adminDefaultTheme : localTheme;

  const theme =
    loadApiTheme && apiTheme
      ? getInitialThemeFromApi(apiTheme, fallbackTheme, cacheOwner)
      : fallbackTheme;
  const textScale = loadApiTheme ? parseTextScale(apiTextScale ?? localTextScale) : localTextScale;
  const textWeight = loadApiTheme
    ? parseTextWeight(apiTextWeight ?? localTextWeight)
    : localTextWeight;
  const highContrast = loadApiTheme
    ? parseHighContrast(apiHighContrast ?? String(localHighContrast))
    : localHighContrast;

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
      appearanceCache.set(storage.KEYS.THEME, newTheme, cacheOwner);
      settingMutation.mutate({ key: "ui_theme", value: newTheme });
    },
    [settingMutation, cacheOwner],
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
      appearanceCache.set(storage.KEYS.UI_TEXT_SCALE, value, cacheOwner);
      settingMutation.mutate({ key: "ui_text_scale", value });
    },
    [settingMutation, cacheOwner],
  );

  const setTextWeight = useCallback(
    (value: TextWeight) => {
      setTextWeightPreference(value);
      applyTextWeightToDOM(value);
      appearanceCache.set(storage.KEYS.UI_TEXT_WEIGHT, value, cacheOwner);
      settingMutation.mutate({ key: "ui_text_weight", value });
    },
    [settingMutation, cacheOwner],
  );

  const setHighContrast = useCallback(
    (value: boolean) => {
      setHighContrastPreference(value);
      applyHighContrastToDOM(value);
      appearanceCache.set(storage.KEYS.UI_HIGH_CONTRAST, String(value), cacheOwner);
      settingMutation.mutate({ key: "ui_high_contrast", value: String(value) });
    },
    [settingMutation, cacheOwner],
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

function getInitialThemeFromApi(
  apiTheme: string | null,
  fallback: ThemeId,
  cacheOwner: string | null,
): ThemeId {
  if (!apiTheme || !isValidTheme(apiTheme)) return fallback;
  return appearanceCache.get(storage.KEYS.THEME, cacheOwner) !== apiTheme ? apiTheme : fallback;
}
