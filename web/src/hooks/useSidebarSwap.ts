import { useEffect, useState } from "react";

/**
 * Which way the app's sidebar column just changed: `forward` into settings,
 * `back` out of it.
 */
export type SidebarSwap = "forward" | "back";

// Module state rather than a ref: the app shell is mounted per route branch,
// so the shell that renders after the swap is not the one that saw the
// sidebar before it.
let showedSettings: boolean | undefined;

/**
 * Whether this render swaps the sidebar column between the app's navigation
 * and the settings sidebar, so the incoming one can animate in.
 * Null on a first load and on every navigation that keeps the same sidebar.
 *
 * The answer is only true for the render that crosses the boundary; the
 * sidebar that mounts on it should hold onto it for its entrance (see
 * `useSidebarEntrance`).
 */
export function useSidebarSwap(inSettings: boolean): SidebarSwap | null {
  const previous = showedSettings;

  useEffect(() => {
    showedSettings = inSettings;
  }, [inSettings]);

  if (previous === undefined || previous === inSettings) return null;
  return inSettings ? "forward" : "back";
}

/**
 * Holds a swap for the life of the sidebar that mounted on it, so later renders
 * of the shell (which report no swap) cannot cut its entrance short.
 */
export function useSidebarEntrance(swap: SidebarSwap | null | undefined): SidebarSwap | undefined {
  const [entrance] = useState(swap ?? undefined);
  return entrance;
}
