import { useMemo, type ReactNode } from "react";

import {
  UICustomizationContext,
  type UICustomizationValue,
} from "@/contexts/uiCustomizationContext";
import {
  settingsCapabilitiesSupportAtomicShortcuts,
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { parseCardPresentation, parsePrimaryMenu, parseShortcuts } from "@/lib/uiCustomization";

const UI_CUSTOMIZATION_KEYS = [
  SETTING_KEYS.NAV_PRIMARY_MENU,
  SETTING_KEYS.NAV_SHORTCUTS,
  SETTING_KEYS.UI_CARD_PRESENTATION,
] as const;

export function UICustomizationProvider({ children }: { children: ReactNode }) {
  const capabilitiesQuery = useSettingsCapabilities();
  const isSupported = UI_CUSTOMIZATION_KEYS.every((key) =>
    settingsCapabilitiesSupportKey(capabilitiesQuery.data, key),
  );
  const supportsAtomicShortcuts = settingsCapabilitiesSupportAtomicShortcuts(
    capabilitiesQuery.data,
  );
  const query = useEffectiveSettings({ keys: UI_CUSTOMIZATION_KEYS, enabled: isSupported });
  const isUnavailable =
    (!capabilitiesQuery.isLoading &&
      (capabilitiesQuery.isError || capabilitiesQuery.data === undefined)) ||
    (isSupported && !query.isLoading && (query.isError || query.data === undefined));
  // A disabled query may still expose data cached before a server downgrade or
  // reconnect. Ignore it until the connected server proves support.
  const primaryMenuValue = isSupported
    ? query.data?.[SETTING_KEYS.NAV_PRIMARY_MENU]?.value
    : undefined;
  const shortcutsValue = isSupported ? query.data?.[SETTING_KEYS.NAV_SHORTCUTS]?.value : undefined;
  const cardPresentationValue = isSupported
    ? query.data?.[SETTING_KEYS.UI_CARD_PRESENTATION]?.value
    : undefined;

  const value = useMemo<UICustomizationValue>(
    () => ({
      cardPresentation: parseCardPresentation(cardPresentationValue),
      cardPresentationSource: isSupported
        ? (query.data?.[SETTING_KEYS.UI_CARD_PRESENTATION]?.source ?? "default")
        : "default",
      primaryMenu: parsePrimaryMenu(primaryMenuValue),
      primaryMenuSource: isSupported
        ? (query.data?.[SETTING_KEYS.NAV_PRIMARY_MENU]?.source ?? "default")
        : "default",
      shortcuts: parseShortcuts(shortcutsValue),
      isSupported,
      supportsAtomicShortcuts,
      isLoading: capabilitiesQuery.isLoading || (isSupported && query.isLoading),
      isUnavailable,
    }),
    [
      capabilitiesQuery.isLoading,
      cardPresentationValue,
      isUnavailable,
      isSupported,
      primaryMenuValue,
      query.data,
      query.isLoading,
      shortcutsValue,
      supportsAtomicShortcuts,
    ],
  );

  return (
    <UICustomizationContext.Provider value={value}>{children}</UICustomizationContext.Provider>
  );
}
