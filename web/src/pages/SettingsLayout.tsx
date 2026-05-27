import { Link, Outlet, useLocation } from "react-router";
import {
  Play,
  Library,
  Clock,
  Subtitles,
  LayoutDashboard,
  Palette,
  Eye,
  Wand2,
  Layers,
  Users,
  Server,
  Sparkles,
} from "lucide-react";
// Sparkles is used by the Personalization nav entry below.
import type { LucideIcon } from "lucide-react";
import PageBack from "@/components/PageBack";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useAuth } from "@/hooks/useAuth";
import { resolveSettingsDocumentTitle } from "@/lib/documentTitle";
import { cn } from "@/lib/utils";

interface NavItem {
  path: string;
  label: string;
  icon: LucideIcon;
  description: string;
  primaryOrAdmin?: boolean;
}

interface NavSection {
  label: string;
  items: NavItem[];
}

const NAV_SECTIONS: NavSection[] = [
  {
    label: "Playback",
    items: [
      {
        path: "playback",
        label: "Playback",
        icon: Play,
        description: "Quality, language, and skipping",
      },
      {
        path: "subtitle-appearance",
        label: "Subtitles",
        icon: Subtitles,
        description: "Language, behavior, and style",
      },
    ],
  },
  {
    label: "Appearance",
    items: [
      {
        path: "appearance",
        label: "Appearance",
        icon: Palette,
        description: "Theme and interface tone",
      },
      {
        path: "theme-editor",
        label: "Theme Editor",
        icon: Wand2,
        description: "Customize colors and CSS",
      },
      {
        path: "accessibility",
        label: "Accessibility",
        icon: Eye,
        description: "Readability and contrast",
      },
      {
        path: "home-screen",
        label: "Home Screen",
        icon: LayoutDashboard,
        description: "Sections and layout",
      },
      {
        path: "card-overlays",
        label: "Card Overlays",
        icon: Layers,
        description: "Badges on poster cards",
      },
      {
        path: "personalize",
        label: "Personalize",
        icon: Sparkles,
        description: "Re-tune your taste profile",
      },
    ],
  },
  {
    label: "Library & Data",
    items: [
      {
        path: "libraries",
        label: "Libraries",
        icon: Library,
        description: "Visibility and access",
      },
      {
        path: "history-import",
        label: "History Import",
        icon: Clock,
        description: "Emby watch history",
      },
      {
        path: "webhook-sync",
        label: "Webhook Sync",
        icon: Server,
        description: "Plex, Emby, and Jellyfin webhook intake",
      },
      {
        path: "watch-providers",
        label: "Watch Providers",
        icon: Clock,
        description: "Trakt watch history and scrobbling",
      },
    ],
  },
  {
    label: "Account",
    items: [
      {
        path: "profiles",
        label: "Profiles",
        icon: Users,
        description: "Names, PINs, and access rules",
        primaryOrAdmin: true,
      },
    ],
  },
];

export default function SettingsLayout() {
  const location = useLocation();
  const { user, profile } = useAuth();
  const segments = location.pathname.split("/");
  const activeSegment = segments[2] || "playback";
  const isAdmin = user?.role === "admin";
  const canManageProfiles = isAdmin || profile?.is_primary === true;

  const visibleSections = NAV_SECTIONS.map((section) => ({
    ...section,
    items: section.items.filter((item) => !item.primaryOrAdmin || canManageProfiles),
  })).filter((section) => section.items.length > 0);

  const flatItems = visibleSections.flatMap((section) => section.items);

  useDocumentTitle(resolveSettingsDocumentTitle(location.pathname));

  return (
    <div className="min-h-[100dvh]">
      <main className="page-shell-wide relative flex min-h-[100dvh] flex-col py-4 sm:py-6">
        <PageBack />
        <div className="page-header gap-5">
          <div className="min-w-0 space-y-3">
            <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Settings</h1>
            <p className="page-subtitle text-sm sm:text-base">
              Manage your playback preferences, libraries, and display options.
            </p>
          </div>
        </div>

        {/* Mobile: horizontal scrolling tab bar */}
        <nav
          aria-label="Settings sections"
          className="surface-panel-subtle mt-6 overflow-x-auto rounded-[1.4rem] p-1 lg:hidden"
          style={{
            WebkitOverflowScrolling: "touch",
            maskImage:
              "linear-gradient(to right, transparent, black 40px, black calc(100% - 40px), transparent)",
            WebkitMaskImage:
              "linear-gradient(to right, transparent, black 40px, black calc(100% - 40px), transparent)",
          }}
        >
          <div className="flex min-w-max items-stretch gap-1">
            {flatItems.map((item) => {
              const isActive = item.path === activeSegment;
              const Icon = item.icon;
              return (
                <Link
                  key={item.path}
                  to={`/settings/${item.path}`}
                  aria-current={isActive ? "page" : undefined}
                  className={cn(
                    "inline-flex items-center gap-2 rounded-[1rem] px-3 py-2.5 text-sm font-medium whitespace-nowrap transition-colors",
                    isActive
                      ? "bg-background text-foreground shadow-sm"
                      : "text-muted-foreground hover:bg-background/70 hover:text-foreground",
                  )}
                >
                  <Icon className="h-4 w-4" />
                  {item.label}
                </Link>
              );
            })}
          </div>
        </nav>

        {/* Desktop: two-column with inline vertical sidebar */}
        <div className="mt-8 min-w-0 flex-1 lg:mt-10 lg:flex lg:gap-10">
          <aside className="hidden lg:block lg:w-[220px] lg:shrink-0">
            <nav aria-label="Settings sections" className="sticky top-6 space-y-5 pl-3">
              {visibleSections.map((section) => {
                const sectionId = `settings-nav-${section.label.toLowerCase().replace(/\s+/g, "-")}`;
                return (
                  <div key={section.label} role="group" aria-labelledby={sectionId}>
                    <h3
                      id={sectionId}
                      className="text-muted-foreground mb-2 px-2 text-[10px] font-semibold tracking-[0.18em] uppercase"
                    >
                      {section.label}
                    </h3>
                    <ul className="list-none space-y-0.5">
                      {section.items.map((item) => {
                        const isActive = item.path === activeSegment;
                        const Icon = item.icon;
                        return (
                          <li key={item.path}>
                            <Link
                              to={`/settings/${item.path}`}
                              aria-current={isActive ? "page" : undefined}
                              className={cn(
                                "relative flex items-center gap-2.5 rounded-xl px-3 py-2.5 text-[13px] font-medium transition-colors duration-150",
                                isActive
                                  ? "text-primary bg-accent"
                                  : "text-muted-foreground hover:text-foreground hover:bg-accent/70",
                              )}
                            >
                              {isActive && (
                                <span
                                  className="absolute top-1/2 left-[-12px] h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
                                  style={{ background: "var(--primary)" }}
                                />
                              )}
                              <Icon className="h-[18px] w-[18px] flex-shrink-0" />
                              <span>{item.label}</span>
                            </Link>
                          </li>
                        );
                      })}
                    </ul>
                  </div>
                );
              })}
            </nav>
          </aside>

          <div className="min-w-0 flex-1 pt-8 lg:pt-0">
            <div className="mx-auto w-full max-w-3xl">
              <Outlet />
            </div>
          </div>
        </div>
      </main>
    </div>
  );
}
