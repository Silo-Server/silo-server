import { createContext, useContext, useEffect, useState, useCallback } from "react";
import type { ReactNode } from "react";
import type { ThemeId } from "@/lib/themes";
import { useSettings, useSetSetting } from "@/hooks/queries/settings";
import { useBranding } from "@/hooks/useBranding";
import { appearanceCache, storage } from "@/utils/storage";
import {
  getInitialTheme,
  isValidTheme,
  parseHighContrast,
  parseTextScale,
  parseTextWeight,
  useAppearanceCacheOwner,
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
  // The account that owns the localStorage warm start. Null while auth is
  // bootstrapping or nobody is signed in, which still trusts the cache so the
  // app paints in the last look this device used.
  const cacheOwner = useAppearanceCacheOwner();
  const loadApiTheme = cacheOwner !== null;

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

  // This state was seeded for whoever was signed in when the provider mounted.
  // Re-seed from the new owner's namespace when the account changes, so signing
  // out and back in as someone else without a reload stops painting the
  // previous account's look. Values are namespaced, so this reads the new
  // account's own warm start rather than falling back to defaults.
  //
  // Adjusted during render rather than in an effect: React re-runs this pass
  // before committing, so the new account never gets a frame painted with the
  // previous one's appearance.
  const [seededOwner, setSeededOwner] = useState(cacheOwner);
  if (seededOwner !== cacheOwner) {
    setSeededOwner(cacheOwner);
    setThemePreference(getInitialTheme(cacheOwner));
    setTextScalePreference(
      parseTextScale(appearanceCache.get(storage.KEYS.UI_TEXT_SCALE, cacheOwner)),
    );
    setTextWeightPreference(
      parseTextWeight(appearanceCache.get(storage.KEYS.UI_TEXT_WEIGHT, cacheOwner)),
    );
    setHighContrastPreference(
      parseHighContrast(appearanceCache.get(storage.KEYS.UI_HIGH_CONTRAST, cacheOwner)),
    );
  }

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
  const localTheme = themePreference;
  const localTextScale = textScalePreference;
  const localTextWeight = textWeightPreference;
  const localHighContrast = highContrastPreference;
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

  // Mirror the server's values into this account's namespace so the next cold
  // start paints them before the settings request resolves. Without this the
  // cache would only ever hold choices made on this device, and a user who
  // picked their theme elsewhere would flash the default on every load.
  //
  // Only keys the user actually has a stored preference for are mirrored: the
  // absence of a cached theme is what lets the admin default apply, so writing
  // a resolved-but-unchosen value here would silently pin them to whatever the
  // default happened to be the first time they loaded the app.
  useEffect(() => {
    if (!loadApiTheme || apiSettings === undefined) return;
    if (isValidTheme(apiTheme)) appearanceCache.set(storage.KEYS.THEME, apiTheme, cacheOwner);
    if (apiTextScale != null) {
      appearanceCache.set(storage.KEYS.UI_TEXT_SCALE, apiTextScale, cacheOwner);
    }
    if (apiTextWeight != null) {
      appearanceCache.set(storage.KEYS.UI_TEXT_WEIGHT, apiTextWeight, cacheOwner);
    }
    if (apiHighContrast != null) {
      appearanceCache.set(storage.KEYS.UI_HIGH_CONTRAST, apiHighContrast, cacheOwner);
    }
  }, [
    loadApiTheme,
    apiSettings,
    apiTheme,
    apiTextScale,
    apiTextWeight,
    apiHighContrast,
    cacheOwner,
  ]);

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
