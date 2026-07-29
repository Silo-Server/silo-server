import { useMemo, useState, type ComponentType } from "react";
import { AlertTriangle } from "lucide-react";
import { useSearchParams } from "react-router";

import { SideNavItem, SideNavSection } from "@/components/SideNav";
import { SettingsSearchInput } from "@/components/settings/SettingsSearchInput";
import {
  countSettingsSearchItems,
  filterSettingsSearchGroups,
} from "@/components/settings/settingsSearch";
import {
  ADMIN_SETTINGS_GROUPS,
  ADMIN_SETTINGS_NAV,
  type AdminSettingsSearchItem,
} from "@/lib/adminSettingsSearch";
import { cn } from "@/lib/utils";
import { useAdminServerStatus } from "@/hooks/queries/admin/settings";

import EmailSettings from "./EmailSettings";
import NotificationsAdminSettings from "./NotificationsAdminSettings";
import GeneralSettings from "./GeneralSettings";
import PlaybackSettings from "./PlaybackSettings";
import ScannerSettings from "./ScannerSettings";
import SearchSettings from "./SearchSettings";
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
import BrandingSettings from "./BrandingSettings";
import OverlaySettings from "./OverlaySettings";
import { RestartServerButton } from "./RestartServerButton";

interface SettingsNav extends AdminSettingsSearchItem {
  component: ComponentType;
}

interface SettingsNavGroup {
  label: string;
  items: SettingsNav[];
}

const SETTINGS_COMPONENTS: Record<string, ComponentType> = {
  general: GeneralSettings,
  branding: BrandingSettings,
  theming: ThemeSettings,
  overlays: OverlaySettings,
  scanner: ScannerSettings,
  search: SearchSettings,
  intro: IntroSettings,
  subtitles: SubtitlesSettings,
  ai: AIServicesSettings,
  playback: PlaybackSettings,
  downloads: DownloadSettings,
  "watch-providers": WatchProvidersSettings,
  integrations: IntegrationsSettings,
  email: EmailSettings,
  notifications: NotificationsAdminSettings,
  "compatibility-proxies": CompatibilityProxiesSettings,
  "rate-limiting": RateLimitSettings,
  database: DatabaseSettings,
  storage: StorageSettings,
  "log-retention": LogRetentionSettings,
};

function settingsComponent(id: string) {
  const component = SETTINGS_COMPONENTS[id];
  if (!component) {
    throw new Error(`Missing admin settings component for ${id}`);
  }
  return component;
}

const SETTINGS_GROUPS: SettingsNavGroup[] = ADMIN_SETTINGS_GROUPS.map((group) => ({
  ...group,
  items: group.items.map((item) => ({ ...item, component: settingsComponent(item.id) })),
}));

const SETTINGS_NAV: SettingsNav[] = ADMIN_SETTINGS_NAV.map((item) => ({
  ...item,
  component: settingsComponent(item.id),
}));

export default function AdminSettingsLayout() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [settingsSearch, setSettingsSearch] = useState("");
  const { data: serverStatus } = useAdminServerStatus();
  const rawActiveId = searchParams.get("tab") || "general";
  const activeId = rawActiveId === "jellyfin" ? "compatibility-proxies" : rawActiveId;
  const filteredSettingsGroups = useMemo(
    () => filterSettingsSearchGroups(SETTINGS_GROUPS, settingsSearch),
    [settingsSearch],
  );
  const filteredSettingsNav = useMemo(
    () => filteredSettingsGroups.flatMap((group) => group.items),
    [filteredSettingsGroups],
  );
  const filteredSettingsCount = countSettingsSearchItems(filteredSettingsGroups);

  function setActiveId(id: string) {
    setSearchParams({ tab: id }, { replace: true });
  }
  const active = SETTINGS_NAV.find((n) => n.id === activeId) ?? SETTINGS_NAV[0]!;
  const ActiveComponent = active.component;

  return (
    <div className="space-y-6">
      <div className="page-header gap-5">
        <div className="min-w-0 space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Settings</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Configure server-wide settings. Most changes apply live; startup-bound fields show a
            restart warning after they are saved.
          </p>
        </div>
        <SettingsSearchInput
          value={settingsSearch}
          onChange={setSettingsSearch}
          resultCount={filteredSettingsCount}
          totalCount={SETTINGS_NAV.length}
          className="w-full sm:max-w-sm"
        />
      </div>

      {serverStatus?.restart_required && (
        <div
          role="status"
          className="surface-panel-subtle flex flex-col gap-3 rounded-xl p-4 sm:flex-row sm:items-center sm:justify-between"
        >
          <div className="text-foreground/80 flex items-center gap-2 text-sm">
            <AlertTriangle className="h-4 w-4" />
            <span>Server restart required for saved settings to take effect.</span>
          </div>
          <RestartServerButton />
        </div>
      )}

      <div className="surface-panel flex min-h-[500px] flex-col overflow-hidden rounded-[1.8rem] border-0 lg:flex-row">
        {/* Mobile: horizontal scrolling pill bar */}
        <nav
          aria-label="Admin settings sections"
          className="border-border overflow-x-auto border-b p-2 lg:hidden"
          style={{ WebkitOverflowScrolling: "touch" }}
        >
          <div className="flex min-w-max items-stretch gap-1">
            {filteredSettingsNav.map((item) => {
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
            {filteredSettingsNav.length === 0 ? (
              <p className="text-muted-foreground px-3 py-2.5 text-sm whitespace-nowrap">
                No matching settings
              </p>
            ) : null}
          </div>
        </nav>

        {/* Desktop: grouped vertical rail */}
        <nav
          aria-label="Admin settings sections"
          className="border-border hidden space-y-5 border-r px-3 py-4 lg:block lg:w-60 lg:flex-shrink-0"
        >
          {filteredSettingsGroups.map((group) => (
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
          {filteredSettingsGroups.length === 0 ? (
            <p className="text-muted-foreground px-2 text-sm">No matching settings</p>
          ) : null}
        </nav>

        {/* Content area */}
        <div className="flex-1 overflow-y-auto p-4 sm:p-6">
          <ActiveComponent />
        </div>
      </div>
    </div>
  );
}
