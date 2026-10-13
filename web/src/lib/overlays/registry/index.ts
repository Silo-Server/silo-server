import type { OverlayDef, OverlayId } from "../types";
import { TECH_OVERLAYS } from "./tech";
import { RATINGS_OVERLAYS } from "./ratings";
import { METADATA_OVERLAYS } from "./metadata";
import { RIBBON_OVERLAYS } from "./ribbons";

// Single source of truth for which overlays exist. Order within this array
// determines the default render order within each corner; users can override
// via CardOverlayPrefs.order in a future drag-reorder UI.
export const OVERLAY_REGISTRY: readonly OverlayDef[] = [
  ...TECH_OVERLAYS,
  ...RATINGS_OVERLAYS,
  ...METADATA_OVERLAYS,
  ...RIBBON_OVERLAYS,
];

// O(1) lookup map used by settings UIs and the schema parser to validate
// overlay IDs without rescanning the registry.
export const OVERLAY_MAP: ReadonlyMap<OverlayId, OverlayDef> = new Map(
  OVERLAY_REGISTRY.map((d) => [d.id, d]),
);

export function getOverlayDef(id: OverlayId): OverlayDef | undefined {
  return OVERLAY_MAP.get(id);
}

// Whether settings offer an overlay: a rating badge only while the server
// shows its source (getRatingsCapability), since cards leave the score out
// otherwise and the badge would never render.
export function isOverlayOffered(
  def: OverlayDef,
  shownRatingSources: ReadonlySet<string>,
): boolean {
  return !def.ratingSource || shownRatingSources.has(def.ratingSource);
}
