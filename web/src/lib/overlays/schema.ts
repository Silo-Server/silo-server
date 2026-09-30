import { PRESET_IDS } from "./presets";
import { OVERLAY_MAP, OVERLAY_REGISTRY } from "./registry";
import { OVERLAY_POSITIONS } from "./types";
import type {
  CardOverlayPrefs,
  OverlayData,
  OverlayId,
  OverlayItemConfig,
  OverlayPosition,
  PresetId,
} from "./types";

export function buildDefaultPrefs(): CardOverlayPrefs {
  return { version: 2, preset: "classic", order: [], items: buildItems(undefined) };
}

// Ids that are legal in the contract schema (card-overlays.json) but have no
// registry entry yet because no API field backs them. The web client neither
// renders nor edits these, but their stored config must survive a round-trip:
// the native clients' settings UIs can author them, and dropping them here
// would erase another client's preference on the next web save. Their bases
// mirror the native registries' defaults (ribbons: top-right, disabled).
const PASSTHROUGH_IDS = [
  "imdb_top_250",
  "rt_certified_fresh",
] as const satisfies readonly OverlayId[];
const PASSTHROUGH_BASE: OverlayItemConfig = { enabled: false, position: "top-right" };

function isKnownOverlayId(v: unknown): v is OverlayId {
  return (
    typeof v === "string" &&
    (OVERLAY_MAP.has(v as OverlayId) || (PASSTHROUGH_IDS as readonly string[]).includes(v))
  );
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
  for (const id of PASSTHROUGH_IDS) {
    const entry = source?.[id];
    if (entry && typeof entry === "object") {
      items[id] = applyItemPatch(PASSTHROUGH_BASE, entry as Record<string, unknown>);
    }
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
    order: Array.isArray(parsed.order) ? (parsed.order as unknown[]).filter(isKnownOverlayId) : [],
    items: buildItems(sourceItems),
  };
}

// Accepts the canonical settings-contract value (an object or null), the
// legacy JSON-string encoding, and anything malformed, always landing on a
// complete prefs document.
export function parseOverlayPrefs(raw: unknown): CardOverlayPrefs {
  if (raw == null) return buildDefaultPrefs();
  let parsed: unknown = raw;
  if (typeof raw === "string") {
    if (!raw) return buildDefaultPrefs();
    try {
      parsed = JSON.parse(raw);
    } catch {
      return buildDefaultPrefs();
    }
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return buildDefaultPrefs();
  const obj = parsed as Record<string, unknown>;
  if (looksLikeV2(obj)) return parseV2(obj);
  return migrateFromV1(obj);
}

// The overlay ids a stored ui.card_overlays value actually contains, before
// parsing fills in the rest of the registry. The server validated that value,
// so it accepts every one of them.
export function storedOverlayIds(raw: unknown): ReadonlySet<string> {
  let parsed = raw;
  if (typeof raw === "string") {
    try {
      parsed = JSON.parse(raw);
    } catch {
      return new Set();
    }
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return new Set();
  const obj = parsed as Record<string, unknown>;
  const items = looksLikeV2(obj) ? obj.items : obj;
  return new Set(items && typeof items === "object" ? Object.keys(items) : []);
}

// What a server can store in ui.card_overlays: its settings manifest revision
// when known, and the ids its stored value already holds.
export interface OverlayServerSupport {
  manifestRevision: number | undefined;
  storedIds: ReadonlySet<string>;
}

// Whether the server accepts `id` in ui.card_overlays. Validation rejects the
// whole document over one unknown id, so while the revision is unknown only
// ids the server already stored count as supported.
export function isOverlaySupportedBy(id: OverlayId, support: OverlayServerSupport): boolean {
  const since = OVERLAY_MAP.get(id)?.introducedInManifest;
  if (since === undefined) return true;
  if (support.manifestRevision !== undefined) return support.manifestRevision >= since;
  return support.storedIds.has(id);
}

// The document as the server can store it: ids it does not accept are
// dropped from items and order.
export function overlayPrefsForServer(
  prefs: CardOverlayPrefs,
  support: OverlayServerSupport,
): CardOverlayPrefs {
  const items = Object.fromEntries(
    Object.entries(prefs.items).filter(([id]) => isOverlaySupportedBy(id as OverlayId, support)),
  ) as CardOverlayPrefs["items"];
  const order = prefs.order.filter((id) => isOverlaySupportedBy(id, support));
  return { ...prefs, order, items };
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

// The download bar a card draws while a watchlist title downloads, as a
// percentage, or null for no bar. The bar belongs to the request_status
// badge: it shows only while overlays are on (prefs is null when they are
// off) and the badge is enabled.
export function requestDownloadBarPercent(
  data: OverlayData,
  prefs: CardOverlayPrefs | null | undefined,
): number | null {
  if (!prefs?.items.request_status?.enabled || !data.request_status) return null;
  const percent = data.request_download_percent;
  return percent == null ? null : Math.min(100, Math.max(0, percent));
}
