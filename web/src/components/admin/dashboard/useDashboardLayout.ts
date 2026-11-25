import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import type { AdminDashboardLayoutDocument } from "@/api/types";
import {
  captureDashboardLayoutSave,
  type DashboardLayoutSaveIntent,
  useAdminDashboardLayout,
  useDashboardLayoutWrites,
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

// Version 1 is still version 1 with row heights, per-widget windows and views
// in it: `rows`, `range` and `view` are additive fields that older documents
// simply omit, and the widget's defaults stand in for them. A bump would only
// be needed for a change that makes an existing field mean something new.
interface StoredLayout {
  version: 1;
  entries: DashboardLayoutEntry[];
  /**
   * Present while `entries` hold an edit made before the server layout was
   * read, so it is not saved yet: the arrangement the edit started from and
   * the profile it was made under. A later visit reconciles it the way the
   * visit that made it would have, had the read returned first. Local only;
   * never sent to the server.
   */
  pending?: { base: DashboardLayoutEntry[]; profileId: string };
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

/** A stored view the widget still offers, or undefined so the default applies. */
function sanitizeView(view: unknown, widget: DashboardWidgetDefinition): string | undefined {
  return typeof view === "string" && widget.views?.allowed.includes(view) ? view : undefined;
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
    const view = sanitizeView(entry.view, widget);
    if (view) {
      sanitized.view = view;
    }
    entries.push(sanitized);
  }
  return entries;
}

function readStoredDocument(userId: number | null): unknown {
  if (userId === null) {
    return null;
  }
  try {
    const raw = window.localStorage.getItem(dashboardLayoutStorageKey(userId));
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

function readStoredLayout(userId: number | null): DashboardLayoutEntry[] | null {
  return sanitizeLayoutDocument(readStoredDocument(userId));
}

type ProfileAuthority = NonNullable<ReturnType<typeof captureProfileRequestContext>>;

/**
 * An edit made before the server layout was read, not yet saved: the
 * arrangement it started from, the profile it was made under, and that
 * profile's authority, or null when the active profile is a different one.
 */
interface DashboardLayoutDraft {
  base: DashboardLayoutEntry[];
  profileId: string;
  authority: ProfileAuthority | null;
}

/**
 * The unsaved edit an earlier visit left in this account's cache, with the
 * entries it produced. Only the profile that made it can save it; under any
 * other the draft carries no authority and stays unsaved.
 */
function readStoredDraft(
  userId: number | null,
  authority: ProfileAuthority | null,
): { draft: DashboardLayoutDraft; entries: DashboardLayoutEntry[] } | null {
  const stored = readStoredDocument(userId) as Partial<StoredLayout> | null;
  const pending = stored?.pending;
  if (!pending || typeof pending.profileId !== "string") {
    return null;
  }
  const base = sanitizeLayoutDocument({ version: 1, entries: pending.base });
  const entries = sanitizeLayoutDocument(stored);
  if (!base || !entries) {
    return null;
  }
  const profileId = pending.profileId;
  return {
    draft: { base, profileId, authority: authority?.profileId === profileId ? authority : null },
    entries,
  };
}

function loadStoredLayout(userId: number | null): DashboardLayoutEntry[] {
  return readStoredLayout(userId) ?? [...DEFAULT_LAYOUT];
}

function toLayoutDocument(entries: DashboardLayoutEntry[]): AdminDashboardLayoutDocument {
  return { version: 1, entries };
}

function persistLayout(
  userId: number | null,
  entries: DashboardLayoutEntry[],
  draft?: DashboardLayoutDraft,
) {
  if (userId === null) {
    return;
  }
  try {
    const stored: StoredLayout = { version: 1, entries };
    if (draft) {
      stored.pending = { base: draft.base, profileId: draft.profileId };
    }
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
  /** Change a widget's presentation; saved the same way as its window. */
  setWidgetView: (id: WidgetId, view: string) => void;
  removeWidget: (id: WidgetId) => void;
  /**
   * Place a hidden widget with its default span, rows, and window. With a
   * `beforeId` it is inserted in front of that entry — one layout update, so a
   * sheet drop rides a single debounced save rather than an add-then-move pair.
   * Without one (or with an id that is not placed) it appends.
   */
  addWidget: (id: WidgetId, beforeId?: WidgetId | null) => void;
  resetLayout: () => void;
  reloadServerLayout: () => Promise<void>;
  serverSaveBlocked: boolean;
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
  const [serverSaveBlocked, setServerSaveBlocked] = useState(false);

  useEffect(clearLegacyStoredLayout, []);

  const remote = useAdminDashboardLayout();
  const { running: layoutWritesRunning, isRunning: isLayoutWriteRunning } =
    useDashboardLayoutWrites();
  const saveLayout = useSaveAdminDashboardLayout();
  const resetRemoteLayout = useResetAdminDashboardLayout();

  const saveMutate = saveLayout.mutateCaptured;
  const resetMutate = resetRemoteLayout.mutate;
  const resetAuthority = captureProfileRequestContext();
  const editAuthority = captureProfileRequestContext();

  // The server response is adopted at most once per mount, and never over an
  // edit the admin already made in this session.
  const settledRef = useRef(false);
  // Set by the first edit made before that response arrives: the arrangement
  // the edit started from, and the authority it was made under. The edit can
  // only be saved once the server version is known, and only if the server
  // still holds that arrangement. It is kept in the cache too, so an edit
  // whose visit ended before the response arrived is reconciled by the next.
  const draftRef = useRef<DashboardLayoutDraft | null>(null);
  // A draft that settling left unsaved: another profile's, one made under a
  // profile or PIN no longer active, or one the server layout has moved on
  // from. The cache keeps marking it pending, through later edits here, so a
  // later visit still reports it instead of adopting the server layout over
  // it, until the admin reloads or resets the layout.
  const heldDraftRef = useRef<DashboardLayoutDraft | null>(null);
  const pendingRef = useRef<DashboardLayoutSaveIntent | null>(null);
  const baselineRef = useRef<string | null>(null);
  const inFlightRef = useRef<DashboardLayoutSaveIntent | null>(null);
  const baselineAuthorityRef = useRef<ReturnType<typeof captureProfileRequestContext>>(null);
  const editSequenceRef = useRef(0);
  const blockedRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const flushSave = useCallback(
    function flush() {
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
      if (inFlightRef.current || blockedRef.current) return;
      const intent = pendingRef.current;
      if (!intent) return;
      pendingRef.current = null;
      inFlightRef.current = intent;
      saveMutate(intent, {
        onSuccess: (etag) => {
          if (inFlightRef.current !== intent) return;
          inFlightRef.current = null;
          // Only this acknowledged write can advance retained local edits. A
          // refreshed GET may have observed a competitor and is never used here.
          if (!blockedRef.current && baselineRef.current === intent.etag) {
            baselineRef.current = etag;
            if (pendingRef.current?.etag === intent.etag)
              pendingRef.current = { ...pendingRef.current, etag };
            flush();
          }
        },
        onError: () => {
          if (inFlightRef.current !== intent) return;
          inFlightRef.current = null;
          if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
          blockedRef.current = true;
          setServerSaveBlocked(true);
        },
      });
    },
    [saveMutate],
  );

  const scheduleSave = useCallback(
    (next: DashboardLayoutEntry[]) => {
      if (!editAuthority || !isCapturedProfileAuthorityActive(editAuthority)) return;
      if (
        !baselineRef.current ||
        !baselineAuthorityRef.current ||
        !isCapturedProfileAuthorityActive(baselineAuthorityRef.current) ||
        blockedRef.current
      ) {
        setServerSaveBlocked(true);
        return;
      }
      pendingRef.current = captureDashboardLayoutSave(
        toLayoutDocument(next),
        baselineRef.current,
        editAuthority,
      );
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current);
      }
      timerRef.current = setTimeout(flushSave, DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS);
    },
    [flushSave, editAuthority],
  );

  const cancelPendingSave = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    pendingRef.current = null;
    draftRef.current = null;
    heldDraftRef.current = null;
  }, []);

  // Send a queued write before this page goes away rather than dropping it.
  // A reload or tab close inside the debounce window would otherwise lose it,
  // and the next load would adopt the older server copy over the admin's last
  // change. Layout saves use keepalive, so the request outlives the page.
  useEffect(() => {
    const flushOnPageHide = () => flushSave();
    window.addEventListener("pagehide", flushOnPageHide);
    return () => {
      window.removeEventListener("pagehide", flushOnPageHide);
      flushSave();
    };
  }, [flushSave]);

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
    baselineRef.current = null;
    baselineAuthorityRef.current = null;
    inFlightRef.current = null;
    blockedRef.current = false;
    setServerSaveBlocked(false);
    setEntries(loadStoredLayout(userId));
  }, [userId, cancelPendingSave]);

  const remoteData = remote.data;
  const remoteSettled = remote.isSuccess;
  const remoteFailed = remote.isError;
  const remoteFetching = remote.isFetching;

  useEffect(() => {
    if (settledRef.current) {
      return;
    }
    if (!remoteSettled) {
      // Without a server version an edit made meanwhile cannot be saved yet.
      if (remoteFailed && (draftRef.current || readStoredDraft(userId, editAuthority))) {
        setServerSaveBlocked(true);
      }
      return;
    }
    // Another page's write may still be on its way, or the refetch it
    // triggered still running: an Insights card's Add to dashboard is flushed
    // as that page unmounts, in the same commit that mounts this one. The
    // cached copy predates that write, so adopting it would drop the change
    // and leave this page saving against a version the server no longer
    // holds. Settle on the copy the write leaves behind instead.
    if (remoteFetching || isLayoutWriteRunning()) {
      return;
    }
    // A draft left by an earlier visit is reconciled with the entries it
    // produced, which may be newer than this page's if another tab made it.
    const restored = draftRef.current ? null : readStoredDraft(userId, editAuthority);
    const draft = draftRef.current ?? restored?.draft ?? null;
    const edited = restored?.entries ?? entries;
    draftRef.current = null;
    settledRef.current = true;
    baselineRef.current = remoteData?.etag ?? null;
    // A draft made in this page under a profile or PIN that is no longer
    // active stays unsaved, and the baseline keeps that inactive authority so
    // later edits here are refused too. One from an earlier visit is saved
    // under its profile's current authority, or not at all under another
    // profile.
    baselineAuthorityRef.current = draft?.authority ?? editAuthority;
    const serverEntries = sanitizeLayoutDocument(remoteData?.layout);
    // An edit made while the query was in flight is newer than the response,
    // so the response is not adopted over it. The edit was made to this
    // browser's copy, so it is saved against the version just read only when
    // the server still holds that copy, or none (as the migration below would
    // upload it). A different server copy, saved from another browser, is not
    // overwritten: saving stays blocked until the admin reloads it. The cache
    // stops marking the edit pending only once it goes to the save queue.
    if (draft) {
      if (restored) {
        setEntries(edited);
      }
      const unsaved =
        !draft.authority ||
        !isCapturedProfileAuthorityActive(draft.authority) ||
        (serverEntries !== null && JSON.stringify(serverEntries) !== JSON.stringify(draft.base));
      heldDraftRef.current = unsaved ? draft : null;
      persistLayout(userId, edited, heldDraftRef.current ?? undefined);
      if (unsaved) {
        blockedRef.current = true;
        setServerSaveBlocked(true);
        return;
      }
      setServerSaveBlocked(false);
      scheduleSave(edited);
      return;
    }
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
      if (editAuthority && isCapturedProfileAuthorityActive(editAuthority) && baselineRef.current) {
        pendingRef.current = captureDashboardLayoutSave(
          toLayoutDocument(local),
          baselineRef.current,
          editAuthority,
        );
        flushSave();
      }
    }
  }, [
    remoteSettled,
    remoteFailed,
    remoteData,
    remoteFetching,
    layoutWritesRunning,
    isLayoutWriteRunning,
    flushSave,
    scheduleSave,
    entries,
    userId,
    editAuthority,
  ]);

  const update = useCallback(
    (updater: (prev: DashboardLayoutEntry[]) => DashboardLayoutEntry[]) => {
      if (!editAuthority || !isCapturedProfileAuthorityActive(editAuthority)) return;
      setEntries((prev) => {
        const next = updater(prev);
        if (next === prev) {
          return prev;
        }
        editSequenceRef.current++;
        if (settledRef.current) {
          persistLayout(userId, next, heldDraftRef.current ?? undefined);
          scheduleSave(next);
        } else {
          // An unsaved edit left by an earlier visit keeps its base: the server
          // is compared with the copy that visit last read from it.
          draftRef.current ??= readStoredDraft(userId, editAuthority)?.draft ?? {
            base: prev,
            profileId: editAuthority.profileId,
            authority: editAuthority,
          };
          persistLayout(userId, next, draftRef.current);
        }
        return next;
      });
    },
    [scheduleSave, userId, editAuthority],
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

  const setWidgetView = useCallback(
    (id: WidgetId, view: string) => {
      update((prev) => {
        const widget = findDashboardWidget(id);
        if (!widget?.views || !sanitizeView(view, widget)) {
          return prev;
        }
        const defaultView = widget.views.default;
        let changed = false;
        const next = prev.map((entry) => {
          // An entry without a view is showing the default, so picking the
          // default there changes nothing and must not save.
          if (entry.id !== id || (entry.view ?? defaultView) === view) {
            return entry;
          }
          changed = true;
          return { ...entry, view };
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
    if (!resetAuthority || !isCapturedProfileAuthorityActive(resetAuthority)) return;
    // Drop the queued write first: saving the arrangement the admin just threw
    // away would resurrect it on the next load. A save already in flight cannot
    // be cancelled here — the shared mutation scope in
    // `hooks/queries/admin/dashboardLayout` makes the reset wait for it instead.
    cancelPendingSave();
    editSequenceRef.current++;
    baselineRef.current = null;
    blockedRef.current = true;
    setServerSaveBlocked(true);
    clearStoredLayout(userId);
    settledRef.current = true;
    setEntries([...DEFAULT_LAYOUT]);
    resetMutate();
  }, [cancelPendingSave, resetMutate, userId, resetAuthority]);

  const reloadServerLayout = async () => {
    const authority = captureProfileRequestContext();
    if (
      !authority ||
      !isCapturedProfileAuthorityActive(authority) ||
      inFlightRef.current ||
      resetRemoteLayout.isPending
    )
      return;
    const editSequence = editSequenceRef.current;
    const result = await remote.refetch();
    if (
      !isCapturedProfileAuthorityActive(authority) ||
      !result.isSuccess ||
      !result.data?.etag ||
      editSequenceRef.current !== editSequence
    )
      return;
    cancelPendingSave();
    const loaded = sanitizeLayoutDocument(result.data.layout) ?? [...DEFAULT_LAYOUT];
    setEntries(loaded);
    persistLayout(userId, loaded);
    baselineRef.current = result.data.etag;
    baselineAuthorityRef.current = authority;
    blockedRef.current = false;
    setServerSaveBlocked(false);
    settledRef.current = true;
  };

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
    setWidgetView,
    removeWidget,
    addWidget,
    resetLayout,
    reloadServerLayout,
    serverSaveBlocked,
  };
}
