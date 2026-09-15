import { useCallback } from "react";

/** Reserve the visible fixed player's footprint, including its bottom inset. */
export function usePlaybackBarHeight(kind: "watch" | "audiobook") {
  return useCallback(
    (element: HTMLDivElement | null) => {
      if (!element) return;
      const property = `--${kind}-bar-height`;
      const root = document.documentElement;
      const measure = () => {
        const { height } = element.getBoundingClientRect();
        const bottom = parseFloat(getComputedStyle(element).bottom) || 0;
        root.style.setProperty(property, `${height + bottom}px`);
      };
      const observer = new ResizeObserver(measure);
      observer.observe(element);
      window.addEventListener("resize", measure);
      measure();
      return () => {
        observer.disconnect();
        window.removeEventListener("resize", measure);
        root.style.removeProperty(property);
      };
    },
    [kind],
  );
}
