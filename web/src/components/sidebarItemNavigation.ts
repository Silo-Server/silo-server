export const SIDEBAR_COLLAPSE_DURATION_MS = 300;
export const SIDEBAR_TRANSITION_FALLBACK_MS = SIDEBAR_COLLAPSE_DURATION_MS + 80;
// Hover expansion and hidden-tab rAF suspension must never hold the detail
// shell indefinitely. Settling is preferred, but this is the absolute cap.
export const SIDEBAR_DETAILS_REVEAL_DEADLINE_MS = SIDEBAR_TRANSITION_FALLBACK_MS * 2;

export function sidebarDetailsRevealDelay(reduceMotion: boolean): number {
  return reduceMotion ? 0 : SIDEBAR_TRANSITION_FALLBACK_MS;
}

export function isCollapsedSidebarSurface(target: EventTarget | null): target is HTMLElement {
  return (
    target instanceof HTMLElement &&
    target.classList.contains("sidebar-surface") &&
    target.dataset.collapsed === "true"
  );
}

export function hasRunningSidebarTransition(surface: HTMLElement): boolean {
  if (typeof surface.getAnimations !== "function") return false;
  const Transition = globalThis.CSSTransition;
  if (typeof Transition === "undefined") return false;
  return surface
    .getAnimations()
    .some(
      (animation) =>
        animation instanceof Transition &&
        animation.transitionProperty === "transform" &&
        animation.playState === "running",
    );
}

export function parseOptionalLibraryId(rawLibraryId: string | null): number | undefined {
  if (!rawLibraryId) return undefined;
  const parsedLibraryId = Number(rawLibraryId);
  return Number.isFinite(parsedLibraryId) ? parsedLibraryId : undefined;
}

export interface SidebarItemNavigationRequest {
  href: string;
  replace?: boolean;
  state?: unknown;
}

export function parseItemNavigationHref(
  href: string,
  origin: string,
): { contentId: string; libraryId?: number } | null {
  try {
    const destination = new URL(href, origin);
    if (destination.origin !== origin || !destination.pathname.startsWith("/item/")) return null;

    const contentId = decodeURIComponent(destination.pathname.slice("/item/".length));
    if (!contentId) return null;
    return {
      contentId,
      libraryId: parseOptionalLibraryId(destination.searchParams.get("libraryId")),
    };
  } catch {
    return null;
  }
}
