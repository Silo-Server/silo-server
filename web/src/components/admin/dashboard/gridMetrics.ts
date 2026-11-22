/**
 * Dashboard grid measurements. The resize math reads them back from the
 * stylesheet, so a drag follows the grid as rendered instead of a second copy
 * of its numbers. Each fallback covers anything that cannot resolve the value
 * (jsdom, a grid that is not mounted yet).
 */

export const GRID_COLUMNS = 12;

/**
 * Room for each widget's move/remove pill above the widget in Customize mode,
 * so it covers neither the widget's own content nor anything of the widget
 * above it.
 *
 * The row gap holds the pill (1.75rem + its 2px border), the 0.375rem the
 * resize handle of the widget above hangs below that widget, and 0.625rem - 2px
 * of clearance between them. Change it with the pill or the handle. Each
 * widget's drop area over the gap takes its height from the same variable.
 * For the first row, the top padding adds to the page's spacing above the grid.
 *
 * Only rows widen, so column spans keep their width. A widget spanning several
 * rows also spans the wider gaps between them, so it is drawn taller here than
 * outside Customize mode; its row count is unchanged.
 */
export const CUSTOMIZE_GRID_CLASS =
  "[--customize-row-gap:2.75rem] gap-y-(--customize-row-gap) pt-3";

/** The column gap of `.admin-widget-grid` in app.css (0.875rem). */
const GRID_COLUMN_GAP_PX = 14;
/** The Customize row gap, the only mode a resize runs in (2.75rem). */
const GRID_CUSTOMIZE_ROW_GAP_PX = 44;
/** Must match `--admin-row-h` on `.admin-widget-grid` in app.css (6.25rem). */
const GRID_ROW_HEIGHT_PX = 100;

/** Row height in CSS pixels, read back from `--admin-row-h`. */
export function readRowHeightPx(grid: HTMLElement | null): number {
  if (!grid) {
    return GRID_ROW_HEIGHT_PX;
  }
  const raw = window.getComputedStyle(grid).getPropertyValue("--admin-row-h").trim();
  const value = Number.parseFloat(raw);
  if (!Number.isFinite(value) || value <= 0) {
    return GRID_ROW_HEIGHT_PX;
  }
  if (raw.endsWith("rem")) {
    const root = Number.parseFloat(window.getComputedStyle(document.documentElement).fontSize);
    return Number.isFinite(root) && root > 0 ? value * root : GRID_ROW_HEIGHT_PX;
  }
  return value;
}

/** Column gap in CSS pixels, read back from the resolved `column-gap`. */
export function readColumnGapPx(grid: HTMLElement | null): number {
  return readGapPx(grid, "columnGap", GRID_COLUMN_GAP_PX);
}

/**
 * Row gap in CSS pixels, read back from the resolved `row-gap`. It differs from
 * the column gap in Customize mode, which widens only the rows.
 */
export function readRowGapPx(grid: HTMLElement | null): number {
  return readGapPx(grid, "rowGap", GRID_CUSTOMIZE_ROW_GAP_PX);
}

function readGapPx(
  grid: HTMLElement | null,
  axis: "columnGap" | "rowGap",
  fallback: number,
): number {
  if (!grid) {
    return fallback;
  }
  const value = Number.parseFloat(window.getComputedStyle(grid)[axis]);
  if (!Number.isFinite(value) || value < 0) {
    return fallback;
  }
  return value;
}
