import { PRESET_IDS } from "./presets";
import { OVERLAY_MAP, OVERLAY_REGISTRY } from "./registry";
import { OVERLAY_POSITIONS } from "./types";
import type {
  CardOverlayPrefs,
  OverlayId,
  OverlayItemConfig,
  OverlayPosition,
  PresetId,
} from "./types";

export function buildDefaultPrefs(): CardOverlayPrefs {
  return { version: 2, preset: "classic", order: [], items: buildItems(undefined) };
}

function isValidPosition(v: unknown): v is OverlayPosition {
  return typeof v === "string" && (OVERLAY_POSITIONS as readonly string[]).includes(v);
}

function isValidPreset(v: unknown): v is PresetId {
  return typeof v === "string" && (PRESET_IDS as readonly string[]).includes(v);
}

function isHexColor(v: unknown): v is string {
  return typeof v === "string" && /^#[0-9a-fA-F]{6}$/.test(v);
}

// Heuristic: v1 docs are flat Record<OverlayId, {enabled,position}>. v2 docs
// have a "version" field or at minimum a "preset" string and "items" object.
function looksLikeV2(parsed: unknown): boolean {
  if (!parsed || typeof parsed !== "object") return false;
  const obj = parsed as Record<string, unknown>;
  if (obj.version === 2) return true;
  return typeof obj.preset === "string" && typeof obj.items === "object" && obj.items != null;
}

function applyItemPatch(
  base: OverlayItemConfig,
  patch: Record<string, unknown>,
): OverlayItemConfig {
  return {
    enabled: typeof patch.enabled === "boolean" ? patch.enabled : base.enabled,
    position: isValidPosition(patch.position) ? patch.position : base.position,
    accentColor: isHexColor(patch.accentColor) ? patch.accentColor : undefined,
    showIcon: typeof patch.showIcon === "boolean" ? patch.showIcon : undefined,
  };
}

// buildItems is the single registry pass shared by both migration paths and
// the default-prefs builder. It produces a complete items map: every overlay
// id gets either a patched entry (when source has it) or a fresh default.
function buildItems(
  source: Record<string, unknown> | undefined,
): Record<OverlayId, OverlayItemConfig> {
  const items = {} as Record<OverlayId, OverlayItemConfig>;
  for (const def of OVERLAY_REGISTRY) {
    const base: OverlayItemConfig = { enabled: def.defaultEnabled, position: def.defaultPosition };
    const entry = source?.[def.id];
    items[def.id] =
      entry && typeof entry === "object"
        ? applyItemPatch(base, entry as Record<string, unknown>)
        : base;
  }
  return items;
}

function migrateFromV1(parsed: Record<string, unknown>): CardOverlayPrefs {
  return { version: 2, preset: "classic", order: [], items: buildItems(parsed) };
}

function parseV2(parsed: Record<string, unknown>): CardOverlayPrefs {
  const items = parsed.items;
  const sourceItems =
    items && typeof items === "object" ? (items as Record<string, unknown>) : undefined;
  return {
    version: 2,
    preset: isValidPreset(parsed.preset) ? parsed.preset : "classic",
    order: Array.isArray(parsed.order)
      ? (parsed.order as unknown[]).filter(
          (id): id is OverlayId => typeof id === "string" && OVERLAY_MAP.has(id as OverlayId),
        )
      : [],
    items: buildItems(sourceItems),
  };
}

export function parseOverlayPrefs(raw: string | null): CardOverlayPrefs {
  if (!raw) return buildDefaultPrefs();
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return buildDefaultPrefs();
  }
  if (!parsed || typeof parsed !== "object") return buildDefaultPrefs();
  const obj = parsed as Record<string, unknown>;
  if (looksLikeV2(obj)) return parseV2(obj);
  return migrateFromV1(obj);
}

export function serializeOverlayPrefs(prefs: CardOverlayPrefs): string {
  return JSON.stringify(prefs);
}

// Whether `id` should be hidden because another enabled overlay already
// displays the same information. The combined `resolution_hdr` badge
// subsumes the standalone `resolution` and `hdr` badges — without this,
// enabling the combined view on top of the defaults produces
// "4K HDR 4K HDR" stacks. The user's stored prefs are left untouched so
// toggling the combined badge off restores the standalones automatically.
export function isOverlaySuppressed(id: OverlayId, prefs: CardOverlayPrefs): boolean {
  if (id === "resolution" || id === "hdr") {
    return prefs.items["resolution_hdr"]?.enabled === true;
  }
  return false;
}

// Returns enabled overlays for a position, in the user's chosen order
// (falling back to registry order for any unranked ids).
export function orderedOverlaysForPosition(prefs: CardOverlayPrefs, position: OverlayPosition) {
  const enabled = OVERLAY_REGISTRY.filter(
    (def) =>
      prefs.items[def.id]?.enabled &&
      prefs.items[def.id]?.position === position &&
      !isOverlaySuppressed(def.id, prefs),
  );
  if (prefs.order.length === 0) return enabled;
  const orderIndex = new Map<OverlayId, number>(prefs.order.map((id, i) => [id, i]));
  return [...enabled].sort((a, b) => (orderIndex.get(a.id) ?? 999) - (orderIndex.get(b.id) ?? 999));
}
