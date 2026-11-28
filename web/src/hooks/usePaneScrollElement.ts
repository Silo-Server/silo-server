import { useCallback, useRef } from "react";

/**
 * A list that scrolls in a details pane on wide screens and with the whole
 * body on narrow ones: `scrollElement` is whichever of the two scrolls, for a
 * virtual list to follow.
 */
export function usePaneScrollElement() {
  const paneRef = useRef<HTMLElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const scrollElement = useCallback(() => {
    const pane = paneRef.current;
    return pane && getComputedStyle(pane).overflowY !== "visible" ? pane : bodyRef.current;
  }, []);
  return { paneRef, bodyRef, scrollElement };
}
