import { useEffect, useRef } from "react";

/**
 * Own only relative-seek actions; leave absolute seeks and play/pause untouched.
 *
 * Handlers are read through refs so callers can pass fresh closures each
 * render without the OS-facing registration being torn down and re-installed
 * on every time update.
 */
export function useMediaSkipHandlers(enabled: boolean, back: () => void, forward: () => void) {
  const backRef = useRef(back);
  const forwardRef = useRef(forward);
  useEffect(() => {
    backRef.current = back;
    forwardRef.current = forward;
  });
  useEffect(() => {
    if (!enabled || typeof navigator === "undefined" || !navigator.mediaSession) return;
    const session = navigator.mediaSession;
    const installed: MediaSessionAction[] = [];
    for (const [action, handler] of [
      ["seekbackward", () => backRef.current()],
      ["seekforward", () => forwardRef.current()],
    ] as const) {
      try {
        // The profile preference wins over an OS-provided default seekOffset.
        session.setActionHandler(action, handler);
        installed.push(action);
      } catch {
        // A browser can expose MediaSession without implementing this action.
      }
    }
    return () => {
      for (const action of installed) session.setActionHandler(action, null);
    };
  }, [enabled]);
}
