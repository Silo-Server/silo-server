import { useCallback } from "react";

/**
 * Publish a pinned element's height on the document root as the CSS length
 * `property` while it is mounted, so chrome elsewhere in the tree can clear it.
 * `includeBottomInset` adds the element's bottom offset, for a bar that floats
 * above the bottom edge. `keepAfterUnmount` leaves the last height on the root
 * when the element unmounts, for a bar that another view covers for a while;
 * the caller then removes the property itself once the bar is gone for good.
 * Pass constants: a new value re-attaches the ref.
 */
export function useRootHeightProperty(
  property: `--${string}`,
  {
    includeBottomInset = false,
    keepAfterUnmount = false,
  }: { includeBottomInset?: boolean; keepAfterUnmount?: boolean } = {},
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
        if (!keepAfterUnmount) root.style.removeProperty(property);
      };
    },
    [property, includeBottomInset, keepAfterUnmount],
  );
}
