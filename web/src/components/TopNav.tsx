import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useLocation } from "react-router";
import {
  Bell,
  CalendarDays,
  Clock,
  FolderOpen,
  Heart,
  LogOut,
  Search,
  Settings,
  Shield,
  Sparkles,
  UserCircle,
  UsersRound,
} from "lucide-react";

import ViewTransitionLink from "@/components/ViewTransitionLink";
import { SiloBrand } from "@/components/SiloBrand";
import { WatchlistIcon } from "@/components/icons/WatchlistIcon";
import { OPEN_GLOBAL_SEARCH_EVENT } from "@/components/GlobalSearch";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useNotificationCapability } from "@/hooks/queries/notificationWebhooks";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { buildPersonalCatalogHref } from "@/pages/catalogSearchParams";
import { fallbackPrimaryMenuLinks, resolvePrimaryMenuLinks } from "@/components/primaryMenuLinks";
import type { ResolvedPrimaryMenuLink } from "@/components/primaryMenuLinks";

/**
 * The WeFlix shell's navigation: a full-width top bar instead of the left rail.
 *
 * Every destination here is one the sidebar already offered, moved rather than
 * added. The primary menu links are resolved by the same shared function the
 * sidebar uses, the icons are the ones the sidebar used, and the account menu
 * holds the same entries in the artboard's two-column arrangement.
 *
 * Three widths, one bar:
 *   - phone (<768px)     logo and icons in the bar, the links in a scrollable
 *                        chip row beneath it, and the account menu in a sheet.
 *   - tablet (768-1023px) the full bar; the link row scrolls when it overflows
 *                        rather than wrapping or clipping.
 *   - desktop (>=1024px)  the full bar, links fitting without scrolling.
 */

/** Tailwind's `md`. A rem query, matching the other shell breakpoints. */
const TABLET_QUERY = "(min-width: 48rem)";

/**
 * Menu destinations that sit after the divider, alongside Discover: the ones
 * about what is coming up rather than what to browse. Identified by the key
 * `menuItemKey` gives a builtin.
 */
const TRAILING_DESTINATIONS: ReadonlySet<string> = new Set(["builtin:calendar"]);

/**
 * Whether the viewport is wide enough for the bar to hold the links and for the
 * account menu to open as a dropdown. Below it, both move into the phone
 * arrangement.
 */
function useTabletUp(): boolean {
  const [matches, setMatches] = useState(
    () => typeof window !== "undefined" && window.matchMedia(TABLET_QUERY).matches,
  );
  useEffect(() => {
    const query = window.matchMedia(TABLET_QUERY);
    const onChange = () => setMatches(query.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  return matches;
}

interface MenuLink {
  key: string;
  label: string;
  /** The same glyph the sidebar used for this destination. */
  icon: ReactNode;
  onSelect: () => void;
}

/**
 * One column of the account menu: a grey uppercase label over white items with
 * no icons, as the artboard draws it. `variant` picks the item element so the
 * dropdown keeps Radix's roving focus and the sheet gets full-height touch
 * targets.
 */
function MenuColumn({
  label,
  links,
  variant,
  onNavigate,
}: {
  label: string;
  links: MenuLink[];
  variant: "menu" | "sheet";
  onNavigate: () => void;
}) {
  return (
    <div className="flex min-w-[9.5rem] flex-col gap-0.5">
      <span className="text-muted-foreground mb-1 px-2 text-[0.6875rem] font-semibold tracking-wide uppercase">
        {label}
      </span>
      {links.map((link) =>
        variant === "menu" ? (
          <DropdownMenuItem
            key={link.key}
            onSelect={() => {
              link.onSelect();
              onNavigate();
            }}
            className="text-popover-foreground flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 text-[0.8125rem] font-medium"
          >
            {link.icon}
            {link.label}
          </DropdownMenuItem>
        ) : (
          <button
            key={link.key}
            type="button"
            onClick={() => {
              link.onSelect();
              onNavigate();
            }}
            className="text-popover-foreground hover:bg-accent focus-visible:ring-ring flex min-h-11 items-center gap-2.5 rounded-lg px-2 text-left text-[0.9375rem] font-medium focus-visible:ring-2 focus-visible:outline-none"
          >
            {link.icon}
            {link.label}
          </button>
        ),
      )}
    </div>
  );
}

export default function TopNav() {
  const location = useLocation();
  const navigate = useViewTransitionNavigate();
  const { user, logout, clearProfile } = useAuth();
  const { profile } = useCurrentProfile();
  const showAdminNav = useIsActingAdmin();
  const { data: libraries } = useUserLibraries();
  const { primaryMenu } = useUICustomization();
  const requestStatus = useRequestFeatureStatus();
  const showRequestsNav = requestStatus.data?.requests_enabled === true;
  // The same rule the sidebar applies: optimistic while the capability loads,
  // hidden only when the admin kill switch is off or the server has no
  // notifications API.
  const notificationCapability = useNotificationCapability();
  const showNotificationsNav = notificationCapability.isError
    ? false
    : (notificationCapability.data?.in_app.enabled ?? true);
  const tabletUp = useTabletUp();
  const [menuOpen, setMenuOpen] = useState(false);

  const links = useMemo<ResolvedPrimaryMenuLink[]>(() => {
    const resolved = resolvePrimaryMenuLinks(primaryMenu, libraries) ?? fallbackPrimaryMenuLinks();
    // The logo is this shell's Home affordance, so the Home destination is not
    // repeated as a link beside it.
    return resolved.filter((link) => link.href !== "/");
  }, [primaryMenu, libraries]);

  const isActive = useCallback(
    (href: string) => {
      const [targetPath, targetQuery] = href.split("?", 2);
      if (targetPath === "/") return location.pathname === "/";
      if (targetQuery !== undefined) {
        return location.pathname === targetPath && location.search === `?${targetQuery}`;
      }
      return location.pathname === targetPath || location.pathname.startsWith(`${targetPath}/`);
    },
    [location.pathname, location.search],
  );

  // The sidebar's own active treatment, reused so the two shells agree on what
  // "current" looks like.
  const linkClass = (active: boolean) =>
    `relative flex shrink-0 items-center rounded-xl px-3 py-2 text-[0.8125rem] font-medium whitespace-nowrap transition-all duration-200 ${
      active
        ? "text-sidebar-accent-foreground bg-sidebar-accent/90 shadow-[0_16px_30px_-24px_rgba(0,0,0,0.7)]"
        : "text-muted-foreground hover:text-sidebar-foreground hover:bg-sidebar-accent/70"
    }`;

  const openSearch = useCallback(() => {
    document.dispatchEvent(new Event(OPEN_GLOBAL_SEARCH_EVENT));
  }, []);

  const accountLinks = useMemo<MenuLink[]>(() => {
    const entries: MenuLink[] = [
      {
        key: "collections",
        label: "Collections",
        icon: <FolderOpen className="h-[18px] w-[18px] shrink-0" />,
        onSelect: () => navigate("/collections"),
      },
      {
        key: "history",
        label: "History",
        icon: <Clock className="h-[18px] w-[18px] shrink-0" />,
        onSelect: () => navigate(buildPersonalCatalogHref("history")),
      },
    ];
    if (showNotificationsNav) {
      entries.push({
        key: "notifications",
        label: "Notifications",
        icon: <Bell className="h-[18px] w-[18px] shrink-0" />,
        onSelect: () => navigate("/notifications"),
      });
    }
    entries.push({
      key: "calendar",
      label: "Calendar",
      icon: <CalendarDays className="h-[18px] w-[18px] shrink-0" />,
      onSelect: () => navigate("/calendar"),
    });
    if (showAdminNav) {
      entries.push({
        key: "admin",
        label: "Admin",
        icon: <Shield className="h-[18px] w-[18px] shrink-0" />,
        onSelect: () => navigate("/admin"),
      });
    }
    entries.push({
      key: "settings",
      label: "Settings",
      icon: <Settings className="h-[18px] w-[18px] shrink-0" />,
      onSelect: () => navigate("/settings"),
    });
    return entries;
  }, [navigate, showAdminNav, showNotificationsNav]);

  const profileLinks = useMemo<MenuLink[]>(() => {
    const entries: MenuLink[] = [
      {
        key: "rooms",
        label: "Watch party",
        icon: <UsersRound className="h-[18px] w-[18px] shrink-0" />,
        onSelect: () => navigate("/rooms"),
      },
    ];
    if (profile) {
      entries.push({
        key: "switch-profile",
        label: "Switch Profile",
        icon: <UserCircle className="h-[18px] w-[18px] shrink-0" />,
        onSelect: () => {
          clearProfile();
          navigate("/profiles");
        },
      });
    }
    entries.push({
      key: "logout",
      label: "Logout",
      icon: <LogOut className="h-[18px] w-[18px] shrink-0" />,
      onSelect: logout,
    });
    return entries;
  }, [clearProfile, logout, navigate, profile]);

  const avatarButton = (
    <button
      type="button"
      aria-label={`${profile?.name ?? user?.username ?? "User"} account menu`}
      className="hover:bg-accent/60 focus-visible:ring-ring flex h-11 w-11 shrink-0 items-center justify-center rounded-full transition-transform focus-visible:ring-2 focus-visible:outline-none active:scale-[0.98] md:h-10 md:w-10"
    >
      <Avatar className="h-8 w-8 md:h-7 md:w-7">
        {profile?.avatar_url ? <AvatarImage src={profile.avatar_url} alt={profile.name} /> : null}
        <AvatarFallback className="bg-primary text-primary-foreground text-xs font-bold">
          {profile?.name?.charAt(0).toUpperCase() ?? user?.username?.charAt(0).toUpperCase() ?? "?"}
        </AvatarFallback>
      </Avatar>
    </button>
  );

  const iconButtonClass =
    "text-muted-foreground hover:text-foreground hover:bg-accent/60 focus-visible:ring-ring flex h-11 w-11 shrink-0 items-center justify-center rounded-full transition-all active:scale-[0.98] focus-visible:ring-2 focus-visible:outline-none md:h-10 md:w-10";

  // The only bar link with a glyph: it reuses the sparkle the sidebar gave
  // "For You", which is the same Discover family in the old shell.
  const discoverLink = showRequestsNav ? (
    <ViewTransitionLink
      to="/requests"
      className={`${linkClass(isActive("/requests"))} gap-1.5`}
      aria-current={isActive("/requests") ? "page" : undefined}
    >
      <Sparkles className="h-4 w-4 shrink-0" />
      Discover
    </ViewTransitionLink>
  ) : null;

  // The bar reads as two groups: what to browse, then what is coming up.
  // Calendar is a primary-menu builtin, so it arrives in `links` and is moved
  // here to sit with Discover after the divider.
  const browseLinks = useMemo(
    () => links.filter((link) => !TRAILING_DESTINATIONS.has(link.key)),
    [links],
  );
  const trailingLinks = useMemo(
    () => links.filter((link) => TRAILING_DESTINATIONS.has(link.key)),
    [links],
  );

  const renderMenuLink = (link: ResolvedPrimaryMenuLink) => (
    <ViewTransitionLink
      key={link.key}
      to={link.href}
      className={linkClass(isActive(link.href))}
      aria-current={isActive(link.href) ? "page" : undefined}
    >
      {link.label}
    </ViewTransitionLink>
  );

  const navLinks = (
    <>
      {browseLinks.map(renderMenuLink)}
      {trailingLinks.length > 0 || discoverLink ? (
        <>
          <span className="bg-border mx-1 h-5 w-px shrink-0" aria-hidden="true" />
          {trailingLinks.map(renderMenuLink)}
          {discoverLink}
        </>
      ) : null}
    </>
  );

  return (
    <header className="bg-background border-border/60 sticky top-0 z-40 w-full border-b">
      <div className="flex h-16 items-center gap-3 px-4 md:gap-5 md:px-6">
        <ViewTransitionLink to="/" aria-label="Home" className="flex shrink-0 items-center">
          <SiloBrand className="h-8 w-[88px] md:h-9 md:w-[100px]" />
        </ViewTransitionLink>

        {/* Tablet and desktop: the links sit in the bar. The row scrolls rather
            than wrapping or clipping when they outgrow the viewport. */}
        {tabletUp ? (
          <nav
            aria-label="Primary"
            className="weflix-scroll-row flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
          >
            {navLinks}
          </nav>
        ) : null}

        <div className="ml-auto flex shrink-0 items-center gap-0.5 md:ml-0 md:gap-1">
          <button
            type="button"
            onClick={openSearch}
            aria-label="Search"
            className={iconButtonClass}
          >
            <Search className="h-5 w-5" />
          </button>
          <ViewTransitionLink
            to={buildPersonalCatalogHref("favorites")}
            aria-label="Favorites"
            className={iconButtonClass}
          >
            <Heart className="h-5 w-5" />
          </ViewTransitionLink>
          <ViewTransitionLink
            to={buildPersonalCatalogHref("watchlist")}
            aria-label="Watchlist"
            className={iconButtonClass}
          >
            <WatchlistIcon className="h-5 w-5" />
          </ViewTransitionLink>

          {tabletUp ? (
            <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
              <DropdownMenuTrigger asChild>{avatarButton}</DropdownMenuTrigger>
              <DropdownMenuContent
                align="end"
                sideOffset={10}
                className="bg-popover border-border/60 w-auto min-w-[21rem] rounded-[24px] p-5"
              >
                <div className="flex gap-5">
                  <MenuColumn
                    label="Your account"
                    links={accountLinks}
                    variant="menu"
                    onNavigate={() => setMenuOpen(false)}
                  />
                  <div className="bg-border w-px shrink-0 self-stretch" aria-hidden="true" />
                  <MenuColumn
                    label="Profiles"
                    links={profileLinks}
                    variant="menu"
                    onNavigate={() => setMenuOpen(false)}
                  />
                </div>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : (
            <Sheet open={menuOpen} onOpenChange={setMenuOpen}>
              <SheetTrigger asChild>{avatarButton}</SheetTrigger>
              <SheetContent side="top" className="bg-popover border-border/60 p-0">
                <SheetHeader className="sr-only">
                  <SheetTitle>Account menu</SheetTitle>
                </SheetHeader>
                <div className="flex gap-5 px-5 pt-14 pb-5">
                  <MenuColumn
                    label="Your account"
                    links={accountLinks}
                    variant="sheet"
                    onNavigate={() => setMenuOpen(false)}
                  />
                  <div className="bg-border w-px shrink-0 self-stretch" aria-hidden="true" />
                  <MenuColumn
                    label="Profiles"
                    links={profileLinks}
                    variant="sheet"
                    onNavigate={() => setMenuOpen(false)}
                  />
                </div>
              </SheetContent>
            </Sheet>
          )}
        </div>
      </div>

      {/* Phone: the same links as a scrollable chip row under the bar. Only one
          of the two navs is rendered at a time, so the links are never in the
          document twice and the "Primary" landmark stays unique. */}
      {tabletUp ? null : (
        <nav
          aria-label="Primary"
          className="weflix-scroll-row flex items-center gap-1 overflow-x-auto px-4 pb-3"
        >
          {navLinks}
        </nav>
      )}
    </header>
  );
}
