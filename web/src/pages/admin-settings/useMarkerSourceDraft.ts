import { useState } from "react";

import type { useSettingsForm } from "@/hooks/useSettingsForm";

export const MARKER_MODE_KEY = "markers.mode";
export const MARKER_ONLINE_STORAGE_KEY = "markers.online_storage";
export const MARKER_LAZY_PLAYBACK_KEY = "markers.lazy_playback";

type MarkerForm = Pick<
  ReturnType<typeof useSettingsForm>,
  "getValue" | "getPersistedValue" | "setValue" | "resetValue" | "isDirty"
>;

/** What `markers.lazy_playback` held before a source change staged it. */
interface LazyPlaybackBefore {
  dirty: boolean;
  value: string;
}

/**
 * Edits to the marker source and online storage, which can stage
 * `markers.lazy_playback` along with them (on-demand online markers turn it
 * on). A value staged that way belongs to those edits: once neither the source
 * nor the storage holds an edit, it is taken back to what it was before, so
 * Save never writes it on its own. Setting it directly makes it the admin's
 * own edit, which source changes no longer take back.
 */
export function useMarkerSourceDraft(form: MarkerForm) {
  const [lazyBefore, setLazyBefore] = useState<LazyPlaybackBefore | null>(null);
  // Only a draft still staged counts; a save or discard settles it.
  const lazyPlaybackLinked = lazyBefore !== null && form.isDirty(MARKER_LAZY_PLAYBACK_KEY);

  function change(key: string, value: string, lazyPlayback?: string) {
    form.setValue(key, value);
    const other = key === MARKER_MODE_KEY ? MARKER_ONLINE_STORAGE_KEY : MARKER_MODE_KEY;
    const edited = value !== form.getPersistedValue(key);
    if (!edited && !form.isDirty(other)) {
      if (lazyPlaybackLinked) {
        if (lazyBefore.dirty) form.setValue(MARKER_LAZY_PLAYBACK_KEY, lazyBefore.value);
        else form.resetValue(MARKER_LAZY_PLAYBACK_KEY);
      }
      setLazyBefore(null);
      return;
    }
    // A change back to the saved value stages nothing of its own.
    if (lazyPlayback === undefined || !edited) return;
    if (!lazyPlaybackLinked) {
      setLazyBefore({
        dirty: form.isDirty(MARKER_LAZY_PLAYBACK_KEY),
        value: form.getValue(MARKER_LAZY_PLAYBACK_KEY),
      });
    }
    form.setValue(MARKER_LAZY_PLAYBACK_KEY, lazyPlayback);
  }

  return {
    /** Stages a marker source, and `lazyPlayback` with it when given. */
    setMarkerMode: (value: string, lazyPlayback?: string) =>
      change(MARKER_MODE_KEY, value, lazyPlayback),
    /** Stages the online storage; on demand also turns lazy playback on. */
    setOnlineStorage: (value: string) =>
      change(MARKER_ONLINE_STORAGE_KEY, value, value === "on_demand" ? "true" : undefined),
    setLazyPlayback: (value: string) => {
      setLazyBefore(null);
      form.setValue(MARKER_LAZY_PLAYBACK_KEY, value);
    },
    /**
     * The staged lazy playback value came with a source or storage edit, which
     * shows it; it needs no row of its own.
     */
    lazyPlaybackLinked,
  };
}
