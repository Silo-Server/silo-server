import { createContext } from "react";

import {
  DEFAULT_CARD_PRESENTATION,
  type CardPresentation,
  type PrimaryMenuDocument,
  type ShortcutDocument,
} from "@/lib/uiCustomization";
import type { SettingSource } from "@/hooks/queries/settingValues";

export interface UICustomizationValue {
  cardPresentation: CardPresentation;
  cardPresentationSource: SettingSource;
  primaryMenu: PrimaryMenuDocument | null;
  primaryMenuSource: SettingSource;
  shortcuts: ShortcutDocument;
  /** All revision-5 customization definitions are understood by this server. */
  isSupported: boolean;
  /** The server can merge shortcut item mutations without lost updates. */
  supportsAtomicShortcuts: boolean;
  isLoading: boolean;
  /** Capability or effective-value loading failed, so whole-document editors must stay closed. */
  isUnavailable: boolean;
}

export const UICustomizationContext = createContext<UICustomizationValue>({
  cardPresentation: DEFAULT_CARD_PRESENTATION,
  cardPresentationSource: "default",
  primaryMenu: null,
  primaryMenuSource: "default",
  shortcuts: { items: [] },
  isSupported: false,
  supportsAtomicShortcuts: false,
  isLoading: false,
  isUnavailable: false,
});
