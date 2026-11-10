import { formatLanguageWhenLoaded } from "@/lib/languageNamesLoader";
import { t } from "@/i18n";
import type { OverlayDef } from "../types";

function formatRuntime(minutes: number | null | undefined): string | null {
  if (!minutes || minutes <= 0) return null;
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

export const METADATA_OVERLAYS: readonly OverlayDef[] = [
  {
    id: "year",
    category: "metadata",
    get label() {
      return t("web.settings.overlays.badge.year.label");
    },
    get description() {
      return t("web.settings.overlays.badge.year.description");
    },
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconCapable: false,
    getValue: (d) => (d.year && d.year > 0 ? String(d.year) : null),
  },
  {
    id: "runtime",
    category: "metadata",
    get label() {
      return t("web.settings.overlays.badge.runtime.label");
    },
    get description() {
      return t("web.settings.overlays.badge.runtime.description");
    },
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconId: "clock",
    iconCapable: true,
    getValue: (d) => formatRuntime(d.runtime),
  },
  {
    id: "original_language",
    category: "metadata",
    get label() {
      return t("web.settings.overlays.badge.originalLanguage.label");
    },
    get description() {
      return t("web.settings.overlays.badge.originalLanguage.description");
    },
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconId: "globe",
    iconCapable: true,
    getValue: (d) => (d.original_language ? formatLanguageWhenLoaded(d.original_language) : null),
    getValueKey: (d) => d.original_language || null,
  },
  {
    id: "studio",
    category: "metadata",
    get label() {
      return t("web.settings.overlays.badge.studio.label");
    },
    get description() {
      return t("web.settings.overlays.badge.studio.description");
    },
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "building",
    iconCapable: true,
    getValue: (d) => d.studio ?? null,
    getValueKey: (d) => d.studio || null,
  },
  {
    id: "network",
    category: "metadata",
    get label() {
      return t("web.settings.overlays.badge.network.label");
    },
    get description() {
      return t("web.settings.overlays.badge.network.description");
    },
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "tv",
    iconCapable: true,
    getValue: (d) => d.network ?? null,
    getValueKey: (d) => d.network || null,
  },
];
