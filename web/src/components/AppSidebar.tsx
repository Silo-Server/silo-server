import { useState, useCallback, useEffect, useMemo, useRef } from "react";
import type { ReactNode } from "react";
import { Link, useLocation, useParams } from "react-router";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { getProfileMenuSide, isSidebarExpanded } from "@/components/AppSidebar.logic";
import { SiloBrand } from "@/components/SiloBrand";
import { useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { usePluginSettingsList } from "@/hooks/queries/pluginSettings";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useSidebarPins, useToggleSidebarPin } from "@/hooks/queries/sidebarPins";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { pluginRouteHref } from "@/lib/pluginRouteHref";
import {
  buildLibraryCollectionCatalogHref,
  buildPersonalCatalogHref,
  buildQueryCatalogHref,
  buildSectionCatalogHref,
  parseCatalogSearchParams,
} from "@/pages/catalogSearchParams";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Film,
  Tv,
  Library,
  Search,
  Heart,
  List,
  Clock,
  FolderOpen,
  LogOut,
  UserCircle,
  Shield,
  Settings,
  Sparkles,
  CalendarDays,
  Home,
  UsersRound,
  ChevronDown,
  ChevronRight,
  PinOff,
  LayoutGrid,
  Puzzle,
  BookHeadphones,
  Send,
} from "lucide-react";
import { useTheme } from "@/hooks/useTheme";
import { CURATED_THEME_IDS, THEMES } from "@/lib/themes";
import { cn } from "@/lib/utils";

function getLibraryIcon(type: string) {
  switch (type) {
    case "movies":
      return <Film className="h-[18px] w-[18px]" />;
    case "series":
      return <Tv className="h-[18px] w-[18px]" />;
    case "audiobook":
    case "audiobooks":
      return <BookHeadphones className="h-[18px] w-[18px]" />;
    default:
      return <Library className="h-[18px] w-[18px]" />;
  }
}

function getLibraryIdFromPathname(pathname: string): number | null {
  const match = pathname.match(/^\/library\/(\d+)(?:\/|$)/);
  if (!match) return null;
  const id = Number(match[1]);
  return Number.isInteger(id) && id > 0 ? id : null;
}

function SidebarLabel({ children, show }: { children: ReactNode; show: boolean }) {
  return (
    <span
      className={`truncate transition-[opacity,max-width] duration-200 ease-out ${
        show ? "max-w-[180px] opacity-100 delay-[50ms]" : "max-w-0 overflow-hidden opacity-0"
      }`}
    >
      {children}
    </span>
  );
}

function SidebarSectionHeader({
  children,
  show,
  collapsible = false,
  expanded = false,
  onToggle,
}: {
  children: ReactNode;
  show: boolean;
  collapsible?: boolean;
  expanded?: boolean;
  onToggle?: () => void;
}) {
  const textClass = "text-muted-foreground text-[10px] font-semibold tracking-[0.22em] uppercase";

  const collapsedDivider = (
    <div
      className={`absolute inset-0 flex items-center justify-center transition-opacity duration-150 ${
        show ? "pointer-events-none opacity-0" : "opacity-100"
      }`}
      aria-hidden="true"
    >
      <div className="bg-sidebar-section-divider h-px w-5 rounded-full" />
    </div>
  );

  if (collapsible) {
    return (
      <div className="relative mb-2">
        {collapsedDivider}
        <button
          type="button"
          onClick={onToggle}
          disabled={!show}
          aria-hidden={!show}
          aria-expanded={expanded}
          aria-label={expanded ? `Collapse ${String(children)}` : `Expand ${String(children)}`}
          tabIndex={show ? 0 : -1}
          className={`${textClass} hover:text-sidebar-foreground flex h-5 w-full items-center gap-1 px-3 transition-opacity duration-150 ${
            show ? "opacity-100" : "pointer-events-none opacity-0"
          }`}
        >
          {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
          <span>{children}</span>
        </button>
      </div>
    );
  }

  return (
    <div className="relative mb-2 px-3">
      {collapsedDivider}
      <div
        aria-hidden={!show}
        className={`${textClass} flex h-5 items-center transition-opacity duration-150 ${
          show ? "opacity-100" : "opacity-0"
        }`}
      >
        {children}
      </div>
    </div>
  );
}

interface AppSidebarProps {
  onNavigate?: () => void;
  collapsed?: boolean;
}

export default function AppSidebar({ onNavigate, collapsed = false }: AppSidebarProps) {
  const location = useLocation();
  const navigate = useViewTransitionNavigate();
  const params = useParams<{ libraryId: string }>();
  const { user, logout, clearProfile } = useAuth();
  const { profile } = useCurrentProfile();
  const { theme, setTheme, previewTheme, resetPreviewTheme } = useTheme();
  const isAdmin = user?.role === "admin";
  const { data: libraries } = useUserLibraries();
  const { pins } = useSidebarPins();
  const { togglePin } = useToggleSidebarPin();
  const { data: pluginSettings } = usePluginSettingsList();
  const requestStatus = useRequestFeatureStatus();
  const showRequestsNav = requestStatus.data?.requests_enabled === true;
  const pluginNavLinks = useMemo(() => {
    const installations = pluginSettings?.installations ?? [];
    const links: { id: string; basePath: string; label: string; pluginId: string }[] = [];
    for (const inst of installations) {
      for (const route of inst.routes) {
        if (!route.navigable || route.navigation_kind !== "user") continue;
        links.push({
          id: `${inst.id}:${route.id || route.path}`,
          basePath: pluginRouteHref(inst.id, route.path),
          label: route.navigation_label || inst.plugin_id,
          pluginId: inst.plugin_id,
        });
      }
    }
    return links;
  }, [pluginSettings]);

  const catalogState = useMemo(
    () =>
      location.pathname === "/catalog"
        ? parseCatalogSearchParams(new URLSearchParams(location.search))
        : null,
    [location.pathname, location.search],
  );
  const activeLibraryId =
    params.libraryId && Number.isInteger(Number(params.libraryId))
      ? Number(params.libraryId)
      : (getLibraryIdFromPathname(location.pathname) ??
        (catalogState?.source === "section" && catalogState.scope === "library"
          ? (catalogState.library_id ?? null)
          : catalogState?.source === "library_collection"
            ? (catalogState.library_id ?? null)
            : null));
  // Hover-to-expand when collapsed (150ms enter delay prevents accidental expansion)
  const [hovered, setHovered] = useState(false);
  const [profileMenuOpen, setProfileMenuOpen] = useState(false);
  const hoverTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const handleMouseEnter = useCallback(() => {
    if (!collapsed) return;
    hoverTimerRef.current = setTimeout(() => setHovered(true), 150);
  }, [collapsed]);
  const handleMouseLeave = useCallback(() => {
    if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
    hoverTimerRef.current = null;
    if (profileMenuOpen) return;
    setHovered(false);
  }, [profileMenuOpen]);
  useEffect(() => {
    return () => {
      if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
    };
  }, []);

  const sidebarExpanded = isSidebarExpanded(collapsed, hovered, profileMenuOpen);
  const showLabels = sidebarExpanded;
  const [librariesExpanded, setLibrariesExpanded] = useState(true);
  const [expandedLibraries, setExpandedLibraries] = useState<Set<number>>(
    () =>
      new Set(
        Object.keys(pins)
          .map(Number)
          .filter((id) => Number.isInteger(id) && id > 0),
      ),
  );

  const toggleLibraryExpand = useCallback((libId: number) => {
    setExpandedLibraries((prev) => {
      const next = new Set(prev);
      if (next.has(libId)) {
        next.delete(libId);
      } else {
        next.add(libId);
      }
      return next;
    });
  }, []);

  function isActive(href: string, exact?: boolean) {
    if (exact) return location.pathname === href;
    return location.pathname === href || location.pathname.startsWith(`${href}/`);
  }

  function isCatalogSourceActive(source: "query" | "favorites" | "watchlist" | "history") {
    return location.pathname === "/catalog" && catalogState?.source === source;
  }

  function isPinnedCatalogActive(
    libId: number,
    pin: { type: "collection" | "section"; id: string },
  ) {
    if (location.pathname !== "/catalog" || !catalogState) {
      return false;
    }

    if (pin.type === "collection") {
      return catalogState.source === "library_collection" && catalogState.collection_id === pin.id;
    }

    return (
      catalogState.source === "section" &&
      catalogState.scope === "library" &&
      catalogState.library_id === libId &&
      catalogState.section_id === pin.id
    );
  }

  const navLinkClass = (href: string, exact?: boolean) =>
    navLinkClassForState(isActive(href, exact));

  const navLinkClassForState = (active: boolean) =>
    `relative flex items-center gap-2.5 rounded-xl px-3 py-3 text-[13px] font-medium transition-all duration-200 ${
      active
        ? "text-sidebar-accent-foreground bg-sidebar-accent/90 shadow-[0_16px_30px_-24px_rgba(0,0,0,0.7)]"
        : "text-muted-foreground hover:text-sidebar-foreground hover:bg-sidebar-accent/70"
    }`;

  return (
    <aside
      className={`border-sidebar-border/70 bg-sidebar/88 sidebar-transition fixed top-0 bottom-0 left-0 z-40 flex flex-col border-r backdrop-blur-2xl ${collapsed && !sidebarExpanded ? "w-16" : "w-[260px]"}`}
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
      style={
        collapsed && sidebarExpanded
          ? { zIndex: 45, boxShadow: "0 25px 50px -12px rgb(0 0 0 / 0.5)" }
          : undefined
      }
    >
      {/* Logo */}
      <div
        className={`sidebar-logo flex items-center py-6 ${showLabels ? "px-5" : "justify-center px-2"}`}
      >
        <SiloBrand
          variant={showLabels ? "wordmark" : "mark"}
          className={showLabels ? "h-12 w-[112px]" : "h-9 w-9"}
        />
      </div>

      {/* Main nav */}
      <nav
        aria-label="Main navigation"
        className="sidebar-nav sidebar-scroll flex-1 space-y-7 overflow-y-auto px-3 pb-5"
      >
        {/* Home */}
        <ul className="list-none space-y-0.5">
          <li>
            <ViewTransitionLink
              to="/"
              onClick={onNavigate}
              className={navLinkClass("/", true)}
              aria-current={isActive("/", true) ? "page" : undefined}
            >
              {isActive("/", true) && (
                <span
                  className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                  style={{ background: "var(--primary)" }}
                />
              )}
              <Home className="h-[18px] w-[18px] shrink-0" />
              <SidebarLabel show={showLabels}>Home</SidebarLabel>
            </ViewTransitionLink>
          </li>
        </ul>

        {/* Libraries */}
        {libraries && libraries.length > 0 && (
          <div className="sidebar-libraries">
            <SidebarSectionHeader
              show={showLabels}
              collapsible
              expanded={librariesExpanded}
              onToggle={() => setLibrariesExpanded((v) => !v)}
            >
              Libraries
            </SidebarSectionHeader>
            {(showLabels ? librariesExpanded : true) && (
              <ul className="list-none space-y-0.5">
                {libraries.map((lib) => {
                  const baseHref = `/library/${lib.id}`;
                  const active = activeLibraryId === lib.id;
                  const href =
                    active && location.pathname === baseHref
                      ? `${baseHref}${location.search}`
                      : baseHref;
                  const libraryPins = pins[String(lib.id)] ?? [];
                  const hasPins = libraryPins.length > 0;
                  const isExpanded = hasPins && !expandedLibraries.has(lib.id);

                  return (
                    <li key={lib.id}>
                      {/* Library row: chevron (if pins) + icon + name link */}
                      <div
                        className={`relative flex items-center rounded-lg text-[13px] font-medium transition-colors duration-150 ${
                          active
                            ? "text-sidebar-accent-foreground bg-sidebar-accent"
                            : "text-muted-foreground hover:text-sidebar-foreground hover:bg-sidebar-accent"
                        }`}
                      >
                        {active && (
                          <span
                            className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                            style={{ background: "var(--primary)" }}
                          />
                        )}
                        {showLabels && hasPins ? (
                          <button
                            type="button"
                            onClick={() => toggleLibraryExpand(lib.id)}
                            className="flex h-full items-center py-3 pr-1 pl-3"
                            aria-label={isExpanded ? `Collapse ${lib.name}` : `Expand ${lib.name}`}
                            aria-expanded={isExpanded}
                          >
                            {isExpanded ? (
                              <ChevronDown className="h-3.5 w-3.5 opacity-60" />
                            ) : (
                              <ChevronRight className="h-3.5 w-3.5 opacity-60" />
                            )}
                          </button>
                        ) : showLabels ? (
                          <span className="w-3 shrink-0 pl-3" />
                        ) : null}
                        <ViewTransitionLink
                          to={href}
                          onClick={onNavigate}
                          className={`flex items-center gap-2.5 py-2.5 ${
                            showLabels ? "flex-1 pr-3" : "w-full px-3"
                          }`}
                          aria-current={active ? "page" : undefined}
                        >
                          <span className="flex w-[18px] flex-shrink-0 items-center justify-center">
                            {getLibraryIcon(lib.type)}
                          </span>
                          <SidebarLabel show={showLabels}>{lib.name}</SidebarLabel>
                        </ViewTransitionLink>
                      </div>

                      {/* Pinned items under this library — hidden when collapsed */}
                      {showLabels && hasPins && isExpanded && (
                        <div className="border-sidebar-border ml-3 space-y-0.5 border-l pl-2">
                          {libraryPins.map((pin) => {
                            const pinHref =
                              pin.type === "collection"
                                ? buildLibraryCollectionCatalogHref(pin.id, pin.label)
                                : buildSectionCatalogHref({
                                    scope: "library",
                                    libraryId: lib.id,
                                    sectionId: pin.id,
                                    title: pin.label,
                                  });
                            const pinActive = isPinnedCatalogActive(lib.id, pin);

                            return (
                              <div
                                key={`${pin.type}-${pin.id}`}
                                className="group/pin relative flex items-center"
                              >
                                <ViewTransitionLink
                                  to={pinHref}
                                  onClick={onNavigate}
                                  className={`flex flex-1 items-center gap-2 rounded-xl px-2.5 py-2.5 text-[12.5px] font-medium transition-colors duration-150 ${
                                    pinActive
                                      ? "text-sidebar-accent-foreground bg-sidebar-accent/90"
                                      : "text-muted-foreground hover:text-sidebar-foreground hover:bg-sidebar-accent/70"
                                  }`}
                                >
                                  {pin.type === "collection" ? (
                                    <FolderOpen className="h-3.5 w-3.5 opacity-60" />
                                  ) : (
                                    <LayoutGrid className="h-3.5 w-3.5 opacity-60" />
                                  )}
                                  <span className="truncate">{pin.label}</span>
                                </ViewTransitionLink>
                                <button
                                  type="button"
                                  onClick={() =>
                                    togglePin(lib.id, {
                                      type: pin.type,
                                      id: pin.id,
                                      label: pin.label,
                                    })
                                  }
                                  className="text-muted-foreground hover:text-destructive absolute right-1 rounded p-1 opacity-0 transition-opacity group-hover/pin:opacity-100"
                                  title="Unpin"
                                >
                                  <PinOff className="h-3 w-3" />
                                </button>
                              </div>
                            );
                          })}
                        </div>
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        )}

        {/* Discover */}
        <div>
          <SidebarSectionHeader show={showLabels}>Discover</SidebarSectionHeader>
          <ul className="list-none space-y-0.5">
            <li>
              <ViewTransitionLink
                to={buildQueryCatalogHref()}
                onClick={onNavigate}
                className={navLinkClassForState(isCatalogSourceActive("query"))}
                aria-current={isCatalogSourceActive("query") ? "page" : undefined}
              >
                {isCatalogSourceActive("query") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <Search className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>Search</SidebarLabel>
                {showLabels && (
                  <kbd className="bg-muted text-muted-foreground pointer-events-none ml-auto hidden rounded border px-1.5 py-0.5 text-[10px] font-medium select-none lg:inline-flex">
                    {"\u2318"}K
                  </kbd>
                )}
              </ViewTransitionLink>
            </li>
            <li>
              <ViewTransitionLink
                to="/recommendations"
                onClick={onNavigate}
                className={navLinkClass("/recommendations")}
                aria-current={isActive("/recommendations") ? "page" : undefined}
              >
                {isActive("/recommendations") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <Sparkles className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>Recommendations</SidebarLabel>
              </ViewTransitionLink>
            </li>
            {showRequestsNav && (
              <li>
                <ViewTransitionLink
                  to="/requests"
                  onClick={onNavigate}
                  className={navLinkClass("/requests")}
                  aria-current={isActive("/requests") ? "page" : undefined}
                >
                  {isActive("/requests") && (
                    <span
                      className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                      style={{ background: "var(--primary)" }}
                    />
                  )}
                  <Send className="h-[18px] w-[18px] shrink-0" />
                  <SidebarLabel show={showLabels}>Requests</SidebarLabel>
                </ViewTransitionLink>
              </li>
            )}
            <li>
              <ViewTransitionLink
                to="/calendar"
                onClick={onNavigate}
                className={navLinkClass("/calendar")}
                aria-current={isActive("/calendar") ? "page" : undefined}
              >
                {isActive("/calendar") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <CalendarDays className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>Calendar</SidebarLabel>
              </ViewTransitionLink>
            </li>
          </ul>
        </div>

        {/* Your Stuff */}
        <div className="sidebar-personal">
          <SidebarSectionHeader show={showLabels}>Your Stuff</SidebarSectionHeader>
          <ul className="list-none space-y-0.5">
            <li>
              <ViewTransitionLink
                to={buildPersonalCatalogHref("favorites")}
                onClick={onNavigate}
                className={navLinkClassForState(isCatalogSourceActive("favorites"))}
                aria-current={isCatalogSourceActive("favorites") ? "page" : undefined}
              >
                {isCatalogSourceActive("favorites") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <Heart className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>Favorites</SidebarLabel>
              </ViewTransitionLink>
            </li>
            <li>
              <ViewTransitionLink
                to={buildPersonalCatalogHref("watchlist")}
                onClick={onNavigate}
                className={navLinkClassForState(isCatalogSourceActive("watchlist"))}
                aria-current={isCatalogSourceActive("watchlist") ? "page" : undefined}
              >
                {isCatalogSourceActive("watchlist") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <List className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>Watchlist</SidebarLabel>
              </ViewTransitionLink>
            </li>
            <li>
              <ViewTransitionLink
                to="/rooms/join"
                onClick={onNavigate}
                className={navLinkClass("/rooms/join")}
                aria-current={isActive("/rooms/join") ? "page" : undefined}
              >
                {isActive("/rooms/join") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <UsersRound className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>Watch Party</SidebarLabel>
              </ViewTransitionLink>
            </li>
            <li>
              <ViewTransitionLink
                to="/collections"
                onClick={onNavigate}
                className={navLinkClass("/collections")}
                aria-current={isActive("/collections") ? "page" : undefined}
              >
                {isActive("/collections") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <FolderOpen className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>Collections</SidebarLabel>
              </ViewTransitionLink>
            </li>
            <li>
              <ViewTransitionLink
                to={buildPersonalCatalogHref("history")}
                onClick={onNavigate}
                className={navLinkClassForState(isCatalogSourceActive("history"))}
                aria-current={isCatalogSourceActive("history") ? "page" : undefined}
              >
                {isCatalogSourceActive("history") && (
                  <span
                    className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                    style={{ background: "var(--primary)" }}
                  />
                )}
                <Clock className="h-[18px] w-[18px] shrink-0" />
                <SidebarLabel show={showLabels}>History</SidebarLabel>
              </ViewTransitionLink>
            </li>
          </ul>
        </div>

        {/* Apps (plugin-supplied user navigation) */}
        {pluginNavLinks.length > 0 && (
          <div className="sidebar-apps">
            <SidebarSectionHeader show={showLabels}>Apps</SidebarSectionHeader>
            <ul className="list-none space-y-0.5">
              {pluginNavLinks.map((link) => (
                <li key={link.id}>
                  <a
                    href={link.basePath}
                    onClick={(e) => {
                      e.preventDefault();
                      void navigateToPluginRoute(link.basePath);
                      onNavigate?.();
                    }}
                    className={navLinkClassForState(false)}
                    title={link.pluginId}
                  >
                    <Puzzle className="h-[18px] w-[18px] shrink-0" />
                    <SidebarLabel show={showLabels}>{link.label}</SidebarLabel>
                  </a>
                </li>
              ))}
            </ul>
          </div>
        )}
      </nav>

      {/* Footer */}
      <div className="sidebar-footer border-sidebar-border/70 space-y-2 border-t px-3 py-3">
        {isAdmin && (
          <Link
            to="/admin"
            onClick={onNavigate}
            className={navLinkClass("/admin")}
            aria-current={isActive("/admin") ? "page" : undefined}
          >
            <Shield className="h-[18px] w-[18px] shrink-0" />
            <SidebarLabel show={showLabels}>Admin</SidebarLabel>
          </Link>
        )}

        {/* User profile dropdown */}
        <DropdownMenu
          onOpenChange={(open) => {
            if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
            hoverTimerRef.current = null;
            setProfileMenuOpen(open);
            if (!open && !collapsed) return;
            setHovered(open);
          }}
        >
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label={`${profile?.name ?? user?.username ?? "User"} menu`}
              className={`hover:bg-sidebar-accent/70 flex items-center rounded-xl py-3 transition-colors duration-150 ${
                showLabels ? "w-full gap-2.5 px-3" : "mx-auto h-10 w-10 justify-center px-0"
              }`}
            >
              <Avatar className="h-7 w-7 shrink-0">
                {profile?.avatar_url ? (
                  <AvatarImage src={profile.avatar_url} alt={profile.name} />
                ) : null}
                <AvatarFallback className="bg-primary text-primary-foreground text-xs font-bold shadow-[0_14px_32px_-20px_rgba(0,0,0,0.8)]">
                  {profile?.name?.charAt(0).toUpperCase() ??
                    user?.username?.charAt(0).toUpperCase() ??
                    "?"}
                </AvatarFallback>
              </Avatar>
              <span
                className={`text-sidebar-foreground truncate text-[13px] font-medium transition-[opacity,max-width] duration-200 ease-out ${
                  showLabels
                    ? "max-w-[180px] opacity-100 delay-[50ms]"
                    : "max-w-0 overflow-hidden opacity-0"
                }`}
              >
                {profile?.name ?? user?.username ?? "User"}
              </span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            side={getProfileMenuSide(collapsed)}
            align="start"
            className="w-[min(20rem,calc(100vw-1.5rem))] rounded-2xl p-2"
          >
            <DropdownMenuLabel className="flex items-center gap-3 px-2 py-2">
              <Avatar className="h-10 w-10">
                {profile?.avatar_url ? (
                  <AvatarImage src={profile.avatar_url} alt={profile.name} />
                ) : null}
                <AvatarFallback className="bg-primary text-primary-foreground text-sm font-bold">
                  {(profile?.name ?? user?.username ?? "?").charAt(0).toUpperCase()}
                </AvatarFallback>
              </Avatar>
              <div className="flex min-w-0 flex-col">
                <span className="truncate text-[14px] font-semibold">
                  {profile?.name ?? user?.username ?? "User"}
                </span>
                {profile && user?.username && user.username !== profile.name && (
                  <span className="text-muted-foreground truncate text-[11px] font-normal">
                    {user.username}
                  </span>
                )}
              </div>
            </DropdownMenuLabel>

            <div
              className="flex items-center justify-between gap-2 px-2.5 pt-1 pb-1.5"
              role="group"
              aria-label="Theme"
            >
              <span className="text-muted-foreground text-[10px] font-medium tracking-[0.14em] uppercase">
                Theme
              </span>
              <div className="flex items-center gap-1.5">
                {CURATED_THEME_IDS.map((id) => {
                  const def = THEMES[id];
                  const isActive = theme === id;
                  return (
                    <DropdownMenuItem
                      key={id}
                      onSelect={(event) => {
                        event.preventDefault();
                        setTheme(id);
                      }}
                      onMouseEnter={() => previewTheme(id)}
                      onMouseLeave={resetPreviewTheme}
                      onFocus={() => previewTheme(id)}
                      onBlur={resetPreviewTheme}
                      aria-label={def.label}
                      title={def.label}
                      className={cn(
                        "relative h-6 w-6 flex-none cursor-pointer rounded-full border p-0 transition-transform hover:scale-110 focus:scale-110",
                        isActive
                          ? "ring-primary ring-offset-popover border-transparent ring-2 ring-offset-2"
                          : "border-border/60",
                      )}
                      style={{ backgroundColor: def.previewBg }}
                    >
                      <span
                        aria-hidden="true"
                        className="absolute top-1/2 left-1/2 h-2 w-2 -translate-x-1/2 -translate-y-1/2 rounded-full"
                        style={{ backgroundColor: def.previewAccent }}
                      />
                    </DropdownMenuItem>
                  );
                })}
              </div>
            </div>

            <DropdownMenuSeparator />

            <DropdownMenuItem
              onClick={() => {
                navigate("/settings/playback");
              }}
              className="gap-2.5 rounded-lg px-2.5 py-2 text-[13px]"
            >
              <Settings className="h-[18px] w-[18px]" />
              Settings
            </DropdownMenuItem>

            <DropdownMenuSeparator />

            <div className="flex flex-col gap-1 pt-0.5">
              {profile && (
                <DropdownMenuItem
                  onClick={() => {
                    clearProfile();
                    navigate("/profiles");
                  }}
                  className="border-sidebar-border/60 bg-sidebar-accent/40 hover:bg-sidebar-accent focus:bg-sidebar-accent gap-3 rounded-xl border px-3 py-3 text-[13px] font-semibold"
                >
                  <UserCircle className="h-[18px] w-[18px]" />
                  Switch Profile
                </DropdownMenuItem>
              )}
              <DropdownMenuItem
                onClick={logout}
                className="border-sidebar-border/60 bg-sidebar-accent/40 hover:bg-sidebar-accent focus:bg-sidebar-accent gap-3 rounded-xl border px-3 py-3 text-[13px] font-semibold"
              >
                <LogOut className="h-[18px] w-[18px]" />
                Logout
              </DropdownMenuItem>
            </div>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </aside>
  );
}
