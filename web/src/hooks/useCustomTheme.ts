import { useCallback, useEffect, useRef, useState } from "react";
import { useEffectiveSettings, useSetSettingValue } from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { useAppearanceCacheOwner } from "@/hooks/themePreferences";
import { appearanceCache, storage } from "@/utils/storage";
import { parseVarsJson } from "@/lib/themeExport";
import { sanitizeCss } from "@/lib/cssSanitizer";
import type { ThemeToken } from "@/lib/themeTokens";

export type ThemeVarOverrides = Partial<Record<ThemeToken, string>>;

/** Both custom-theme keys are profile-wide in the contract (no device scope). */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

const CUSTOM_THEME_KEYS = [SETTING_KEYS.UI_CUSTOM_THEME_VARS, SETTING_KEYS.UI_CUSTOM_CSS] as const;

/**
 * The canonical API stores `ui.custom_theme_vars` as a JSON object; the local
 * cache still mirrors it as a JSON string. Accept either shape so a value that
 * round-tripped through the legacy string endpoints keeps parsing.
 */
function parseVarsValue(value: unknown): ThemeVarOverrides {
  if (typeof value === "string") return parseVarsJson(value);
  return typeof value === "object" && value !== null ? (value as ThemeVarOverrides) : {};
}

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
  // Owner of the localStorage warm start; null while auth bootstraps, when
  // nobody is signed in, or before a profile is selected, which keeps the last
  // look on the login and profile screens.
  const cacheOwner = useAppearanceCacheOwner();
  const loadApi = cacheOwner !== null;

  // API values, one batched effective read for both keys. A source of
  // "default" means this profile has stored nothing, which must leave the
  // local draft alone rather than clearing it.
  const { data: effectiveSettings } = useEffectiveSettings({
    keys: CUSTOM_THEME_KEYS,
    enabled: loadApi,
  });
  const varsSetting = effectiveSettings?.[SETTING_KEYS.UI_CUSTOM_THEME_VARS];
  const cssSetting = effectiveSettings?.[SETTING_KEYS.UI_CUSTOM_CSS];
  const settingMutation = useSetSettingValue();

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

  // A debounced persist closes over the identity it was scheduled for. Drop
  // any pending write when the owner changes — a different account, or a
  // sibling profile on the same account — or the editor unmounts: otherwise a
  // timer armed by the previous identity fires against the new one's session
  // and stores their predecessor's CSS under their name.
  useEffect(() => {
    return () => {
      clearTimeout(varsTimerRef.current);
      clearTimeout(cssTimerRef.current);
    };
  }, [cacheOwner]);

  // This state was seeded for whoever was signed in when the hook mounted.
  // Re-seed from the new owner's namespace when the identity changes, so an
  // account or profile switch without a reload stops rendering the previous
  // identity's tokens even before their own settings arrive. Adjusted during
  // render, so there is no frame in which the new identity sees the old one's
  // theme.
  const [seededOwner, setSeededOwner] = useState(cacheOwner);
  if (seededOwner !== cacheOwner) {
    setSeededOwner(cacheOwner);
    setLocalVars(parseVarsJson(appearanceCache.get(storage.KEYS.UI_CUSTOM_THEME_VARS, cacheOwner)));
    setLocalCss(appearanceCache.get(storage.KEYS.UI_CUSTOM_CSS, cacheOwner) ?? "");
    setIsDirty(false);
  }

  // Sync stored API values into local state when they arrive.
  //
  // A source of "default" is an answer, not silence: the profile has no stored
  // custom theme, because it never had one or because another client just
  // deleted it. The cached copy has to go with it, or a theme removed
  // elsewhere would keep painting on this browser forever. An unsaved local
  // draft is left alone — the user is mid-edit, and their work is not the
  // server's to discard.
  useEffect(() => {
    if (!loadApi || varsSetting === undefined) return;
    if (varsSetting.source === "default") {
      if (isDirty) return;
      setLocalVars({});
      appearanceCache.remove(storage.KEYS.UI_CUSTOM_THEME_VARS, cacheOwner);
      return;
    }
    const parsed = parseVarsValue(varsSetting.value);
    setLocalVars(parsed);
    appearanceCache.set(storage.KEYS.UI_CUSTOM_THEME_VARS, JSON.stringify(parsed), cacheOwner);
  }, [loadApi, varsSetting, cacheOwner, isDirty]);

  useEffect(() => {
    if (!loadApi || cssSetting === undefined) return;
    if (cssSetting.source === "default") {
      if (isDirty) return;
      setLocalCss("");
      appearanceCache.remove(storage.KEYS.UI_CUSTOM_CSS, cacheOwner);
      return;
    }
    if (typeof cssSetting.value !== "string") return;
    setLocalCss(cssSetting.value);
    appearanceCache.set(storage.KEYS.UI_CUSTOM_CSS, cssSetting.value, cacheOwner);
  }, [loadApi, cssSetting, cacheOwner, isDirty]);

  const persistVars = useCallback(
    (vars: ThemeVarOverrides) => {
      appearanceCache.set(storage.KEYS.UI_CUSTOM_THEME_VARS, JSON.stringify(vars), cacheOwner);
      settingMutation.mutate({
        key: SETTING_KEYS.UI_CUSTOM_THEME_VARS,
        value: vars,
        identity: PROFILE_SCOPE,
      });
    },
    [settingMutation, cacheOwner],
  );

  const persistCss = useCallback(
    (css: string) => {
      const safe = sanitizeCss(css);
      appearanceCache.set(storage.KEYS.UI_CUSTOM_CSS, safe, cacheOwner);
      settingMutation.mutate({
        key: SETTING_KEYS.UI_CUSTOM_CSS,
        value: safe,
        identity: PROFILE_SCOPE,
      });
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
