// Card-relative sizing shared by the badge layer and the watch-status corner.
// Lengths are authored in pixels at the reference poster width and become
// container query units, so a badge keeps its proportion on every card size.

// The desktop Home carousel's measured 185px overlay layer is the visual
// reference, including when the host card has its standard 1px border.
export const POSTER_REFERENCE_WIDTH = 185;
export const EDGE_INSET = 8;
export const EDGE_GAP = 8;

// The watch-status corner's diameter. The top badge row stops short of it so
// a badge never runs under the indicator.
export const WATCH_STATUS_SIZE = 22;

// container: container query units; legacy: CSS variables a ResizeObserver
// fills from the measured width; fixed: reference pixels (wide cards).
export type ScalingMode = "container" | "legacy" | "fixed";

export function overlayLength(pixels: number, mode: ScalingMode, legacyVariable: string): string {
  if (mode === "fixed") return `${pixels}px`;
  if (mode === "legacy") return `var(${legacyVariable}, ${pixels}px)`;
  return `${Number(((pixels / POSTER_REFERENCE_WIDTH) * 100).toFixed(6))}cqi`;
}

// Every poster card asks on every render, so the answer is cached. It is keyed
// to the `CSS` object so a replaced global (as in tests) is asked again.
let containerQueryUnitsSupport: { css: typeof CSS; supported: boolean } | null = null;

export function supportsContainerQueryUnits(): boolean {
  if (typeof CSS === "undefined" || typeof CSS.supports !== "function") return false;
  if (containerQueryUnitsSupport?.css !== CSS) {
    containerQueryUnitsSupport = { css: CSS, supported: CSS.supports("width", "1cqi") };
  }
  return containerQueryUnitsSupport.supported;
}

export function setLegacyLength(
  element: HTMLElement,
  variable: string,
  pixels: number,
  scale: number,
) {
  element.style.setProperty(variable, `${Number((pixels * scale).toFixed(4))}px`);
}
