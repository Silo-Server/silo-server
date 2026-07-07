import type { OverlayDef } from "../types";

// "Ribbon" overlays — status/award badges that sit in the visual register
// of corner ribbons. They're shipped as registry entries today, but most
// rely on data sources that don't yet flow (IMDb Top 250 rankings, RT
// Certified Fresh flag). getValue returns null until the data is populated,
// so they're invisible on real cards — only the settings UI shows them with
// an "(awaiting data)" note.

function formatShowStatus(value: string | undefined): string | null {
  if (!value) return null;
  switch (value.toLowerCase()) {
    case "returning":
    case "returning series":
    case "continuing":
    case "in_production":
    case "in production":
      return "Returning";
    case "ended":
      return "Ended";
    case "cancelled":
    case "canceled":
      return "Cancelled";
    case "upcoming":
    case "planned":
      return "Upcoming";
    default:
      return value;
  }
}

export const RIBBON_OVERLAYS: readonly OverlayDef[] = [
  {
    id: "show_status",
    category: "ribbons",
    label: "Show Status",
    description: "Series lifecycle: Returning, Ended, Cancelled",
    defaultPosition: "top-right",
    defaultEnabled: false,
    iconId: "tv",
    iconCapable: true,
    availabilityNote: "Populated by metadata plugins (TMDB/TVDB updates pending)",
    getValue: (d) => formatShowStatus(d.show_status),
  },
  {
    id: "imdb_top_250",
    category: "ribbons",
    label: "IMDb Top 250",
    description: "Rank when present in the IMDb Top 250 chart",
    defaultPosition: "top-right",
    defaultEnabled: false,
    iconId: "ribbon",
    defaultAccent: "#f5c518",
    iconCapable: true,
    availabilityNote: "Requires an IMDb Top 250 data source (planned)",
    getValue: (d) => (d.imdb_top_250 != null ? `#${d.imdb_top_250}` : null),
  },
  {
    id: "rt_certified_fresh",
    category: "ribbons",
    label: "RT Certified Fresh",
    description: "Shown for Rotten Tomatoes Certified Fresh titles",
    defaultPosition: "top-right",
    defaultEnabled: false,
    iconId: "tomato",
    defaultAccent: "#fa320a",
    iconCapable: true,
    availabilityNote: "Requires Rotten Tomatoes certification data (planned)",
    getValue: (d) => (d.rt_certified_fresh ? "Certified Fresh" : null),
  },
];
