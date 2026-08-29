import { useCallback, useEffect, useLayoutEffect, useState } from "react";
import { Link, useLocation } from "react-router";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { Menu, Search } from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import AppSidebar from "@/components/AppSidebar";
import ServerActivity from "@/components/ServerActivity";
import { SiloBrand } from "@/components/SiloBrand";
import { GlobalSearch } from "@/components/GlobalSearch";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { buildQueryCatalogHref, parseCatalogSearchParams } from "@/pages/catalogSearchParams";
import type { ReactNode, TransitionEvent as ReactTransitionEvent } from "react";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";
import {
  prefersReducedMotion,
  useImmediateSidebarCollapse,
} from "@/hooks/useImmediateSidebarCollapse";
import SidebarItemNavigationProvider from "@/components/SidebarItemNavigationProvider";
import {
  hasRunningSidebarTransition,
  isCollapsedSidebarSurface,
  parseItemNavigationHref,
  SIDEBAR_DETAILS_REVEAL_DEADLINE_MS,
  type SidebarItemNavigationRequest,
} from "@/components/sidebarItemNavigation";
import { useSidebarItemDetailsGate } from "@/hooks/useSidebarItemDetailsGate";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { catalogKeys } from "@/hooks/queries/keys";
import { fetchCatalogItemDetail } from "@/hooks/queries/catalogRead";

interface LayoutProps {
  children: ReactNode;
}

export default function Layout({ children }: LayoutProps) {
  const location = useLocation();
  const navigate = useViewTransitionNavigate();
  const queryClient = useQueryClient();
  const [mobileOpen, setMobileOpen] = useState(false);
  // Tracks whether the mobile header should slide off-screen on scroll.
  // We auto-hide on Calendar to maximize vertical room for the sticky week strip
  // and the day timeline below it. Pulling up restores the header.
  const [mobileHeaderHidden, setMobileHeaderHidden] = useState(false);
  const { user } = useAuth();
  const { profile } = useCurrentProfile();
  const showAdminActivity = useIsActingAdmin();
  const { isBackgroundBarVisible } = useWatchPlaybackController();
  const audiobookPlayback = useAudiobookPlaybackController();
  const hasBackgroundBar = isBackgroundBarVisible || audiobookPlayback?.isBackgroundBarVisible;

  const isHomePath = location.pathname === "/";
  const isLibraryRoute = location.pathname.startsWith("/library/");
  const isItemRoute = location.pathname.startsWith("/item/");
  const itemRouteLocation = `${location.pathname}${location.search}`;
  // Breakpoint changes naturally cause other layout renders; navigation only
  // needs the viewport value at the moment it is attempted.
  const hasDesktopSidebar = window.matchMedia("(min-width: 64rem)").matches;
  const isSearchLandingRoute =
    location.pathname === "/catalog" &&
    (() => {
      const state = parseCatalogSearchParams(new URLSearchParams(location.search));
      return state.source === "query" && !state.q;
    })();
  const isRecommendationsRoute = location.pathname === "/recommendations";
  const isCalendarRoute = location.pathname === "/calendar";
  const isRequestDetailRoute = /^\/requests\/(movie|series)\//.test(location.pathname);
  const needsNoPadding =
    isHomePath ||
    isLibraryRoute ||
    isItemRoute ||
    isRequestDetailRoute ||
    isSearchLandingRoute ||
    isRecommendationsRoute ||
    isCalendarRoute;

  // The item route commits its lightweight shell immediately. The following
  // frame starts the sidebar/main compositor transition; prefetched details
  // and artwork are revealed only after that motion completes.
  const isDetailImmersion = isItemRoute;
  const targetDetailImmersion = isDetailImmersion;
  const visualDetailImmersion = useImmediateSidebarCollapse(targetDetailImmersion);
  const {
    itemDetailsReady,
    pendingLocationKey,
    reveal: revealItemDetails,
  } = useSidebarItemDetailsGate(location.key, isItemRoute && hasDesktopSidebar);

  const beginItemNavigation = useCallback(
    (request: SidebarItemNavigationRequest) => {
      const itemTarget = parseItemNavigationHref(request.href, window.location.origin);
      if (isItemRoute || !itemTarget || !hasDesktopSidebar) {
        return false;
      }

      void queryClient.prefetchQuery({
        queryKey: catalogKeys.itemDetail(itemTarget.contentId, itemTarget.libraryId),
        queryFn: () => fetchCatalogItemDetail(itemTarget.contentId, itemTarget.libraryId),
      });
      navigate(request.href, {
        replace: request.replace,
        state: request.state,
      });
      return true;
    },
    [hasDesktopSidebar, isItemRoute, navigate, queryClient],
  );

  // Transitionend is the fast path. Polling handles reconstructed layers, and
  // the hard deadline guarantees hover expansion or a hidden-tab suspended rAF
  // can never leave the item route on its lightweight shell indefinitely.
  useEffect(() => {
    if (!isItemRoute || !pendingLocationKey) return;

    // The gate stages the collapse so the detail skeleton is not what animates
    // into view. A prefetched detail has no skeleton to hide, so waiting for
    // the sidebar would only delay a page that is already ready to paint.
    if (hasCachedItemDetail(queryClient, itemRouteLocation)) {
      revealItemDetails(pendingLocationKey);
      return;
    }

    const startedAt = Date.now();
    let timer: number;
    let cancelled = false;

    const revealWhenSettled = () => {
      if (cancelled) return;
      const surface = document.querySelector<HTMLElement>(".sidebar-surface");
      const deadlineReached =
        prefersReducedMotion() || Date.now() - startedAt >= SIDEBAR_DETAILS_REVEAL_DEADLINE_MS;
      if (
        !deadlineReached &&
        surface &&
        (!isCollapsedSidebarSurface(surface) || hasRunningSidebarTransition(surface))
      ) {
        const remaining = SIDEBAR_DETAILS_REVEAL_DEADLINE_MS - (Date.now() - startedAt);
        timer = window.setTimeout(revealWhenSettled, Math.min(50, Math.max(0, remaining)));
        return;
      }
      revealItemDetails(pendingLocationKey);
    };

    revealWhenSettled();
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [isItemRoute, itemRouteLocation, pendingLocationKey, queryClient, revealItemDetails]);

  const handleSidebarTransitionEnd = useCallback(
    (event: ReactTransitionEvent<HTMLDivElement>) => {
      if (event.propertyName === "transform" && isCollapsedSidebarSurface(event.target)) {
        if (pendingLocationKey) revealItemDetails(pendingLocationKey);
      }
    },
    [pendingLocationKey, revealItemDetails],
  );

  // Publish both sidebar states on the document root so out-of-tree chrome
  // (notably ImpersonationBanner, which renders above all routes) can align
  // with the sidebar. The target drives the snapped `--app-sidebar-offset`;
  // the visual state is the same one-frame handoff `.sidebar-main-stage` uses
  // in-tree, letting that chrome animate its edge instead of jumping.
  //
  // Layout effect, not effect: the sidebar receives `data-collapsed` during
  // render, so publishing these after paint would start the banner's transition
  // a frame late and it would trail the sidebar by ~23px at peak velocity.
  useLayoutEffect(() => {
    const root = document.documentElement;
    // Marks that this shell is mounted, and with it the only element named
    // `main-content`. app.css holds the root view-transition group still while
    // it is set, so the frozen sidebar snapshot cannot cross-fade over the live
    // collapse — and the routes rendered outside this shell keep the default
    // root transition, which is the only thing they have to animate.
    root.dataset.appShell = "true";
    if (targetDetailImmersion) {
      root.dataset.sidebarCollapsed = "true";
    } else {
      delete root.dataset.sidebarCollapsed;
    }
    if (visualDetailImmersion) {
      root.dataset.sidebarVisualCollapsed = "true";
    } else {
      delete root.dataset.sidebarVisualCollapsed;
    }
    return () => {
      delete root.dataset.appShell;
      delete root.dataset.sidebarCollapsed;
      delete root.dataset.sidebarVisualCollapsed;
    };
  }, [targetDetailImmersion, visualDetailImmersion]);

  // Auto-hide the mobile header on scroll-down for the Calendar route only.
  // Direction-based (not threshold-based) so a small scroll-up reveals the
  // header even mid-page — matches the iOS Safari pattern.
  useEffect(() => {
    if (!isCalendarRoute) {
      setMobileHeaderHidden(false);
      return;
    }
    let lastY = window.scrollY;
    let ticking = false;
    const onScroll = () => {
      if (ticking) return;
      ticking = true;
      requestAnimationFrame(() => {
        const y = window.scrollY;
        const delta = y - lastY;
        if (Math.abs(delta) > 4) {
          if (delta > 0 && y > 80) setMobileHeaderHidden(true);
          else if (delta < 0) setMobileHeaderHidden(false);
          lastY = y;
        }
        ticking = false;
      });
    };
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, [isCalendarRoute]);

  // Publish the header visibility on <html> so child sticky elements (e.g. the
  // calendar's WeekNavigator wrapper) can pin closer to the viewport top when
  // the mobile header is off-screen — keeping the visual gap from collapsing.
  useEffect(() => {
    const root = document.documentElement;
    if (mobileHeaderHidden) {
      root.dataset.mobileHeaderHidden = "true";
    } else {
      delete root.dataset.mobileHeaderHidden;
    }
    return () => {
      delete root.dataset.mobileHeaderHidden;
    };
  }, [mobileHeaderHidden]);

  return (
    <SidebarItemNavigationProvider begin={beginItemNavigation} itemDetailsReady={itemDetailsReady}>
      <div className="bg-background relative min-h-[100dvh] overflow-x-clip">
        <a
          href="#main-content"
          className="focus:bg-background focus:text-foreground focus:ring-ring sr-only focus:not-sr-only focus:fixed focus:top-4 focus:left-4 focus:z-50 focus:rounded-lg focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:ring-2 focus:outline-none"
        >
          Skip to content
        </a>
        <GlobalSearch />
        <div className="from-primary/8 pointer-events-none fixed inset-x-0 top-0 z-0 h-40 bg-gradient-to-b to-transparent blur-3xl" />
        {/* Desktop sidebar — hidden below lg. Its box stays 260px wide in both
          states; `collapsed` only drives the moving frame that hides everything
          past the 64px rail, so entering a detail page never resizes it. */}
        <div className="hidden lg:block" onTransitionEnd={handleSidebarTransitionEnd}>
          <AppSidebar collapsed={visualDetailImmersion} />
        </div>

        {/* Mobile header — visible below lg. Slides up on scroll-down within
          the Calendar route to free vertical space; pulling up reveals it. */}
        <div
          className={`mobile-header glass-dark border-border/70 sticky top-0 z-30 mx-3 mt-3 flex items-center justify-between rounded-2xl border px-4 py-3 transition-transform duration-200 ease-out lg:hidden ${
            mobileHeaderHidden ? "-translate-y-[140%]" : "translate-y-0"
          }`}
        >
          <div className="flex items-center gap-3">
            <button
              onClick={() => setMobileOpen(true)}
              className="text-muted-foreground hover:text-foreground hover:bg-accent/60 flex h-10 w-10 items-center justify-center rounded-xl transition-all active:scale-[0.98]"
              aria-label="Open menu"
            >
              <Menu className="h-5 w-5" />
            </button>
            <ViewTransitionLink to="/" className="flex items-center gap-2.5">
              <SiloBrand className="h-10 w-[94px]" />
            </ViewTransitionLink>
          </div>
          <div className="flex items-center gap-2">
            <ViewTransitionLink
              to={buildQueryCatalogHref()}
              className="text-muted-foreground hover:text-foreground hover:bg-accent/60 flex h-10 w-10 items-center justify-center rounded-xl transition-all active:scale-[0.98]"
            >
              <Search className="h-5 w-5" />
            </ViewTransitionLink>
            {showAdminActivity && <ServerActivity hideWhenEmpty />}
            <Link
              to="/settings"
              aria-label={`${profile?.name ?? user?.username ?? "User"} settings`}
              className="flex h-9 w-9 items-center justify-center rounded-full transition-transform active:scale-[0.98]"
            >
              <Avatar className="h-9 w-9 shadow-[0_16px_32px_-22px_rgba(0,0,0,0.7)]">
                {profile?.avatar_url ? (
                  <AvatarImage src={profile.avatar_url} alt={profile.name} />
                ) : null}
                <AvatarFallback className="bg-primary text-primary-foreground text-xs font-bold">
                  {profile?.name?.charAt(0).toUpperCase() ??
                    user?.username?.charAt(0).toUpperCase() ??
                    "?"}
                </AvatarFallback>
              </Avatar>
            </Link>
          </div>
        </div>

        {/* Mobile sidebar drawer */}
        <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
          <SheetContent side="left" className="w-[280px] p-0 sm:max-w-[280px]">
            <SheetHeader className="sr-only">
              <SheetTitle>Navigation</SheetTitle>
            </SheetHeader>
            <AppSidebar onNavigate={() => setMobileOpen(false)} />
          </SheetContent>
        </Sheet>

        {/* Desktop admin activity indicator (top-right, hidden on mobile) */}
        {showAdminActivity && (
          <div className="fixed top-6 right-5 z-40 hidden lg:block">
            <ServerActivity hideWhenEmpty />
          </div>
        )}

        {/* Main content — offset by sidebar width on desktop */}
        <main
          id="main-content"
          data-sidebar-target-collapsed={targetDetailImmersion ? "true" : undefined}
          data-sidebar-visual-collapsed={visualDetailImmersion ? "true" : undefined}
          className={`sidebar-main-stage relative min-h-screen ${
            targetDetailImmersion ? "lg:ml-16" : "lg:ml-[260px]"
          } ${hasBackgroundBar ? "pb-32 sm:pb-36" : ""}`}
          style={{ viewTransitionName: "main-content" }}
        >
          {needsNoPadding ? (
            children
          ) : (
            <div className="relative z-10 px-4 py-4 sm:px-6 lg:px-10 lg:py-8 xl:px-12">
              {children}
            </div>
          )}
        </main>
      </div>
    </SidebarItemNavigationProvider>
  );
}

/**
 * Reports whether the detail for the item route currently being entered is
 * already in the query cache — either prefetched by `beginItemNavigation` or
 * left behind by an earlier visit.
 */
function hasCachedItemDetail(queryClient: QueryClient, itemRouteLocation: string): boolean {
  const target = parseItemNavigationHref(itemRouteLocation, window.location.origin);
  if (!target) return false;
  return (
    queryClient.getQueryData(catalogKeys.itemDetail(target.contentId, target.libraryId)) !==
    undefined
  );
}
