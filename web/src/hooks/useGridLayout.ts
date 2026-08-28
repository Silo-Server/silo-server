import { useState, useCallback, useRef, useEffect } from "react";

const RESIZE_SETTLE_DELAY_MS = 120;

interface GridLayout {
  columnCount: number;
  rowHeight: number;
}

interface UseGridLayoutOptions {
  gap: number;
  textAreaHeight: number;
  /** Re-measure when the grid's responsive class set changes at the same width. */
  layoutKey?: string;
}

export function useGridLayout({ gap, textAreaHeight, layoutKey = "" }: UseGridLayoutOptions) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const resizeTimerRef = useRef<number | null>(null);
  const [layout, setLayout] = useState<GridLayout>({
    columnCount: 8,
    rowHeight: 300,
  });

  const measure = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;

    const computed = getComputedStyle(el);
    const columns = computed.gridTemplateColumns.split(" ").length;
    const containerWidth = el.clientWidth;
    const totalGap = gap * (columns - 1);
    const cardWidth = (containerWidth - totalGap) / columns;
    const posterHeight = cardWidth * 1.5;
    const rowHeight = posterHeight + textAreaHeight + gap;

    setLayout({ columnCount: columns, rowHeight });
  }, [gap, textAreaHeight]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    // CSS grid responds to every viewport step without React's help. Re-running
    // the virtualizer measurement for every ResizeObserver notification does
    // not: it forces a computed-style read and a React render on each frame of
    // browser chrome animations (for example Brave's hover sidebar). Keep the
    // existing virtual row geometry during that short animation, then reconcile
    // it once the viewport has settled.
    const scheduleMeasure = () => {
      if (resizeTimerRef.current !== null) {
        window.clearTimeout(resizeTimerRef.current);
      }
      resizeTimerRef.current = window.setTimeout(() => {
        resizeTimerRef.current = null;
        measure();
      }, RESIZE_SETTLE_DELAY_MS);
    };
    const observer = new ResizeObserver(scheduleMeasure);
    observer.observe(el);
    return () => {
      observer.disconnect();
      if (resizeTimerRef.current !== null) {
        window.clearTimeout(resizeTimerRef.current);
        resizeTimerRef.current = null;
      }
    };
  }, [measure]);

  useEffect(() => {
    measure();
  }, [layoutKey, measure]);

  return { containerRef, layout };
}
