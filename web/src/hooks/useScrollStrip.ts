import { useCallback, useEffect, useRef, useState } from "react";

/** Which sides of a strip have more past them. */
export interface ScrollEdges {
  left: boolean;
  right: boolean;
}

/**
 * Fades the strip's edges that have more past them: clear for `--strip-hold`
 * (0 unless set; room for a button over the edge), then fading in by
 * `--strip-fade` (2rem unless set).
 */
export function edgeMask({ left, right }: ScrollEdges): string | undefined {
  if (left && right) {
    return "[mask-image:linear-gradient(to_right,transparent_var(--strip-hold,0px),black_var(--strip-fade,2rem),black_calc(100%_-_var(--strip-fade,2rem)),transparent_calc(100%_-_var(--strip-hold,0px)))]";
  }
  if (right) {
    return "[mask-image:linear-gradient(to_right,black_calc(100%_-_var(--strip-fade,2rem)),transparent_calc(100%_-_var(--strip-hold,0px)))]";
  }
  if (left) {
    return "[mask-image:linear-gradient(to_right,transparent_var(--strip-hold,0px),black_var(--strip-fade,2rem))]";
  }
  return undefined;
}

/**
 * A strip that scrolls sideways: tracks which edges have more past them and
 * keeps the active item (`activeSelector`, inside the strip) in view, at least
 * `inset` px from a faded edge, whenever `activeKey` changes or the strip or
 * its first child changes size. Put the items in one child of the scroller so
 * their size can be observed.
 */
export function useScrollStrip<T extends HTMLElement>(
  activeSelector: string,
  activeKey: unknown,
  inset = 16,
) {
  const ref = useRef<T>(null);
  const [edges, setEdges] = useState<ScrollEdges>({ left: false, right: false });
  const measure = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const left = el.scrollLeft > 1;
    const right = el.scrollLeft + el.clientWidth < el.scrollWidth - 1;
    setEdges((prev) => (prev.left === left && prev.right === right ? prev : { left, right }));
  }, []);

  // Scrolls only the strip, never the page.
  const reveal = useCallback(() => {
    const el = ref.current;
    const item = el?.querySelector<HTMLElement>(activeSelector);
    if (!el || !item) return;
    const box = item.getBoundingClientRect();
    const start = box.left - el.getBoundingClientRect().left + el.scrollLeft;
    const end = start + box.width;
    if (start < el.scrollLeft + inset) el.scrollLeft = start - inset;
    else if (end > el.scrollLeft + el.clientWidth - inset) {
      el.scrollLeft = end - el.clientWidth + inset;
    }
    measure();
  }, [activeSelector, inset, measure]);

  useEffect(() => {
    reveal();
  }, [activeKey, reveal]);

  // After first paint the items can widen (counts or options loading) and the
  // strip can narrow (a summary appearing beside it), either of which can push
  // the active item back past the edge: reveal it again whenever either one
  // changes size. Scrolling by hand changes neither, so it is left alone.
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(reveal);
    observer.observe(el);
    if (el.firstElementChild) observer.observe(el.firstElementChild);
    return () => observer.disconnect();
  }, [reveal]);

  /** Scrolls most of a strip's width toward `side`. */
  const page = useCallback((side: "left" | "right") => {
    const el = ref.current;
    if (!el) return;
    const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    el.scrollBy?.({
      left: (side === "left" ? -1 : 1) * el.clientWidth * 0.75,
      behavior: reduce ? "auto" : "smooth",
    });
  }, []);

  return { ref, edges, measure, page };
}
