import { useEffect, useId, useSyncExternalStore } from "react";

/**
 * Registry of forms that currently hold staged, unsaved edits.
 *
 * Draft state belongs to the component that owns the form (see
 * `useSettingsForm`), so nothing above it can tell whether navigating away
 * would throw work on the floor. Each form reports "I am dirty" here instead,
 * and a guard mounted higher in the tree (`UnsavedChangesGuard`) turns that
 * into a router-level block with a confirmation prompt.
 *
 * A module store rather than a context on purpose: the reporting side stays
 * free of both a provider and the router, so `useSettingsForm` keeps working in
 * places that mount no guard at all (the setup wizard) and in plain
 * `renderHook` tests.
 */
const dirtySources = new Set<string>();
const listeners = new Set<() => void>();

function handleBeforeUnload(event: BeforeUnloadEvent) {
  event.preventDefault();
  // Chrome still requires returnValue to be set for the prompt to appear.
  event.returnValue = "";
}

// The router guard covers in-app navigation; reload and tab close never reach
// the router, so the registry arms the browser's own prompt whenever anything
// is dirty. Registering the same handler twice is a no-op, so this can run on
// every change.
function syncBeforeUnload() {
  if (typeof window === "undefined") return;
  if (dirtySources.size > 0) {
    window.addEventListener("beforeunload", handleBeforeUnload);
  } else {
    window.removeEventListener("beforeunload", handleBeforeUnload);
  }
}

function emit() {
  syncBeforeUnload();
  for (const listener of listeners) {
    listener();
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function hasUnsavedChangesSnapshot() {
  return dirtySources.size > 0;
}

/**
 * Reports one form's dirty state to the registry. The entry is removed when the
 * form goes clean (save or discard) and when the component unmounts, so a form
 * can never leave a stale claim behind.
 */
export function useReportUnsavedChanges(hasUnsavedChanges: boolean) {
  // One stable id per hook instance: two dirty pages must count as two
  // sources, and remounting the same page must not leak the old entry.
  const id = useId();

  useEffect(() => {
    if (!hasUnsavedChanges) return;
    dirtySources.add(id);
    emit();
    return () => {
      dirtySources.delete(id);
      emit();
    };
  }, [hasUnsavedChanges, id]);
}

/** True while any mounted form has edits that were never saved. */
export function useHasUnsavedChanges(): boolean {
  return useSyncExternalStore(subscribe, hasUnsavedChangesSnapshot, hasUnsavedChangesSnapshot);
}
