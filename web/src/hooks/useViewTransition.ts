import { useNavigate } from "react-router";
import { useCallback } from "react";
import type { NavigateOptions, To } from "react-router";
import { useNavigationTransition } from "@/components/navigationTransitionContext";

/**
 * Uses Silo's commit-aware route transition when available and falls back to
 * React Router's native data-router option elsewhere.
 *
 * Falls back to regular navigation in browsers that don't support
 * the View Transitions API.
 */
export function useViewTransitionNavigate() {
  const navigate = useNavigate();
  const transitionNavigate = useNavigationTransition();

  return useCallback(
    (to: To, options?: NavigateOptions) => {
      if (transitionNavigate) {
        transitionNavigate(to, options);
        return;
      }
      navigate(to, { ...options, viewTransition: true });
    },
    [navigate, transitionNavigate],
  );
}
