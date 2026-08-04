import { useEffect, useMemo, useRef, useState, type ComponentType } from "react";
import { AlertTriangle, ChevronLeft } from "lucide-react";
import { Link, useSearchParams } from "react-router";

import { SideNavItem, SideNavSection } from "@/components/SideNav";
import { SettingsOverviewNav } from "@/components/settings/SettingsOverviewNav";
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

const SHELL_HEADING_SETTINGS = new Set(["branding", "theming"]);

export default function AdminSettingsLayout() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [settingsSearch, setSettingsSearch] = useState("");
  const activeContentRef = useRef<HTMLDivElement>(null);
  const activeHeadingRef = useRef<HTMLHeadingElement>(null);
  const { data: serverStatus } = useAdminServerStatus();
  const rawActiveId = searchParams.get("tab");
  const activeId = rawActiveId === "jellyfin" ? "compatibility-proxies" : rawActiveId;
  const filteredSettingsGroups = useMemo(
    () => filterSettingsSearchGroups(SETTINGS_GROUPS, settingsSearch),
    [settingsSearch],
  );
  const overviewGroups = useMemo(
    () =>
      filteredSettingsGroups.map((group) => ({
        ...group,
        items: group.items.map((item) => ({
          id: item.id,
          label: item.label,
          description: item.description,
          icon: item.icon,
          href: `/admin/settings?tab=${encodeURIComponent(item.id)}`,
        })),
      })),
    [filteredSettingsGroups],
  );
  const filteredSettingsCount = countSettingsSearchItems(filteredSettingsGroups);

  function setActiveId(id: string) {
    setSearchParams({ tab: id }, { replace: true });
  }
  const active = activeId ? SETTINGS_NAV.find((item) => item.id === activeId) : undefined;
  const ActiveComponent = active?.component;

  useEffect(() => {
    if (!active) return;

    window.scrollTo(0, 0);
    if (activeContentRef.current) {
      activeContentRef.current.scrollTop = 0;
    }
    (activeHeadingRef.current ?? activeContentRef.current)?.focus({ preventScroll: true });
  }, [active]);

  return (
    <div className="space-y-6">
      {active ? (
        <Link
          to="/admin/settings"
          className="text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex w-fit items-center gap-1.5 rounded-lg pr-2 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none lg:hidden"
        >
          <ChevronLeft className="h-4 w-4" aria-hidden="true" />
          All settings
        </Link>
      ) : null}

      <div className={cn("page-header gap-5", active && "hidden lg:flex")}>
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
          shortcutMediaQuery={active ? "(min-width: 64rem)" : undefined}
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

      {active && ActiveComponent ? (
        <div className="surface-panel flex min-h-[500px] flex-col overflow-hidden rounded-[1.8rem] border-0 lg:flex-row">
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

          <div
            ref={activeContentRef}
            role="region"
            aria-label={`${active.label} settings`}
            tabIndex={-1}
            className="flex-1 space-y-6 overflow-y-auto p-4 focus:outline-none sm:p-6"
          >
            {SHELL_HEADING_SETTINGS.has(active.id) ? (
              <h2
                ref={activeHeadingRef}
                tabIndex={-1}
                className="text-2xl font-semibold tracking-tight focus:outline-none sm:text-3xl lg:sr-only"
              >
                {active.label}
              </h2>
            ) : null}
            <ActiveComponent />
          </div>
        </div>
      ) : (
        <div className="mx-auto w-full max-w-6xl">
          <SettingsOverviewNav
            groups={overviewGroups}
            ariaLabel="Admin settings sections"
            idPrefix="admin-settings-index"
          />
        </div>
      )}
    </div>
  );
}
