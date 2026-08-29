import { useEffect } from "react";
import { useLocation, useNavigationType } from "react-router";

import { markNavigationDirection, resolveCommittedDirection } from "@/lib/navigationHistory";

/**
 * Keeps `html[data-navigation-direction]` in step with the browser. Mount once,
 * above every route — `WatchPlaybackChrome` and `WatchTogetherJoin` start view
 * transitions outside `Layout`, and the tracked index has to survive the
 * auth-gated routes that unmount it.
 *
 * Push direction comes from the caller, at click time, because only the caller
 * knows whether a link is a descent or a step back up the hierarchy. Back and
 * forward cannot come from there: React Router swaps currentLocation and
 * nextLocation on a reverse POP, so both legs of a back-and-forth produce the
 * byte-identical pair and `useViewTransitionState` is blind to direction by
 * construction. The committed history index is the only signal left.
 *
 * Deriving it from the location effect rather than from a `popstate` listener is
 * what makes it work at all — see `resolveCommittedDirection`. A listener cannot
 * be registered before the router's, and the router's runs React's state update
 * synchronously, so by the time a listener fires, the index it needed to compare
 * against has already been advanced.
 */
export function useNavigationDirection(): void {
  const location = useLocation();
  const navigationType = useNavigationType();

  useEffect(() => {
    const direction = resolveCommittedDirection();
    // A push or replace was already stamped at click time by whoever knew the
    // intent; re-deriving it here would only ever agree, or — for a replace,
    // which does not move the index — wrongly clear it. A POP has no click to
    // have stamped it, so this is the only chance, including when the answer is
    // null: that takes the attribute off and plays the neutral transition
    // rather than leaving the previous navigation's direction standing.
    if (navigationType === "POP") markNavigationDirection(direction);
  }, [location, navigationType]);
}
