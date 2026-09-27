import { createContext, useContext, useEffect, useState, useCallback } from "react";
import type { ReactNode } from "react";
import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { useProfileDefaultWriter } from "@/hooks/queries/profileDefaults";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { appearanceCache, storage } from "@/utils/storage";
import type { StorageKey } from "@/utils/storage";
import {
  parseHighContrast,
  parseTextScale,
  parseTextWeight,
  useAppearanceCacheOwner,
} from "@/hooks/themePreferences";
import type { TextScale, TextWeight } from "@/hooks/themePreferences";

/**
 * The per-profile readability preferences. The colour theme is not one of
 * them: Silo paints a single base theme (Cinema Dark, the static
 * html[data-theme] in index.html), and only the admin customizes it,
 * server-wide.
 */
interface ThemeContextValue {
  textScale: TextScale;
  setTextScale: (value: TextScale) => void;
  textWeight: TextWeight;
  setTextWeight: (value: TextWeight) => void;
  highContrast: boolean;
  setHighContrast: (value: boolean) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

/**
 * The three appearance keys this provider needs, fetched in one batched
 * effective read rather than a query per key.
 */
const APPEARANCE_KEYS = [
  SETTING_KEYS.UI_TEXT_SCALE,
  SETTING_KEYS.UI_TEXT_WEIGHT,
  SETTING_KEYS.UI_HIGH_CONTRAST,
] as const;

/*
 * These keys are profile-scoped with an optional per-device override; the
 * effective read already resolves profile_device over profile, and these
 * setters write the profile-wide value. There is no device-override UI for
 * appearance on the web, but a device row can still exist — the settings
 * migration converts legacy user_device_settings rows into profile_device
 * ones, other clients write them, and the admin surface can too — so the
 * shared writer clears one alongside the profile write. Without that a
 * migrated override would shadow every later choice with no affordance to
 * remove it, and the control would snap straight back.
 */

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
  const loadApi = cacheOwner !== null;

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
  // read for all three appearance keys. The server resolves the profile_device
  // override over the profile value, so this needs no per-scope reads.
  //
  // A source of "default" means the profile has stored no choice of its own;
  // that must stay distinguishable from an explicit choice so the local warm
  // start keeps its layering, so those values are dropped here rather than
  // treated as the profile's preference.
  const { data: effectiveSettings } = useEffectiveSettings({
    keys: APPEARANCE_KEYS,
    enabled: loadApi,
  });
  const storedValue = (key: (typeof APPEARANCE_KEYS)[number]): unknown => {
    const setting = effectiveSettings?.[key];
    return setting !== undefined && setting.source !== "default" ? setting.value : undefined;
  };
  const rawApiTextScale = storedValue(SETTING_KEYS.UI_TEXT_SCALE);
  const apiTextScale = typeof rawApiTextScale === "string" ? rawApiTextScale : undefined;
  const rawApiTextWeight = storedValue(SETTING_KEYS.UI_TEXT_WEIGHT);
  const apiTextWeight = typeof rawApiTextWeight === "string" ? rawApiTextWeight : undefined;
  const rawApiHighContrast = storedValue(SETTING_KEYS.UI_HIGH_CONTRAST);
  const apiHighContrast = typeof rawApiHighContrast === "boolean" ? rawApiHighContrast : undefined;
  const { save: saveProfileDefault } = useProfileDefaultWriter(effectiveSettings);

  // The server's value is this profile's own stored choice, so it wins outright
  // whenever it is present. It is deliberately not compared against the local
  // cache: the effect below mirrors the server's value into that very cache,
  // so any such comparison stops holding after the first render.
  const textScale = loadApi
    ? parseTextScale(apiTextScale ?? textScalePreference)
    : textScalePreference;
  const textWeight = loadApi
    ? parseTextWeight(apiTextWeight ?? textWeightPreference)
    : textWeightPreference;
  const highContrast = loadApi
    ? (apiHighContrast ?? highContrastPreference)
    : highContrastPreference;

  // Mirror the server's values into this identity's namespace so the next cold
  // start paints them before the settings request resolves. Without this the
  // cache would only ever hold choices made on this device, and a user who
  // chose elsewhere would flash the default on every load.
  //
  // Only keys the profile actually has a stored preference for are mirrored:
  // writing a resolved-but-unchosen value here would silently pin the profile
  // to whatever the default happened to be the first time it loaded the app.
  // The mirror runs both ways. A key the server answered for but has no stored
  // value at — source "default" — is a key this profile has no preference for,
  // whether it never chose one or another client just deleted it. Its cached
  // value has to go, or a removal made elsewhere would never take effect here
  // and the stale entry would keep winning the fallback below.
  //
  // Only an explicit "default" clears. A key simply absent from the response
  // is not an answer about it, and treating silence as a deletion would drop
  // a good warm start on any partial read. Only this owner's namespace is
  // touched; another identity's warm start is not ours to clear.
  useEffect(() => {
    if (!loadApi || effectiveSettings === undefined) return;
    const mirror = (
      key: (typeof APPEARANCE_KEYS)[number],
      cacheKey: StorageKey,
      value: string | undefined,
    ) => {
      if (value !== undefined) {
        appearanceCache.set(cacheKey, value, cacheOwner);
        return false;
      }
      if (effectiveSettings[key]?.source !== "default") return false;
      appearanceCache.remove(cacheKey, cacheOwner);
      return true;
    };
    const cleared = {
      textScale: mirror(SETTING_KEYS.UI_TEXT_SCALE, storage.KEYS.UI_TEXT_SCALE, apiTextScale),
      textWeight: mirror(SETTING_KEYS.UI_TEXT_WEIGHT, storage.KEYS.UI_TEXT_WEIGHT, apiTextWeight),
      highContrast: mirror(
        SETTING_KEYS.UI_HIGH_CONTRAST,
        storage.KEYS.UI_HIGH_CONTRAST,
        apiHighContrast === undefined ? undefined : String(apiHighContrast),
      ),
    };

    // The local state was seeded from those same cached values, and the render
    // below falls back to it whenever the server has none — so clearing the
    // cache alone would leave the removed value on screen until a reload.
    // Parsing undefined yields each preference's own default, and React bails
    // out when the value is already that, so this cannot loop.
    if (cleared.textScale) setTextScalePreference(parseTextScale(undefined));
    if (cleared.textWeight) setTextWeightPreference(parseTextWeight(undefined));
    if (cleared.highContrast) setHighContrastPreference(parseHighContrast(undefined));
  }, [loadApi, effectiveSettings, apiTextScale, apiTextWeight, apiHighContrast, cacheOwner]);

  useEffect(() => {
    applyTextScaleToDOM(textScale);
  }, [textScale]);

  useEffect(() => {
    applyTextWeightToDOM(textWeight);
  }, [textWeight]);

  useEffect(() => {
    applyHighContrastToDOM(highContrast);
  }, [highContrast]);

  const setTextScale = useCallback(
    (value: TextScale) => {
      setTextScalePreference(value);
      applyTextScaleToDOM(value);
      appearanceCache.set(storage.KEYS.UI_TEXT_SCALE, value, cacheOwner);
      void saveProfileDefault(SETTING_KEYS.UI_TEXT_SCALE, value);
    },
    [saveProfileDefault, cacheOwner],
  );

  const setTextWeight = useCallback(
    (value: TextWeight) => {
      setTextWeightPreference(value);
      applyTextWeightToDOM(value);
      appearanceCache.set(storage.KEYS.UI_TEXT_WEIGHT, value, cacheOwner);
      void saveProfileDefault(SETTING_KEYS.UI_TEXT_WEIGHT, value);
    },
    [saveProfileDefault, cacheOwner],
  );

  const setHighContrast = useCallback(
    (value: boolean) => {
      setHighContrastPreference(value);
      applyHighContrastToDOM(value);
      appearanceCache.set(storage.KEYS.UI_HIGH_CONTRAST, String(value), cacheOwner);
      void saveProfileDefault(SETTING_KEYS.UI_HIGH_CONTRAST, value);
    },
    [saveProfileDefault, cacheOwner],
  );

  return (
    <ThemeContext
      value={{
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
