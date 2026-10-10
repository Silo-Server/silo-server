// Public surface of the overlay library. Import from "@/lib/overlays" — do
// not import internal modules directly. This keeps refactors of the internal
// file layout free of consumer churn.

export type {
  AccentStrategy,
  CardOverlayPrefs,
  OverlayCategory,
  OverlayData,
  OverlayDef,
  OverlayIconId,
  OverlayItemConfig,
  OverlayPosition,
  OverlayPreset,
  OverlayShadow,
  OverlayId,
  PresetId,
} from "./types";

export { OVERLAY_REGISTRY, OVERLAY_MAP, getOverlayDef, isOverlayOffered } from "./registry";
export { OVERLAY_POSITIONS, OVERLAY_CATEGORIES, WORDMARK_TEXT } from "./types";
export {
  buildDefaultPrefs,
  parseOverlayPrefs,
  serializeOverlayPrefs,
  orderedOverlaysForPosition,
  isOverlaySuppressed,
  isOverlaySupportedBy,
  overlayPrefsForServer,
  requestDownloadBarPercent,
  storedOverlayIds,
} from "./schema";
export type { OverlayServerSupport } from "./schema";
export { OVERLAY_PRESETS, PRESET_IDS, getPreset, ACCENT_PALETTE } from "./presets";
export { POSITION_OPTIONS, CATEGORY_GROUPS, CATEGORY_META } from "./ui-constants";
export { OverlayIcon } from "./icons";
export {
  overlayDataFromBrowseItem,
  overlayDataFromEpisodeListItem,
  overlayDataFromSectionItem,
  overlayDataFromWatchlistTitle,
} from "./extractors";
export {
  OVERLAY_PREVIEW_SAMPLES,
  OVERLAY_PREVIEW_VARIANTS,
  SAMPLE_MOVIE_DATA,
  SAMPLE_REQUEST_DATA,
  SAMPLE_SHOW_DATA,
} from "./sample-data";
export type { OverlayPreviewVariant } from "./sample-data";
export { ATTENTION_ACCENT } from "./registry/ribbons";
