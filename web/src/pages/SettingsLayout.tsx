import { useMemo, useState } from "react";
import { Link, Outlet, useLocation } from "react-router";
import {
  Play,
  Library,
  Clock,
  Cloud,
  Subtitles,
  LayoutDashboard,
  Palette,
  Eye,
  Wand2,
  Layers,
  Users,
  Server,
  Sparkles,
  Bell,
} from "lucide-react";
// Sparkles is used by the Personalization nav entry below.
import type { LucideIcon } from "lucide-react";
import PageBack from "@/components/PageBack";
import { SideNavItem, SideNavSection } from "@/components/SideNav";
import { SettingsSearchInput } from "@/components/settings/SettingsSearchInput";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { resolveSettingsDocumentTitle } from "@/lib/documentTitle";
import {
  countSettingsSearchItems,
  filterSettingsSearchGroups,
} from "@/components/settings/settingsSearch";
import { cn } from "@/lib/utils";

interface NavItem {
  path: string;
  label: string;
  icon: LucideIcon;
  description: string;
  keywords?: readonly string[];
  settings?: readonly { label: string; description?: string; keywords?: readonly string[] }[];
  primaryOrAdmin?: boolean;
}

interface NavSection {
  label: string;
  items: NavItem[];
}

const settingIndex = (...labels: string[]) => labels.map((label) => ({ label }));

const NAV_SECTIONS: NavSection[] = [
  {
    label: "Playback",
    items: [
      {
        path: "playback",
        label: "Playback",
        icon: Play,
        description: "Quality, language, and skipping",
        keywords: [
          "video quality",
          "spoken language",
          "metadata language",
          "auto skip",
          "auto play",
          "next up",
          "preview",
        ],
        settings: settingIndex(
          "Video quality",
          "Spoken language",
          "Metadata language",
          "Auto-skip intros",
          "Auto-skip credits",
          "Auto-skip recaps",
          "Start next at preview",
          "Auto-play next episode",
          "Next up episodes",
        ),
      },
      {
        path: "subtitle-appearance",
        label: "Subtitles",
        icon: Subtitles,
        description: "Language, behavior, and style",
        keywords: [
          "subtitle language",
          "forced subtitles",
          "captions",
          "font size",
          "font color",
          "background",
          "position",
        ],
        settings: settingIndex(
          "Subtitle language",
          "Subtitle behavior",
          "Show forced subtitles",
          "Preview",
          "Font size",
          "Font family",
          "Font color",
          "Text outline",
          "Outline color",
          "Background style",
          "Background opacity",
          "Background color",
          "Subtitle position",
        ),
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
        keywords: [
          "theme",
          "profile theme",
          "dark",
          "light",
          "custom theme",
          "date format",
          "time format",
          "clock",
          "24-hour",
          "12-hour",
        ],
        settings: settingIndex(
          "Theme",
          "Date & time",
          "Date format",
          "Time format",
          "Current selection",
          "Reset to Cinema Dark",
        ),
      },
      {
        path: "theme-editor",
        label: "Theme Editor",
        icon: Wand2,
        description: "Customize colors and CSS",
        keywords: ["design tokens", "token overrides", "custom css", "community themes"],
        settings: settingIndex("Preview", "Token Overrides", "Custom CSS", "Community Themes"),
      },
      {
        path: "accessibility",
        label: "Accessibility",
        icon: Eye,
        description: "Readability and contrast",
        keywords: ["contrast", "readability", "motion", "transparency", "text"],
        settings: settingIndex("Text size", "Text weight", "Contrast", "High Contrast", "Preview"),
      },
      {
        path: "home-screen",
        label: "Home Screen",
        icon: LayoutDashboard,
        description: "Sections and layout",
        keywords: ["sections", "rows", "continue watching", "next up", "library order"],
        settings: settingIndex(
          "Scope",
          "Sections",
          "Reset section customizations",
          "Continue Watching",
          "Next Up",
          "Recently Added",
          "Library order",
        ),
      },
      {
        path: "card-overlays",
        label: "Card Overlays",
        icon: Layers,
        description: "Badges on poster cards",
        keywords: ["poster", "badges", "overlay", "accent color", "preset"],
        settings: settingIndex(
          "Preview",
          "Preset",
          "Accent color",
          "Show icon",
          "Position",
          "How styling works",
        ),
      },
      {
        path: "personalize",
        label: "Personalize",
        icon: Sparkles,
        description: "Re-tune your taste profile",
        keywords: ["taste profile", "recommendations", "ratings", "likes", "dislikes"],
        settings: settingIndex("Refine your taste profile", "Taste profile", "Recommendations"),
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
        keywords: [
          "library visibility",
          "access",
          "disabled libraries",
          "library order",
          "playback preferences",
        ],
        settings: settingIndex(
          "Remember library pages",
          "Library visibility",
          "Library order",
          "Spoken language",
          "Subtitle language",
          "Subtitle behavior",
          "Forced subtitles",
          "Playback preferences",
        ),
      },
      {
        path: "history-import",
        label: "History Import",
        icon: Clock,
        description: "Emby watch history",
        keywords: ["emby", "watched history", "import", "mapping", "sync"],
        settings: settingIndex(
          "New import",
          "Import history",
          "Fetched",
          "Matched",
          "Unmatched",
          "Progress",
          "History",
          "Skipped",
        ),
      },
      {
        path: "webhook-sync",
        label: "Webhook Sync",
        icon: Server,
        description: "Plex, Emby, and Jellyfin webhook intake",
        keywords: ["plex", "emby", "jellyfin", "webhook", "progress", "watched"],
        settings: settingIndex(
          "Add a connection",
          "Connected servers",
          "Recent deliveries",
          "Plex",
          "Emby",
          "Jellyfin",
          "Server URL",
          "Token",
        ),
      },
      {
        path: "watch-providers",
        label: "Watch Providers",
        icon: Cloud,
        description: "Trakt watch history and scrobbling",
        keywords: ["trakt", "import", "export", "scrobble", "favorites", "watch history"],
        settings: settingIndex(
          "Last imported",
          "Last exported",
          "Watched",
          "Progress",
          "Favorites",
          "Exported",
          "Import watched history",
          "Import paused progress",
          "Send watched changes",
          "Send unwatched changes",
          "Sync favorites",
          "Sync favorite removals",
          "Scrobble playback",
        ),
      },
    ],
  },
  {
    label: "Account",
    items: [
      {
        path: "notifications",
        label: "Notifications",
        icon: Bell,
        description: "New-episode alerts and webhooks",
        keywords: ["new episodes", "email", "discord", "browser push", "webhooks"],
        settings: settingIndex(
          "New Episode Notifications",
          "Email Notifications",
          "Discord Notifications",
          "Browser Notifications",
          "Webhooks",
          "Per-episode alerts",
          "Digest",
          "Webhook URL",
        ),
      },
      {
        path: "profiles",
        label: "Profiles",
        icon: Users,
        description: "Names, PINs, and access rules",
        keywords: ["profile name", "pin", "access", "primary profile", "household"],
        settings: settingIndex(
          "Profile name",
          "PIN",
          "Library access",
          "Create profile",
          "Delete profile",
          "Primary profile",
        ),
        primaryOrAdmin: true,
      },
    ],
  },
];

export default function SettingsLayout() {
  const location = useLocation();
  const [settingsSearch, setSettingsSearch] = useState("");
  const { profile } = useCurrentProfile();
  const actingAdmin = useIsActingAdmin();
  const segments = location.pathname.split("/");
  const activeSegment = segments[2] || "playback";
  const canManageProfiles = actingAdmin || profile?.is_primary === true;

  const visibleSections = useMemo(
    () =>
      NAV_SECTIONS.map((section) => ({
        ...section,
        items: section.items.filter((item) => !item.primaryOrAdmin || canManageProfiles),
      })).filter((section) => section.items.length > 0),
    [canManageProfiles],
  );

  const flatItems = useMemo(
    () => visibleSections.flatMap((section) => section.items),
    [visibleSections],
  );
  const filteredSections = useMemo(
    () => filterSettingsSearchGroups(visibleSections, settingsSearch),
    [settingsSearch, visibleSections],
  );
  const filteredFlatItems = useMemo(
    () => filteredSections.flatMap((section) => section.items),
    [filteredSections],
  );
  const filteredSettingsCount = countSettingsSearchItems(filteredSections);

  useDocumentTitle(resolveSettingsDocumentTitle(location.pathname));

  return (
    <div className="min-h-[100dvh]">
      <main className="page-shell-wide relative flex min-h-[100dvh] flex-col py-4 sm:py-6">
        <PageBack to="/" preferHistory={false} floating />
        <div className="page-header mt-10 gap-5 sm:mt-12">
          <div className="min-w-0 space-y-3">
            <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Settings</h1>
            <p className="page-subtitle text-sm sm:text-base">
              Manage your playback preferences, libraries, and display options.
            </p>
          </div>
          <SettingsSearchInput
            value={settingsSearch}
            onChange={setSettingsSearch}
            resultCount={filteredSettingsCount}
            totalCount={flatItems.length}
            className="w-full sm:max-w-sm"
          />
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
            {filteredFlatItems.map((item) => {
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
            {filteredFlatItems.length === 0 ? (
              <p className="text-muted-foreground px-3 py-2.5 text-sm whitespace-nowrap">
                No matching settings
              </p>
            ) : null}
          </div>
        </nav>

        {/* Desktop: two-column with inline vertical sidebar */}
        <div className="mt-8 min-w-0 flex-1 lg:mt-10 lg:flex lg:gap-10">
          <aside className="hidden lg:block lg:w-[220px] lg:shrink-0">
            <nav aria-label="Settings sections" className="sticky top-6 space-y-5 pl-3">
              {filteredSections.map((section) => (
                <SideNavSection key={section.label} label={section.label} idPrefix="settings-nav">
                  {section.items.map((item) => (
                    <SideNavItem
                      key={item.path}
                      label={item.label}
                      icon={item.icon}
                      href={`/settings/${item.path}`}
                      active={item.path === activeSegment}
                    />
                  ))}
                </SideNavSection>
              ))}
              {filteredSections.length === 0 ? (
                <p className="text-muted-foreground px-2 text-sm">No matching settings</p>
              ) : null}
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
