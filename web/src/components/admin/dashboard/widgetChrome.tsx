import { createContext, useCallback, useContext, useEffect, useMemo, type ReactNode } from "react";

import type { WidgetId, WidgetRange } from "./types";

/**
 * What a widget knows about its own frame.
 *
 * Widgets are rendered by the grid with no props — the registry stores a bare
 * component type — so the things they need from their placement, the window the
 * admin picked and the height they are allowed to give back, travel through
 * context instead.
 */
export interface WidgetChrome {
  /** The placed widget's id, or null when rendered outside the grid. */
  id: WidgetId | null;
  range: WidgetRange;
  setRange: (range: WidgetRange) => void;
  /**
   * Report whether this widget currently has nothing worth a full-height box.
   *
   * Only honored for widgets whose registry entry sets `collapsedRows`; for the
   * rest the grid records the flag and ignores it.
   */
  setCollapsed: (collapsed: boolean) => void;
}

const WidgetChromeContext = createContext<WidgetChrome | null>(null);

/**
 * The window a widget reads outside the grid: a plain day.
 *
 * Widgets are also rendered by unit tests and could be by any future host, and
 * a chart that threw because nobody wrapped it would be a worse failure than
 * one that quietly shows the usual day. Collapse is likewise a no-op: an
 * unhosted widget has no row span to give back.
 */
const FALLBACK_CHROME: WidgetChrome = {
  id: null,
  range: "day",
  setRange: () => {},
  setCollapsed: () => {},
};

export function WidgetChromeProvider({
  id,
  range,
  setRange,
  setCollapsed,
  children,
}: {
  id: WidgetId;
  range: WidgetRange | undefined;
  setRange: (id: WidgetId, range: WidgetRange) => void;
  setCollapsed?: (id: WidgetId, collapsed: boolean) => void;
  children: ReactNode;
}) {
  // Kept out of the memo below so its identity survives a range change: the
  // reporting effect is keyed on it, and re-running that effect would flick the
  // widget back to full height for a frame.
  const reportCollapsed = useCallback(
    (collapsed: boolean) => setCollapsed?.(id, collapsed),
    [id, setCollapsed],
  );
  const value = useMemo<WidgetChrome>(
    () => ({
      id,
      range: range ?? FALLBACK_CHROME.range,
      setRange: (next: WidgetRange) => setRange(id, next),
      setCollapsed: reportCollapsed,
    }),
    [id, range, setRange, reportCollapsed],
  );
  return <WidgetChromeContext.Provider value={value}>{children}</WidgetChromeContext.Provider>;
}

/** The window this widget is showing, and how to change it. */
export function useWidgetRange(): WidgetChrome {
  return useContext(WidgetChromeContext) ?? FALLBACK_CHROME;
}

/**
 * Tell the grid this widget is collapsible and whether it is collapsed now.
 *
 * Keyed on the boolean rather than on the data behind it, so a widget that
 * refetches on an interval reports once per actual change; the cleanup clears
 * the flag when the widget unmounts, so a removed widget never leaves the grid
 * holding a stale collapse.
 */
export function useReportCollapsed(collapsed: boolean): void {
  const { setCollapsed } = useContext(WidgetChromeContext) ?? FALLBACK_CHROME;
  useEffect(() => {
    setCollapsed(collapsed);
    return () => setCollapsed(false);
  }, [collapsed, setCollapsed]);
}
