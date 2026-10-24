import { useEffect } from "react";

import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { hasEarlierEntry } from "@/lib/navigationHistory";

// Remotes send Back as Escape, Backspace, or GoBack depending on the remote
// and the browser. BrowserBack is left out: the browser already goes back on it.
export const REMOTE_BACK_KEYS = new Set(["Escape", "Backspace", "GoBack"]);

const CLAIMS_BACK =
  'input, textarea, select, [contenteditable="true"], [role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"]';

/**
 * Steps back one page when a remote's Back key reaches the page unclaimed.
 *
 * Listens on window, so dialogs, menus, popovers and the player, which all
 * listen lower down, close or claim Back first; any of them that handles the
 * key prevents its default and this leaves it alone. A key typed into a field
 * belongs to the field, and with no in-app page behind there is nothing to do.
 *
 * Off when `enabled` is false: admin pages hold unsaved forms that a stray
 * Backspace on a toggle would throw away, and no one administers from a couch.
 */
export function useRemoteBackKey(enabled: boolean) {
  const navigate = useViewTransitionNavigate();

  useEffect(() => {
    if (!enabled) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (!REMOTE_BACK_KEYS.has(event.key) || event.defaultPrevented) return;
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      // Escape in fullscreen is the browser leaving fullscreen.
      if (event.key === "Escape" && document.fullscreenElement) return;
      const target = event.target instanceof Element ? event.target : null;
      if (target?.closest(CLAIMS_BACK) || document.querySelector('[aria-modal="true"]')) return;
      if (!hasEarlierEntry()) return;
      event.preventDefault();
      navigate(-1);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [enabled, navigate]);
}
