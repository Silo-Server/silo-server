import { useEffect, useState } from "react";
import { Link, useLocation } from "react-router";
import { Menu, Search } from "lucide-react";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import AppSidebar from "@/components/AppSidebar";
import ServerActivity from "@/components/ServerActivity";
import { SiloBrand } from "@/components/SiloBrand";
import { GlobalSearch } from "@/components/GlobalSearch";
import { RealtimeEventsProvider } from "@/components/RealtimeEventsProvider";
import { useEventChannel } from "@/components/realtimeEventsContext";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { buildQueryCatalogHref, parseCatalogSearchParams } from "@/pages/catalogSearchParams";
import type { ReactNode } from "react";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";

interface LayoutProps {
  children: ReactNode;
}

export default function Layout({ children }: LayoutProps) {
  const location = useLocation();
  const [mobileOpen, setMobileOpen] = useState(false);
  // Tracks whether the mobile header should slide off-screen on scroll.
  // We auto-hide on Calendar to maximize vertical room for the sticky week strip
  // and the day timeline below it. Pulling up restores the header.
  const [mobileHeaderHidden, setMobileHeaderHidden] = useState(false);
  const { user } = useAuth();
  const { profile } = useCurrentProfile();
  const isAdmin = user?.role === "admin";
  const { isBackgroundBarVisible } = useWatchPlaybackController();

  const isHomePath = location.pathname === "/";
  const isLibraryRoute = location.pathname.startsWith("/library/");
  const isItemRoute = location.pathname.startsWith("/item/");
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

  // Immersion: auto-collapse sidebar on detail pages so content gets full stage
  const isDetailImmersion = isItemRoute;

  // Publish the current sidebar width on the document root so out-of-tree
  // chrome (notably ImpersonationBanner, which renders above all routes)
  // can align with whichever sidebar state is active.
  useEffect(() => {
    const root = document.documentElement;
    if (isDetailImmersion) {
      root.dataset.sidebarCollapsed = "true";
    } else {
      delete root.dataset.sidebarCollapsed;
    }
    return () => {
      delete root.dataset.sidebarCollapsed;
    };
  }, [isDetailImmersion]);

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
    <RealtimeEventsProvider>
      <LayoutEventChannels />
      {isAdmin && <AdminEventChannels />}
      <div className="bg-background relative min-h-[100dvh] overflow-x-clip">
        <a
          href="#main-content"
          className="focus:bg-background focus:text-foreground focus:ring-ring sr-only focus:not-sr-only focus:fixed focus:top-4 focus:left-4 focus:z-50 focus:rounded-lg focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:ring-2 focus:outline-none"
        >
          Skip to content
        </a>
        <GlobalSearch />
        <div className="from-primary/8 pointer-events-none fixed inset-x-0 top-0 z-0 h-40 bg-gradient-to-b to-transparent blur-3xl" />
        {/* Desktop sidebar — hidden below lg */}
        <div className="hidden lg:block">
          <AppSidebar collapsed={isDetailImmersion} />
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
            {isAdmin && <ServerActivity hideWhenEmpty />}
            <Link
              to="/settings/playback"
              className="bg-primary text-primary-foreground flex h-9 w-9 items-center justify-center rounded-xl text-xs font-bold shadow-[0_16px_32px_-22px_rgba(0,0,0,0.7)]"
            >
              {profile?.name?.charAt(0).toUpperCase() ??
                user?.username?.charAt(0).toUpperCase() ??
                "?"}
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
        {isAdmin && (
          <div className="fixed top-6 right-5 z-40 hidden lg:block">
            <ServerActivity hideWhenEmpty />
          </div>
        )}

        {/* Main content — offset by sidebar width on desktop */}
        <main
          id="main-content"
          className={`main-transition relative min-h-screen ${
            isDetailImmersion ? "lg:ml-16" : "lg:ml-[260px]"
          } ${isBackgroundBarVisible ? "pb-32 sm:pb-36" : ""}`}
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
    </RealtimeEventsProvider>
  );
}

function LayoutEventChannels() {
  useEventChannel("catalog");
  useEventChannel("user_state");
  return null;
}

/** Subscribe to admin WS channels so ServerActivity receives live updates. */
function AdminEventChannels() {
  useEventChannel("sessions");
  useEventChannel("tasks");
  useEventChannel("scans");
  useEventChannel("jobs");
  return null;
}
