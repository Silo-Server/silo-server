import {
  lazy,
  Suspense,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  createBrowserRouter,
  createRoutesFromElements,
  Outlet,
  Routes,
  Route,
  Navigate,
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";
// The DOM build of the provider is the same component with `ReactDOM.flushSync`
// wired in, which is what lets a view-transition navigation apply its state
// inside `startViewTransition`. Links across the app opt into view transitions.
import { RouterProvider } from "react-router/dom";
import { QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { queryClient } from "@/lib/query-client";
import { AuthProvider, useAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useNavigationDirection } from "@/hooks/useNavigationDirection";
import { ThemeProvider } from "@/hooks/useTheme";
import { DateTimeFormatProvider, useDateTimeFormat } from "@/hooks/useDateTimeFormat";
import { CustomThemeProvider } from "@/contexts/CustomThemeProvider";
import { BrandingProvider } from "@/contexts/BrandingProvider";
import { UICustomizationProvider } from "@/contexts/UICustomizationProvider";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import ImpersonationBanner from "@/components/ImpersonationBanner";
import { loadStoredImpersonationAdminSession } from "@/lib/impersonationSession";
import { Toaster } from "@/components/ui/sonner";
import { RealtimeEventsProvider } from "@/components/RealtimeEventsProvider";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { useSettingValuesRealtime } from "@/hooks/queries/settingValues";
import Layout from "@/components/Layout";
import Home from "@/pages/Home";
import Login from "@/pages/Login";
import Catalog from "@/pages/Catalog";
import { useFavorites } from "@/hooks/queries/favorites";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { isTasteSeedDismissed } from "@/lib/tasteSeed";
import { OnboardingGate } from "@/components/onboarding/OnboardingGate";
import { useOnboardingState } from "@/hooks/queries/onboarding";
import SettingsLayout from "@/pages/SettingsLayout";
import PlaybackSettings from "@/pages/settings/PlaybackSettings";
import {
  WatchPlaybackBar,
  WatchPlaybackHost,
  WatchPlaybackProvider,
} from "@/playback/WatchPlaybackChrome";
import { AudiobookPlaybackProvider } from "@/pages/audiobooks/player/audiobookPlaybackContext";
import {
  buildLegacyBrowseCatalogHref,
  buildPersonalCatalogHref,
  buildQueryCatalogHref,
  buildUserCollectionCatalogHref,
} from "@/pages/catalogSearchParams";
import { buildLegacyAutoscanRedirectTarget } from "@/pages/autoscanSearchParams";
import { buildLegacyWebhookSyncRedirectTarget } from "@/lib/webhookSync";
import { toast } from "sonner";
import { prewarmCodecDetection } from "@/player/hooks/useCodecDetection";
import { prefetchRouteChunks, type RouteChunkImport } from "@/lib/routeChunkPrefetch";

// Hot routes keep their import factory in a named binding so the idle warm-up
// below can pull the chunk before the user navigates. See HOT_ROUTE_CHUNKS.
const importLibraryPage = () => import("@/pages/LibraryPage");
const importItemDetail = () => import("@/pages/ItemDetail/index");
const importPersonDetail = () => import("@/pages/PersonDetail");
const importCollections = () => import("@/pages/Collections");
const importRecommendations = () => import("@/pages/Recommendations");

const AdminLayout = lazy(() => import("@/components/AdminLayout"));
const OAuthComplete = lazy(() => import("@/pages/OAuthComplete"));
const ActivateDevice = lazy(() => import("@/pages/ActivateDevice"));
const SetupWizard = lazy(() => import("@/pages/SetupWizard"));
const Profiles = lazy(() => import("@/pages/Profiles"));
const LibraryPage = lazy(importLibraryPage);
const ItemDetail = lazy(importItemDetail);
const EbookReader = lazy(() => import("@/pages/EbookReader"));
const PersonDetail = lazy(importPersonDetail);
const Collections = lazy(importCollections);
const CollectionEditor = lazy(() => import("@/pages/CollectionEditor"));
const Notifications = lazy(() => import("@/pages/Notifications"));
const DeviceSettings = lazy(() => import("@/pages/settings/DeviceSettings"));
const NotificationsSettings = lazy(() => import("@/pages/settings/NotificationsSettings"));
const Requests = lazy(() => import("@/pages/Requests"));
const RequestBrowse = lazy(() => import("@/pages/RequestBrowse"));
const RequestDetail = lazy(() => import("@/pages/RequestDetail"));
const AdminDashboard = lazy(() => import("@/pages/AdminDashboard"));
const AdminActivity = lazy(() => import("@/pages/AdminActivity"));
const AdminLogs = lazy(() => import("@/pages/AdminLogs"));
const AdminDiagnostics = lazy(() => import("@/pages/AdminDiagnostics"));
const AdminAccessGroups = lazy(() => import("@/pages/AdminAccessGroups"));
const AdminUsers = lazy(() => import("@/pages/AdminUsers"));
const AdminRequests = lazy(() => import("@/pages/AdminRequests"));
const AdminDevices = lazy(() => import("@/pages/AdminDevices"));
const AdminLibraries = lazy(() => import("@/pages/AdminLibraries"));
const AdminSettingsLayout = lazy(() => import("@/pages/admin-settings/AdminSettingsLayout"));
const AdminNodes = lazy(() => import("@/pages/AdminNodes"));
const AdminSections = lazy(() => import("@/pages/AdminSections"));
const AdminCollections = lazy(() => import("@/pages/AdminCollections"));
const AdminCollectionEditor = lazy(() => import("@/pages/AdminCollectionEditor"));
const AdminPlaybackHistory = lazy(() => import("@/pages/AdminPlaybackHistory"));
const AdminMarkerHistory = lazy(() => import("@/pages/AdminMarkerHistory"));
const AdminMaintenance = lazy(() => import("@/pages/AdminMaintenance"));
const AdminApiKeys = lazy(() => import("@/pages/AdminApiKeys"));
const AdminSubtitles = lazy(() => import("@/pages/AdminSubtitles"));
const AdminUserDetail = lazy(() => import("@/pages/AdminUserDetail"));
const AdminTasks = lazy(() => import("@/pages/AdminTasks"));
const AdminTaskDetail = lazy(() => import("@/pages/AdminTaskDetail"));
const AdminPlugins = lazy(() => import("@/pages/AdminPlugins"));
const AdminHistoryImport = lazy(() => import("@/pages/AdminHistoryImport"));
const AdminRecommendations = lazy(() => import("@/pages/AdminRecommendations"));
const AdminPolicyLayout = lazy(() => import("@/pages/admin-policy/AdminPolicyLayout"));
const Recommendations = lazy(importRecommendations);
const RecommendationsSection = lazy(() => import("@/pages/RecommendationsSection"));
const Calendar = lazy(() => import("@/pages/Calendar"));
const Signup = lazy(() => import("@/pages/Signup"));
const InviteClaim = lazy(() => import("@/pages/InviteClaim"));
const HouseholdSetup = lazy(() => import("@/pages/HouseholdSetup"));
const TasteSeed = lazy(() => import("@/pages/TasteSeed"));
const AppearanceSettings = lazy(() => import("@/pages/settings/AppearanceSettings"));
const AccessibilitySettings = lazy(() => import("@/pages/settings/AccessibilitySettings"));
const ProfilesSettings = lazy(() => import("@/pages/settings/ProfilesSettings"));
const LibrarySettings = lazy(() => import("@/pages/settings/LibrarySettings"));
const HistoryImportSettings = lazy(() => import("@/pages/settings/HistoryImportSettings"));
const WebhookSyncSettings = lazy(() => import("@/pages/settings/WebhookSyncSettings"));
const WatchProvidersSettings = lazy(() => import("@/pages/settings/WatchProvidersSettings"));
const SubtitleAppearanceSettings = lazy(
  () => import("@/pages/settings/SubtitleAppearanceSettings"),
);
const HomeScreenSettings = lazy(() => import("@/pages/settings/HomeScreenSettings"));
const ThemeEditorSettings = lazy(() => import("@/pages/settings/ThemeEditorSettings"));
const CardOverlaySettings = lazy(() => import("@/pages/settings/CardOverlaySettings"));
const PersonalizeSettings = lazy(() => import("@/pages/settings/PersonalizeSettings"));
const ConnectAppsSettings = lazy(() => import("@/pages/settings/ConnectAppsSettings"));
const InterfaceSettings = lazy(() => import("@/pages/settings/InterfaceSettings"));
const WatchTogetherJoin = lazy(() => import("@/pages/WatchTogetherJoin"));
const WatchTogetherRoomPage = lazy(() => import("@/pages/WatchTogetherRoomPage"));
const WatchRoute = lazy(() => import("@/pages/WatchRoute"));
const ProfileCustomizeHome = lazy(() => import("@/pages/ProfileCustomizeHome"));

/**
 * Routes a browsing session reaches within the first few interactions. Home
 * links straight into item details, the sidebar into libraries, and item pages
 * into people and recommendations, so paying their chunk cost while the app is
 * idle is cheaper than paying it inside a navigation.
 */
const HOT_ROUTE_CHUNKS: readonly RouteChunkImport[] = [
  importItemDetail,
  importLibraryPage,
  importPersonDetail,
  importRecommendations,
  importCollections,
];

/**
 * Scrolls to top on pathname change. Kept in place of react-router's
 * `<ScrollRestoration>`, which the data router would now allow: that one
 * restores the previous offset on back/forward, while every page here expects
 * to open at the top.
 */
function useScrollRestoration() {
  const { pathname } = useLocation();
  // Layout effect, not effect: after paint the browser has already shown one
  // frame of the new route at the old route's scroll offset, which reads as
  // a jump to the top rather than an arrival at it.
  useLayoutEffect(() => {
    window.scrollTo(0, 0);
  }, [pathname]);
}

/** Announces route changes to screen readers via an aria-live region. */
function RouteAnnouncer() {
  const location = useLocation();
  const [announcement, setAnnouncement] = useState("");

  useEffect(() => {
    // Small delay so document.title has time to update via useDocumentTitle hooks
    const id = setTimeout(() => {
      setAnnouncement(document.title || "Page loaded");
    }, 100);
    return () => clearTimeout(id);
  }, [location.pathname]);

  return (
    <div aria-live="assertive" role="status" className="sr-only">
      {announcement}
    </div>
  );
}

function ScrollRestorationManager() {
  useScrollRestoration();
  return null;
}

/**
 * Tracks history provenance and the direction the page is moving in. A leaf
 * rather than a call inside `AppShell`: the hook reads the location, and
 * subscribing `AppShell` to it would re-render the whole provider stack on
 * every navigation.
 */
function NavigationDirectionManager() {
  useNavigationDirection();
  return null;
}

function RouteLoading() {
  return (
    <div className="p-8" role="status" aria-live="polite">
      <span className="sr-only">Loading page</span>
      Loading...
    </div>
  );
}

/**
 * Builds a guard redirect target (e.g. "/login") that preserves the current
 * location so the user returns to it after authenticating.
 */
function guardRedirectTarget(base: string, location: ReturnType<typeof useLocation>): string {
  const destination = `${location.pathname}${location.search}`;
  if (destination === "/" || destination === "") {
    return base;
  }
  return `${base}?redirect=${encodeURIComponent(destination)}`;
}

function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading, setupLoading } = useAuth();
  const location = useLocation();
  if (loading || setupLoading) {
    return (
      <div className="p-8" role="status" aria-live="polite">
        <span className="sr-only">Loading application</span>
        Loading...
      </div>
    );
  }
  if (!user) return <Navigate to={guardRedirectTarget("/login", location)} replace />;
  return <>{children}</>;
}

function SetupGate({ children }: { children: ReactNode }) {
  const { user, setupLoading, setupRequired } = useAuth();
  if (setupLoading) {
    return (
      <div className="p-8" role="status" aria-live="polite">
        <span className="sr-only">Loading application</span>
        Loading...
      </div>
    );
  }
  if (setupRequired && !user) return <Navigate to="/setup" replace />;
  return <>{children}</>;
}

function RequireProfile({ children }: { children: ReactNode }) {
  const { profile } = useAuth();
  const location = useLocation();
  if (!profile) return <Navigate to={guardRedirectTarget("/profiles", location)} replace />;
  return <>{children}</>;
}

function RequireAdmin({ children }: { children: ReactNode }) {
  const actingAdmin = useIsActingAdmin();
  if (!actingAdmin) return <Navigate to="/" replace />;
  return <>{children}</>;
}

function RequirePrimaryOrAdmin({ children }: { children: ReactNode }) {
  const actingAdmin = useIsActingAdmin();
  const { profile } = useCurrentProfile();
  if (!actingAdmin && profile?.is_primary !== true) {
    return <Navigate to="/" replace />;
  }
  return <>{children}</>;
}

function RequireRequestsEnabled({ children }: { children: ReactNode }) {
  const status = useRequestFeatureStatus();
  if (status.isLoading) {
    return (
      <div className="p-8" role="status" aria-live="polite">
        <span className="sr-only">Loading request availability</span>
        Loading...
      </div>
    );
  }
  if (status.data?.requests_enabled !== true) return <Navigate to="/" replace />;
  return <>{children}</>;
}

/**
 * Redirects new profiles (no favorites yet, no skip flag) to the taste-seed
 * onboarding screen the first time they land on Home. Only checks on Home so
 * deep-links to other pages aren't blocked. Once the user picks any items
 * (or favorites anything by normal use), or explicitly skips, the gate stops
 * redirecting.
 */
function TasteSeedGate({ children }: { children: ReactNode }) {
  const { profile } = useAuth();
  const { data: favorites, isPending, isError } = useFavorites();
  const onboarding = useOnboardingState({ enabled: profile !== null });

  if (isPending || isError || !profile) return <>{children}</>;

  // While the feature tour is pending (or its state unknown) the tour owns
  // the first-run moment — it ends by handing off to /taste-seed itself, so
  // redirecting now would jump the queue.
  if (onboarding.data === undefined || !onboarding.data.done) return <>{children}</>;

  const hasFavorites = (favorites?.length ?? 0) > 0;
  const dismissed = isTasteSeedDismissed(profile.id);

  if (!hasFavorites && !dismissed) {
    return <Navigate to="/taste-seed" replace />;
  }
  return <>{children}</>;
}

/** Clears user-scoped query caches on profile switch or logout. */
function QueryCacheManager() {
  const { user, profile } = useAuth();
  const qc = useQueryClient();
  const prevProfileId = useRef(profile?.id);

  useEffect(() => {
    if (!user) {
      qc.clear();
      prevProfileId.current = undefined;
      return;
    }
    if (prevProfileId.current && prevProfileId.current !== profile?.id) {
      qc.removeQueries({ queryKey: ["favorites"] });
      qc.removeQueries({ queryKey: ["watchlist"] });
      qc.removeQueries({ queryKey: ["history"] });
      qc.removeQueries({ queryKey: ["collections"] });
      qc.removeQueries({ queryKey: ["libraryPlaybackPreferences"] });
      qc.removeQueries({ queryKey: ["progress"] });
      qc.removeQueries({ queryKey: ["sections"] });
      qc.removeQueries({ queryKey: ["calendar"] });
      qc.removeQueries({ queryKey: ["requests"] });
      qc.removeQueries({ queryKey: ["notifications"] });
      // Recommendation rows include per-profile user_state (is_favorite, etc.);
      // the taste-seed picker depends on this for pre-selection.
      qc.removeQueries({ queryKey: ["recommendations"] });
    }
    prevProfileId.current = profile?.id;
  }, [user, profile?.id, qc]);

  return null;
}

function AppChrome() {
  const { user, isImpersonating, endImpersonation } = useAuth();
  const navigate = useNavigate();

  if (!user?.impersonation || !isImpersonating) {
    return null;
  }

  async function handleEndImpersonation() {
    const returnPath = loadStoredImpersonationAdminSession()?.returnPath ?? "/admin/users";

    try {
      await endImpersonation();
      navigate(returnPath, { replace: true });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to end impersonation");
    }
  }

  return (
    <ImpersonationBanner
      userName={user.username}
      impersonatorName={user.impersonation.impersonator_username}
      onEnd={handleEndImpersonation}
    />
  );
}

function LegacySearchRedirect() {
  const [searchParams] = useSearchParams();
  return <Navigate to={buildQueryCatalogHref(searchParams.get("q") ?? undefined)} replace />;
}

function LegacyBrowseRedirect() {
  const [searchParams] = useSearchParams();
  const href = buildLegacyBrowseCatalogHref(searchParams);

  if (!href) {
    return <Navigate to={buildQueryCatalogHref()} replace />;
  }

  return <Navigate to={href} replace />;
}

function LegacyWebhookSyncRedirect() {
  const { search } = useLocation();
  return <Navigate to={buildLegacyWebhookSyncRedirectTarget(search)} replace />;
}

/**
 * `/admin/autoscan` → the Autoscan tab on Libraries. The old query has to be
 * translated, not dropped: the panel's own view moved from `tab` to `view`
 * because `tab` now names the Libraries tab hosting it.
 */
function LegacyAutoscanRedirect() {
  const { search } = useLocation();
  return <Navigate to={buildLegacyAutoscanRedirectTarget(search)} replace />;
}

function LegacyPersonalCatalogRedirect({
  source,
}: {
  source: "favorites" | "watchlist" | "history";
}) {
  return <Navigate to={buildPersonalCatalogHref(source)} replace />;
}

function LegacyUserCollectionRedirect() {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();

  if (!id) {
    return <Navigate to="/collections" replace />;
  }

  return (
    <Navigate
      to={buildUserCollectionCatalogHref(
        id,
        new URLSearchParams(location.search).get("title") ?? undefined,
      )}
      replace
    />
  );
}

// Re-renders the entire routed page tree when the date/time format
// preference changes, so pages formatting dates via lib/datetime module state
// pick up the new preference without per-component subscriptions.
function ReactiveAppRoutes() {
  useDateTimeFormat();
  return <AppRoutes />;
}

function UICustomizedLayout({ children }: { children: ReactNode }) {
  return (
    <UICustomizationProvider>
      <Layout>{children}</Layout>
    </UICustomizationProvider>
  );
}

function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/login/oauth-complete" element={<OAuthComplete />} />
      <Route path="/activate" element={<ActivateDevice />} />
      <Route path="/setup" element={<SetupWizard />} />
      <Route path="/signup" element={<Signup />} />
      <Route path="/invite/:token" element={<InviteClaim />} />
      <Route path="/household-setup" element={<HouseholdSetup />} />
      <Route
        path="/*"
        element={
          <SetupGate>
            <RequireAuth>
              <Routes>
                <Route path="/profiles" element={<Profiles />} />
                <Route
                  path="/taste-seed"
                  element={
                    <RequireProfile>
                      <TasteSeed />
                    </RequireProfile>
                  }
                />
                <Route
                  path="/watch/:id"
                  element={
                    <RequireProfile>
                      <WatchRoute />
                    </RequireProfile>
                  }
                />
                <Route
                  path="/reader/ebook/:contentId"
                  element={
                    <RequireProfile>
                      <EbookReader />
                    </RequireProfile>
                  }
                />
                {/* Admin area — own layout, no profile required */}
                <Route
                  path="/admin/*"
                  element={
                    <RequireAdmin>
                      <AdminLayout />
                    </RequireAdmin>
                  }
                >
                  <Route index element={<AdminDashboard />} />
                  <Route path="activity" element={<AdminActivity />} />
                  <Route path="logs" element={<AdminLogs />} />
                  <Route path="diagnostics" element={<AdminDiagnostics />} />
                  <Route path="libraries" element={<AdminLibraries />} />
                  <Route path="maintenance" element={<AdminMaintenance />} />
                  <Route path="collections" element={<AdminCollections />} />
                  <Route path="collections/new" element={<AdminCollectionEditor />} />
                  <Route path="collections/:id/edit" element={<AdminCollectionEditor />} />
                  <Route path="requests" element={<AdminRequests />} />
                  {/* Autoscan is a tab on Libraries now; keep old links working. */}
                  <Route path="autoscan" element={<LegacyAutoscanRedirect />} />
                  <Route path="history" element={<AdminPlaybackHistory />} />
                  <Route path="marker-history" element={<AdminMarkerHistory />} />
                  <Route path="history-import" element={<AdminHistoryImport />} />
                  <Route path="users" element={<AdminUsers />} />
                  <Route path="users/:id" element={<AdminUserDetail />} />
                  <Route path="access-groups" element={<AdminAccessGroups />} />
                  <Route path="devices" element={<AdminDevices />} />
                  <Route path="devices/:userId/:deviceId" element={<AdminDevices />} />
                  <Route path="nodes" element={<AdminNodes />} />
                  <Route path="sections" element={<AdminSections />} />
                  <Route path="plugins" element={<AdminPlugins />} />
                  <Route path="settings/*" element={<AdminSettingsLayout />} />
                  <Route path="policy" element={<AdminPolicyLayout />} />
                  <Route path="recommendations" element={<AdminRecommendations />} />
                  <Route path="api-keys" element={<AdminApiKeys />} />
                  <Route path="subtitles" element={<AdminSubtitles />} />
                  <Route path="tasks" element={<AdminTasks />} />
                  <Route path="tasks/:key" element={<AdminTaskDetail />} />
                  <Route path="stats" element={<Navigate to="/admin" replace />} />
                  <Route path="*" element={<Navigate to="/admin" replace />} />
                </Route>
                {/* Settings area — own layout, requires profile */}
                <Route
                  path="/settings/*"
                  element={
                    <RequireProfile>
                      <UICustomizedLayout>
                        <SettingsLayout />
                      </UICustomizedLayout>
                    </RequireProfile>
                  }
                >
                  <Route index element={null} />
                  <Route path="appearance" element={<AppearanceSettings />} />
                  <Route path="interface" element={<InterfaceSettings />} />
                  <Route path="theme-editor" element={<ThemeEditorSettings />} />
                  <Route path="accessibility" element={<AccessibilitySettings />} />
                  <Route path="playback" element={<PlaybackSettings />} />
                  <Route
                    path="profiles"
                    element={
                      <RequirePrimaryOrAdmin>
                        <ProfilesSettings />
                      </RequirePrimaryOrAdmin>
                    }
                  />
                  <Route path="libraries" element={<LibrarySettings />} />
                  <Route path="history-import" element={<HistoryImportSettings />} />
                  <Route path="plex-webhooks" element={<LegacyWebhookSyncRedirect />} />
                  <Route path="webhook-sync" element={<WebhookSyncSettings />} />
                  <Route path="watch-providers" element={<WatchProvidersSettings />} />
                  <Route path="subtitle-appearance" element={<SubtitleAppearanceSettings />} />
                  <Route path="home-screen" element={<HomeScreenSettings />} />
                  <Route path="card-overlays" element={<CardOverlaySettings />} />
                  <Route path="personalize" element={<PersonalizeSettings />} />
                  <Route path="devices" element={<DeviceSettings />} />
                  <Route path="notifications" element={<NotificationsSettings />} />
                  <Route path="connect-apps" element={<ConnectAppsSettings />} />
                  <Route path="*" element={<Navigate to="/settings/playback" replace />} />
                </Route>
                <Route
                  path="/*"
                  element={
                    <RequireProfile>
                      <UICustomizedLayout>
                        <Routes>
                          <Route
                            path="/"
                            element={
                              <OnboardingGate>
                                <TasteSeedGate>
                                  <Home />
                                </TasteSeedGate>
                              </OnboardingGate>
                            }
                          />
                          <Route path="/catalog" element={<Catalog />} />
                          <Route path="/library/:libraryId" element={<LibraryPage />} />
                          <Route path="/search" element={<LegacySearchRedirect />} />
                          <Route path="/browse" element={<LegacyBrowseRedirect />} />
                          <Route path="/item/:id" element={<ItemDetail />} />
                          <Route path="/person/:id" element={<PersonDetail />} />
                          <Route path="/rooms/:roomId" element={<WatchTogetherRoomPage />} />
                          <Route path="/rooms/join" element={<WatchTogetherJoin />} />
                          <Route
                            path="/favorites"
                            element={<LegacyPersonalCatalogRedirect source="favorites" />}
                          />
                          <Route
                            path="/watchlist"
                            element={<LegacyPersonalCatalogRedirect source="watchlist" />}
                          />
                          <Route
                            path="/history"
                            element={<LegacyPersonalCatalogRedirect source="history" />}
                          />
                          <Route path="/collections" element={<Collections />} />
                          <Route path="/collections/new" element={<CollectionEditor />} />
                          <Route path="/collections/:id/edit" element={<CollectionEditor />} />
                          <Route
                            path="/collections/:id"
                            element={<LegacyUserCollectionRedirect />}
                          />
                          <Route
                            path="/requests"
                            element={
                              <RequireRequestsEnabled>
                                <Requests />
                              </RequireRequestsEnabled>
                            }
                          />
                          <Route
                            path="/requests/:mediaType/:tmdbId"
                            element={
                              <RequireRequestsEnabled>
                                <RequestDetail />
                              </RequireRequestsEnabled>
                            }
                          />
                          <Route
                            path="/requests/browse/studio/:slug"
                            element={
                              <RequireRequestsEnabled>
                                <RequestBrowse kind="studio" />
                              </RequireRequestsEnabled>
                            }
                          />
                          <Route
                            path="/requests/browse/network/:slug"
                            element={
                              <RequireRequestsEnabled>
                                <RequestBrowse kind="network" />
                              </RequireRequestsEnabled>
                            }
                          />
                          <Route
                            path="/requests/browse/genre/:slug"
                            element={
                              <RequireRequestsEnabled>
                                <RequestBrowse kind="genre" />
                              </RequireRequestsEnabled>
                            }
                          />
                          <Route path="/recommendations" element={<Recommendations />} />
                          <Route
                            path="/recommendations/section/:kind"
                            element={<RecommendationsSection />}
                          />
                          <Route
                            path="/recommendations/section/:kind/:key"
                            element={<RecommendationsSection />}
                          />
                          <Route path="/calendar" element={<Calendar />} />
                          <Route path="/notifications" element={<Notifications />} />
                          <Route
                            path="/profile/customize-home"
                            element={<ProfileCustomizeHome />}
                          />
                          <Route path="*" element={<Navigate to="/" replace />} />
                        </Routes>
                      </UICustomizedLayout>
                    </RequireProfile>
                  }
                />
              </Routes>
            </RequireAuth>
          </SetupGate>
        }
      />
    </Routes>
  );
}

function RealtimeEventChannels() {
  const actingAdmin = useIsActingAdmin();

  useEventChannel("catalog");
  useEventChannel("user_state");
  // Subscribes user_settings and invalidates the canonical value queries, so a
  // setting changed on another device (or by an admin) reaches this tab.
  useSettingValuesRealtime();
  // Profile-scoped; the server rejects the subscription until the connection
  // is bound to a profile via the websocket ticket, which is harmless.
  useEventChannel("notifications");

  return actingAdmin ? <AdminRealtimeEventChannels /> : null;
}

function AdminRealtimeEventChannels() {
  useEventChannel("jobs");
  useEventChannel("sessions");
  useEventChannel("tasks");
  useEventChannel("scans");
  useEventChannel("settings");
  return null;
}

function PlaybackCapabilityPrewarmer() {
  useEffect(() => {
    void prewarmCodecDetection();
  }, []);
  return null;
}

/** Warms the hot route chunks once the first screen has settled. */
function RouteChunkPrewarmer() {
  const { user } = useAuth();
  const isAuthenticated = Boolean(user);
  useEffect(() => {
    // Nothing behind these routes is reachable while signed out, and the login
    // screen is exactly where bandwidth should stay free for the first paint.
    if (!isAuthenticated) return;
    return prefetchRouteChunks(HOT_ROUTE_CHUNKS);
  }, [isAuthenticated]);
  return null;
}

/**
 * Everything that used to sit directly inside `<BrowserRouter>`: providers,
 * app-wide chrome, and the routed page tree behind one Suspense boundary.
 *
 * `RouterProvider` takes no children, so this is the data router's single root
 * layout route. Nothing was hoisted above the router: ErrorBoundary,
 * RealtimeEventsProvider and WatchPlaybackProvider all read the location or
 * navigate, and the providers that need nothing from the router sit above those
 * in the chain — so splitting the stack would reorder providers for no gain.
 * The element below is created once at module scope, so React skips
 * re-rendering this subtree on navigation exactly as it did when the tree hung
 * off `<BrowserRouter>`.
 */
function AppShell() {
  return (
    <AuthProvider>
      <QueryClientProvider client={queryClient}>
        <ErrorBoundary>
          <BrandingProvider>
            <ThemeProvider>
              <CustomThemeProvider>
                <DateTimeFormatProvider>
                  <WatchPlaybackProvider>
                    <AudiobookPlaybackProvider>
                      <RealtimeEventsProvider>
                        <PlaybackCapabilityPrewarmer />
                        <RouteChunkPrewarmer />
                        <RealtimeEventChannels />
                        <ScrollRestorationManager />
                        <NavigationDirectionManager />
                        <RouteAnnouncer />
                        <QueryCacheManager />
                        <AppChrome />
                        <Suspense fallback={<RouteLoading />}>
                          <Outlet />
                        </Suspense>
                        <WatchPlaybackHost />
                        <WatchPlaybackBar />
                        <Toaster />
                      </RealtimeEventsProvider>
                    </AudiobookPlaybackProvider>
                  </WatchPlaybackProvider>
                </DateTimeFormatProvider>
              </CustomThemeProvider>
            </ThemeProvider>
          </BrandingProvider>
        </ErrorBoundary>
      </QueryClientProvider>
    </AuthProvider>
  );
}

/**
 * A data router is what makes navigation blockable (`useBlocker`, used by
 * `UnsavedChangesGuard` to protect staged settings edits) — that is the whole
 * reason for `createBrowserRouter` here. The route tree itself stays
 * declarative below the root: the splat child hands off to `<Routes>`, whose
 * descendant routes match from `/` because a splat contributes nothing to the
 * pathname base.
 */
const appRoutes = createRoutesFromElements(
  <Route element={<AppShell />}>
    <Route path="*" element={<ReactiveAppRoutes />} />
  </Route>,
);

export default function App() {
  // Per-App-instance rather than a module-scope singleton: a router captures
  // the current history the moment it is built, and tests render App more than
  // once against different entries.
  const [router] = useState(() => createBrowserRouter(appRoutes));

  return <RouterProvider router={router} />;
}
