import { useCallback, useEffect, useRef, useState } from "react";
import { useSettings, useSetSetting } from "@/hooks/queries/settings";
import { useAppearanceCacheOwner } from "@/hooks/themePreferences";
import { appearanceCache, storage } from "@/utils/storage";
import { parseVarsJson } from "@/lib/themeExport";
import { sanitizeCss } from "@/lib/cssSanitizer";
import type { ThemeToken } from "@/lib/themeTokens";

export type ThemeVarOverrides = Partial<Record<ThemeToken, string>>;

interface UseCustomThemeResult {
  vars: ThemeVarOverrides;
  customCss: string;
  /** Update a single token (instant, debounced persist). */
  setVar: (token: ThemeToken, value: string) => void;
  /** Remove a single token override. */
  resetVar: (token: ThemeToken) => void;
  /** Replace all variable overrides at once. */
  setAllVars: (vars: ThemeVarOverrides) => void;
  /** Update the raw CSS. */
  setCustomCss: (css: string) => void;
  /** Reset all custom overrides. */
  resetAll: () => void;
  /** Import a full set of overrides (from file or catalog). */
  importOverrides: (vars: ThemeVarOverrides, css: string) => void;
  /** Whether local state differs from last-persisted state. */
  isDirty: boolean;
}

export function useCustomTheme(): UseCustomThemeResult {
  // Owner of the localStorage warm start; null while auth bootstraps or when
  // nobody is signed in, which keeps the last look on the login screen.
  const cacheOwner = useAppearanceCacheOwner();
  const loadApi = cacheOwner !== null;

  // API values
  const { data: apiSettings } = useSettings({ enabled: loadApi });
  const apiVars = apiSettings?.ui_custom_theme_vars;
  const apiCss = apiSettings?.ui_custom_css;
  const settingMutation = useSetSetting();

  // Local draft state (for instant updates without waiting for API)
  const [localVars, setLocalVars] = useState<ThemeVarOverrides>(() =>
    parseVarsJson(appearanceCache.get(storage.KEYS.UI_CUSTOM_THEME_VARS, cacheOwner)),
  );
  const [localCss, setLocalCss] = useState<string>(
    () => appearanceCache.get(storage.KEYS.UI_CUSTOM_CSS, cacheOwner) ?? "",
  );
  const [isDirty, setIsDirty] = useState(false);

  // Debounce timers
  const varsTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const cssTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // A debounced persist closes over the account it was scheduled for. Drop any
  // pending write when the account changes or the editor unmounts: otherwise a
  // timer armed by the previous account fires against the new account's session
  // and stores their predecessor's CSS under their name.
  useEffect(() => {
    return () => {
      clearTimeout(varsTimerRef.current);
      clearTimeout(cssTimerRef.current);
    };
  }, [cacheOwner]);

  // This state was seeded for whoever was signed in when the hook mounted.
  // Re-seed from the new owner's namespace when the account changes, so an
  // account switch without a reload stops rendering the previous account's
  // tokens even before their own settings arrive. Adjusted during render, so
  // there is no frame in which the new account sees the old one's theme.
  const [seededOwner, setSeededOwner] = useState(cacheOwner);
  if (seededOwner !== cacheOwner) {
    setSeededOwner(cacheOwner);
    setLocalVars(parseVarsJson(appearanceCache.get(storage.KEYS.UI_CUSTOM_THEME_VARS, cacheOwner)));
    setLocalCss(appearanceCache.get(storage.KEYS.UI_CUSTOM_CSS, cacheOwner) ?? "");
    setIsDirty(false);
  }

  // Sync API values into local state when they arrive
  useEffect(() => {
    if (loadApi && apiVars !== undefined) {
      const parsed = parseVarsJson(apiVars);
      setLocalVars(parsed);
      appearanceCache.set(storage.KEYS.UI_CUSTOM_THEME_VARS, JSON.stringify(parsed), cacheOwner);
    }
  }, [loadApi, apiVars, cacheOwner]);

  useEffect(() => {
    if (loadApi && apiCss !== undefined && apiCss !== null) {
      setLocalCss(apiCss);
      appearanceCache.set(storage.KEYS.UI_CUSTOM_CSS, apiCss, cacheOwner);
    }
  }, [loadApi, apiCss, cacheOwner]);

  const persistVars = useCallback(
    (vars: ThemeVarOverrides) => {
      const json = JSON.stringify(vars);
      appearanceCache.set(storage.KEYS.UI_CUSTOM_THEME_VARS, json, cacheOwner);
      settingMutation.mutate({ key: "ui_custom_theme_vars", value: json });
    },
    [settingMutation, cacheOwner],
  );

  const persistCss = useCallback(
    (css: string) => {
      const safe = sanitizeCss(css);
      appearanceCache.set(storage.KEYS.UI_CUSTOM_CSS, safe, cacheOwner);
      settingMutation.mutate({ key: "ui_custom_css", value: safe });
    },
    [settingMutation, cacheOwner],
  );

  const setVar = useCallback(
    (token: ThemeToken, value: string) => {
      const next = { ...localVars, [token]: value };
      setLocalVars(next);
      setIsDirty(true);
      clearTimeout(varsTimerRef.current);
      varsTimerRef.current = setTimeout(() => {
        persistVars(next);
        setIsDirty(false);
      }, 500);
    },
    [localVars, persistVars],
  );

  const resetVar = useCallback(
    (token: ThemeToken) => {
      const next = { ...localVars };
      delete next[token];
      setLocalVars(next);
      persistVars(next);
      setIsDirty(false);
    },
    [localVars, persistVars],
  );

  const setAllVars = useCallback(
    (vars: ThemeVarOverrides) => {
      setLocalVars(vars);
      persistVars(vars);
      setIsDirty(false);
    },
    [persistVars],
  );

  const setCustomCss = useCallback(
    (css: string) => {
      setLocalCss(css);
      setIsDirty(true);
      clearTimeout(cssTimerRef.current);
      cssTimerRef.current = setTimeout(() => {
        persistCss(css);
        setIsDirty(false);
      }, 1000);
    },
    [persistCss],
  );

  const resetAll = useCallback(() => {
    setLocalVars({});
    setLocalCss("");
    persistVars({});
    persistCss("");
    setIsDirty(false);
  }, [persistVars, persistCss]);

  const importOverrides = useCallback(
    (vars: ThemeVarOverrides, css: string) => {
      setLocalVars(vars);
      setLocalCss(css);
      persistVars(vars);
      persistCss(css);
      setIsDirty(false);
    },
    [persistVars, persistCss],
  );

  return {
    vars: localVars,
    customCss: localCss,
    setVar,
    resetVar,
    setAllVars,
    setCustomCss,
    resetAll,
    importOverrides,
    isDirty,
  };
}
