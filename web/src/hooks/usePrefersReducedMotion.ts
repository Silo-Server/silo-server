import { useSyncExternalStore } from "react";

const REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)";

function reducedMotionQuery(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return null;
  return window.matchMedia(REDUCED_MOTION_QUERY);
}

/** Whether the user asked the OS or browser to reduce motion, read now. */
export function prefersReducedMotion(): boolean {
  return reducedMotionQuery()?.matches ?? false;
}

/**
 * The `behavior` for a scripted scroll: `"smooth"` normally, `"auto"` (an
 * instant jump) under reduced motion. Use it for every `scrollTo`,
 * `scrollBy`, and `scrollIntoView` the app starts itself.
 */
export function motionScrollBehavior(): ScrollBehavior {
  return prefersReducedMotion() ? "auto" : "smooth";
}

function subscribe(onChange: () => void): () => void {
  const query = reducedMotionQuery();
  if (!query || typeof query.addEventListener !== "function") return () => {};
  query.addEventListener("change", onChange);
  return () => query.removeEventListener("change", onChange);
}

/** {@link prefersReducedMotion}, re-rendering when the preference changes. */
export function usePrefersReducedMotion(): boolean {
  return useSyncExternalStore(subscribe, prefersReducedMotion, () => false);
}
