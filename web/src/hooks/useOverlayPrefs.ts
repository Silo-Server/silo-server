import { useMemo, useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiClientError } from "@/api/client";
import {
  effectiveSettingsQueryKey,
  isDefinitiveSettingMutationRejection,
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
  type EffectiveSettingsMap,
} from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { settingsKeys } from "@/hooks/queries/keys";
import { storage } from "@/utils/storage";
import { parseOverlayPrefs, serializeOverlayPrefs, type CardOverlayPrefs } from "@/lib/overlays";
import {
  normalizeCardQuickActionMode,
  type EnabledCardQuickActionMode,
} from "@/lib/cardQuickActions";

/** Card overlay preferences are profile-wide in the contract (no device scope). */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

const OVERLAY_KEYS = [
  SETTING_KEYS.UI_CARD_OVERLAYS,
  SETTING_KEYS.UI_CARD_OVERLAYS_ENABLED,
  SETTING_KEYS.UI_CARD_QUICK_ACTIONS,
  SETTING_KEYS.UI_CARD_QUICK_ACTIONS_ENABLED,
] as const;

interface OverlayConfig {
  enabled: boolean;
  defaults?: string;
  quick_actions_enabled?: boolean;
  quick_actions_default?: string;
}

// Overlay booleans are inherit-with-override, not a policy gate: the server
// setting is only the default for profiles that have not chosen, and an
// explicit profile choice wins in either direction.
function inheritBoolean(userValue: unknown, serverDefault: boolean): boolean {
  return typeof userValue === "boolean" ? userValue : serverDefault;
}

// The server-wide overlay defaults live in server_settings, not the
// user-settings contract, so this endpoint stays alongside the canonical
// values API.
function useOverlayConfig() {
  return useQuery({
    queryKey: settingsKeys.overlayConfig(),
    queryFn: () => api<OverlayConfig>("/settings/overlay-config"),
    staleTime: 60_000,
  });
}

export function useOverlayPrefs() {
  // The effective endpoint requires a profile header; without one the user
  // has no stored preference and the admin defaults apply on their own.
  const profileId = storage.get(storage.KEYS.PROFILE_ID);
  const hasProfile = Boolean(profileId);
  const { data: effective, isLoading: userLoading } = useEffectiveSettings({
    keys: OVERLAY_KEYS,
    enabled: hasProfile,
  });
  const { data: config, isLoading: configLoading } = useOverlayConfig();
  const { mutate: setSettingValue } = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const queryClient = useQueryClient();
  const effectiveQueryKey = useMemo(
    () => effectiveSettingsQueryKey({ keys: OVERLAY_KEYS, profileId: profileId ?? undefined }),
    // The active profile is part of effectiveSettingsQueryKey. Recompute the
    // target when profile selection changes rather than writing into the
    // previous profile's cache entry.
    [profileId],
  );

  const setProfileValue = useCallback(
    (key: SettingKey, value: unknown) => {
      // Writing a stored value back unchanged is a no-op: skip the network
      // round-trip and the downstream re-render cascade.
      if (effective?.[key]?.value === value) return;
      queryClient.setQueryData<EffectiveSettingsMap>(effectiveQueryKey, (current) => ({
        ...current,
        [key]: { key, value, source: "profile", scope: "profile" },
      }));
      setSettingValue(
        { key, value, identity: PROFILE_SCOPE },
        {
          // The shared mutation invalidates on success and ambiguous errors.
          // A definitive rejection never reached storage, so refetch instead of
          // restoring a snapshot: rapid successive writes make any snapshot an
          // unpersisted optimistic value, while a refetch reconciles the cache
          // with what the server actually stored.
          onError: (error) => {
            if (isDefinitiveSettingMutationRejection(error)) {
              void queryClient.invalidateQueries({ queryKey: effectiveQueryKey });
            }
          },
        },
      );
    },
    [effective, effectiveQueryKey, queryClient, setSettingValue],
  );

  // The contract default is null — "no preference expressed" — which is what
  // lets the server-wide admin default apply; a stored value wins outright.
  const userValue = effective?.[SETTING_KEYS.UI_CARD_OVERLAYS]?.value ?? null;
  const overlaysEnabledUserValue = effective?.[SETTING_KEYS.UI_CARD_OVERLAYS_ENABLED]?.value;
  const quickActionUserValue = effective?.[SETTING_KEYS.UI_CARD_QUICK_ACTIONS]?.value ?? null;
  const quickActionsEnabledUserValue =
    effective?.[SETTING_KEYS.UI_CARD_QUICK_ACTIONS_ENABLED]?.value;

  const prefs = useMemo(() => {
    // User setting takes priority; fall back to admin defaults
    const source = userValue ?? config?.defaults ?? null;
    return parseOverlayPrefs(source);
  }, [userValue, config?.defaults]);

  // Absent server config (including while it loads), overlays are on — the
  // shipped default — and quick actions are off.
  const overlaysEnabled = inheritBoolean(overlaysEnabledUserValue, config?.enabled !== false);
  const quickActionsEnabled = inheritBoolean(
    quickActionsEnabledUserValue,
    config?.quick_actions_enabled === true,
  );
  const configuredQuickActionMode = normalizeCardQuickActionMode(
    quickActionUserValue ?? config?.quick_actions_default,
  );

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
      setProfileValue(SETTING_KEYS.UI_CARD_OVERLAYS, next);
    },
    [userValue, setProfileValue],
  );

  const setOverlaysEnabled = useCallback(
    (next: boolean) => setProfileValue(SETTING_KEYS.UI_CARD_OVERLAYS_ENABLED, next),
    [setProfileValue],
  );

  const setQuickActionMode = useCallback(
    (next: EnabledCardQuickActionMode) => {
      // Compare against the mode the control displays, not a differently
      // normalized reading of the stored value: an unrecognized stored value
      // displays the admin default, which must stay selectable.
      if (quickActionUserValue != null && configuredQuickActionMode === next) return;
      setProfileValue(SETTING_KEYS.UI_CARD_QUICK_ACTIONS, next);
    },
    [configuredQuickActionMode, quickActionUserValue, setProfileValue],
  );

  const setQuickActionsEnabled = useCallback(
    (next: boolean) => setProfileValue(SETTING_KEYS.UI_CARD_QUICK_ACTIONS_ENABLED, next),
    [setProfileValue],
  );

  const resetPrefs = useCallback(async () => {
    await Promise.all(
      OVERLAY_KEYS.map(async (key) => {
        try {
          await clearValue.mutateAsync({ key, identity: PROFILE_SCOPE });
        } catch (error) {
          // A missing scoped value already means this part of the preference
          // is inheriting from the server default.
          if (!(error instanceof ApiClientError && error.status === 404)) throw error;
        }
      }),
    );
  }, [clearValue]);

  const hasOverride = OVERLAY_KEYS.some((key) => effective?.[key]?.source === "profile");

  // While either query is in flight, report null prefs instead of built-in
  // defaults: rendering defaults first would flash badges that vanish (or
  // change) the moment the user's own config or the server default loads.
  const isLoading = (hasProfile && userLoading) || configLoading;

  return {
    prefs: overlaysEnabled && !isLoading ? prefs : null,
    setPrefs,
    overlaysEnabled,
    setOverlaysEnabled,
    quickActionMode:
      quickActionsEnabled && !isLoading ? configuredQuickActionMode : ("none" as const),
    quickActionPreference: configuredQuickActionMode,
    setQuickActionMode,
    quickActionsEnabled,
    setQuickActionsEnabled,
    resetPrefs,
    hasOverride,
    isResetting: clearValue.isPending,
    isLoading,
  };
}
