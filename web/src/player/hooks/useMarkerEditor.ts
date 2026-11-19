import { useCallback, useEffect, useRef, useState } from "react";
import { t, type MessageKey } from "@/i18n";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { playerV2 } from "../player-v2";
import { markerUpdateToV2 } from "../marker-wire";
import type { MarkerDraft, MarkerKind, PlayerTimeRange } from "../types";

/** Edit order shown in the panel: chronological-ish within an episode. */
export const MARKER_KINDS: MarkerKind[] = ["intro", "recap", "credits", "preview"];

/** Message ids of the human labels. "credits" doubles as the outro, so we say so. */
export const MARKER_LABEL_KEYS: Record<MarkerKind, MessageKey> = {
  intro: "web.markers.kind.intro",
  recap: "web.markers.kind.recap",
  credits: "web.markers.kind.credits",
  preview: "web.markers.kind.preview",
};

/**
 * Solid accent per kind, shared by the seek bar's hover dot and the marker
 * editor panel. Every client uses this palette (TheIntroDB's segment colours,
 * the Tailwind 400 shades); the seek bar tints use the same hues.
 */
export const MARKER_DOT_COLORS: Record<MarkerKind, string> = {
  intro: "bg-blue-400",
  recap: "bg-orange-400",
  credits: "bg-purple-400",
  preview: "bg-cyan-400",
};

// Initial span (seconds) given to a freshly created marker before the user
// refines the opposite edge.
const NEW_MARKER_SPAN = 60;
// Minimum gap kept between start and end so a range never inverts.
const MIN_GAP = 0.5;

const EMPTY_DRAFT: MarkerDraft = { intro: null, recap: null, credits: null, preview: null };

function rangesEqual(a: PlayerTimeRange | null, b: PlayerTimeRange | null): boolean {
  if (a === null || b === null) return a === b;
  return a.start === b.start && a.end === b.end;
}

function normalizeDraft(markers: MarkerDraft): MarkerDraft {
  return {
    intro: markers.intro ?? null,
    recap: markers.recap ?? null,
    credits: markers.credits ?? null,
    preview: markers.preview ?? null,
  };
}

function clampRangeToUpper(range: PlayerTimeRange, upper: number): PlayerTimeRange {
  const start = Math.max(0, Math.min(upper, range.start));
  const end = Math.max(start, Math.min(upper, range.end));
  return { start, end };
}

export interface MarkerEditor {
  editing: boolean;
  draft: MarkerDraft;
  activeKind: MarkerKind;
  saving: boolean;
  error: string | null;
  dirty: boolean;
  canEdit: boolean;
  begin: () => void;
  cancel: () => void;
  selectKind: (kind: MarkerKind) => void;
  setEdge: (kind: MarkerKind, edge: "start" | "end", seconds: number) => void;
  clearKind: (kind: MarkerKind) => void;
  /** Reverts a segment to the value it had when editing began. */
  resetKind: (kind: MarkerKind) => void;
  /** Reverts every segment to the values they had when editing began. */
  resetAll: () => void;
  /** Whether a segment differs from its value when editing began. */
  isKindDirty: (kind: MarkerKind) => boolean;
  save: () => Promise<void>;
}

interface UseMarkerEditorParams {
  fileId: number | null | undefined;
  duration: number;
  canEdit?: boolean;
  /** The current saved markers (from props); snapshotted into the draft on begin. */
  markers: MarkerDraft;
  /**
   * Notifies the host of the kinds this save wrote so it can patch local state
   * immediately. Kinds the save left out are absent: the draft's copy of them is
   * the snapshot from when editing began, which another session may have
   * changed since.
   */
  onSaved?: (saved: Partial<MarkerDraft>) => void;
}

/**
 * Drives in-player marker editing. Holds a draft of all four segment ranges,
 * exposes edge/clear mutators (driven by both the edit panel and the seek-bar
 * handles), and persists changes via the authenticated /markers endpoint.
 *
 * On save only the segments the user actually touched are sent: unchanged
 * segments are omitted so an online/scanner-sourced marker isn't silently
 * rewritten to source="manual" just because the user edited a different one.
 */
export function useMarkerEditor({
  fileId,
  duration,
  canEdit: canEditParam = true,
  markers,
  onSaved,
}: UseMarkerEditorParams): MarkerEditor {
  const config = usePlayerConfig();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<MarkerDraft>(EMPTY_DRAFT);
  const [activeKind, setActiveKind] = useState<MarkerKind>("intro");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const originalRef = useRef<MarkerDraft>(EMPTY_DRAFT);
  const [draftFileId, setDraftFileId] = useState(fileId);

  // A draft belongs to the file it was opened on. A version switch keeps the
  // player mounted and hands us another file, so close the draft rather than
  // let Save write it to a file the edits were never made on.
  if (draftFileId !== fileId) {
    setDraftFileId(fileId);
    setEditing(false);
    setError(null);
  }

  // Each opening of the panel, so a save that resolves after the panel was
  // closed and opened again leaves the new opening's draft alone (#4108).
  const openingRef = useRef(0);

  // The file playing now, for a save that resolves after a version switch.
  const currentFileIdRef = useRef(fileId);
  useEffect(() => {
    currentFileIdRef.current = fileId;
  }, [fileId]);

  const canEdit = canEditParam && fileId != null && duration > 0;

  const begin = useCallback(() => {
    const snapshot = normalizeDraft(markers);
    openingRef.current += 1;
    originalRef.current = snapshot;
    setDraft(snapshot);
    setActiveKind(MARKER_KINDS.find((kind) => snapshot[kind] != null) ?? "intro");
    setError(null);
    setEditing(true);
  }, [markers]);

  const cancel = useCallback(() => {
    openingRef.current += 1;
    setEditing(false);
    setError(null);
  }, []);

  const selectKind = useCallback((kind: MarkerKind) => setActiveKind(kind), []);

  const setEdge = useCallback(
    (kind: MarkerKind, edge: "start" | "end", seconds: number) => {
      const upper = Math.max(0, duration > 0 ? duration : seconds);
      const clamped = Math.max(0, Math.min(upper, seconds));
      setDraft((prev) => {
        const existing = prev[kind];
        let next: PlayerTimeRange;
        if (!existing) {
          const span = duration > 0 ? Math.min(NEW_MARKER_SPAN, duration) : NEW_MARKER_SPAN;
          next =
            edge === "start"
              ? { start: clamped, end: Math.min(upper, clamped + span) }
              : { start: Math.max(0, clamped - span), end: clamped };
        } else if (edge === "start") {
          next = { start: Math.min(clamped, existing.end - MIN_GAP), end: existing.end };
        } else {
          next = { start: existing.start, end: Math.max(clamped, existing.start + MIN_GAP) };
        }
        return { ...prev, [kind]: clampRangeToUpper(next, upper) };
      });
      setActiveKind(kind);
      setError(null);
    },
    [duration],
  );

  const clearKind = useCallback((kind: MarkerKind) => {
    setDraft((prev) => ({ ...prev, [kind]: null }));
    setError(null);
  }, []);

  const resetKind = useCallback((kind: MarkerKind) => {
    setDraft((prev) => ({ ...prev, [kind]: originalRef.current[kind] }));
    setError(null);
  }, []);

  const resetAll = useCallback(() => {
    setDraft(originalRef.current);
    setError(null);
  }, []);

  const isKindDirty = useCallback(
    (kind: MarkerKind) => !rangesEqual(draft[kind], originalRef.current[kind]),
    [draft],
  );

  const dirty = MARKER_KINDS.some((kind) => !rangesEqual(draft[kind], originalRef.current[kind]));

  const save = useCallback(async () => {
    if (!editing || fileId == null || !canEdit) return;
    const original = originalRef.current;
    const body: Partial<MarkerDraft> = {};
    for (const kind of MARKER_KINDS) {
      if (rangesEqual(draft[kind], original[kind])) continue;
      const range = draft[kind];
      body[kind] = range ? { start: range.start, end: range.end } : null;
    }
    if (Object.keys(body).length === 0) {
      setEditing(false);
      return;
    }
    const opening = openingRef.current;
    // Whether the panel still shows the draft this save sent: the same file,
    // and not closed or opened again since.
    const stillThisDraft = () =>
      currentFileIdRef.current === fileId && openingRef.current === opening;
    setSaving(true);
    setError(null);
    try {
      await playerV2(config, "PUT /api/v2/markers/files/{file_id}", {
        path: { file_id: String(fileId) },
        body: markerUpdateToV2(body),
      });
    } catch (e) {
      // Show the failure even on a panel reopened on this file since, so the
      // user learns the earlier edits didn't save. After a version switch the
      // error would land on another file's panel, so only log it.
      if (currentFileIdRef.current === fileId) {
        setError(e instanceof Error ? e.message : t("web.markers.saveFailed"));
      } else {
        console.error("Marker save failed after a version switch:", e);
      }
      return;
    } finally {
      setSaving(false);
    }
    // A version switch, or closing and reopening the panel, during the request
    // closed this draft, and the editor may since hold a newer one. Leave that
    // one's edits alone.
    if (stillThisDraft()) {
      originalRef.current = draft;
      setDraft(draft);
      setEditing(false);
    } else if (currentFileIdRef.current === fileId) {
      // Reopened on the same file: its "before" values were read while this
      // save was in flight. The saved kinds are its baseline now, and a kind
      // it hasn't touched shows the saved value, so its dirty state and its
      // next save compare against what the server holds.
      const before = originalRef.current;
      const saved = { ...before };
      for (const kind of MARKER_KINDS) {
        if (kind in body) saved[kind] = draft[kind];
      }
      originalRef.current = saved;
      setDraft((current) => {
        const next = { ...current };
        for (const kind of MARKER_KINDS) {
          if (kind in body && rangesEqual(current[kind], before[kind])) next[kind] = draft[kind];
        }
        return next;
      });
    }
    try {
      onSaved?.(body);
    } catch (e) {
      console.error("Marker save callback failed:", e);
    }
  }, [canEdit, config, draft, editing, fileId, onSaved]);

  return {
    editing,
    draft,
    activeKind,
    saving,
    error,
    dirty,
    canEdit,
    begin,
    cancel,
    selectKind,
    setEdge,
    clearKind,
    resetKind,
    resetAll,
    isKindDirty,
    save,
  };
}
