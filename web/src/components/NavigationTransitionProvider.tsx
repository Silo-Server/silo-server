import { useCallback, useLayoutEffect, useRef, type ReactNode } from "react";
import { useLocation, useNavigate, type To } from "react-router";
import { createPath, resolvePath } from "react-router";
import {
  NavigationTransitionContext,
  type NavigationHistoryTransitionOptions,
  type NavigationTransitionDirection,
  type NavigationTransitionNavigate,
  type NavigationTransitionOptions,
} from "@/components/navigationTransitionContext";
import {
  isExpectedNavigationCommit,
  MAX_TRACKED_HISTORY_PATHS,
  pruneNavigationHistory,
} from "@/lib/backNavigation";
import { SIDEBAR_RAIL_WIDTH, SIDEBAR_SURFACE_WIDTH } from "@/components/AppSidebar.logic";
import {
  DETAIL_MAIN_STAGE_MOTION_ATTRIBUTE,
  DETAIL_MAIN_STAGE_MOTION_END_EVENT,
  SIDEBAR_COLLAPSE_DURATION_MS,
} from "@/components/sidebarItemNavigation";

interface PendingCommit {
  sourceLocationKey: string;
  destinationPath: string | null;
  navigationSequence: number;
  acceptDestinationMismatch: boolean;
  resolve: () => void;
  timeout: number;
}

interface PendingMainStageMotion {
  sourceLocationKey: string;
  destinationPath: string | null;
  navigationSequence: number;
  acceptDestinationMismatch: boolean;
  direction: NavigationTransitionDirection;
  token: string;
}

interface ActiveMainStageMotion {
  animation: Animation;
  token: string;
}

interface ViewTransitionHandle {
  finished: Promise<unknown>;
  skipTransition?: () => void;
}

type StartViewTransition = (update: () => void | Promise<void>) => ViewTransitionHandle;

const COMMIT_FALLBACK_MS = 2_000;

interface LocationSnapshot {
  pathname: string;
  search: string;
  hash: string;
  key: string;
}

interface StoredNavigationHistory {
  sessionId: string;
  paths: Array<[number, string]>;
}

interface NavigationHistorySession {
  sessionId: string;
  paths: Map<number, string>;
}

const HISTORY_SESSION_STATE_KEY = "__siloNavigationSession";
const HISTORY_PATHS_STORAGE_KEY = "silo:navigation-history-paths:v1";

function browserHistoryState(): Record<string, unknown> {
  const state = window.history.state;
  return state && typeof state === "object" ? (state as Record<string, unknown>) : {};
}

function newHistorySessionId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

function restoreNavigationHistory(): NavigationHistorySession {
  const stateSessionId = browserHistoryState()[HISTORY_SESSION_STATE_KEY];
  if (typeof stateSessionId !== "string") {
    return { sessionId: newHistorySessionId(), paths: new Map() };
  }

  try {
    const raw = window.sessionStorage.getItem(HISTORY_PATHS_STORAGE_KEY);
    const stored = raw ? (JSON.parse(raw) as Partial<StoredNavigationHistory>) : null;
    if (stored?.sessionId !== stateSessionId || !Array.isArray(stored.paths)) {
      return { sessionId: stateSessionId, paths: new Map() };
    }

    const paths = new Map<number, string>();
    for (const entry of stored.paths.slice(-MAX_TRACKED_HISTORY_PATHS)) {
      if (Array.isArray(entry) && Number.isInteger(entry[0]) && typeof entry[1] === "string") {
        paths.set(entry[0], entry[1]);
      }
    }
    return { sessionId: stateSessionId, paths };
  } catch {
    return { sessionId: stateSessionId, paths: new Map() };
  }
}

function persistNavigationHistory(session: NavigationHistorySession, currentIndex: number): void {
  try {
    const paths: Array<[number, string]> = [];
    const firstRetainedIndex = Math.max(0, currentIndex - (MAX_TRACKED_HISTORY_PATHS - 1));
    for (let index = firstRetainedIndex; index <= currentIndex; index += 1) {
      const path = session.paths.get(index);
      if (path != null) paths.push([index, path]);
    }
    window.sessionStorage.setItem(
      HISTORY_PATHS_STORAGE_KEY,
      JSON.stringify({ sessionId: session.sessionId, paths } satisfies StoredNavigationHistory),
    );
  } catch {
    // History persistence is an enhancement; restricted storage must never
    // block navigation or route transitions.
  }
}

function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

function getStartViewTransition(): StartViewTransition | undefined {
  return (
    document as Document & {
      startViewTransition?: StartViewTransition;
    }
  ).startViewTransition;
}

function browserHistoryIndex(): number | null {
  const index = (window.history.state as { idx?: unknown } | null)?.idx;
  return typeof index === "number" && Number.isInteger(index) ? index : null;
}

function destinationPath(to: To, current: LocationSnapshot): string | null {
  try {
    return createPath(resolvePath(to, current.pathname));
  } catch {
    return null;
  }
}

function isDesktopItemBoundaryChange(currentPathname: string, nextPathname: string): boolean {
  if (typeof window.matchMedia !== "function") return false;
  const currentIsItem = currentPathname.startsWith("/item/");
  const nextIsItem = nextPathname.startsWith("/item/");
  return currentIsItem !== nextIsItem && window.matchMedia("(min-width: 64rem)").matches;
}

function isDesktopItemToItemChange(currentPathname: string, nextPathname: string): boolean {
  if (typeof window.matchMedia !== "function") return false;
  return (
    currentPathname.startsWith("/item/") &&
    nextPathname.startsWith("/item/") &&
    window.matchMedia("(min-width: 64rem)").matches
  );
}

function startMainStageMotion(direction: NavigationTransitionDirection): Animation | null {
  const mainStage = document.querySelector<HTMLElement>(".sidebar-main-stage");
  if (!mainStage || typeof mainStage.animate !== "function") return null;

  const travel = SIDEBAR_SURFACE_WIDTH - SIDEBAR_RAIL_WIDTH;
  const rootStyles = window.getComputedStyle(document.documentElement);
  const easing =
    rootStyles.getPropertyValue("--ease-sidebar-collapse").trim() ||
    "cubic-bezier(0.32, 0.72, 0, 1)";
  const initialOffset = direction === "back" ? -travel : travel;

  // Home swaps to the destination route at its previous visual edge, then
  // carries that new page 196px with the sidebar's compositor timeline. Do
  // the same for nested item routes without snapshotting the variable-height
  // scrolling main element (which previously caused layout work per frame).
  const animation = mainStage.animate(
    [{ transform: `translateX(${initialOffset}px)` }, { transform: "translateX(0)" }],
    {
      duration: SIDEBAR_COLLAPSE_DURATION_MS,
      easing,
      fill: "both",
    },
  );

  // Home paints the committed lightweight shell at its inverse offset before
  // the visual state changes and the CSS transition begins. Hold this effect
  // at its first keyframe across the same two-painted-frame handoff so nested
  // routes neither jump on commit nor feel a frame shorter than Home.
  animation.pause();
  animation.currentTime = 0;
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        if (animation.playState === "paused") animation.play();
      });
    });
  });

  return animation;
}

function finishMainStageMotion(token: string): void {
  const root = document.documentElement;
  if (root.getAttribute(DETAIL_MAIN_STAGE_MOTION_ATTRIBUTE) !== token) return;
  root.removeAttribute(DETAIL_MAIN_STAGE_MOTION_ATTRIBUTE);
  window.dispatchEvent(new Event(DETAIL_MAIN_STAGE_MOTION_END_EVENT));
}

/**
 * Owns same-document route transitions for the declarative BrowserRouter.
 *
 * React Router's `viewTransition` navigation option is only implemented by its
 * data routers. Silo currently uses BrowserRouter, so this provider starts the
 * browser transition itself and keeps its update callback pending until the
 * destination location has committed. That commit barrier is important for
 * POP navigation, whose history event and React render are asynchronous.
 */
export default function NavigationTransitionProvider({ children }: { children: ReactNode }) {
  const location = useLocation();
  const navigate = useNavigate();
  const locationRef = useRef<LocationSnapshot>(location);
  const pendingCommitRef = useRef<PendingCommit | null>(null);
  const activeTransitionRef = useRef<ViewTransitionHandle | null>(null);
  const pendingMainStageMotionRef = useRef<PendingMainStageMotion | null>(null);
  const activeMainStageAnimationRef = useRef<ActiveMainStageMotion | null>(null);
  const navigationSequenceRef = useRef(0);
  const navigationHistoryRef = useRef<NavigationHistorySession | null>(null);
  if (!navigationHistoryRef.current) {
    navigationHistoryRef.current = restoreNavigationHistory();
  }

  const resolvePendingCommit = useCallback(() => {
    const pending = pendingCommitRef.current;
    if (!pending) return;

    pendingCommitRef.current = null;
    window.clearTimeout(pending.timeout);
    pending.resolve();
  }, []);

  useLayoutEffect(() => {
    locationRef.current = location;
    const historyIndex = browserHistoryIndex();
    if (historyIndex !== null) {
      const navigationHistory = navigationHistoryRef.current!;
      const state = browserHistoryState();
      if (state[HISTORY_SESSION_STATE_KEY] !== navigationHistory.sessionId) {
        window.history.replaceState(
          { ...state, [HISTORY_SESSION_STATE_KEY]: navigationHistory.sessionId },
          "",
        );
      }

      const currentPath = createPath(location);
      const previouslyTrackedPath = navigationHistory.paths.get(historyIndex);
      if (previouslyTrackedPath != null && previouslyTrackedPath !== currentPath) {
        for (const index of navigationHistory.paths.keys()) {
          if (index >= historyIndex) navigationHistory.paths.delete(index);
        }
      }
      navigationHistory.paths.set(historyIndex, currentPath);
      pruneNavigationHistory(navigationHistory.paths, historyIndex);
      persistNavigationHistory(navigationHistory, historyIndex);
    }

    const pendingMainStageMotion = pendingMainStageMotionRef.current;
    if (pendingMainStageMotion) {
      const committedExpectedDestination = isExpectedNavigationCommit(
        pendingMainStageMotion.destinationPath,
        pendingMainStageMotion.navigationSequence,
        navigationSequenceRef.current,
        pendingMainStageMotion.sourceLocationKey,
        location.key,
        createPath(location),
      );
      const committedHistoryDestination =
        pendingMainStageMotion.acceptDestinationMismatch &&
        isExpectedNavigationCommit(
          pendingMainStageMotion.destinationPath,
          pendingMainStageMotion.navigationSequence,
          navigationSequenceRef.current,
          pendingMainStageMotion.sourceLocationKey,
          location.key,
          createPath(location),
          true,
        );

      if (committedHistoryDestination && !committedExpectedDestination) {
        pendingMainStageMotionRef.current = null;
        // A native POP can legitimately land somewhere other than the logical
        // fallback (for example after a reload or restricted sessionStorage).
        // Never hold the committed page hidden while waiting for a route that
        // the browser did not choose, and do not animate a guessed direction.
        finishMainStageMotion(pendingMainStageMotion.token);
      } else if (committedExpectedDestination) {
        pendingMainStageMotionRef.current = null;
        if (activeMainStageAnimationRef.current) {
          activeMainStageAnimationRef.current.animation.cancel();
          finishMainStageMotion(activeMainStageAnimationRef.current.token);
        }
        const animation = startMainStageMotion(pendingMainStageMotion.direction);
        const activeMotion = animation ? { animation, token: pendingMainStageMotion.token } : null;
        activeMainStageAnimationRef.current = activeMotion;
        if (animation) {
          void animation.finished
            .catch(() => undefined)
            .finally(() => {
              if (activeMainStageAnimationRef.current === activeMotion) {
                // `fill: both` exposes the incoming offset before the first
                // paint. Once finished, cancel the inert effect so normal page
                // scrolling does not retain a route-sized animation layer.
                animation.cancel();
                activeMainStageAnimationRef.current = null;
                finishMainStageMotion(pendingMainStageMotion.token);
              }
            });
        } else {
          finishMainStageMotion(pendingMainStageMotion.token);
        }
      }
    }

    const pending = pendingCommitRef.current;
    const committedDestination =
      pending &&
      isExpectedNavigationCommit(
        pending.destinationPath,
        pending.navigationSequence,
        navigationSequenceRef.current,
        pending.sourceLocationKey,
        location.key,
        createPath(location),
        pending.acceptDestinationMismatch,
      );
    if (committedDestination) {
      // A changed location key is authoritative for a native history
      // traversal, whose actual destination can differ from a logical
      // fallback when provenance is unavailable. Push/replace navigation must
      // still match its exact target so an intermediate commit cannot finish
      // a newer transition.
      resolvePendingCommit();
    }
  }, [location, resolvePendingCommit]);

  useLayoutEffect(
    () => () => {
      activeTransitionRef.current?.skipTransition?.();
      if (activeMainStageAnimationRef.current) {
        activeMainStageAnimationRef.current.animation.cancel();
        finishMainStageMotion(activeMainStageAnimationRef.current.token);
      }
      if (pendingMainStageMotionRef.current) {
        finishMainStageMotion(pendingMainStageMotionRef.current.token);
      }
      pendingMainStageMotionRef.current = null;
      resolvePendingCommit();
      delete document.documentElement.dataset.navigationDirection;
    },
    [resolvePendingCommit],
  );

  const transitionNavigate = useCallback<NavigationTransitionNavigate>(
    ((
      to: To | number,
      options?: NavigationTransitionOptions | NavigationHistoryTransitionOptions,
    ) => {
      const currentLocation = locationRef.current;
      const transitionOptions =
        typeof to === "number" ? undefined : (options as NavigationTransitionOptions | undefined);
      const {
        direction: requestedDirection,
        preferHistory = false,
        ...navigateOptions
      } = transitionOptions ?? {};
      const trackedHistoryTarget = (() => {
        if (typeof to !== "number") return null;
        const currentHistoryIndex = browserHistoryIndex();
        if (currentHistoryIndex === null) return null;
        return navigationHistoryRef.current!.paths.get(currentHistoryIndex + to) ?? null;
      })();
      const expectedTo =
        typeof to === "number"
          ? (trackedHistoryTarget ??
            (options as NavigationHistoryTransitionOptions | undefined)?.expectedTo)
          : to;
      const nextPath = expectedTo ? destinationPath(expectedTo, currentLocation) : null;
      const currentPath = createPath(currentLocation);
      const matchingHistoryDelta = (() => {
        if (typeof to === "number" || !preferHistory || !nextPath) return null;
        const currentHistoryIndex = browserHistoryIndex();
        if (currentHistoryIndex === null) return null;

        for (let index = currentHistoryIndex - 1; index >= 0; index -= 1) {
          if (navigationHistoryRef.current!.paths.get(index) === nextPath) {
            return index - currentHistoryIndex;
          }
        }
        return null;
      })();
      const historyDelta = typeof to === "number" ? to : matchingHistoryDelta;
      const direction: NavigationTransitionDirection =
        historyDelta !== null
          ? historyDelta < 0
            ? "back"
            : "forward"
          : (requestedDirection ?? "forward");

      // ViewTransitionLink prevents the native click once it reaches this
      // coordinator. Do not create a duplicate history entry or hold a browser
      // transition open when the destination URL is already current.
      if (to === 0 || (typeof to !== "number" && nextPath === currentPath)) {
        return;
      }

      const performNavigation = () => {
        if (typeof to === "number") {
          navigate(to);
        } else if (matchingHistoryDelta !== null) {
          navigate(matchingHistoryDelta);
        } else {
          navigate(to, navigateOptions);
        }
      };

      const navigationSequence = ++navigationSequenceRef.current;
      activeTransitionRef.current?.skipTransition?.();
      if (activeMainStageAnimationRef.current) {
        activeMainStageAnimationRef.current.animation.cancel();
        finishMainStageMotion(activeMainStageAnimationRef.current.token);
      }
      if (pendingMainStageMotionRef.current) {
        finishMainStageMotion(pendingMainStageMotionRef.current.token);
      }
      activeMainStageAnimationRef.current = null;
      pendingMainStageMotionRef.current = null;
      resolvePendingCommit();
      delete document.documentElement.dataset.navigationDirection;

      // A freshly opened tab, an older pre-upgrade history entry, or blocked
      // session storage can leave a POP destination unknowable until after it
      // commits. Navigate it live instead of guessing and risking a root
      // snapshot over the sidebar's compositor transition.
      if (typeof to === "number" && !nextPath) {
        performNavigation();
        return;
      }

      // Home's existing desktop entry/exit motion is a live compositor
      // transform shared by the sidebar and main stage. A document snapshot
      // would cover that motion with the root pseudo-element, so preserve the
      // established path at this boundary. Item-to-item changes keep the
      // sidebar collapsed and use the commit-aware route transition below.
      if (
        nextPath &&
        isDesktopItemBoundaryChange(currentLocation.pathname, resolvePath(nextPath).pathname)
      ) {
        performNavigation();
        return;
      }

      const nextPathname = nextPath ? resolvePath(nextPath).pathname : null;
      const mainStage = document.querySelector<HTMLElement>(".sidebar-main-stage");
      if (
        nextPathname &&
        typeof mainStage?.animate === "function" &&
        isDesktopItemToItemChange(currentLocation.pathname, nextPathname) &&
        !prefersReducedMotion()
      ) {
        const token = String(navigationSequence);
        document.documentElement.setAttribute(DETAIL_MAIN_STAGE_MOTION_ATTRIBUTE, token);
        pendingMainStageMotionRef.current = {
          sourceLocationKey: currentLocation.key,
          destinationPath: nextPath,
          navigationSequence,
          acceptDestinationMismatch: historyDelta !== null,
          direction,
          token,
        };
        performNavigation();
        return;
      }

      const startViewTransition = getStartViewTransition();
      if (!startViewTransition || prefersReducedMotion()) {
        performNavigation();
        return;
      }

      let updateStarted = false;
      try {
        document.documentElement.dataset.navigationDirection = direction;
        const transition = startViewTransition.call(document, async () => {
          // Skipping an in-flight ViewTransition stops its animation but does
          // not cancel its update callback. Only the latest intent may route.
          if (navigationSequence !== navigationSequenceRef.current) return;
          updateStarted = true;
          const sourceLocationKey = locationRef.current.key;
          const committed = new Promise<void>((resolve) => {
            const timeout = window.setTimeout(resolve, COMMIT_FALLBACK_MS);
            pendingCommitRef.current = {
              sourceLocationKey,
              destinationPath: nextPath,
              navigationSequence,
              acceptDestinationMismatch: historyDelta !== null,
              resolve,
              timeout,
            };
          });

          try {
            performNavigation();
            await committed;
          } finally {
            resolvePendingCommit();
          }
        });

        activeTransitionRef.current = transition;
        void transition.finished
          .catch(() => undefined)
          .finally(() => {
            if (activeTransitionRef.current === transition) {
              activeTransitionRef.current = null;
              delete document.documentElement.dataset.navigationDirection;
            }
          });
      } catch {
        delete document.documentElement.dataset.navigationDirection;
        // A synchronous API failure before the update callback starts must not
        // prevent navigation. If the callback already ran, it owns navigation.
        if (!updateStarted) performNavigation();
      }
    }) as NavigationTransitionNavigate,
    [navigate, resolvePendingCommit],
  );

  return (
    <NavigationTransitionContext.Provider value={transitionNavigate}>
      {children}
    </NavigationTransitionContext.Provider>
  );
}
