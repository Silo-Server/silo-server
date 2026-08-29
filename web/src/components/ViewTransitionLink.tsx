import { forwardRef } from "react";
import { createPath, Link, useResolvedPath } from "react-router";
import type { LinkProps } from "react-router";
import { useSidebarItemNavigation } from "@/components/sidebarItemNavigationContext";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { markNavigationDirection } from "@/lib/navigationHistory";

type ViewTransitionLinkProps = LinkProps &
  React.AnchorHTMLAttributes<HTMLAnchorElement> & {
    /**
     * This link goes back up to an ancestor — a breadcrumb crumb, a "back to
     * series" link. See `ViewTransitionNavigateOptions.up`; the behaviour is
     * identical, and this is the only way to express it on a link.
     */
    up?: boolean;
  };

/**
 * Opts into React Router view transitions, stamps the direction the page should
 * move in, and lets the desktop layout prepare item-detail navigation so its
 * heavy content cannot overlap sidebar motion.
 */
const ViewTransitionLink = forwardRef<HTMLAnchorElement, ViewTransitionLinkProps>(
  function ViewTransitionLink({ to, replace, state, up = false, onClick, children, ...rest }, ref) {
    const beginSidebarItemNavigation = useSidebarItemNavigation();
    const navigate = useViewTransitionNavigate();
    const resolvedPath = useResolvedPath(to);

    return (
      <Link
        ref={ref}
        to={to}
        replace={replace}
        state={state}
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
            href: createPath(resolvedPath),
            replace,
            state,
          });
          if (intercepted) event.preventDefault();
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
