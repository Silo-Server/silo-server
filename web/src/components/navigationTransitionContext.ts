import { createContext, useContext } from "react";
import type { NavigateOptions, To } from "react-router";

export type NavigationTransitionDirection = "forward" | "back";

export interface NavigationTransitionOptions extends NavigateOptions {
  /** Controls the route snapshot motion independently of history mutation. */
  direction?: NavigationTransitionDirection;
  /**
   * Reuse the nearest earlier entry for this exact URL when it is known.
   * Parent breadcrumbs use this to unwind item history instead of pushing a
   * duplicate Series or Season entry.
   */
  preferHistory?: boolean;
}

export interface NavigationTransitionNavigate {
  (to: To, options?: NavigationTransitionOptions): void;
  (delta: number, options?: NavigationHistoryTransitionOptions): void;
}

export interface NavigationHistoryTransitionOptions {
  /** Known destination used to choose the correct visual transition for POP. */
  expectedTo?: To;
}

export const NavigationTransitionContext = createContext<NavigationTransitionNavigate | null>(null);

export function useNavigationTransition(): NavigationTransitionNavigate | null {
  return useContext(NavigationTransitionContext);
}
