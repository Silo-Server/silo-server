import { Link, useLocation } from "react-router";
import {
  LayoutDashboard,
  Radio,
  Library,
  LayoutPanelTop,
  PanelsTopLeft,
  Users,
  MonitorSmartphone,
  History,
  Captions,
  Download,
  SlidersHorizontal,
  Server,
  Bot,
  ArrowLeft,
  Wrench,
  KeyRound,
  CalendarClock,
  ScrollText,
  Blocks,
  Puzzle,
  Send,
  RefreshCw,
  SkipForward,
  Megaphone,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { SideNavItem, SideNavSection } from "@/components/SideNav";
import { SiloBrand } from "@/components/SiloBrand";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useBuildInfo } from "@/hooks/queries/admin/system";
import { pluginRouteHref } from "@/lib/pluginRouteHref";

interface SidebarItem {
  label: string;
  icon: LucideIcon;
  href: string;
  exact?: boolean;
  badge?: ReactNode;
  // external=true means render as <a> (full page navigation) instead of
  // react-router <Link>. Used for plugin routes mounted at /api/v1/plugins/...
  external?: boolean;
}

interface SidebarSection {
  label: string;
  items: SidebarItem[];
}

interface AdminSidebarProps {
  onNavigate?: () => void;
}

function useSessionCount() {
  const { data: sessions = [] } = useAdminSessions();
  return sessions.length;
}

export default function AdminSidebar({ onNavigate }: AdminSidebarProps) {
  const location = useLocation();
  const sessionCount = useSessionCount();
  const buildInfo = useBuildInfo();
  // Falls back to "dev build" when the binary carries no VCS/ldflags revision
  // (e.g. `go run` or an image built without BUILD_REVISION) rather than a stark
  // "unavailable".
  let buildDisplay = "dev build";
  if (buildInfo.isPending && !buildInfo.data) {
    buildDisplay = "loading...";
  } else if (buildInfo.isError) {
    buildDisplay = "load failed";
  } else if (buildInfo.data?.available) {
    buildDisplay = buildInfo.data.display;
  }

  // Grouped by admin intent: monitoring, curating the catalog, background
  // processing the server runs on its own, people and their data, and the
  // server installation itself.
  const sections: SidebarSection[] = [
    {
      label: "Overview",
      items: [
        { label: "Dashboard", icon: LayoutDashboard, href: "/admin", exact: true },
        {
          label: "Activity",
          icon: Radio,
          href: "/admin/activity",
          badge:
            sessionCount > 0 ? <span className="live-badge">{sessionCount} live</span> : undefined,
        },
        { label: "Logs", icon: ScrollText, href: "/admin/logs" },
      ],
    },
    {
      label: "Content",
      items: [
        { label: "Libraries", icon: Library, href: "/admin/libraries" },
        { label: "Collections", icon: LayoutPanelTop, href: "/admin/collections" },
        { label: "Sections", icon: PanelsTopLeft, href: "/admin/sections" },
        { label: "Requests", icon: Send, href: "/admin/requests" },
      ],
    },
    {
      label: "Automation",
      items: [
        { label: "Autoscan", icon: RefreshCw, href: "/admin/autoscan" },
        { label: "Scheduled Tasks", icon: CalendarClock, href: "/admin/tasks" },
        { label: "Subtitles", icon: Captions, href: "/admin/subtitles" },
        { label: "Markers", icon: SkipForward, href: "/admin/marker-history" },
        { label: "Recommendations", icon: Bot, href: "/admin/recommendations" },
      ],
    },
    {
      label: "Users",
      items: [
        { label: "Users", icon: Users, href: "/admin/users" },
        { label: "Announcements", icon: Megaphone, href: "/admin/announcements" },
        { label: "Devices", icon: MonitorSmartphone, href: "/admin/devices" },
        { label: "Playback History", icon: History, href: "/admin/history" },
        { label: "History Import", icon: Download, href: "/admin/history-import" },
      ],
    },
    {
      label: "System",
      items: [
        { label: "Settings", icon: SlidersHorizontal, href: "/admin/settings" },
        { label: "Plugins", icon: Blocks, href: "/admin/plugins" },
        { label: "Nodes", icon: Server, href: "/admin/nodes" },
        { label: "API Keys", icon: KeyRound, href: "/admin/api-keys" },
        { label: "Maintenance", icon: Wrench, href: "/admin/maintenance" },
      ],
    },
  ];

  // Use the admin installations endpoint, not /settings/plugins — the user
  // settings endpoint filters to plugins that expose user settings / a user-
  // navigable route, which excludes admin-only plugins like arrproxy and
  // arrouter. The admin sidebar needs the full installation list to render
  // its "Plugin Apps" group.
  const { data: adminInstallations } = useAdminPluginInstallations();
  const adminPluginItems: SidebarItem[] = [];
  for (const inst of adminInstallations ?? []) {
    if (!inst.enabled) continue;
    for (const route of inst.routes ?? []) {
      if (!route.navigable || route.navigation_kind !== "admin") continue;
      adminPluginItems.push({
        label: route.navigation_label || inst.plugin_id,
        icon: Puzzle,
        href: pluginRouteHref(inst.id, route.path),
        external: true,
      });
    }
  }
  if (adminPluginItems.length > 0) {
    sections.push({ label: "Plugin Apps", items: adminPluginItems });
  }

  function isActive(item: SidebarItem) {
    if (item.exact) return location.pathname === item.href;
    return location.pathname === item.href || location.pathname.startsWith(`${item.href}/`);
  }

  return (
    <aside className="border-sidebar-border/70 bg-sidebar/92 fixed top-0 bottom-0 left-0 z-40 flex w-[240px] flex-col border-r backdrop-blur-2xl">
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
      {/* Nav sections */}
      <nav
        aria-label="Admin navigation"
        className="sidebar-scroll flex-1 space-y-5 overflow-y-auto px-3"
      >
        {sections.map((section) => (
          <SideNavSection key={section.label} label={section.label} idPrefix="admin-nav">
            {section.items.map((item) =>
              item.external ? (
                <SideNavItem
                  key={item.href}
                  label={item.label}
                  icon={item.icon}
                  href={item.href}
                  external
                  active={isActive(item)}
                  badge={item.badge}
                  onClick={(e) => {
                    e.preventDefault();
                    void navigateToPluginRoute(item.href);
                    onNavigate?.();
                  }}
                />
              ) : (
                <SideNavItem
                  key={item.href}
                  label={item.label}
                  icon={item.icon}
                  href={item.href}
                  active={isActive(item)}
                  badge={item.badge}
                  onClick={onNavigate}
                />
              ),
            )}
          </SideNavSection>
        ))}
      </nav>

      {/* Footer */}
      <div className="space-y-3 px-3 pb-4">
        <div className="border-sidebar-border/70 bg-sidebar-accent/40 rounded-xl border px-3 py-2">
          <div className="text-muted-foreground text-[10px] font-semibold tracking-[0.18em] uppercase">
            Build
          </div>
          <div className="text-sidebar-foreground mt-1 font-mono text-[12px] leading-5">
            {buildDisplay}
          </div>
        </div>
        {/* Back to app */}
        <Link
          to="/"
          onClick={onNavigate}
          className="text-muted-foreground hover:text-foreground hover:bg-accent/70 flex items-center gap-2.5 rounded-xl px-3 py-2.5 text-[13px] font-medium transition-colors duration-150"
        >
          <ArrowLeft className="h-[18px] w-[18px]" />
          <span>Back to App</span>
        </Link>
      </div>
    </aside>
  );
}
