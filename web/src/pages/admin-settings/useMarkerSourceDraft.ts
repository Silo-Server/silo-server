import { useRef } from "react";

import type { useSettingsForm } from "@/hooks/useSettingsForm";

export const MARKER_MODE_KEY = "markers.mode";
export const MARKER_ONLINE_STORAGE_KEY = "markers.online_storage";
export const MARKER_LAZY_PLAYBACK_KEY = "markers.lazy_playback";

type MarkerForm = Pick<
  ReturnType<typeof useSettingsForm>,
  "getValue" | "getPersistedValue" | "setValue" | "isDirty"
>;

/** What `markers.lazy_playback` held before source edits staged it. */
interface LazyPlaybackBefore {
  dirty: boolean;
  value: string;
}

/** Which marker sources a `markers.mode` value turns on. */
export function markerSources(mode: string) {
  return {
    online: mode === "online" || mode === "both",
    local: mode === "local" || mode === "both",
  };
}

/**
 * The lazy playback value a marker source change from `saved` to `next`
 * implies: a source turned on looks markers up at playback, and turning every
 * source off stops that. Dropping one of two sources leaves it alone.
 */
function lazyPlaybackForMode(saved: string, next: string): string | undefined {
  const before = markerSources(saved);
  const after = markerSources(next);
  if ((after.online && !before.online) || (after.local && !before.local)) return "true";
  if (!after.online && !after.local) return "false";
  return undefined;
}

/**
 * Edits to the marker source and online storage, which stage
 * `markers.lazy_playback` along with them: on-demand online storage turns it
 * on while online markers are, and where `modeStagesLazyPlayback` is set (the
 * setup wizard, which has no lazy playback row), so does the source change
 * itself. The value is worked out from the source and storage edits still
 * standing, not from the order they were made in, and once none of them
 * implies one, lazy playback goes back to what it held before, the admin's
 * own edit included. Save therefore never writes it without the visible edit
 * that caused it.
 */
export function useMarkerSourceDraft(
  form: MarkerForm,
  { modeStagesLazyPlayback = false }: { modeStagesLazyPlayback?: boolean } = {},
) {
  const lazyBefore = useRef<LazyPlaybackBefore | null>(null);

  function change(key: string, value: string) {
    // With nothing staged, a remembered value is from before a save or
    // discard and no longer what to go back to.
    if (
      !form.isDirty(MARKER_MODE_KEY) &&
      !form.isDirty(MARKER_ONLINE_STORAGE_KEY) &&
      !form.isDirty(MARKER_LAZY_PLAYBACK_KEY)
    ) {
      lazyBefore.current = null;
    }
    form.setValue(key, value);

    const draft = (k: string) => (k === key ? value : form.getValue(k));
    const mode = draft(MARKER_MODE_KEY);
    const savedMode = form.getPersistedValue(MARKER_MODE_KEY);
    const storage = draft(MARKER_ONLINE_STORAGE_KEY);
    let lazyPlayback: string | undefined;
    if (
      markerSources(mode).online &&
      storage === "on_demand" &&
      storage !== form.getPersistedValue(MARKER_ONLINE_STORAGE_KEY)
    ) {
      lazyPlayback = "true";
    } else if (modeStagesLazyPlayback && mode !== savedMode) {
      lazyPlayback = lazyPlaybackForMode(savedMode, mode);
    }

    if (lazyPlayback !== undefined) {
      lazyBefore.current ??= {
        dirty: form.isDirty(MARKER_LAZY_PLAYBACK_KEY),
        value: form.getValue(MARKER_LAZY_PLAYBACK_KEY),
      };
      form.setValue(MARKER_LAZY_PLAYBACK_KEY, lazyPlayback);
      return;
    }
    const before = lazyBefore.current;
    if (!before) return;
    lazyBefore.current = null;
    // The remembered value, through setValue rather than resetValue: taken
    // back while a save is in flight, or before the snapshot it wrote has
    // arrived, it stays staged, so the next save undoes what that save wrote.
    // The saved value would be wrong here once a refetch shows the write.
    form.setValue(MARKER_LAZY_PLAYBACK_KEY, before.value);
  }

  return {
    setMarkerMode: (value: string) => change(MARKER_MODE_KEY, value),
    setOnlineStorage: (value: string) => change(MARKER_ONLINE_STORAGE_KEY, value),
    // The admin's own edit: nothing to go back to.
    setLazyPlayback: (value: string) => {
      lazyBefore.current = null;
      form.setValue(MARKER_LAZY_PLAYBACK_KEY, value);
    },
  };
}

/**
 * Lazy playback staged on by on-demand online storage, which shows it; it
 * needs no row of its own. A different staged value is the admin's own.
 */
export function lazyPlaybackCarriedByStorage(form: MarkerForm) {
  return (
    markerSources(form.getValue(MARKER_MODE_KEY)).online &&
    form.isDirty(MARKER_ONLINE_STORAGE_KEY) &&
    form.getValue(MARKER_ONLINE_STORAGE_KEY) === "on_demand" &&
    form.getValue(MARKER_LAZY_PLAYBACK_KEY) === "true"
  );
}
