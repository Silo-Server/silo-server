import { useCallback, useEffect, useRef, useState } from "react";
import { useSettings, useSetSetting } from "@/hooks/queries/settings";
import { useOptionalAuth } from "@/hooks/useAuth";
import { appearanceCacheOwner } from "@/hooks/themePreferences";
import { customThemeCache, storage } from "@/utils/storage";
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

const EMPTY_VARS: ThemeVarOverrides = {};

export function useCustomTheme(): UseCustomThemeResult {
  const auth = useOptionalAuth();
  // Owner of the localStorage warm start; null while auth bootstraps or when
  // nobody is signed in, which keeps the last look on the login screen.
  const cacheOwner = appearanceCacheOwner({
    loading: auth?.loading ?? false,
    user: auth?.user ? { id: auth.user.id } : null,
  });
  const loadApi = cacheOwner !== null;
  // Read once per render, before the effects below re-stamp the cache, so every
  // useCustomTheme instance in this render pass agrees on the answer.
  const cacheTrusted = customThemeCache.isTrusted(cacheOwner);

  // API values
  const { data: apiSettings } = useSettings({ enabled: loadApi });
  const apiVars = apiSettings?.ui_custom_theme_vars;
  const apiCss = apiSettings?.ui_custom_css;
  const settingMutation = useSetSetting();

  // Local draft state (for instant updates without waiting for API)
  const [localVars, setLocalVars] = useState<ThemeVarOverrides>(() =>
    parseVarsJson(customThemeCache.get(storage.KEYS.UI_CUSTOM_THEME_VARS, cacheOwner)),
  );
  const [localCss, setLocalCss] = useState<string>(
    () => customThemeCache.get(storage.KEYS.UI_CUSTOM_CSS, cacheOwner) ?? "",
  );
  const [isDirty, setIsDirty] = useState(false);

  // Debounce timers
  const varsTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const cssTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // Another account's custom theme is never a valid starting point: drop it (and
  // take ownership of the now-empty cache) as soon as we know who is signed in.
  // Declared before the API sync effects so it can never wipe values they just
  // wrote for the new owner.
  useEffect(() => {
    if (cacheOwner === null || cacheTrusted) return;
    customThemeCache.clear(cacheOwner);
    setLocalVars(EMPTY_VARS);
    setLocalCss("");
  }, [cacheOwner, cacheTrusted]);

  // Sync API values into local state when they arrive
  useEffect(() => {
    if (loadApi && apiVars !== undefined) {
      const parsed = parseVarsJson(apiVars);
      setLocalVars(parsed);
      customThemeCache.set(storage.KEYS.UI_CUSTOM_THEME_VARS, JSON.stringify(parsed), cacheOwner);
    }
  }, [loadApi, apiVars, cacheOwner]);

  useEffect(() => {
    if (loadApi && apiCss !== undefined && apiCss !== null) {
      setLocalCss(apiCss);
      customThemeCache.set(storage.KEYS.UI_CUSTOM_CSS, apiCss, cacheOwner);
    }
  }, [loadApi, apiCss, cacheOwner]);

  const persistVars = useCallback(
    (vars: ThemeVarOverrides) => {
      const json = JSON.stringify(vars);
      customThemeCache.set(storage.KEYS.UI_CUSTOM_THEME_VARS, json, cacheOwner);
      settingMutation.mutate({ key: "ui_custom_theme_vars", value: json });
    },
    [settingMutation, cacheOwner],
  );

  const persistCss = useCallback(
    (css: string) => {
      const safe = sanitizeCss(css);
      customThemeCache.set(storage.KEYS.UI_CUSTOM_CSS, safe, cacheOwner);
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
    // Until the cache is proven to be ours, render nothing custom rather than
    // the previous account's tokens and CSS.
    vars: cacheTrusted ? localVars : EMPTY_VARS,
    customCss: cacheTrusted ? localCss : "",
    setVar,
    resetVar,
    setAllVars,
    setCustomCss,
    resetAll,
    importOverrides,
    isDirty,
  };
}
