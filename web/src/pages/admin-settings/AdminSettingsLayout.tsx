import { useEffect, useMemo, useRef, useState, type ComponentType } from "react";
import { ChevronLeft } from "lucide-react";
import { Link, Navigate, useParams, useSearchParams } from "react-router";

import {
  ADMIN_SETTINGS_GROUPS,
  ADMIN_SETTINGS_NAV,
  resolveAdminSettingsPageID,
  type AdminSettingsSearchItem,
} from "@/lib/adminSettingsSearch";
import {
  countSettingsSearchItems,
  filterSettingsSearchGroups,
} from "@/components/settings/settingsSearch";
import { SettingsSearchInput } from "@/components/settings/SettingsSearchInput";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { settingsPageHref } from "@/hooks/admin/useSettingsOverview";
import { SettingsPageRail } from "@/components/settings/SettingsPageRail";

import GeneralSettings from "./GeneralSettings";
import AppearanceSettings from "./AppearanceSettings";
import SecurityAccessSettings from "./SecurityAccessSettings";
import LibraryMetadataSettings from "./LibraryMetadataSettings";
import PlaybackSettings from "./PlaybackSettings";
import DownloadsSettings from "./DownloadsSettings";
import ProvidersSettings from "./ProvidersSettings";
import WatchSyncSettings from "./WatchSyncSettings";
import AISettings from "./AISettings";
import NotificationsAdminSettings from "./NotificationsAdminSettings";
import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";
import InfrastructureSettings from "./InfrastructureSettings";
import SettingsOverview from "./SettingsOverview";
import "@/styles/admin-settings.css";

interface SettingsNav extends AdminSettingsSearchItem {
  component: ComponentType;
}

const SETTINGS_COMPONENTS: Record<string, ComponentType> = {
  general: GeneralSettings,
  appearance: AppearanceSettings,
  security: SecurityAccessSettings,
  library: LibraryMetadataSettings,
  playback: PlaybackSettings,
  downloads: DownloadsSettings,
  providers: ProvidersSettings,
  "watch-sync": WatchSyncSettings,
  ai: AISettings,
  notifications: NotificationsAdminSettings,
  compatibility: CompatibilityProxiesSettings,
  infrastructure: InfrastructureSettings,
};

function settingsComponent(id: string) {
  const component = SETTINGS_COMPONENTS[id];
  if (!component) {
    throw new Error(`Missing admin settings component for ${id}`);
  }
  return component;
}

const SETTINGS_NAV: SettingsNav[] = ADMIN_SETTINGS_NAV.map((item) => ({
  ...item,
  component: settingsComponent(item.id),
}));

export default function AdminSettingsLayout() {
  const params = useParams();
  const [searchParams] = useSearchParams();
  const activeContentRef = useRef<HTMLDivElement>(null);
  const [settingsSearch, setSettingsSearch] = useState("");
  const filteredGroups = useMemo(
    () => filterSettingsSearchGroups(ADMIN_SETTINGS_GROUPS, settingsSearch),
    [settingsSearch],
  );
  const filteredItems = useMemo(
    () => filteredGroups.flatMap((group) => group.items),
    [filteredGroups],
  );
  const rawPageId = params["*"]?.replace(/^\/+|\/+$/g, "") || null;
  const legacyTabId = searchParams.get("tab");
  const requestedId = rawPageId ?? legacyTabId;
  const activeId = resolveAdminSettingsPageID(requestedId);

  const active = activeId ? SETTINGS_NAV.find((item) => item.id === activeId) : undefined;
  const ActiveComponent = active?.component;

  useEffect(() => {
    if (!activeId) return;

    window.scrollTo(0, 0);
    if (activeContentRef.current) {
      activeContentRef.current.scrollTop = 0;
    }
    activeContentRef.current?.focus({ preventScroll: true });
  }, [activeId]);

  // Turn old query-string tabs and retired page ids into canonical page URLs.
  if (requestedId && !activeId) {
    return <Navigate to="/admin/settings" replace />;
  }
  if (
    activeId &&
    (rawPageId !== activeId || legacyTabId !== null || searchParams.toString() !== "")
  ) {
    return <Navigate to={settingsPageHref(activeId)} replace />;
  }

  return (
    <div className="w-full max-w-[96rem]">
      {/* Settings pages stage their edits and only write them from the SaveBar,
          so leaving the shell — rail, back link, admin sidebar, browser back —
          would drop them. One guard for the whole area; the pages themselves
          only report that they are dirty. */}
      <UnsavedChangesGuard />

      {active && ActiveComponent ? (
        <div className="w-full space-y-5">
          <div className="flex flex-wrap items-center gap-3">
            <Link
              to="/admin/settings"
              className="text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex w-fit items-center gap-1.5 rounded-lg pr-2 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none"
            >
              <ChevronLeft className="h-4 w-4" aria-hidden="true" />
              All settings
            </Link>
          </div>

          {/* Same shell as the user settings page (SettingsLayout): one panel,
              nav rail on the left, content column on the right. */}
          <div className="surface-panel-lg flex min-w-0 flex-col lg:min-h-[500px] lg:flex-row lg:overflow-hidden">
            <aside className="border-border hidden lg:block lg:w-60 lg:flex-shrink-0 lg:border-r">
              {/* Lives with the rail it filters, not in the header row: the
                  admin shell floats its own fixed Search ⌘K / activity controls
                  over the top-right corner, which overlapped an input there. */}
              <div className="pt-5 pr-3 pl-5">
                <SettingsSearchInput
                  value={settingsSearch}
                  onChange={setSettingsSearch}
                  resultCount={countSettingsSearchItems(filteredGroups)}
                  captureShortcut={false}
                />
              </div>
              <SettingsPageRail activeId={active.id} items={filteredItems} />
            </aside>

            <div className="min-w-0 flex-1 p-4 sm:p-6">
              <div
                ref={activeContentRef}
                role="region"
                aria-label={`${active.label} settings`}
                tabIndex={-1}
                className="w-full max-w-3xl min-w-0 focus:outline-none"
              >
                <ActiveComponent />
              </div>
            </div>
          </div>
        </div>
      ) : (
        <SettingsOverview />
      )}
    </div>
  );
}
