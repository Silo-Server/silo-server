import { createContext, useContext, useEffect, useState, useCallback } from "react";
import type { ReactNode } from "react";
import type { ThemeId } from "@/lib/themes";
import { useEffectiveSettings, useSetSettingValue } from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
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

/**
 * The four appearance keys this provider needs, fetched in one batched
 * effective read rather than a query per key.
 */
const APPEARANCE_KEYS = [
  SETTING_KEYS.UI_THEME,
  SETTING_KEYS.UI_TEXT_SCALE,
  SETTING_KEYS.UI_TEXT_WEIGHT,
  SETTING_KEYS.UI_HIGH_CONTRAST,
] as const;

/**
 * These keys are profile-scoped with an optional per-device override; the
 * effective read already resolves profile_device over profile, and this
 * provider's setters write the profile-wide value (there is no device-override
 * UI for appearance today).
 */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

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
  // The identity that owns the localStorage warm start. Null while auth is
  // bootstrapping, nobody is signed in, or no profile is selected yet, which
  // still trusts the cache so the app paints in the last look this device used.
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
  // Re-seed from the new owner's namespace when the identity changes — a
  // different account, or a sibling profile on the same account — so switching
  // without a reload stops painting the previous identity's look. Values are
  // namespaced, so this reads the new identity's own warm start rather than
  // falling back to defaults.
  //
  // Adjusted during render rather than in an effect: React re-runs this pass
  // before committing, so the new identity never gets a frame painted with the
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

  // Load persisted settings from the canonical effective endpoint, one batched
  // read for all four appearance keys. The server resolves the profile_device
  // override over the profile value, so this needs no per-scope reads.
  //
  // A source of "default" means the profile has stored no choice of its own;
  // that must stay distinguishable from an explicit choice so the admin default
  // and the local warm start keep their layering, so those values are dropped
  // here rather than treated as the profile's preference.
  const { data: effectiveSettings } = useEffectiveSettings({
    keys: APPEARANCE_KEYS,
    enabled: loadApiTheme,
  });
  const storedValue = (key: (typeof APPEARANCE_KEYS)[number]): unknown => {
    const setting = effectiveSettings?.[key];
    return setting !== undefined && setting.source !== "default" ? setting.value : undefined;
  };
  const rawApiTheme = storedValue(SETTING_KEYS.UI_THEME);
  const apiTheme = typeof rawApiTheme === "string" ? rawApiTheme : undefined;
  const rawApiTextScale = storedValue(SETTING_KEYS.UI_TEXT_SCALE);
  const apiTextScale = typeof rawApiTextScale === "string" ? rawApiTextScale : undefined;
  const rawApiTextWeight = storedValue(SETTING_KEYS.UI_TEXT_WEIGHT);
  const apiTextWeight = typeof rawApiTextWeight === "string" ? rawApiTextWeight : undefined;
  const rawApiHighContrast = storedValue(SETTING_KEYS.UI_HIGH_CONTRAST);
  const apiHighContrast = typeof rawApiHighContrast === "boolean" ? rawApiHighContrast : undefined;
  const settingMutation = useSetSettingValue();

  // Admin-set server default theme applies only when the user has expressed no
  // preference of their own (no stored local choice and no profile ui.theme).
  // A profile's explicit choice always wins, preserving the per-profile
  // layering.
  const { defaultTheme: adminDefaultTheme } = useBranding();
  const localTheme = themePreference;
  const localTextScale = textScalePreference;
  const localTextWeight = textWeightPreference;
  const localHighContrast = highContrastPreference;
  const hasStoredThemeChoice = appearanceCache.get(storage.KEYS.THEME, cacheOwner) != null;
  const fallbackTheme: ThemeId =
    !hasStoredThemeChoice && isValidTheme(adminDefaultTheme) ? adminDefaultTheme : localTheme;

  // The server's value is this profile's own stored choice, so it wins outright
  // whenever it is present and valid. It is deliberately not compared against
  // the local cache: the effect below mirrors the server's value into that very
  // cache, so any such comparison stops holding after the first render and the
  // theme silently reverts to the default on the second.
  const theme = loadApiTheme && isValidTheme(apiTheme) ? apiTheme : fallbackTheme;
  const textScale = loadApiTheme ? parseTextScale(apiTextScale ?? localTextScale) : localTextScale;
  const textWeight = loadApiTheme
    ? parseTextWeight(apiTextWeight ?? localTextWeight)
    : localTextWeight;
  const highContrast = loadApiTheme ? (apiHighContrast ?? localHighContrast) : localHighContrast;

  // Mirror the server's values into this identity's namespace so the next cold
  // start paints them before the settings request resolves. Without this the
  // cache would only ever hold choices made on this device, and a user who
  // picked their theme elsewhere would flash the default on every load.
  //
  // Only keys the profile actually has a stored preference for are mirrored:
  // the absence of a cached theme is what lets the admin default apply, so
  // writing a resolved-but-unchosen value here would silently pin them to
  // whatever the default happened to be the first time they loaded the app.
  useEffect(() => {
    if (!loadApiTheme || effectiveSettings === undefined) return;
    if (isValidTheme(apiTheme)) appearanceCache.set(storage.KEYS.THEME, apiTheme, cacheOwner);
    if (apiTextScale !== undefined) {
      appearanceCache.set(storage.KEYS.UI_TEXT_SCALE, apiTextScale, cacheOwner);
    }
    if (apiTextWeight !== undefined) {
      appearanceCache.set(storage.KEYS.UI_TEXT_WEIGHT, apiTextWeight, cacheOwner);
    }
    if (apiHighContrast !== undefined) {
      appearanceCache.set(storage.KEYS.UI_HIGH_CONTRAST, String(apiHighContrast), cacheOwner);
    }
  }, [
    loadApiTheme,
    effectiveSettings,
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
      settingMutation.mutate({
        key: SETTING_KEYS.UI_THEME,
        value: newTheme,
        identity: PROFILE_SCOPE,
      });
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
      settingMutation.mutate({
        key: SETTING_KEYS.UI_TEXT_SCALE,
        value,
        identity: PROFILE_SCOPE,
      });
    },
    [settingMutation, cacheOwner],
  );

  const setTextWeight = useCallback(
    (value: TextWeight) => {
      setTextWeightPreference(value);
      applyTextWeightToDOM(value);
      appearanceCache.set(storage.KEYS.UI_TEXT_WEIGHT, value, cacheOwner);
      settingMutation.mutate({
        key: SETTING_KEYS.UI_TEXT_WEIGHT,
        value,
        identity: PROFILE_SCOPE,
      });
    },
    [settingMutation, cacheOwner],
  );

  const setHighContrast = useCallback(
    (value: boolean) => {
      setHighContrastPreference(value);
      applyHighContrastToDOM(value);
      appearanceCache.set(storage.KEYS.UI_HIGH_CONTRAST, String(value), cacheOwner);
      settingMutation.mutate({
        key: SETTING_KEYS.UI_HIGH_CONTRAST,
        value,
        identity: PROFILE_SCOPE,
      });
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
