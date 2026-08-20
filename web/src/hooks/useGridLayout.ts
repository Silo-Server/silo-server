import { useState, useCallback, useRef, useEffect } from "react";

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
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [measure]);

  useEffect(() => {
    measure();
  }, [layoutKey, measure]);

  return { containerRef, layout };
}
