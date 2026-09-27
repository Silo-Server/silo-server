import { formatLanguageWhenLoaded } from "@/lib/languageNamesLoader";
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
    label: "Year",
    description: "Release year",
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconCapable: false,
    getValue: (d) => (d.year && d.year > 0 ? String(d.year) : null),
  },
  {
    id: "runtime",
    category: "metadata",
    label: "Runtime",
    description: "Item runtime in hours and minutes",
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconId: "clock",
    iconCapable: true,
    getValue: (d) => formatRuntime(d.runtime),
  },
  {
    id: "original_language",
    category: "metadata",
    label: "Language",
    description: "Original language of the content",
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconId: "globe",
    iconCapable: true,
    getValue: (d) => (d.original_language ? formatLanguageWhenLoaded(d.original_language) : null),
  },
  {
    id: "studio",
    category: "metadata",
    label: "Studio",
    description: "Primary production studio (movies)",
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "building",
    iconCapable: true,
    getValue: (d) => d.studio ?? null,
  },
  {
    id: "network",
    category: "metadata",
    label: "Network",
    description: "Primary network (series)",
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "tv",
    iconCapable: true,
    getValue: (d) => d.network ?? null,
  },
];
