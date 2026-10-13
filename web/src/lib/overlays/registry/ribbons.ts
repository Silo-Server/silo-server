import type { OverlayDef } from "../types";

// "Ribbon" overlays — status badges that sit in the visual register of corner
// ribbons. Only entries backed by a real API field belong in this registry;
// registry membership makes a control visible in Settings.

// Amber, for a title TMDB no longer lists as saved.
export const ATTENTION_ACCENT = "#f59e0b";

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
    id: "request_status",
    category: "ribbons",
    label: "Request Status",
    description:
      "Where a watchlist title the library doesn't have yet stands: downloading, release date, awaiting approval",
    defaultPosition: "top-left",
    defaultEnabled: true,
    iconCapable: true,
    introducedInManifest: 15,
    getValue: (d) => d.request_status || null,
    getIcon: (d) => d.request_status_icon ?? null,
    getAccent: (d) => (d.request_status_attention ? ATTENTION_ACCENT : null),
  },
];
