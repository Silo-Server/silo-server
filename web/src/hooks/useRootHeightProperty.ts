import { useCallback } from "react";

/**
 * Publish a pinned element's height on the document root as the CSS length
 * `property` while it is mounted, so chrome elsewhere in the tree can clear it.
 * `includeBottomInset` adds the element's bottom offset, for a bar that floats
 * above the bottom edge. Pass constants: a new value re-attaches the ref.
 */
export function useRootHeightProperty(
  property: `--${string}`,
  { includeBottomInset = false }: { includeBottomInset?: boolean } = {},
) {
  return useCallback(
    (element: HTMLElement | null) => {
      if (!element) return;
      const root = document.documentElement;
      const measure = () => {
        const { height } = element.getBoundingClientRect();
        const bottom = includeBottomInset ? parseFloat(getComputedStyle(element).bottom) || 0 : 0;
        root.style.setProperty(property, `${height + bottom}px`);
      };
      // Window resizes still re-measure where ResizeObserver is unavailable.
      const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
      observer?.observe(element);
      window.addEventListener("resize", measure);
      measure();
      return () => {
        observer?.disconnect();
        window.removeEventListener("resize", measure);
        root.style.removeProperty(property);
      };
    },
    [property, includeBottomInset],
  );
}
