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
  OverlayFontWeight,
  OverlayImage,
  OverlayShape,
  OverlayStyle,
  PresetId,
  WatchStatusMode,
} from "./types";

export { OVERLAY_REGISTRY, OVERLAY_MAP, getOverlayDef, isOverlayOffered } from "./registry";
export { OVERLAY_POSITIONS, OVERLAY_CATEGORIES, WATCH_STATUS_MODES, WORDMARK_TEXT } from "./types";
export {
  buildDefaultPrefs,
  parseOverlayPrefs,
  serializeOverlayPrefs,
  orderedOverlaysForPosition,
  isOverlaySuppressed,
  isOverlaySupportedBy,
  isOverlayImageRef,
  overlayPrefsForServer,
  OVERLAY_STYLE_MANIFEST_REVISION,
  parseOverlayStyle,
  STYLE_SIZE_MAX,
  STYLE_SIZE_MIN,
  supportsOverlayStyles,
  withoutUnlistedImages,
  requestDownloadBarPercent,
  storedOverlayIds,
} from "./schema";
export type { OverlayServerSupport } from "./schema";
export { OVERLAY_PRESETS, PRESET_IDS, getPreset, ACCENT_PALETTE } from "./presets";
export { applyOverlayStyle, overlayImageUrl, readableTextOn, watchStatusOf } from "./style";
export {
  POSITION_OPTIONS,
  CATEGORY_GROUPS,
  CATEGORY_META,
  WATCH_STATUS_OPTIONS,
} from "./ui-constants";
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
  OVERLAY_PREVIEW_WATCH_STATES,
  SAMPLE_MOVIE_DATA,
  SAMPLE_REQUEST_DATA,
  SAMPLE_SHOW_DATA,
} from "./sample-data";
export type { OverlayPreviewVariant } from "./sample-data";
export { ATTENTION_ACCENT } from "./registry/ribbons";
