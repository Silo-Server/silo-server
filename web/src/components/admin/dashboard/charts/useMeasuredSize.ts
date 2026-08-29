import { useEffect, useRef, useState } from "react";

export interface MeasuredSize {
  width: number;
  height: number;
}

/** Sub-pixel churn is noise, not a resize: re-render only past half a pixel. */
const SIGNIFICANT_CHANGE_PX = 0.5;

/**
 * Content-box size of an element, tracked with a ResizeObserver.
 *
 * A chart in a widget the admin can resize has no height it can hard-code, and
 * an SVG stretched to an unknown box distorts its marks. Measuring the box in
 * CSS pixels lets a chart draw in the same units the browser paints in.
 *
 * `null` until the first observation (and wherever ResizeObserver is missing),
 * so callers keep a static fallback rather than drawing into a zero-sized box.
 */
export function useMeasuredSize<T extends HTMLElement>() {
  const ref = useRef<T | null>(null);
  const [size, setSize] = useState<MeasuredSize | null>(null);

  useEffect(() => {
    const node = ref.current;
    if (!node || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) {
        return;
      }
      setSize((previous) =>
        previous !== null &&
        Math.abs(previous.width - rect.width) < SIGNIFICANT_CHANGE_PX &&
        Math.abs(previous.height - rect.height) < SIGNIFICANT_CHANGE_PX
          ? previous
          : { width: rect.width, height: rect.height },
      );
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  return { ref, size };
}
