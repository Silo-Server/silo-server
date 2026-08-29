import { useCallback, useLayoutEffect, useRef } from "react";

/**
 * Caps a CSS grid at a measured number of rows and scrolls the remainder.
 *
 * The cap is measured, not derived. Cards that pair a fixed-ratio image with a
 * variable block of text have no row height a stylesheet can name, and the
 * column count changes at every breakpoint, so a `calc()` would have to encode
 * both and would drift the moment either changed. Reading the offset of the
 * first item on the row after the cap stays correct for any combination.
 *
 * The measurement is written straight to the element rather than held in state:
 * it is a layout value applied to the node it was read from, so a render round
 * trip would buy nothing and would make every resize frame cascade.
 *
 * Returns a callback ref. Callers typically render the grid only after loading
 * finishes, so binding the observer to the node as it mounts is the only way to
 * be sure it is attached — an effect would run once against a null ref and,
 * with nothing in its dependencies changing afterwards, never retry.
 */
export function useGridRowCap<T extends HTMLElement>(visibleRows: number, itemCount: number) {
  const gridRef = useRef<T | null>(null);
  const observerRef = useRef<ResizeObserver | null>(null);

  const applyCap = useCallback(() => {
    const grid = gridRef.current;
    if (!grid) return;

    // A subtree skipped by `content-visibility: auto` reports every child at
    // the same offset. That reads as "short enough, no cap needed" and would
    // drop the cap moments before the rows scroll into view, so leave whatever
    // is already applied and wait for the observer to report a laid-out box.
    if (
      typeof grid.checkVisibility === "function" &&
      !grid.checkVisibility({ contentVisibilityAuto: true })
    ) {
      return;
    }

    // A capped grid still lays its children out at their natural offsets — the
    // scrollport clips rather than compresses — so this stays correct without
    // clearing the previous cap and forcing an extra reflow first.
    let firstRowTop: number | null = null;
    let currentRowTop: number | null = null;
    let clippedRowTop: number | null = null;
    let rowCount = 0;

    for (const child of grid.children) {
      const top = (child as HTMLElement).offsetTop;
      // Children come in DOM order, so each new offset starts a row.
      if (top === currentRowTop) continue;
      currentRowTop = top;
      rowCount += 1;
      if (firstRowTop === null) firstRowTop = top;
      if (rowCount > visibleRows) {
        clippedRowTop = top;
        break;
      }
    }

    // Short enough to need no cap: drop any left over from a narrower layout so
    // the section never shows an inert scrollport.
    if (firstRowTop === null || clippedRowTop === null) {
      if (grid.style.maxHeight) grid.style.maxHeight = "";
      return;
    }

    const style = getComputedStyle(grid);
    const rowGap = parseFloat(style.rowGap) || 0;
    const padding = (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0);
    // `clippedRowTop` is the top of the first hidden row, so drop the gap that
    // precedes it to land flush with the last visible row. Padding is added
    // back because `max-height` is measured against the border box.
    const next = `${Math.round(clippedRowTop - firstRowTop - rowGap + padding)}px`;

    // Writing an unchanged value would keep the observer below ping-ponging.
    if (grid.style.maxHeight !== next) grid.style.maxHeight = next;
  }, [visibleRows]);

  const setGrid = useCallback(
    (node: T | null) => {
      observerRef.current?.disconnect();
      observerRef.current = null;
      gridRef.current = node;
      if (!node || typeof ResizeObserver === "undefined") return;

      // Width changes move the breakpoint, which changes both the column count
      // and the row height. Observing the grid covers both.
      const observer = new ResizeObserver(applyCap);
      observer.observe(node);
      observerRef.current = observer;
    },
    [applyCap],
  );

  // Content can change without the box changing — swapping to a season with a
  // different episode count while the grid stays capped at the same height.
  useLayoutEffect(applyCap, [applyCap, itemCount]);

  return setGrid;
}
