import { useCallback, useEffect, useRef, useState } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { playerFetch } from "../player-fetch";
import type { MarkerDraft, MarkerKind, PlayerTimeRange } from "../types";

/** Edit order shown in the panel: chronological-ish within an episode. */
export const MARKER_KINDS: MarkerKind[] = ["intro", "recap", "credits", "preview"];

/** Human labels. "credits" doubles as the outro, so we say so. */
export const MARKER_LABELS: Record<MarkerKind, string> = {
  intro: "Intro",
  recap: "Recap",
  credits: "Credits / Outro",
  preview: "Preview",
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

function draftsEqual(a: MarkerDraft, b: MarkerDraft): boolean {
  return MARKER_KINDS.every((kind) => rangesEqual(a[kind], b[kind]));
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
  /** Notifies the host of the saved draft so it can patch local state immediately. */
  onSaved?: (draft: MarkerDraft) => void;
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
  const savedBaselineRef = useRef<MarkerDraft | null>(null);

  const canEdit = canEditParam && fileId != null && duration > 0;

  useEffect(() => {
    const saved = savedBaselineRef.current;
    if (saved && draftsEqual(normalizeDraft(markers), saved)) {
      savedBaselineRef.current = null;
    }
  }, [markers]);

  useEffect(() => {
    savedBaselineRef.current = null;
  }, [fileId]);

  const begin = useCallback(() => {
    const snapshot = savedBaselineRef.current ?? normalizeDraft(markers);
    originalRef.current = snapshot;
    setDraft(snapshot);
    setActiveKind(MARKER_KINDS.find((kind) => snapshot[kind] != null) ?? "intro");
    setError(null);
    setEditing(true);
  }, [markers]);

  const cancel = useCallback(() => {
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
    if (fileId == null || !canEdit) return;
    const original = originalRef.current;
    const body: Record<string, { start: number; end: number } | null> = {};
    for (const kind of MARKER_KINDS) {
      if (rangesEqual(draft[kind], original[kind])) continue;
      const range = draft[kind];
      body[kind] = range ? { start: range.start, end: range.end } : null;
    }
    if (Object.keys(body).length === 0) {
      setEditing(false);
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await playerFetch(config, `/markers/files/${fileId}`, {
        method: "PUT",
        body: JSON.stringify(body),
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to save markers");
      return;
    } finally {
      setSaving(false);
    }
    originalRef.current = draft;
    savedBaselineRef.current = draft;
    setDraft(draft);
    setEditing(false);
    try {
      onSaved?.(draft);
    } catch (e) {
      console.error("Marker save callback failed:", e);
    }
  }, [canEdit, config, draft, fileId, onSaved]);

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
