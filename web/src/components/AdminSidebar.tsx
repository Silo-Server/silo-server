import { Link, useLocation } from "react-router";
import { ArrowLeft, Search } from "lucide-react";
import type { ReactNode } from "react";
import {
  SideNavAttentionDot,
  SideNavItem,
  SideNavSection,
  SideNavSubItem,
  SideNavTopic,
} from "@/components/SideNav";
import { SiloBrand } from "@/components/SiloBrand";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import {
  buildAdminNavSections,
  buildAdminPluginNavItems,
  isAdminNavTopic,
  type AdminNavItem,
} from "@/lib/adminNavigation";
import { adminSettingsHref } from "@/lib/adminSettingsSearch";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { SEARCH_SHORTCUT_LABEL } from "@/lib/keyboardShortcut";
import { useSettingsAttention } from "@/hooks/admin/useSettingsHealth";
import { useAdminNavTopics } from "@/hooks/useAdminNavTopics";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { usePolicyCapability } from "@/hooks/queries/admin/policy";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useBuildInfo } from "@/hooks/queries/admin/system";
import { useAdminRequestCounts } from "@/hooks/queries/admin/requests";
import { usePrefetchAdminServerSettings } from "@/hooks/queries/admin/settings";
import { cn } from "@/lib/utils";

interface AdminSidebarProps {
  onNavigate?: () => void;
  embedded?: boolean;
  /**
   * Opens the admin command palette. The desktop shell passes it; the phone
   * drawer leaves it out, since the phone header already has a search button
   * and the drawer would have to close under the palette.
   */
  onSearch?: () => void;
}

const SETTINGS_PREFIX = "/admin/settings/";

function preloadSettingsPages() {
  void import("@/pages/admin-settings/AdminSettingsLayout");
}

function useSessionCount() {
  const { data: sessions = [] } = useAdminSessions();
  return sessions.length;
}

/**
 * Pages whose settings are broken right now: a restart a saved setting is
 * waiting on, or a configured service that is failing. Setup the admin has not
 * done yet ("Email not set up") is left to the Dashboard's notice, so a server
 * that deliberately runs without mail does not carry a dot forever.
 */
function useBrokenSettingsBadges(): Record<string, ReactNode> {
  const badges: Record<string, ReactNode> = {};
  for (const item of useSettingsAttention()) {
    if (item.state !== "warn" || !item.action) continue;
    badges[adminSettingsHref(item.action.page)] = (
      <SideNavAttentionDot label={`${item.label} ${item.stateText.toLowerCase()}`} />
    );
  }
  return badges;
}

export default function AdminSidebar({
  onNavigate,
  embedded = false,
  onSearch,
}: AdminSidebarProps) {
  const location = useLocation();
  const prefetchSettings = usePrefetchAdminServerSettings();
  const sessionCount = useSessionCount();
  const buildInfo = useBuildInfo();
  const policyCapability = usePolicyCapability();
  // Falls back to "dev build" when the binary carries no VCS/ldflags revision
  // (e.g. `go run` or an image built without BUILD_REVISION) rather than a stark
  // "unavailable".
  let buildDisplay = "dev build";
  if (buildInfo.isPending && !buildInfo.data) {
    buildDisplay = "loading...";
  } else if (buildInfo.isError) {
    buildDisplay = "load failed";
  } else if (buildInfo.data?.available) {
    const buildNumber = buildInfo.data.build_number ?? 0;
    buildDisplay =
      buildNumber > 0 ? `${buildNumber} · ${buildInfo.data.display}` : buildInfo.data.display;
  }

  const activityBadge =
    sessionCount > 0 ? <span className="live-badge">{sessionCount} live</span> : undefined;
  const pendingRequests = useAdminRequestCounts().data?.needs_approval ?? 0;
  const requestsBadge =
    pendingRequests > 0 ? (
      <span className="bg-primary text-primary-foreground rounded-full px-1.5 py-px text-[10px] font-semibold tabular-nums">
        <span aria-hidden="true">{pendingRequests}</span>
        <span className="sr-only">, {pendingRequests} need approval</span>
      </span>
    ) : undefined;
  const badges: Record<string, ReactNode> = {
    ...useBrokenSettingsBadges(),
    "/admin/activity": activityBadge,
    "/admin/requests": requestsBadge,
  };
  const sections = buildAdminNavSections({
    policyEditorAvailable: policyCapability.data?.editor_available === true,
  });

  // Use the admin installations endpoint, not /settings/plugins — the user
  // settings endpoint filters to plugins that expose user settings / a user-
  // navigable route, which excludes admin-only plugins like arrproxy and
  // arrouter. The admin sidebar needs the full installation list to render
  // its "Plugin Apps" group.
  const { data: adminInstallations } = useAdminPluginInstallations();
  const adminPluginItems = buildAdminPluginNavItems(adminInstallations);

  function isActive(item: AdminNavItem) {
    if (item.exact) return location.pathname === item.href;
    return location.pathname === item.href || location.pathname.startsWith(`${item.href}/`);
  }

  const activeTopic = sections
    .flatMap((section) => section.entries)
    .find((entry) => isAdminNavTopic(entry) && entry.items.some(isActive));
  const topics = useAdminNavTopics(
    activeTopic && isAdminNavTopic(activeTopic) ? activeTopic.id : null,
  );

  function renderPluginItem(item: AdminNavItem) {
    return (
      <SideNavItem
        key={item.href}
        label={item.label}
        icon={item.icon}
        href={item.href}
        external
        active={isActive(item)}
        onClick={(e) => {
          e.preventDefault();
          void navigateToPluginRoute(item.href);
          onNavigate?.();
        }}
      />
    );
  }

  // A settings page draws from the settings form: fetch its code and values
  // while the pointer is on the link, so the click lands on a filled page.
  function intentFor(item: AdminNavItem) {
    if (!item.href.startsWith(SETTINGS_PREFIX)) return undefined;
    return () => {
      preloadSettingsPages();
      prefetchSettings();
    };
  }

  return (
    <aside
      data-layout={embedded ? "drawer" : "desktop"}
      className={cn(
        "border-sidebar-border/70 bg-sidebar/92 flex w-[240px] flex-col border-r backdrop-blur-2xl",
        embedded
          ? "relative h-full w-full border-r-0 [&_nav_a]:min-h-11 [&_nav_button]:min-h-11"
          : "fixed top-0 bottom-0 left-0 z-40",
      )}
    >
      {/* Logo */}
      <div className="flex items-center gap-2.5 px-5 pt-6 pb-4">
        <Link
          to="/"
          onClick={onNavigate}
          aria-label="Go to app home"
          className="focus-visible:ring-ring/50 inline-flex rounded-md transition-opacity hover:opacity-85 focus-visible:ring-[3px] focus-visible:outline-none"
        >
          <SiloBrand className="h-12 w-[112px]" />
        </Link>
      </div>
      {onSearch ? (
        <div className="px-3 pb-4">
          <button
            type="button"
            onClick={onSearch}
            aria-label="Search admin sections"
            title={`Search admin sections (${SEARCH_SHORTCUT_LABEL})`}
            className="border-border/70 bg-surface/70 text-muted-foreground hover:text-foreground hover:bg-accent/60 focus-visible:ring-ring/60 flex h-10 w-full items-center gap-2.5 rounded-xl border px-3 text-[13px] transition-colors focus-visible:ring-[3px] focus-visible:outline-none"
          >
            <Search className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>Search</span>
            <kbd className="border-border/70 pointer-events-none ml-auto rounded border px-1.5 py-0.5 font-mono text-[10px] whitespace-nowrap select-none">
              {SEARCH_SHORTCUT_LABEL}
            </kbd>
          </button>
        </div>
      ) : null}
      {/* Nav sections */}
      <nav
        aria-label="Admin navigation"
        className="sidebar-scroll flex-1 space-y-5 overflow-y-auto px-3"
      >
        {sections.map((section) => (
          <SideNavSection key={section.label} label={section.label} idPrefix="admin-nav">
            {section.entries.map((entry) => {
              if (!isAdminNavTopic(entry)) {
                return (
                  <SideNavItem
                    key={entry.href}
                    label={entry.label}
                    icon={entry.icon}
                    href={entry.href}
                    active={isActive(entry)}
                    badge={badges[entry.href]}
                    onClick={onNavigate}
                  />
                );
              }
              // A closed topic shows the first badge among its pages, so a
              // pending request or a broken setting is never folded away.
              const childBadge = entry.items.map((item) => badges[item.href]).find(Boolean);
              return (
                <SideNavTopic
                  key={entry.id}
                  label={entry.label}
                  icon={entry.icon}
                  open={topics.isOpen(entry.id)}
                  onToggle={() => topics.toggle(entry.id)}
                  badge={childBadge}
                >
                  {entry.items.map((item) => (
                    <SideNavSubItem
                      key={item.href}
                      label={item.label}
                      href={item.href}
                      active={isActive(item)}
                      badge={badges[item.href]}
                      onClick={onNavigate}
                      onIntent={intentFor(item)}
                    />
                  ))}
                </SideNavTopic>
              );
            })}
          </SideNavSection>
        ))}
        {adminPluginItems.length > 0 ? (
          <SideNavSection label="Plugin Apps" idPrefix="admin-nav">
            {adminPluginItems.map(renderPluginItem)}
          </SideNavSection>
        ) : null}
      </nav>

      {/* Footer */}
      <div className="space-y-3 px-3 pb-4">
        <div className="border-sidebar-border/70 bg-sidebar-accent/40 rounded-xl border px-3 py-2">
          <div className="text-muted-foreground text-[10px] font-semibold tracking-[0.18em] uppercase">
            Build
          </div>
          <div
            className="text-sidebar-foreground mt-1 font-mono text-[12px] leading-5"
            title={buildInfo.data?.built_at ? `Built ${buildInfo.data.built_at}` : undefined}
          >
            {buildDisplay}
          </div>
        </div>
        {/* Back to app — admin was almost always entered from the app, so this
            plays the motion backwards rather than as a descent. */}
        <ViewTransitionLink
          to="/"
          up
          onClick={onNavigate}
          className="text-muted-foreground hover:text-foreground hover:bg-accent/70 flex items-center gap-2.5 rounded-xl px-3 py-2.5 text-[13px] font-medium transition-colors duration-150"
        >
          <ArrowLeft className="h-[18px] w-[18px]" />
          <span>Back to App</span>
        </ViewTransitionLink>
      </div>
    </aside>
  );
}
