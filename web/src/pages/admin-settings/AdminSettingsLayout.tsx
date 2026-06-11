import { useSearchParams } from "react-router";
import {
  Settings2,
  Captions,
  Cloud,
  PlayCircle,
  ScanSearch,
  Gauge,
  Download,
  Puzzle,
  Network,
  Database,
  HardDrive,
  ScrollText,
  Paintbrush,
  Layers,
  Subtitles,
  Sparkles,
  Mail,
  Bell,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { SideNavItem, SideNavSection } from "@/components/SideNav";
import { cn } from "@/lib/utils";

import EmailSettings from "./EmailSettings";
import NotificationsAdminSettings from "./NotificationsAdminSettings";
import GeneralSettings from "./GeneralSettings";
import PlaybackSettings from "./PlaybackSettings";
import ScannerSettings from "./ScannerSettings";
import IntroSettings from "./IntroSettings";
import SubtitlesSettings from "./SubtitlesSettings";
import AIServicesSettings from "./AIServicesSettings";
import RateLimitSettings from "./RateLimitSettings";
import WatchProvidersSettings from "./WatchProvidersSettings";
import IntegrationsSettings from "./IntegrationsSettings";
import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";
import DatabaseSettings from "./DatabaseSettings";
import StorageSettings from "./StorageSettings";
import DownloadSettings from "./DownloadSettings";
import LogRetentionSettings from "./LogRetentionSettings";
import ThemeSettings from "./ThemeSettings";
import OverlaySettings from "./OverlaySettings";

interface SettingsNav {
  id: string;
  label: string;
  icon: LucideIcon;
  component: React.ComponentType;
}

interface SettingsNavGroup {
  label: string;
  items: SettingsNav[];
}

// Tab ids are stable URL state (?tab=...) — regroup or reorder freely, but
// renaming an id breaks bookmarks and deep links.
const SETTINGS_GROUPS: SettingsNavGroup[] = [
  {
    label: "Server",
    items: [
      { id: "general", label: "General", icon: Settings2, component: GeneralSettings },
      { id: "theming", label: "Theming", icon: Paintbrush, component: ThemeSettings },
      { id: "overlays", label: "Card Overlays", icon: Layers, component: OverlaySettings },
    ],
  },
  {
    label: "Media",
    items: [
      { id: "scanner", label: "Scanner & Matcher", icon: ScanSearch, component: ScannerSettings },
      { id: "intro", label: "Intro Markers", icon: Captions, component: IntroSettings },
      { id: "subtitles", label: "Subtitles", icon: Subtitles, component: SubtitlesSettings },
      { id: "ai", label: "AI Services", icon: Sparkles, component: AIServicesSettings },
      { id: "playback", label: "Playback", icon: PlayCircle, component: PlaybackSettings },
      { id: "downloads", label: "Downloads", icon: Download, component: DownloadSettings },
    ],
  },
  {
    label: "Connections",
    items: [
      {
        id: "watch-providers",
        label: "Watch Providers",
        icon: Cloud,
        component: WatchProvidersSettings,
      },
      { id: "integrations", label: "Integrations", icon: Puzzle, component: IntegrationsSettings },
      { id: "email", label: "Email", icon: Mail, component: EmailSettings },
      {
        id: "notifications",
        label: "Notifications",
        icon: Bell,
        component: NotificationsAdminSettings,
      },
      {
        id: "compatibility-proxies",
        label: "Compatibility Proxies",
        icon: Network,
        component: CompatibilityProxiesSettings,
      },
      { id: "rate-limiting", label: "Rate Limiting", icon: Gauge, component: RateLimitSettings },
    ],
  },
  {
    label: "Data",
    items: [
      { id: "database", label: "Database", icon: Database, component: DatabaseSettings },
      { id: "storage", label: "Storage", icon: HardDrive, component: StorageSettings },
      {
        id: "log-retention",
        label: "Log Retention",
        icon: ScrollText,
        component: LogRetentionSettings,
      },
    ],
  },
];

const SETTINGS_NAV: SettingsNav[] = SETTINGS_GROUPS.flatMap((group) => group.items);

export default function AdminSettingsLayout() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawActiveId = searchParams.get("tab") || "general";
  const activeId = rawActiveId === "jellyfin" ? "compatibility-proxies" : rawActiveId;

  function setActiveId(id: string) {
    setSearchParams({ tab: id }, { replace: true });
  }
  const active = SETTINGS_NAV.find((n) => n.id === activeId) ?? SETTINGS_NAV[0]!;
  const ActiveComponent = active.component;

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Settings</h1>
        <p className="page-subtitle text-sm sm:text-base">
          Configure server-wide settings. Most changes require a server restart to take effect.
        </p>
      </div>

      <div className="surface-panel flex min-h-[500px] flex-col overflow-hidden rounded-[1.8rem] border-0 lg:flex-row">
        {/* Mobile: horizontal scrolling pill bar */}
        <nav
          aria-label="Admin settings sections"
          className="border-border overflow-x-auto border-b p-2 lg:hidden"
          style={{ WebkitOverflowScrolling: "touch" }}
        >
          <div className="flex min-w-max items-stretch gap-1">
            {SETTINGS_NAV.map((item) => {
              const isActive = item.id === active.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => setActiveId(item.id)}
                  aria-current={isActive ? "page" : undefined}
                  className={cn(
                    "inline-flex items-center gap-2 rounded-[1rem] px-3 py-2.5 text-[13px] font-medium whitespace-nowrap transition-colors",
                    isActive
                      ? "bg-background text-foreground shadow-sm"
                      : "text-muted-foreground hover:bg-background/70 hover:text-foreground",
                  )}
                >
                  <item.icon className="h-4 w-4" />
                  {item.label}
                </button>
              );
            })}
          </div>
        </nav>

        {/* Desktop: grouped vertical rail */}
        <nav
          aria-label="Admin settings sections"
          className="border-border hidden space-y-5 border-r px-3 py-4 lg:block lg:w-60 lg:flex-shrink-0"
        >
          {SETTINGS_GROUPS.map((group) => (
            <SideNavSection key={group.label} label={group.label} idPrefix="admin-settings-nav">
              {group.items.map((item) => (
                <SideNavItem
                  key={item.id}
                  label={item.label}
                  icon={item.icon}
                  active={item.id === active.id}
                  onClick={() => setActiveId(item.id)}
                />
              ))}
            </SideNavSection>
          ))}
        </nav>

        {/* Content area */}
        <div className="flex-1 overflow-y-auto p-4 sm:p-6">
          <ActiveComponent />
        </div>
      </div>
    </div>
  );
}
