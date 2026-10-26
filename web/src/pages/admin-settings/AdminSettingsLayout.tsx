import { useEffect, useRef, type ComponentType } from "react";
import { Navigate, useParams, useSearchParams } from "react-router";

import {
  ADMIN_SETTINGS_NAV,
  adminSettingsHref,
  resolveAdminSettingsPageID,
  type AdminSettingsSearchItem,
} from "@/lib/adminSettingsSearch";
import { PageColumn } from "@/components/page/PageHeader";
import { SettingsPageDescriptionContext } from "@/components/settings/settingsPageDescription";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";

import GeneralSettings from "./GeneralSettings";
import AppearanceSettings from "./AppearanceSettings";
import SecurityAccessSettings from "./SecurityAccessSettings";
import SignInSettings from "./SignInSettings";
import LibraryMetadataSettings from "./LibraryMetadataSettings";
import AutoscanSettings from "./AutoscanSettings";
import PlaybackSettings from "./PlaybackSettings";
import DownloadsSettings from "./DownloadsSettings";
import RequestsSettings from "./RequestsSettings";
import ProvidersSettings from "./ProvidersSettings";
import AISettings from "./AISettings";
import NotificationsAdminSettings from "./NotificationsAdminSettings";
import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";
import NetworkAccessSettings from "./NetworkAccessSettings";
import StorageSettings from "./StorageSettings";
import SearchSettings from "./SearchSettings";
import LoggingSettings from "./LoggingSettings";
import EmailSettings from "./EmailSettings";
import "@/styles/admin-settings.css";

interface SettingsNav extends AdminSettingsSearchItem {
  component: ComponentType;
}

const SETTINGS_COMPONENTS: Record<string, ComponentType> = {
  general: GeneralSettings,
  appearance: AppearanceSettings,
  security: SecurityAccessSettings,
  "sign-in": SignInSettings,
  library: LibraryMetadataSettings,
  autoscan: AutoscanSettings,
  playback: PlaybackSettings,
  downloads: DownloadsSettings,
  requests: RequestsSettings,
  providers: ProvidersSettings,
  ai: AISettings,
  notifications: NotificationsAdminSettings,
  compatibility: CompatibilityProxiesSettings,
  "network-access": NetworkAccessSettings,
  storage: StorageSettings,
  search: SearchSettings,
  logging: LoggingSettings,
  email: EmailSettings,
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

/**
 * Server settings pages. The admin sidebar lists each one under the topic it
 * belongs to; this only mounts the page the URL names. The bare
 * /admin/settings, where the old Settings entry led, opens the first page.
 */
export default function AdminSettingsLayout() {
  const params = useParams();
  const [searchParams] = useSearchParams();
  const activeContentRef = useRef<HTMLDivElement>(null);
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

  // Old query-string tabs and retired page ids become canonical page URLs; an
  // unknown or missing id opens the first page.
  if (!active || !ActiveComponent) {
    const fallback = SETTINGS_NAV[0];
    return fallback ? <Navigate to={adminSettingsHref(fallback.id)} replace /> : null;
  }
  if (rawPageId !== activeId || legacyTabId !== null || searchParams.toString() !== "") {
    return <Navigate to={adminSettingsHref(active.id)} replace />;
  }

  return (
    <div className="w-full">
      {/* Settings pages stage their edits and only write them from the SaveBar,
          so leaving a page — the admin sidebar, browser back — would drop
          them. One guard for the whole area; the pages themselves only report
          that they are dirty. */}
      <UnsavedChangesGuard />
      <div
        ref={activeContentRef}
        role="region"
        aria-label={`${active.label} settings`}
        tabIndex={-1}
        className="min-w-0 focus:outline-none"
      >
        <PageColumn width="settings">
          <SettingsPageDescriptionContext.Provider value={active.description}>
            <ActiveComponent />
          </SettingsPageDescriptionContext.Provider>
        </PageColumn>
      </div>
    </div>
  );
}
