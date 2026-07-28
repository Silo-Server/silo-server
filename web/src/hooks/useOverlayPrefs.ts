import { useMemo, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useEffectiveSettings, useSetSettingValue } from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { settingsKeys } from "@/hooks/queries/keys";
import { storage } from "@/utils/storage";
import { parseOverlayPrefs, serializeOverlayPrefs, type CardOverlayPrefs } from "@/lib/overlays";

/** `ui.card_overlays` is profile-wide in the contract (no device scope). */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

const OVERLAY_KEYS = [SETTING_KEYS.UI_CARD_OVERLAYS] as const;

interface OverlayConfig {
  enabled: boolean;
  defaults?: string;
}

// The admin kill switch and server-wide defaults live in server_settings, not
// the user-settings contract, so this endpoint stays alongside the canonical
// values API.
function useOverlayConfig() {
  return useQuery({
    queryKey: [...settingsKeys.all, "overlay-config"] as const,
    queryFn: () => api<OverlayConfig>("/settings/overlay-config"),
    staleTime: 60_000,
  });
}

export function useOverlayPrefs() {
  // The effective endpoint requires a profile header; without one the user
  // has no stored preference and the admin defaults apply on their own.
  const hasProfile = Boolean(storage.get(storage.KEYS.PROFILE_ID));
  const { data: effective, isLoading: userLoading } = useEffectiveSettings({
    keys: OVERLAY_KEYS,
    enabled: hasProfile,
  });
  const { data: config, isLoading: configLoading } = useOverlayConfig();
  const setValue = useSetSettingValue();

  // The contract default is null — "no preference expressed" — which is what
  // lets the server-wide admin default apply; a stored value wins outright.
  const userValue = effective?.[SETTING_KEYS.UI_CARD_OVERLAYS]?.value ?? null;

  const prefs = useMemo(() => {
    // User setting takes priority; fall back to admin defaults
    const source = userValue ?? config?.defaults ?? null;
    return parseOverlayPrefs(source);
  }, [userValue, config?.defaults]);

  // Admin kill switch: if disabled server-wide, return null prefs
  const enabled = config?.enabled !== false;

  const setPrefs = useCallback(
    (next: CardOverlayPrefs) => {
      // Avoid a network round-trip and downstream re-render cascade when
      // the user toggles a control to its current value. Comparison goes
      // through the parser so key ordering in the stored JSON is irrelevant.
      if (
        userValue != null &&
        serializeOverlayPrefs(parseOverlayPrefs(userValue)) === serializeOverlayPrefs(next)
      ) {
        return;
      }
      setValue.mutate({ key: SETTING_KEYS.UI_CARD_OVERLAYS, value: next, identity: PROFILE_SCOPE });
    },
    [userValue, setValue],
  );

  // While either query is in flight, report null prefs instead of built-in
  // defaults: rendering defaults first would flash badges that vanish (or
  // change) the moment the user's own config or the admin kill switch loads.
  const isLoading = (hasProfile && userLoading) || configLoading;

  return {
    prefs: enabled && !isLoading ? prefs : null,
    setPrefs,
    isLoading,
    enabled,
  };
}
