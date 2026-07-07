import type { CSSProperties } from "react";

// Position on the poster card where a badge group is anchored.
export const OVERLAY_POSITIONS = ["top-left", "top-right", "bottom-left", "bottom-right"] as const;
export type OverlayPosition = (typeof OVERLAY_POSITIONS)[number];

// Logical groupings used by the settings UI to organize controls.
export const OVERLAY_CATEGORIES = ["tech", "ratings", "metadata", "ribbons"] as const;
export type OverlayCategory = (typeof OVERLAY_CATEGORIES)[number];

// Stable identifiers for every overlay the system knows about.
export type OverlayId =
  // tech
  | "resolution"
  | "hdr"
  | "resolution_hdr"
  | "audio"
  | "audio_channels"
  | "video_codec"
  | "container"
  | "aspect_ratio"
  | "release_type"
  | "edition"
  | "multi_audio"
  | "multi_sub"
  // ratings
  | "rating_imdb"
  | "rating_tmdb"
  | "rating_rt"
  | "rating_rt_audience"
  | "content_rating"
  // metadata
  | "year"
  | "runtime"
  | "original_language"
  | "studio"
  | "network"
  // ribbons (status / awards)
  | "show_status"
  | "imdb_top_250"
  | "rt_certified_fresh";

// Flat data bag passed to overlay renderers. Each field is optional —
// extractors fill what's available and getValue() returns null when missing.
export interface OverlayData {
  // tech (from OverlaySummary)
  resolution?: string;
  hdr?: string;
  audio?: string;
  audio_channels?: string;
  video_codec?: string;
  container?: string;
  aspect_ratio?: string;
  release_type?: string;
  edition?: string;
  multi_audio?: boolean;
  multi_sub?: boolean;
  // ratings / metadata (from MediaItem)
  rating_imdb?: number | null;
  rating_tmdb?: number | null;
  rating_rt_critic?: number | null;
  rating_rt_audience?: number | null;
  content_rating?: string;
  year?: number | null;
  runtime?: number | null;
  original_language?: string;
  studio?: string;
  network?: string;
  // ribbons
  show_status?: string;
  imdb_top_250?: number | null;
  rt_certified_fresh?: boolean | null;
}

// Per-overlay user configuration. accentColor and showIcon are optional
// overrides; undefined falls back to the def's defaultAccent / preset.
export interface OverlayItemConfig {
  enabled: boolean;
  position: OverlayPosition;
  accentColor?: string; // hex string e.g. "#f5c518"
  showIcon?: boolean; // undefined = use preset default
}

export type PresetId = "minimal" | "classic" | "vibrant" | "pill" | "square";

// Versioned root document stored under user_settings.card_overlays.
export interface CardOverlayPrefs {
  version: 2;
  preset: PresetId;
  order: OverlayId[]; // empty = use registry order
  items: Record<OverlayId, OverlayItemConfig>;
}

// How a preset paints the accent color when one is set.
export type AccentStrategy = "bg" | "border" | "text" | "dot";

export interface OverlayPreset {
  id: PresetId;
  label: string;
  description: string;
  badgeClass: string; // Tailwind class fragment for the badge container
  badgeStyle: (accentColor: string | undefined) => CSSProperties;
  iconSize: number;
  preferIcon: boolean; // true when icon-prefixed renderings are preferred
  gapClass: string; // gap between stacked badges in a corner
  accentStrategy: AccentStrategy;
}

// Registry entry describing one overlay type.
export interface OverlayDef {
  id: OverlayId;
  category: OverlayCategory;
  label: string;
  description: string;
  defaultPosition: OverlayPosition;
  defaultEnabled: boolean;
  iconId?: OverlayIconId; // static icon, when applicable
  defaultAccent?: string; // suggested accent color in palette pickers
  iconCapable: boolean; // whether the icon toggle should appear in settings
  availabilityNote?: string; // shown when data source isn't wired up yet
  getValue: (data: OverlayData) => string | null;
  getIcon?: (data: OverlayData) => OverlayIconId | null; // dynamic icon by data
}

// Typed icon identifiers — every icon used anywhere must be in this union.
// Lucide-backed icons are mapped in icons.ts; brand marks ("hdr10", "atmos"
// etc.) are inline SVG components defined in icons.tsx.
export type OverlayIconId =
  // generic (Lucide)
  | "star"
  | "clock"
  | "tv"
  | "film"
  | "award"
  | "ribbon"
  | "subtitles"
  | "languages"
  | "building"
  | "shield"
  | "layout"
  | "monitor"
  | "volume"
  | "calendar"
  | "globe"
  // brand marks (inline SVG)
  | "hdr10"
  | "hdr"
  | "dolby-vision"
  | "atmos"
  | "av1"
  | "tomato";

// Wordmark icons render their text as the mark itself (defined in icons.tsx).
// When a badge's label says the same thing, the renderer suppresses the label
// so the badge doesn't read "HDR10 HDR10".
export const WORDMARK_TEXT: Partial<Record<OverlayIconId, string>> = {
  hdr: "HDR",
  hdr10: "HDR10",
  atmos: "ATMOS",
  av1: "AV1",
};
