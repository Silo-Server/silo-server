import { useEffect, useRef } from "react";
import { useBlocker } from "react-router";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { useHasUnsavedChanges } from "@/hooks/useUnsavedChanges";

/**
 * Confirms before an in-app navigation throws away staged edits.
 *
 * Mount it once inside the shell that owns the forms (it covers everything they
 * can navigate to: nav rails, back links, the surrounding sidebar, and browser
 * back/forward, since every one of those goes through the router). Forms
 * announce themselves through `useReportUnsavedChanges`; tab close and reload
 * stay with the `beforeunload` guard in `useSettingsForm`, which is the only
 * thing the browser lets us intercept there.
 *
 * `useBlocker` needs a data router — the app mounts one in `App.tsx`, and tests
 * rendering this component need `createMemoryRouter` rather than
 * `<MemoryRouter>`.
 */
export function UnsavedChangesGuard() {
  const hasUnsavedChanges = useHasUnsavedChanges();
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      // Search-only updates (a tab id, a filter) keep the form mounted, so they
      // lose nothing and must not prompt.
      hasUnsavedChanges && currentLocation.pathname !== nextLocation.pathname,
  );
  const proceeding = useRef(false);

  useEffect(() => {
    if (blocker.state !== "blocked") {
      proceeding.current = false;
    }
  }, [blocker.state]);

  return (
    <AlertDialog
      open={blocker.state === "blocked"}
      onOpenChange={(open) => {
        // Cancel, Escape and the overlay all close the dialog and mean "stay
        // here". Discard closes it too, but only after the navigation is
        // already on its way — resetting then would resurrect the blocker.
        if (open || proceeding.current) return;
        blocker.reset?.();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Discard unsaved changes?</AlertDialogTitle>
          <AlertDialogDescription>
            This page has edits that were never saved. Leaving now throws them away.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              proceeding.current = true;
              blocker.proceed?.();
            }}
          >
            Discard
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
