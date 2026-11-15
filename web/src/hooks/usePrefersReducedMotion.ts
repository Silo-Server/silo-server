import { useMediaQuery } from "@/hooks/useMediaQuery";

const REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)";

/** Whether the user asked the OS or browser to reduce motion, read now. */
export function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia(REDUCED_MOTION_QUERY).matches
  );
}

/**
 * The `behavior` for a scripted scroll: `"smooth"` normally, `"auto"` (an
 * instant jump) under reduced motion. Use it for every `scrollTo`,
 * `scrollBy`, and `scrollIntoView` the app starts itself.
 */
export function motionScrollBehavior(): ScrollBehavior {
  return prefersReducedMotion() ? "auto" : "smooth";
}

/** {@link prefersReducedMotion}, re-rendering when the preference changes. */
export function usePrefersReducedMotion(): boolean {
  return useMediaQuery(REDUCED_MOTION_QUERY);
}

/** {@link motionScrollBehavior}, re-rendering when the preference changes. */
export function useMotionScrollBehavior(): ScrollBehavior {
  return usePrefersReducedMotion() ? "auto" : "smooth";
}
