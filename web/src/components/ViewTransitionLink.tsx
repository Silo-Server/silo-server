import { forwardRef } from "react";
import { createPath, Link, useResolvedPath } from "react-router";
import type { LinkProps } from "react-router";
import { useSidebarItemNavigation } from "@/components/sidebarItemNavigationContext";
import {
  useNavigationTransition,
  type NavigationTransitionDirection,
} from "@/components/navigationTransitionContext";

type ViewTransitionLinkProps = LinkProps &
  React.AnchorHTMLAttributes<HTMLAnchorElement> & {
    transitionDirection?: NavigationTransitionDirection;
    preferHistory?: boolean;
  };

/**
 * Uses the app-level route transition and lets the desktop layout prepare item
 * details before navigation. Modified clicks and links targeting another
 * browsing context retain the browser's native behavior.
 */
const ViewTransitionLink = forwardRef<HTMLAnchorElement, ViewTransitionLinkProps>(
  function ViewTransitionLink(
    {
      to,
      replace,
      state,
      transitionDirection = "forward",
      preferHistory = false,
      onClick,
      children,
      ...rest
    },
    ref,
  ) {
    const beginSidebarItemNavigation = useSidebarItemNavigation();
    const transitionNavigate = useNavigationTransition();
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
            rest.download != null ||
            (rest.target && rest.target !== "_self")
          ) {
            return;
          }

          const href = createPath(resolvedPath);
          const intercepted = beginSidebarItemNavigation?.({
            href,
            replace,
            state,
          });
          if (intercepted) {
            event.preventDefault();
            return;
          }

          if (transitionNavigate) {
            event.preventDefault();
            transitionNavigate(href, {
              replace,
              state,
              direction: transitionDirection,
              preferHistory,
            });
          }
        }}
        {...rest}
      >
        {children}
      </Link>
    );
  },
);

export default ViewTransitionLink;
