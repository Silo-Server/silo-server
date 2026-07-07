import { useMemo, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useSetting, useSetSetting } from "@/hooks/queries/settings";
import { settingsKeys } from "@/hooks/queries/keys";
import { parseOverlayPrefs, serializeOverlayPrefs, type CardOverlayPrefs } from "@/lib/overlays";

const SETTING_KEY = "card_overlays";

interface OverlayConfig {
  enabled: boolean;
  defaults?: string;
}

function useOverlayConfig() {
  return useQuery({
    queryKey: [...settingsKeys.all, "overlay-config"] as const,
    queryFn: () => api<OverlayConfig>("/settings/overlay-config"),
    staleTime: 60_000,
  });
}

export function useOverlayPrefs() {
  const { data: raw, isLoading: userLoading } = useSetting(SETTING_KEY);
  const { data: config, isLoading: configLoading } = useOverlayConfig();
  const setSetting = useSetSetting();

  const prefs = useMemo(() => {
    // User setting takes priority; fall back to admin defaults
    const source = raw ?? config?.defaults ?? null;
    return parseOverlayPrefs(source);
  }, [raw, config?.defaults]);

  // Admin kill switch: if disabled server-wide, return null prefs
  const enabled = config?.enabled !== false;

  const setPrefs = useCallback(
    (next: CardOverlayPrefs) => {
      const serialized = serializeOverlayPrefs(next);
      // Avoid a network round-trip and downstream re-render cascade when
      // the user toggles a control to its current value.
      if (raw === serialized) return;
      setSetting.mutate({ key: SETTING_KEY, value: serialized });
    },
    [raw, setSetting],
  );

  // While either query is in flight, report null prefs instead of built-in
  // defaults: rendering defaults first would flash badges that vanish (or
  // change) the moment the user's own config or the admin kill switch loads.
  const isLoading = userLoading || configLoading;

  return {
    prefs: enabled && !isLoading ? prefs : null,
    setPrefs,
    isLoading,
    enabled,
  };
}
