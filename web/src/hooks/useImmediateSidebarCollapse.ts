import { useEffect, useRef, useState } from "react";

export function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/**
 * Separates the layout snap from the visible compositor transition by one
 * animation frame. This is the minimum handoff needed for the inverse main
 * transform to take effect before the sidebar starts moving; it never waits on
 * item metadata, poster decoding, backdrop loading, or frame-rate heuristics.
 */
export function useImmediateSidebarCollapse(collapsed: boolean): boolean {
  const [visualCollapsed, setVisualCollapsed] = useState(collapsed);
  const [reduceMotion, setReduceMotion] = useState(prefersReducedMotion);
  const frameRef = useRef(0);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const handleChange = (event: MediaQueryListEvent) => {
      // Synchronize the internal visual state on both edges so disabling the
      // preference cannot play a stale catch-up transition.
      setVisualCollapsed(collapsed);
      setReduceMotion(event.matches);
    };
    query.addEventListener("change", handleChange);
    return () => query.removeEventListener("change", handleChange);
  }, [collapsed]);

  useEffect(() => {
    if (visualCollapsed === collapsed || reduceMotion) return;

    if (typeof requestAnimationFrame !== "function") {
      const timer = window.setTimeout(() => setVisualCollapsed(collapsed), 0);
      return () => window.clearTimeout(timer);
    }

    frameRef.current = requestAnimationFrame(() => setVisualCollapsed(collapsed));
    return () => cancelAnimationFrame(frameRef.current);
  }, [collapsed, reduceMotion, visualCollapsed]);

  return reduceMotion ? collapsed : visualCollapsed;
}
