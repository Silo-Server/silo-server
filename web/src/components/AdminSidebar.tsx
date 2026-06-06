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
} from "lucide-react";
import type { ReactNode } from "react";
import { SiloBrand } from "@/components/SiloBrand";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useBuildInfo } from "@/hooks/queries/admin/system";
import { pluginRouteHref } from "@/lib/pluginRouteHref";

interface SidebarItem {
  label: string;
  icon: ReactNode;
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

  const sections: SidebarSection[] = [
    {
      label: "Overview",
      items: [
        {
          label: "Dashboard",
          icon: <LayoutDashboard className="h-[18px] w-[18px]" />,
          href: "/admin",
          exact: true,
        },
        {
          label: "Activity",
          icon: <Radio className="h-[18px] w-[18px]" />,
          href: "/admin/activity",
          badge:
            sessionCount > 0 ? <span className="live-badge">{sessionCount} live</span> : undefined,
        },
        {
          label: "Logs",
          icon: <ScrollText className="h-[18px] w-[18px]" />,
          href: "/admin/logs",
        },
      ],
    },
    {
      label: "Content",
      items: [
        {
          label: "Libraries",
          icon: <Library className="h-[18px] w-[18px]" />,
          href: "/admin/libraries",
        },
        {
          label: "Collections",
          icon: <LayoutPanelTop className="h-[18px] w-[18px]" />,
          href: "/admin/collections",
        },
        {
          label: "Requests",
          icon: <Send className="h-[18px] w-[18px]" />,
          href: "/admin/requests",
        },
        {
          label: "Autoscan",
          icon: <RefreshCw className="h-[18px] w-[18px]" />,
          href: "/admin/autoscan",
        },
        {
          label: "Sections",
          icon: <PanelsTopLeft className="h-[18px] w-[18px]" />,
          href: "/admin/sections",
        },
        {
          label: "Subtitles",
          icon: <Captions className="h-[18px] w-[18px]" />,
          href: "/admin/subtitles",
        },
      ],
    },
    {
      label: "Users",
      items: [
        {
          label: "Users",
          icon: <Users className="h-[18px] w-[18px]" />,
          href: "/admin/users",
        },
        {
          label: "Devices",
          icon: <MonitorSmartphone className="h-[18px] w-[18px]" />,
          href: "/admin/devices",
        },
        {
          label: "Playback History",
          icon: <History className="h-[18px] w-[18px]" />,
          href: "/admin/history",
        },
        {
          label: "History Import",
          icon: <Download className="h-[18px] w-[18px]" />,
          href: "/admin/history-import",
        },
      ],
    },
    {
      label: "Server",
      items: [
        {
          label: "Scheduled Tasks",
          icon: <CalendarClock className="h-[18px] w-[18px]" />,
          href: "/admin/tasks",
        },
        {
          label: "Nodes",
          icon: <Server className="h-[18px] w-[18px]" />,
          href: "/admin/nodes",
        },
        {
          label: "Maintenance",
          icon: <Wrench className="h-[18px] w-[18px]" />,
          href: "/admin/maintenance",
        },
        {
          label: "Plugins",
          icon: <Blocks className="h-[18px] w-[18px]" />,
          href: "/admin/plugins",
        },
        {
          label: "Settings",
          icon: <SlidersHorizontal className="h-[18px] w-[18px]" />,
          href: "/admin/settings",
        },
        {
          label: "Recommendations",
          icon: <Bot className="h-[18px] w-[18px]" />,
          href: "/admin/recommendations",
        },
        {
          label: "API Keys",
          icon: <KeyRound className="h-[18px] w-[18px]" />,
          href: "/admin/api-keys",
        },
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
        icon: <Puzzle className="h-[18px] w-[18px]" />,
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
          <div
            key={section.label}
            role="group"
            aria-labelledby={`admin-nav-${section.label.toLowerCase()}`}
          >
            <h3
              id={`admin-nav-${section.label.toLowerCase()}`}
              className="text-muted-foreground mb-2 px-2 text-[10px] font-semibold tracking-[0.18em] uppercase"
            >
              {section.label}
            </h3>
            <ul className="list-none space-y-0.5">
              {section.items.map((item) => {
                const active = isActive(item);
                const className = `relative flex items-center gap-2.5 rounded-xl px-3 py-2.5 text-[13px] font-medium transition-colors duration-150 ${
                  active
                    ? "text-primary bg-accent"
                    : "text-muted-foreground hover:text-foreground hover:bg-accent/70"
                } `;
                const inner = (
                  <>
                    {active && (
                      <span
                        className="absolute top-1/2 left-[-12px] h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                        style={{ background: "var(--primary)" }}
                      />
                    )}
                    <span className="flex w-[18px] flex-shrink-0 items-center justify-center">
                      {item.icon}
                    </span>
                    <span>{item.label}</span>
                    {item.badge && <span className="ml-auto">{item.badge}</span>}
                  </>
                );
                return (
                  <li key={item.href}>
                    {item.external ? (
                      <a
                        href={item.href}
                        onClick={(e) => {
                          e.preventDefault();
                          void navigateToPluginRoute(item.href);
                          onNavigate?.();
                        }}
                        aria-current={active ? "page" : undefined}
                        className={className}
                      >
                        {inner}
                      </a>
                    ) : (
                      <Link
                        to={item.href}
                        onClick={onNavigate}
                        aria-current={active ? "page" : undefined}
                        className={className}
                      >
                        {inner}
                      </Link>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
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
