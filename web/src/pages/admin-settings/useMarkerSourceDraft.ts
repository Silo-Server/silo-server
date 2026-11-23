import { useRef } from "react";

import type { useSettingsForm } from "@/hooks/useSettingsForm";

export const MARKER_MODE_KEY = "markers.mode";
export const MARKER_ONLINE_STORAGE_KEY = "markers.online_storage";
export const MARKER_LAZY_PLAYBACK_KEY = "markers.lazy_playback";

type MarkerForm = Pick<
  ReturnType<typeof useSettingsForm>,
  "getValue" | "getPersistedValue" | "setValue" | "resetValue" | "isDirty"
>;

/** What `markers.lazy_playback` held before source edits staged it. */
interface LazyPlaybackBefore {
  dirty: boolean;
  value: string;
}

function sources(mode: string) {
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
  const before = sources(saved);
  const after = sources(next);
  if ((after.online && !before.online) || (after.local && !before.local)) return "true";
  if (!after.online && !after.local) return "false";
  return undefined;
}

/**
 * Edits to the marker source and online storage, which stage
 * `markers.lazy_playback` along with them: on-demand online storage turns it
 * on, and where `modeStagesLazyPlayback` is set (the setup wizard, which has no
 * lazy playback row), so does the source change itself. The staged value is
 * worked out from the source and storage edits still standing, not from the
 * order they were made in, and once none of them implies one, lazy playback
 * goes back to what it held before. Save therefore never writes it without
 * the visible edit that caused it. Setting it directly makes it the admin's
 * own edit.
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
    if (storage === "on_demand" && storage !== form.getPersistedValue(MARKER_ONLINE_STORAGE_KEY)) {
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
    if (before.dirty) form.setValue(MARKER_LAZY_PLAYBACK_KEY, before.value);
    else form.resetValue(MARKER_LAZY_PLAYBACK_KEY);
  }

  return {
    setMarkerMode: (value: string) => change(MARKER_MODE_KEY, value),
    setOnlineStorage: (value: string) => change(MARKER_ONLINE_STORAGE_KEY, value),
    setLazyPlayback: (value: string) => {
      lazyBefore.current = null;
      form.setValue(MARKER_LAZY_PLAYBACK_KEY, value);
    },
  };
}

/**
 * On-demand online storage staged in the draft: it carries the lazy playback
 * value it turned on, so that value needs no row of its own.
 */
export function onDemandStorageStaged(form: MarkerForm) {
  return (
    form.isDirty(MARKER_ONLINE_STORAGE_KEY) &&
    form.getValue(MARKER_ONLINE_STORAGE_KEY) === "on_demand"
  );
}
