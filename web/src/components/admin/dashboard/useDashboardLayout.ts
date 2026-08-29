import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { AdminDashboardLayoutDocument } from "@/api/types";
import {
  useAdminDashboardLayout,
  useResetAdminDashboardLayout,
  useSaveAdminDashboardLayout,
} from "@/hooks/queries/admin/dashboardLayout";
import { useAuth } from "@/hooks/useAuth";
import { DASHBOARD_WIDGETS, DEFAULT_LAYOUT, findDashboardWidget } from "./registry";
import type {
  DashboardLayoutEntry,
  DashboardWidgetDefinition,
  WidgetId,
  WidgetRange,
} from "./types";

/**
 * The unscoped key this cache used before layouts were scoped per account.
 *
 * Two admins sharing a browser shared one cached layout under it, and the
 * "no server layout yet" migration would have uploaded whichever admin cached
 * last to the account that logged in next. It is only ever deleted: its value
 * cannot be attributed to an account, so it is never adopted.
 */
export const LEGACY_DASHBOARD_LAYOUT_STORAGE_KEY = "silo.admin-dashboard-layout.v1";

/** Where this account's cached arrangement lives in localStorage. */
export function dashboardLayoutStorageKey(userId: number): string {
  return `${LEGACY_DASHBOARD_LAYOUT_STORAGE_KEY}.u${userId}`;
}

// Edits are bursty — a drag emits several moves, a resize several spans — so
// the server write waits for the burst to settle. Local state and localStorage
// are updated synchronously, so the delay is never visible.
export const DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS = 800;

// Version 1 is still version 1 with row heights and per-widget windows in it:
// `rows` and `range` are additive fields that older documents simply omit, and
// sanitizing fills them from the widget's defaults. A bump would only be needed
// for a change that makes an existing field mean something new.
interface StoredLayout {
  version: 1;
  entries: DashboardLayoutEntry[];
}

function clampSpan(span: unknown, widget: DashboardWidgetDefinition): number {
  if (typeof span !== "number" || !Number.isFinite(span)) {
    return widget.defaultSpan;
  }
  return Math.min(widget.maxSpan, Math.max(widget.minSpan, Math.round(span)));
}

/**
 * Row heights were added after the first layouts were saved, so an entry
 * without them is the common case rather than a corrupt one: it predates the
 * field and takes the widget's default height.
 */
function clampRows(rows: unknown, widget: DashboardWidgetDefinition): number {
  if (typeof rows !== "number" || !Number.isFinite(rows)) {
    return widget.defaultRows;
  }
  return Math.min(widget.maxRows, Math.max(widget.minRows, Math.round(rows)));
}

/**
 * The window a stored entry asks for, or the widget's default.
 *
 * A widget that offers no ranges never carries one, so a value stored while it
 * did — or a range removed from its allowed list since — is dropped rather than
 * requested from an endpoint that would clamp it into something else.
 */
function sanitizeRange(range: unknown, widget: DashboardWidgetDefinition): WidgetRange | undefined {
  const ranges = widget.ranges;
  if (!ranges) {
    return undefined;
  }
  if (typeof range === "string" && (ranges.allowed as readonly string[]).includes(range)) {
    return range as WidgetRange;
  }
  return ranges.default;
}

/**
 * Validates a layout document from any source — localStorage or the server —
 * and drops what this build cannot render. Returns null when the value is not
 * a v1 layout document at all, which callers read as "there is no layout here"
 * rather than "the layout is empty".
 */
function sanitizeLayoutDocument(value: unknown): DashboardLayoutEntry[] | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  const parsed = value as Partial<StoredLayout>;
  if (parsed.version !== 1 || !Array.isArray(parsed.entries)) {
    return null;
  }
  const seen = new Set<WidgetId>();
  const entries: DashboardLayoutEntry[] = [];
  for (const entry of parsed.entries) {
    if (!entry || typeof entry !== "object" || typeof entry.id !== "string") {
      continue;
    }
    const widget = findDashboardWidget(entry.id);
    if (!widget || seen.has(widget.id)) {
      continue;
    }
    seen.add(widget.id);
    const sanitized: DashboardLayoutEntry = {
      id: widget.id,
      span: clampSpan(entry.span, widget),
      rows: clampRows(entry.rows, widget),
    };
    // Written only when the widget has ranges, so a layout document never
    // carries `"range": undefined` for the widgets that do not.
    const range = sanitizeRange(entry.range, widget);
    if (range) {
      sanitized.range = range;
    }
    entries.push(sanitized);
  }
  return entries;
}

function readStoredLayout(userId: number | null): DashboardLayoutEntry[] | null {
  if (userId === null) {
    return null;
  }
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(dashboardLayoutStorageKey(userId));
  } catch {
    return null;
  }
  if (!raw) {
    return null;
  }
  try {
    return sanitizeLayoutDocument(JSON.parse(raw));
  } catch {
    return null;
  }
}

function loadStoredLayout(userId: number | null): DashboardLayoutEntry[] {
  return readStoredLayout(userId) ?? [...DEFAULT_LAYOUT];
}

function toLayoutDocument(entries: DashboardLayoutEntry[]): AdminDashboardLayoutDocument {
  return { version: 1, entries };
}

function persistLayout(userId: number | null, entries: DashboardLayoutEntry[]) {
  if (userId === null) {
    return;
  }
  try {
    const stored: StoredLayout = { version: 1, entries };
    window.localStorage.setItem(dashboardLayoutStorageKey(userId), JSON.stringify(stored));
  } catch {
    // Storage may be unavailable (private mode, quota); the layout still works in-memory.
  }
}

function clearStoredLayout(userId: number | null) {
  if (userId === null) {
    return;
  }
  try {
    window.localStorage.removeItem(dashboardLayoutStorageKey(userId));
  } catch {
    // Ignore storage failures; in-memory state still resets.
  }
}

/** Drops the pre-scoping cache so it can never be uploaded to an account. */
function clearLegacyStoredLayout() {
  try {
    window.localStorage.removeItem(LEGACY_DASHBOARD_LAYOUT_STORAGE_KEY);
  } catch {
    // Ignore storage failures; nothing reads the legacy key either way.
  }
}

/** A resize of one or both axes; an omitted axis keeps its current value. */
export interface DashboardWidgetSize {
  span?: number;
  rows?: number;
}

export interface DashboardLayout {
  entries: DashboardLayoutEntry[];
  hiddenWidgets: DashboardWidgetDefinition[];
  isCustomizing: boolean;
  setCustomizing: (customizing: boolean) => void;
  moveWidget: (id: WidgetId, beforeId: WidgetId | null) => void;
  resizeWidget: (id: WidgetId, size: DashboardWidgetSize) => void;
  /**
   * Change a widget's window. Unlike moving and resizing this is an everyday
   * viewing action, not an arrangement one, so it works outside customize mode
   * — and rides the same debounced save, because where an admin left a chart is
   * part of the layout they expect to find again.
   */
  setWidgetRange: (id: WidgetId, range: WidgetRange) => void;
  removeWidget: (id: WidgetId) => void;
  /**
   * Place a hidden widget with its default span, rows, and window. With a
   * `beforeId` it is inserted in front of that entry — one layout update, so a
   * sheet drop rides a single debounced save rather than an add-then-move pair.
   * Without one (or with an id that is not placed) it appends.
   */
  addWidget: (id: WidgetId, beforeId?: WidgetId | null) => void;
  resetLayout: () => void;
}

/**
 * Owns the admin's widget arrangement.
 *
 * localStorage is the instant-paint and offline copy; the server row is the
 * source of truth across browsers. The hook paints from localStorage, adopts
 * the server layout once it arrives, migrates a local-only layout up to the
 * server the first time it finds none there, and debounces subsequent writes.
 * A failed write never rolls back local state — the arrangement the admin sees
 * is the one they just made.
 *
 * The cache is keyed by account: two admins sharing a browser must not see —
 * or migrate up — each other's arrangement.
 */
export function useDashboardLayout(): DashboardLayout {
  // The admin routes only render behind an authenticated session, so this is a
  // user in practice; without one the layout simply stays in memory.
  const userId = useAuth().user?.id ?? null;
  const [entries, setEntries] = useState<DashboardLayoutEntry[]>(() => loadStoredLayout(userId));
  const [isCustomizing, setCustomizing] = useState(false);

  useEffect(clearLegacyStoredLayout, []);

  const remote = useAdminDashboardLayout();
  const saveLayout = useSaveAdminDashboardLayout();
  const resetRemoteLayout = useResetAdminDashboardLayout();

  const saveMutate = saveLayout.mutate;
  const resetMutate = resetRemoteLayout.mutate;

  // The server response is adopted at most once per mount, and never over an
  // edit the admin already made in this session.
  const settledRef = useRef(false);
  const editedRef = useRef(false);
  const pendingRef = useRef<DashboardLayoutEntry[] | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const flushSave = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    const pending = pendingRef.current;
    pendingRef.current = null;
    if (pending) {
      saveMutate(toLayoutDocument(pending));
    }
  }, [saveMutate]);

  const scheduleSave = useCallback(
    (next: DashboardLayoutEntry[]) => {
      pendingRef.current = next;
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current);
      }
      timerRef.current = setTimeout(flushSave, DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS);
    },
    [flushSave],
  );

  const cancelPendingSave = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    pendingRef.current = null;
  }, []);

  // Send a queued write before this page goes away rather than dropping it.
  useEffect(() => flushSave, [flushSave]);

  // A different account while the hook stays mounted (impersonation starts or
  // ends without a remount) must not inherit the previous account's state: its
  // queued save would upload the old arrangement under the new account, and
  // the settled flag would block adopting the new account's server layout.
  const accountRef = useRef(userId);
  useEffect(() => {
    if (accountRef.current === userId) {
      return;
    }
    accountRef.current = userId;
    cancelPendingSave();
    settledRef.current = false;
    editedRef.current = false;
    setEntries(loadStoredLayout(userId));
  }, [userId, cancelPendingSave]);

  const remoteData = remote.data;
  const remoteSettled = remote.isSuccess;

  useEffect(() => {
    if (!remoteSettled || settledRef.current) {
      return;
    }
    settledRef.current = true;
    // An edit made while the query was in flight is newer than the response;
    // its own debounced save carries it to the server.
    if (editedRef.current) {
      return;
    }
    const serverEntries = sanitizeLayoutDocument(remoteData?.layout);
    if (serverEntries) {
      setEntries(serverEntries);
      persistLayout(userId, serverEntries);
      return;
    }
    // No server layout yet: hand this browser's arrangement up once so the
    // admin's other browsers inherit it instead of starting from defaults.
    // Only this account's own cache qualifies.
    const local = readStoredLayout(userId);
    if (local) {
      saveMutate(toLayoutDocument(local));
    }
  }, [remoteSettled, remoteData, saveMutate, userId]);

  const update = useCallback(
    (updater: (prev: DashboardLayoutEntry[]) => DashboardLayoutEntry[]) => {
      setEntries((prev) => {
        const next = updater(prev);
        if (next === prev) {
          return prev;
        }
        editedRef.current = true;
        persistLayout(userId, next);
        scheduleSave(next);
        return next;
      });
    },
    [scheduleSave, userId],
  );

  const moveWidget = useCallback(
    (id: WidgetId, beforeId: WidgetId | null) => {
      update((prev) => {
        if (id === beforeId) {
          return prev;
        }
        const moving = prev.find((entry) => entry.id === id);
        if (!moving) {
          return prev;
        }
        const without = prev.filter((entry) => entry.id !== id);
        if (beforeId === null) {
          return [...without, moving];
        }
        const index = without.findIndex((entry) => entry.id === beforeId);
        if (index === -1) {
          return [...without, moving];
        }
        return [...without.slice(0, index), moving, ...without.slice(index)];
      });
    },
    [update],
  );

  const resizeWidget = useCallback(
    (id: WidgetId, size: DashboardWidgetSize) => {
      update((prev) => {
        const widget = findDashboardWidget(id);
        if (!widget) {
          return prev;
        }
        let changed = false;
        const next = prev.map((entry) => {
          if (entry.id !== id) {
            return entry;
          }
          // Each axis is clamped to its own range, and an axis the caller left
          // out keeps the value it already had rather than snapping to a
          // default — a column drag must not silently restore a row height.
          const nextSpan = size.span === undefined ? entry.span : clampSpan(size.span, widget);
          const nextRows = size.rows === undefined ? entry.rows : clampRows(size.rows, widget);
          if (nextSpan === entry.span && nextRows === entry.rows) {
            return entry;
          }
          changed = true;
          return { ...entry, span: nextSpan, rows: nextRows };
        });
        return changed ? next : prev;
      });
    },
    [update],
  );

  const setWidgetRange = useCallback(
    (id: WidgetId, range: WidgetRange) => {
      update((prev) => {
        const widget = findDashboardWidget(id);
        // A range the widget does not offer is ignored rather than stored: the
        // endpoints clamp their windows, so the chart would silently disagree
        // with the picker.
        if (!widget?.ranges || !widget.ranges.allowed.includes(range)) {
          return prev;
        }
        let changed = false;
        const next = prev.map((entry) => {
          if (entry.id !== id || entry.range === range) {
            return entry;
          }
          changed = true;
          return { ...entry, range };
        });
        return changed ? next : prev;
      });
    },
    [update],
  );

  const removeWidget = useCallback(
    (id: WidgetId) => {
      update((prev) =>
        prev.some((entry) => entry.id === id) ? prev.filter((entry) => entry.id !== id) : prev,
      );
    },
    [update],
  );

  const addWidget = useCallback(
    (id: WidgetId, beforeId?: WidgetId | null) => {
      update((prev) => {
        const widget = findDashboardWidget(id);
        if (!widget || prev.some((entry) => entry.id === id)) {
          return prev;
        }
        const added: DashboardLayoutEntry = {
          id: widget.id,
          span: widget.defaultSpan,
          rows: widget.defaultRows,
        };
        if (widget.ranges) {
          added.range = widget.ranges.default;
        }
        // An unknown beforeId appends rather than dropping the add: the anchor
        // may have been removed between the drag starting and the drop landing.
        const index = beforeId == null ? -1 : prev.findIndex((entry) => entry.id === beforeId);
        if (index === -1) {
          return [...prev, added];
        }
        return [...prev.slice(0, index), added, ...prev.slice(index)];
      });
    },
    [update],
  );

  const resetLayout = useCallback(() => {
    // Drop the queued write first: saving the arrangement the admin just threw
    // away would resurrect it on the next load. A save already in flight cannot
    // be cancelled here — the shared mutation scope in
    // `hooks/queries/admin/dashboardLayout` makes the reset wait for it instead.
    cancelPendingSave();
    clearStoredLayout(userId);
    editedRef.current = true;
    settledRef.current = true;
    setEntries([...DEFAULT_LAYOUT]);
    resetMutate();
  }, [cancelPendingSave, resetMutate, userId]);

  const hiddenWidgets = useMemo(() => {
    const visible = new Set(entries.map((entry) => entry.id));
    return DASHBOARD_WIDGETS.filter((widget) => !visible.has(widget.id));
  }, [entries]);

  return {
    entries,
    hiddenWidgets,
    isCustomizing,
    setCustomizing,
    moveWidget,
    resizeWidget,
    setWidgetRange,
    removeWidget,
    addWidget,
    resetLayout,
  };
}
