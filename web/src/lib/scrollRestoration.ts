/**
 * Where each history entry was scrolled to, so Back and Forward return to it.
 *
 * Positions are kept per history entry, never per URL: the same library
 * reached again from the sidebar is a fresh visit and opens at the top. They
 * live in memory and are written to sessionStorage when the page is hidden,
 * so a Back across a reload, whose list starts with an empty query cache,
 * still finds them.
 *
 * A page with a windowed `ItemGrid` also records the item at the top of the
 * viewport and how far into its row the viewport sits. The grid restores
 * that rather than the pixel offset: its row pitch is measured from the cards
 * it renders and starts over on every mount, so the same pixel offset can
 * land on a different row on the way back.
 */

export interface GridAnchor {
  /** Index of the first item in the top row. */
  index: number;
  /** Pixels from that row's top to the top of the viewport; negative above the grid. */
  offset: number;
}

export interface SavedPosition {
  y: number;
  grid?: GridAnchor;
}

interface HistoryEntryLocation {
  key: string;
  pathname: string;
  search: string;
  hash: string;
}

const STORAGE_KEY = "silo:scroll-positions";
const MAX_ENTRIES = 100;
/**
 * How long the page must hold still after a restore lands before the restore
 * lets go. A loading page can swap placeholders for content of a different
 * height just after the target became reachable, and the browser clamps the
 * offset on the way through.
 */
const SETTLE_MS = 1000;
/**
 * React Router's key for an entry it did not create: the first entry of a
 * page load, and the entries a raw `<a href="#...">` skip link pushes.
 */
const UNKEYED_ENTRY = "default";

/**
 * React Router keys every entry it pushes. The entries it did not push all
 * share one key, so those are told apart by their URL instead.
 */
export function historyEntryId(location: HistoryEntryLocation): string {
  if (location.key !== UNKEYED_ENTRY) return location.key;
  return `${UNKEYED_ENTRY}:${location.pathname}${location.search}${location.hash}`;
}

let positions: Map<string, SavedPosition> | null = null;
let currentId: string | null = null;
let pending: SavedPosition | null = null;
let landed = false;
let settleTimer: ReturnType<typeof setTimeout> | undefined;
let version = 0;
const listeners = new Set<() => void>();

function store(): Map<string, SavedPosition> {
  if (positions) return positions;
  positions = new Map();
  if (typeof window === "undefined") return positions;
  try {
    const saved = JSON.parse(window.sessionStorage.getItem(STORAGE_KEY) ?? "[]") as [
      string,
      SavedPosition,
    ][];
    // A new page load in this tab starts a new run of unkeyed entries; ones
    // saved by an earlier load would restore into the wrong page.
    const navigation = performance.getEntriesByType?.("navigation")[0] as
      | PerformanceNavigationTiming
      | undefined;
    const freshLoad = navigation?.type === "navigate";
    for (const [id, position] of saved) {
      if (freshLoad && id.startsWith(`${UNKEYED_ENTRY}:`)) continue;
      positions.set(id, position);
    }
  } catch {
    // Unreadable or unavailable storage only costs restoring across a reload.
  }
  window.addEventListener("pagehide", persist);
  // A discarded or killed mobile tab may never see pagehide.
  document.addEventListener("visibilitychange", persistWhenHidden);
  return positions;
}

function persist() {
  if (!positions) return;
  try {
    window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify([...positions]));
  } catch {
    // Quota or privacy mode: positions still hold for this page load.
  }
}

function persistWhenHidden() {
  if (document.visibilityState === "hidden") persist();
}

function save(id: string, position: SavedPosition) {
  const map = store();
  // Re-inserting keeps the map in last-used order, so the cap drops the
  // entries least likely to be returned to.
  map.delete(id);
  map.set(id, position);
  if (map.size > MAX_ENTRIES) map.delete(map.keys().next().value!);
}

function notify() {
  version++;
  for (const listener of listeners) listener();
}

/**
 * Makes `id` the entry positions are recorded for. With `restore`, returns the
 * position saved for it and holds it as pending until `finishRestore`;
 * recording is paused meanwhile, so the page's own loading scroll can't
 * overwrite the target.
 */
export function enterHistoryEntry(id: string, restore: boolean): SavedPosition | null {
  const saved = restore ? (store().get(id) ?? null) : null;
  clearTimeout(settleTimer);
  landed = false;
  // A grid that stays mounted across the change, as on a same-URL replace,
  // has to hear about the new entry to record its anchor there.
  const changed = id !== currentId || saved !== pending;
  currentId = id;
  pending = saved;
  if (changed) notify();
  return saved;
}

/** The current entry's pending grid target, when a grid is to restore it. */
export function pendingGridRestore(): GridAnchor | undefined {
  return pending?.grid;
}

/**
 * A grid restore that has not reached its row yet. The rows on screen until
 * then are not where the page is headed.
 */
export function awaitingGridRestore(): boolean {
  return Boolean(pending?.grid) && !landed;
}

export function isRestorePending(): boolean {
  return pending !== null;
}

/**
 * The page is at its saved position. The restore stays in force, re-applied
 * on every layout change, until the page has held still for `SETTLE_MS`.
 */
export function restoreLanded() {
  if (!pending) return;
  landed = true;
  clearTimeout(settleTimer);
  settleTimer = setTimeout(finishRestore, SETTLE_MS);
}

/** The page's layout changed while restoring; have a grid re-apply its anchor. */
export function relayoutRestore() {
  if (pending?.grid) notify();
}

/** The restore settled, or will not land: the page now owns its scroll position. */
export function finishRestore() {
  clearTimeout(settleTimer);
  landed = false;
  if (!pending) return;
  pending = null;
  notify();
}

/** The first item of the top row saved for an entry, if a grid recorded one. */
export function savedGridIndex(id: string): number | undefined {
  return store().get(id)?.grid?.index;
}

export function recordScroll(y: number) {
  if (!currentId || pending) return;
  const saved = store().get(currentId);
  // A grid anchor is only good for the offset it was taken at.
  save(currentId, saved?.grid && saved.y === y ? saved : { y });
}

export function recordGridAnchor(y: number, grid: GridAnchor) {
  if (!currentId || pending) return;
  save(currentId, { y, grid });
}

export function subscribeScrollRestoration(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function scrollRestorationVersion(): number {
  return version;
}

/** Test seam: module state outlives a render, so tests have to reset it. */
export function resetScrollRestoration() {
  if (typeof window !== "undefined") {
    window.removeEventListener("pagehide", persist);
    document.removeEventListener("visibilitychange", persistWhenHidden);
  }
  clearTimeout(settleTimer);
  positions = null;
  currentId = null;
  pending = null;
  landed = false;
  version = 0;
}
