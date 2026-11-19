import { useCallback, useEffect, useRef, useState } from "react";

const SETTLE_MS = 50;

/**
 * The item range a windowed catalog grid has on screen, for the pages it
 * reads. Scroll reports settle briefly before they change it, and a new key
 * (another list, query or filter) starts again at the first page.
 */
export function useCatalogVisibleRange(
  key: string,
  limit: number,
): [range: [number, number], onVisibleRangeChange: (start: number, end: number) => void] {
  const firstPage: [number, number] = [0, limit - 1];
  const [state, setState] = useState<{ key: string; range: [number, number] }>({
    key,
    range: firstPage,
  });
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const onVisibleRangeChange = useCallback(
    (start: number, end: number) => {
      clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => setState({ key, range: [start, end] }), SETTLE_MS);
    },
    [key],
  );
  useEffect(() => () => clearTimeout(timerRef.current), []);
  return [state.key === key ? state.range : firstPage, onVisibleRangeChange];
}
