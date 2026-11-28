import { useRef } from "react";

import type { useSettingsForm } from "@/hooks/useSettingsForm";

export const MARKER_MODE_KEY = "markers.mode";
export const MARKER_LAZY_PLAYBACK_KEY = "markers.lazy_playback";
export const MARKER_ONLINE_STORAGE_KEY = "markers.online_storage";

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
 * implies under the online `storage` choice: a source turned on looks
 * markers up at playback, and turning every source off stops that. Online
 * markers fetched when needed are looked up at playback without it, so
 * turning them on implies nothing. Dropping one of two sources leaves it
 * alone.
 */
function lazyPlaybackForMode(saved: string, next: string, storage: string): string | undefined {
  const before = markerSources(saved);
  const after = markerSources(next);
  const onlineOn = after.online && !before.online && storage !== "on_demand";
  if (onlineOn || (after.local && !before.local)) return "true";
  if (!after.online && !after.local) return "false";
  return undefined;
}

/**
 * Marker source edits for the setup wizard, which has no lazy playback row:
 * a source change stages `markers.lazy_playback` along with it. The value is
 * worked out from the source and storage edits still standing, and once they
 * imply none, lazy playback goes back to what it held before. Save therefore
 * never writes it without the visible edit that caused it. A storage change
 * on its own leaves lazy playback alone: on-demand lookups run at playback
 * whatever it holds, and under server detection it also turns on local
 * detection.
 */
export function useMarkerSourceDraft(form: MarkerForm) {
  const lazyBefore = useRef<LazyPlaybackBefore | null>(null);

  function forgetStaleBefore() {
    // With nothing staged, a remembered value is from before a save or
    // discard and no longer what to go back to.
    if (!form.isDirty(MARKER_MODE_KEY) && !form.isDirty(MARKER_LAZY_PLAYBACK_KEY)) {
      lazyBefore.current = null;
    }
  }

  function stageLazyPlayback(mode: string, storage: string) {
    const savedMode = form.getPersistedValue(MARKER_MODE_KEY);
    const lazyPlayback =
      mode === savedMode ? undefined : lazyPlaybackForMode(savedMode, mode, storage);
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

  function setMarkerMode(value: string) {
    forgetStaleBefore();
    form.setValue(MARKER_MODE_KEY, value);
    stageLazyPlayback(value, form.getValue(MARKER_ONLINE_STORAGE_KEY));
  }

  function setOnlineStorage(value: string) {
    forgetStaleBefore();
    form.setValue(MARKER_ONLINE_STORAGE_KEY, value);
    stageLazyPlayback(form.getValue(MARKER_MODE_KEY), value);
  }

  return { setMarkerMode, setOnlineStorage };
}
