import { useCallback, useEffect, useRef } from "react";
import { createPath, resolvePath, useLocation, useNavigate } from "react-router";
import type { NavigateOptions, To } from "react-router";

import { markNavigationDirection } from "@/lib/navigationHistory";

export interface ViewTransitionNavigateOptions extends NavigateOptions {
  /**
   * The destination is an ancestor of where we are now, not a new place: play
   * the page motion backwards.
   *
   * Direction only. It deliberately does not unwind to an existing entry —
   * see the note in `@/lib/navigationHistory` for why that cannot be made
   * sound against React Router's history indices.
   */
  up?: boolean;
}

/**
 * The app's imperative navigation chokepoint: opts every navigation into React
 * Router's view transitions and stamps the direction `main-content` should move
 * in before the router opens the transition.
 *
 * `up` is the only way to express intent, here and on `ViewTransitionLink`. It
 * is a boolean rather than a direction string because forward is the default
 * and back/forward direction is never a caller's concern — `useNavigationDirection`
 * derives that from the history index the browser has already committed.
 *
 * Falls back to regular navigation in browsers without the View Transitions API.
 */
export function useViewTransitionNavigate() {
  const navigate = useNavigate();
  const location = useLocation();

  // The current location is read at click time, so it must stay out of the
  // callback's dependencies: consumers hold this function in effect deps
  // (SearchBar's search-as-you-type), and an identity that changed on every
  // navigation would make those effects navigate again in a loop.
  const locationRef = useRef(location);
  useEffect(() => {
    locationRef.current = location;
  }, [location]);

  return useCallback(
    (to: To | number, options?: ViewTransitionNavigateOptions) => {
      if (typeof to === "number") {
        // A delta is already a POP, so the popstate listener derives the same
        // answer a tick later; stamping here just gets it in before the router.
        markNavigationDirection(to < 0 ? "back" : "forward");
        navigate(to);
        return;
      }

      const { up = false, ...navigateOptions } = options ?? {};
      const current = locationRef.current;
      // React Router resolves a relative `to` against the matched route rather
      // than the raw pathname. Every href this app navigates with is absolute,
      // where the two agree; a relative `to` could disagree and turn a push
      // into a replace, so callers pass absolute paths.
      const target = createPath(resolvePath(to, current.pathname));
      // React Router gives `<Link>` the same-URL guard for free; the imperative
      // path gets nothing, and a second identical entry makes the browser's
      // back button look broken.
      const replace = navigateOptions.replace ?? target === createPath(current);

      // Direction is the only thing `up` changes; the navigation itself is a
      // normal push either way, so every caller keeps its own entry semantics
      // and every NavigateOptions field is forwarded on both paths.
      markNavigationDirection(up ? "back" : "forward");
      navigate(to, { ...navigateOptions, replace, viewTransition: true });
    },
    [navigate],
  );
}
