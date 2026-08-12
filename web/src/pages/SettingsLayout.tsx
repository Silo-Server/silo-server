import { useMemo, useState } from "react";
import { Link, Outlet, useLocation } from "react-router";
import {
  ChevronLeft,
  ChevronRight,
  Play,
  Library,
  Cast,
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
  MonitorSmartphone,
  PanelTop,
} from "lucide-react";
// Sparkles is used by the Personalization nav entry below.
import type { LucideIcon } from "lucide-react";
import PageBack from "@/components/PageBack";
import { SideNavItem, SideNavSection } from "@/components/SideNav";
import { SettingsOverviewNav } from "@/components/settings/SettingsOverviewNav";
import { SettingsSearchInput } from "@/components/settings/SettingsSearchInput";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
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

/**
 * Settings pages that manage their own multi-column layout, so the shell's
 * reading-width cap would squeeze them instead of helping.
 */
const WIDE_SETTINGS_PAGES = new Set(["devices"]);

/**
 * Grouped by the question a person arrives with, not by which service stores
 * the value: "how does it play", "how does it look", "what do I see", "what is
 * it wired to", "who am I". Devices sits under Playback because every setting
 * on that screen is a playback override, and Personalize sits with Home Screen
 * because both shape what the app puts in front of you.
 */
const NAV_SECTIONS: NavSection[] = [
  {
    label: "Playback",
    items: [
      {
        path: "playback",
        label: "Playback",
        icon: Play,
        description: "Quality, languages, skipping, and what plays next.",
        keywords: [
          "video quality",
          "bitrate",
          "bandwidth",
          "spoken language",
          "metadata language",
          "auto skip",
          "auto play",
          "next up",
          "preview",
        ],
        settings: settingIndex(
          "Preferred quality",
          "Maximum bitrate",
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
        description: "Subtitle language, when they appear, and how they look.",
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
      {
        path: "devices",
        label: "Your Devices",
        icon: MonitorSmartphone,
        description: "Per-device quality, HDR, and audio or subtitle sync.",
        keywords: [
          "devices",
          "tv",
          "phone",
          "tablet",
          "browser",
          "this device",
          "forget device",
          "hdr",
          "dolby vision",
          "sound delay",
          "lip sync",
        ],
        settings: settingIndex(
          "Preferred quality",
          "Maximum bitrate",
          "HDR",
          "Dolby Vision",
          "Play Dolby Vision films as HDR10",
          "Match content frame rate",
          "How video fills the screen",
          "Audio sync offset",
          "Subtitle sync offset",
          "Forget this device",
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
        description: "Theme, interface tone, and date and time formats.",
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
        path: "interface",
        label: "Navigation & Cards",
        icon: PanelTop,
        description: "Your primary menu, poster size, and card captions.",
        keywords: [
          "navigation",
          "menu",
          "pin library",
          "poster size",
          "card size",
          "hide title",
          "hide year",
          "artwork only",
          "preset",
        ],
        settings: settingIndex(
          "Card preset",
          "Poster size",
          "Caption",
          "Title & metadata",
          "Title only",
          "Artwork only",
          "Primary menu",
          "Choose destination or shortcut",
          "Add to menu",
          "Reset to default",
        ),
      },
      {
        path: "card-overlays",
        label: "Card Overlays",
        icon: Layers,
        description: "Badges drawn on poster cards, and where they sit.",
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
        path: "accessibility",
        label: "Accessibility",
        icon: Eye,
        description: "Text size, weight, and contrast for easier reading.",
        keywords: ["contrast", "readability", "motion", "transparency", "text"],
        settings: settingIndex("Text size", "Text weight", "Contrast", "High Contrast", "Preview"),
      },
      {
        path: "theme-editor",
        label: "Theme Editor",
        icon: Wand2,
        description: "Fine-tune theme colors and add your own CSS.",
        keywords: ["design tokens", "token overrides", "custom css", "community themes"],
        settings: settingIndex("Preview", "Token Overrides", "Custom CSS", "Community Themes"),
      },
    ],
  },
  {
    label: "Home & Discovery",
    items: [
      {
        path: "home-screen",
        label: "Home Screen",
        icon: LayoutDashboard,
        description: "Which rows appear on Home, and in what order.",
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
        path: "personalize",
        label: "Personalize",
        icon: Sparkles,
        description: "Re-tune the taste profile behind your recommendations.",
        keywords: ["taste profile", "recommendations", "ratings", "likes", "dislikes"],
        settings: settingIndex("Refine your taste profile", "Taste profile", "Recommendations"),
      },
      {
        path: "libraries",
        label: "Libraries",
        icon: Library,
        description: "Which libraries you see, their order, and per-library audio.",
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
    ],
  },
  {
    label: "Connections",
    items: [
      {
        path: "connect-apps",
        label: "Connect Apps",
        icon: Cast,
        description: "Sign-in details for Silo and Jellyfin-compatible apps.",
        keywords: [
          "jellyfin",
          "infuse",
          "swiftfin",
          "jellycon",
          "findroid",
          "compatibility",
          "sign in",
          "login",
          "server address",
          "username",
          "pin",
        ],
        settings: settingIndex(
          "Silo app or website",
          "Jellyfin-compatible app",
          "Server",
          "Username",
          "Password",
          "Which profile are you signing in as?",
          "A Jellyfin app says my username or password is wrong",
          "Every profile at a glance",
        ),
      },
      {
        path: "watch-providers",
        label: "Watch Providers",
        icon: Cloud,
        description: "Trakt watch history, favorites, and scrobbling.",
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
      {
        path: "webhook-sync",
        label: "Webhook Sync",
        icon: Server,
        description: "Take progress from Plex, Emby, and Jellyfin webhooks.",
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
        path: "history-import",
        label: "History Import",
        icon: Clock,
        description: "Bring an existing Emby watch history into Silo.",
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
    ],
  },
  {
    label: "Account",
    items: [
      {
        path: "profiles",
        label: "Profiles",
        icon: Users,
        description: "Household profile names, PINs, and library access.",
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
      {
        path: "notifications",
        label: "Notifications",
        icon: Bell,
        description: "New-episode alerts by email, Discord, push, or webhook.",
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
    ],
  },
];

interface SettingsOverviewProps {
  sections: readonly { label: string; items: readonly NavItem[] }[];
  profile: { name: string; avatar_url?: string } | null;
}

/**
 * The index is the same directory the admin settings index uses: category jump
 * links, then one uniform card grid per group across the full page width.
 *
 * The two-column list it replaced paired groups side by side, so a short group
 * next to a long one left a column of dead space taller than the short group
 * itself — the exact shape of the desktop complaint.
 */
function SettingsOverview({ sections, profile }: SettingsOverviewProps) {
  const profileName = profile?.name ?? "Your profile";

  return (
    <div className="w-full space-y-6">
      <Link
        to="/profiles"
        aria-label={`Current profile: ${profileName}`}
        className="surface-panel-subtle hover:bg-surface-hover/70 focus-visible:ring-ring flex min-h-16 items-center gap-3 rounded-2xl px-4 py-3 transition-colors focus-visible:ring-2 focus-visible:outline-none sm:max-w-md"
      >
        <Avatar className="border-border h-10 w-10 border">
          {profile?.avatar_url ? <AvatarImage src={profile.avatar_url} alt="" /> : null}
          <AvatarFallback className="bg-accent text-foreground font-semibold">
            {profileName.charAt(0).toUpperCase()}
          </AvatarFallback>
        </Avatar>
        <div className="min-w-0 flex-1">
          <p className="text-muted-foreground text-xs">Current profile</p>
          <p className="truncate text-sm font-semibold">{profileName}</p>
        </div>
        <ChevronRight className="text-muted-foreground h-4 w-4 shrink-0" aria-hidden="true" />
      </Link>

      <SettingsOverviewNav
        groups={sections.map((section) => ({
          ...section,
          items: section.items.map((item) => ({
            id: item.path,
            label: item.label,
            description: item.description,
            icon: item.icon,
            href: `/settings/${item.path}`,
          })),
        }))}
        ariaLabel="Settings sections"
        idPrefix="settings-index"
        variant="directory"
      />
    </div>
  );
}

export default function SettingsLayout() {
  const location = useLocation();
  const [settingsSearch, setSettingsSearch] = useState("");
  const { profile } = useCurrentProfile();
  const actingAdmin = useIsActingAdmin();
  const segments = location.pathname.split("/");
  const activeSegment = segments[2] || null;
  const canManageProfiles = actingAdmin || profile?.is_primary === true;
  // Most settings pages are a single column of rows and read best measured.
  // A page that is itself two panes needs the room, so it opts out.
  const wideSetting = activeSegment ? WIDE_SETTINGS_PAGES.has(activeSegment) : false;

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
  const filteredSettingsCount = countSettingsSearchItems(filteredSections);

  useDocumentTitle(resolveSettingsDocumentTitle(location.pathname));

  return (
    <div className="min-h-[100dvh]">
      <main className="page-shell-wide relative flex min-h-[100dvh] flex-col py-4 sm:py-6">
        {activeSegment ? (
          <>
            <div className="hidden lg:block">
              <PageBack to="/" preferHistory={false} floating />
            </div>
            <Link
              to="/settings"
              className="text-muted-foreground hover:text-foreground focus-visible:ring-ring mt-1 inline-flex w-fit items-center gap-1.5 rounded-lg pr-2 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none lg:hidden"
            >
              <ChevronLeft className="h-4 w-4" aria-hidden="true" />
              All settings
            </Link>
            <div className="page-header mt-10 hidden gap-5 sm:mt-12 lg:flex">
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
                shortcutMediaQuery={activeSegment ? "(min-width: 64rem)" : undefined}
              />
            </div>

            {/* From lg up, the rail and the active page share one panel, the
                same shell the admin settings use. Below lg the rail is hidden
                and the page is already a stack of its own panels, so the extra
                chrome would only nest a card inside a card — mobile keeps the
                bare layout.

                Admin scrolls its detail pane inside the panel; here the page
                scrolls and the rail sticks, because Your Devices owns a
                viewport-height scroller that a nested one would strand. */}
            <div className="surface-panel-lg mt-5 flex min-w-0 flex-1 flex-col lg:mt-10 lg:min-h-[500px] lg:flex-row lg:overflow-hidden">
              <aside className="border-border hidden lg:block lg:w-60 lg:flex-shrink-0 lg:border-r">
                <nav
                  aria-label="Settings sections"
                  className="sticky top-6 space-y-5 py-5 pr-3 pl-5"
                >
                  {filteredSections.map((section) => (
                    <SideNavSection
                      key={section.label}
                      label={section.label}
                      idPrefix="settings-nav"
                    >
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

              <div className="min-w-0 flex-1 p-4 sm:p-6">
                <div className={cn("w-full", wideSetting ? "max-w-none" : "max-w-3xl")}>
                  <Outlet />
                </div>
              </div>
            </div>
          </>
        ) : (
          <>
            <PageBack to="/" preferHistory={false} floating />
            <div className="page-header mt-10 mb-6 gap-5 sm:mt-12 sm:mb-8">
              <div className="min-w-0 space-y-3">
                <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Settings</h1>
                <p className="page-subtitle text-sm sm:text-base">
                  Make Silo work the way you like.
                </p>
              </div>
              <SettingsSearchInput
                value={settingsSearch}
                onChange={setSettingsSearch}
                resultCount={filteredSettingsCount}
                totalCount={flatItems.length}
                className="w-full sm:max-w-sm lg:w-[26rem] lg:max-w-none"
                showShortcutHint
              />
            </div>
            <SettingsOverview sections={filteredSections} profile={profile} />
          </>
        )}
      </main>
    </div>
  );
}
