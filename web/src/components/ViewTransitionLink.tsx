import { forwardRef, useCallback, useContext, useMemo } from "react";
import { QueryClientContext } from "@tanstack/react-query";
import { createPath, Link, useLocation, useResolvedPath } from "react-router";
import type { LinkProps } from "react-router";
import { parseItemNavigationHref } from "@/components/sidebarItemNavigation";
import { useSidebarItemNavigation } from "@/components/sidebarItemNavigationContext";
import { prefetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { catalogKeys } from "@/hooks/queries/keys";
import { useIntentPrefetch, type IntentPrefetchMode } from "@/hooks/useIntentPrefetch";
import { shouldUseRouteViewTransition, useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { markItemNavigationStart } from "@/lib/itemNavigationTiming";
import { markNavigationDirection } from "@/lib/navigationHistory";

type ViewTransitionLinkProps = LinkProps &
  React.AnchorHTMLAttributes<HTMLAnchorElement> & {
    /**
     * This link goes back up to an ancestor — a breadcrumb crumb, a "back to
     * series" link. See `ViewTransitionNavigateOptions.up`; the behaviour is
     * identical, and this is the only way to express it on a link.
     */
    up?: boolean;
    /**
     * When an item link warms the detail it opens (see `IntentPrefetchMode`).
     * Item links prefetch on press by default; cards people aim at use
     * `intent`. Links to anything other than an item never prefetch.
     */
    prefetchOn?: IntentPrefetchMode;
    /** Replaces the default detail prefetch, for a page that needs more. */
    onPrefetch?: () => Promise<unknown>;
  };

function chain<E>(
  first: ((event: E) => void) | undefined,
  second: ((event: E) => void) | undefined,
) {
  if (!first) return second;
  if (!second) return first;
  return (event: E) => {
    first(event);
    second(event);
  };
}

/**
 * Opts into React Router view transitions, stamps the direction the page should
 * move in, lets the desktop layout prepare item-detail navigation so its heavy
 * content cannot overlap sidebar motion, and warms an item's detail when the
 * user shows intent to open it.
 */
const ViewTransitionLink = forwardRef<HTMLAnchorElement, ViewTransitionLinkProps>(
  function ViewTransitionLink(
    {
      to,
      replace,
      state,
      up = false,
      prefetchOn = "press",
      onPrefetch,
      onClick,
      onPointerEnter,
      onPointerLeave,
      onPointerDown,
      onPointerUp,
      onPointerCancel,
      onFocus,
      onBlur,
      children,
      ...rest
    },
    ref,
  ) {
    const beginSidebarItemNavigation = useSidebarItemNavigation();
    const navigate = useViewTransitionNavigate();
    const location = useLocation();
    const resolvedPath = useResolvedPath(to);
    // Read without useQueryClient so a link rendered outside a provider (most
    // component tests) still works; it just never prefetches.
    const queryClient = useContext(QueryClientContext);
    const href = createPath(resolvedPath);
    const itemTarget = useMemo(() => parseItemNavigationHref(href, window.location.origin), [href]);
    const contentId = itemTarget?.contentId ?? null;
    const libraryId = itemTarget?.libraryId;

    const prefetchDetail = useCallback(() => {
      if (onPrefetch) return onPrefetch();
      if (!queryClient || contentId === null) return Promise.resolve();
      return prefetchCatalogItemDetail(queryClient, contentId, libraryId);
    }, [onPrefetch, queryClient, contentId, libraryId]);
    const intent = useIntentPrefetch(
      queryClient || onPrefetch ? contentId : null,
      prefetchDetail,
      prefetchOn,
    );

    return (
      <Link
        ref={ref}
        to={to}
        replace={replace}
        state={state}
        onPointerEnter={chain(onPointerEnter, intent.onPointerEnter)}
        onPointerLeave={chain(onPointerLeave, intent.onPointerLeave)}
        onPointerDown={chain(onPointerDown, intent.onPointerDown)}
        onPointerUp={chain(onPointerUp, intent.onPointerUp)}
        onPointerCancel={chain(onPointerCancel, intent.onPointerCancel)}
        onFocus={chain(onFocus, intent.onFocus)}
        onBlur={chain(onBlur, intent.onBlur)}
        onClick={(event) => {
          onClick?.(event);
          if (
            event.defaultPrevented ||
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.shiftKey ||
            event.altKey ||
            (rest.target && rest.target !== "_self")
          ) {
            return;
          }

          // A click on the current item's own link starts no navigation to time.
          if (contentId !== null && resolvedPath.pathname !== location.pathname) {
            // Read before the sidebar handoff below starts its own prefetch.
            const cached =
              queryClient?.getQueryData(catalogKeys.itemDetail(contentId, libraryId)) !== undefined;
            markItemNavigationStart(contentId, cached);
          }

          if (up) {
            // Still a real `<a href>`, so middle-click, "open in new tab" and
            // "copy link" keep working; only the plain left-click is ours.
            event.preventDefault();
            navigate(resolvedPath, { up: true, replace, state });
            return;
          }

          // A same-URL click needs no guard here: React Router turns a `<Link>`
          // with no explicit `replace` into one, and the interception below
          // hands off to the same imperative chokepoint, which has its own.
          markNavigationDirection("forward");
          const intercepted = beginSidebarItemNavigation?.({
            href,
            replace,
            state,
          });
          if (intercepted) {
            event.preventDefault();
            return;
          }

          // React Router's <Link> needs the decision before its own click
          // handler runs. Route the small set of snapshot-free navigations
          // through the same imperative chokepoint used everywhere else.
          if (!shouldUseRouteViewTransition(location.pathname, resolvedPath.pathname)) {
            event.preventDefault();
            navigate(resolvedPath, { replace, state, viewTransition: false });
          }
        }}
        viewTransition
        {...rest}
      >
        {children}
      </Link>
    );
  },
);

export default ViewTransitionLink;
