import { useEffect, useRef, useState } from "react";

export function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/**
 * Chromium user agents also contain "AppleWebKit", so exclude the Blink
 * product tokens rather than treating that substring alone as an engine
 * check. iOS variants intentionally remain WebKit because they use the same
 * layout engine even when the browser is branded Chrome, Edge, or Firefox.
 */
export function isWebKitEngine(userAgent: string): boolean {
  return (
    /AppleWebKit/i.test(userAgent) &&
    !/(?:Chrome|Chromium|HeadlessChrome|Edg|OPR|SamsungBrowser|Vivaldi|YaBrowser)\//i.test(
      userAgent,
    )
  );
}

/**
 * Firefox desktop identifies both its Gecko engine and Firefox product token.
 * Firefox on iOS remains covered by the WebKit check above.
 */
export function isFirefoxEngine(userAgent: string): boolean {
  return /Gecko\/\d+/i.test(userAgent) && /Firefox\/\d+/i.test(userAgent);
}

/**
 * Separates the layout snap from the visible compositor transition. Chromium
 * and Firefox keep the established one-frame handoff. WebKit gets one paint
 * boundary before the visual state changes because it can otherwise coalesce
 * the committed compensation transform and its removal into the same paint.
 * The handoff never waits on item metadata, artwork, or frame-rate heuristics.
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

    const waitForWebKitPaint =
      typeof navigator !== "undefined" && isWebKitEngine(navigator.userAgent);

    frameRef.current = requestAnimationFrame(() => {
      if (!waitForWebKitPaint) {
        setVisualCollapsed(collapsed);
        return;
      }

      frameRef.current = requestAnimationFrame(() => setVisualCollapsed(collapsed));
    });
    return () => cancelAnimationFrame(frameRef.current);
  }, [collapsed, reduceMotion, visualCollapsed]);

  return reduceMotion ? collapsed : visualCollapsed;
}
