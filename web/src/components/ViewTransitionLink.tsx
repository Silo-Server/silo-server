import { forwardRef } from "react";
import { createPath, Link, useResolvedPath } from "react-router";
import type { LinkProps } from "react-router";
import { useSidebarItemNavigation } from "@/components/sidebarItemNavigationContext";

/**
 * Opts into React Router view transitions and lets the desktop layout prepare
 * item-detail navigation so its heavy content cannot overlap sidebar motion.
 */
const ViewTransitionLink = forwardRef<
  HTMLAnchorElement,
  LinkProps & React.AnchorHTMLAttributes<HTMLAnchorElement>
>(function ViewTransitionLink({ to, replace, state, onClick, children, ...rest }, ref) {
  const beginSidebarItemNavigation = useSidebarItemNavigation();
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
});

export default ViewTransitionLink;
