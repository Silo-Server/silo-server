import { useEffect, useRef, useState } from "react";
import {
  BrowserRouter,
  Routes,
  Route,
  Navigate,
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";
import { QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { queryClient } from "@/lib/query-client";
import { AuthProvider, useAuth } from "@/hooks/useAuth";
import { ThemeProvider } from "@/hooks/useTheme";
import { CustomThemeProvider } from "@/contexts/CustomThemeProvider";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import ImpersonationBanner from "@/components/ImpersonationBanner";
import { loadStoredImpersonationAdminSession } from "@/lib/impersonationSession";
import { Toaster } from "@/components/ui/sonner";
import Layout from "@/components/Layout";
import AdminLayout from "@/components/AdminLayout";
import Home from "@/pages/Home";
import Login from "@/pages/Login";
import OAuthComplete from "@/pages/OAuthComplete";
import ActivateDevice from "@/pages/ActivateDevice";
import SetupWizard from "@/pages/SetupWizard";
import Profiles from "@/pages/Profiles";
import Catalog from "@/pages/Catalog";
import LibraryPage from "@/pages/LibraryPage";
import ItemDetail from "@/pages/ItemDetail/index";
import PersonDetail from "@/pages/PersonDetail";
import Collections from "@/pages/Collections";
import CollectionEditor from "@/pages/CollectionEditor";
import Requests from "@/pages/Requests";
import RequestBrowse from "@/pages/RequestBrowse";
import RequestDetail from "@/pages/RequestDetail";
import AdminDashboard from "@/pages/AdminDashboard";
import AdminActivity from "@/pages/AdminActivity";
import AdminLogs from "@/pages/AdminLogs";
import AdminUsers from "@/pages/AdminUsers";
import AdminRequests from "@/pages/AdminRequests";
import AdminDevices from "@/pages/AdminDevices";
import AdminLibraries from "@/pages/AdminLibraries";
import AdminSettingsLayout from "@/pages/admin-settings/AdminSettingsLayout";
import AdminNodes from "@/pages/AdminNodes";
import AdminSections from "@/pages/AdminSections";
import AdminCollections from "@/pages/AdminCollections";
import AdminCollectionEditor from "@/pages/AdminCollectionEditor";
import AdminPlaybackHistory from "@/pages/AdminPlaybackHistory";
import AdminMaintenance from "@/pages/AdminMaintenance";
import AdminApiKeys from "@/pages/AdminApiKeys";
import AdminSubtitles from "@/pages/AdminSubtitles";
import AdminUserDetail from "@/pages/AdminUserDetail";
import AdminTasks from "@/pages/AdminTasks";
import AdminTaskDetail from "@/pages/AdminTaskDetail";
import AdminPlugins from "@/pages/AdminPlugins";
import AdminHistoryImport from "@/pages/AdminHistoryImport";
import AdminRecommendations from "@/pages/AdminRecommendations";
import Recommendations from "@/pages/Recommendations";
import RecommendationsSection from "@/pages/RecommendationsSection";
import Calendar from "@/pages/Calendar";
import Signup from "@/pages/Signup";
import TasteSeed from "@/pages/TasteSeed";
import { useFavorites } from "@/hooks/queries/favorites";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { isTasteSeedDismissed } from "@/lib/tasteSeed";
import SettingsLayout from "@/pages/SettingsLayout";
import AppearanceSettings from "@/pages/settings/AppearanceSettings";
import AccessibilitySettings from "@/pages/settings/AccessibilitySettings";
import PlaybackSettings from "@/pages/settings/PlaybackSettings";
import ProfilesSettings from "@/pages/settings/ProfilesSettings";
import LibrarySettings from "@/pages/settings/LibrarySettings";
import HistoryImportSettings from "@/pages/settings/HistoryImportSettings";
import WebhookSyncSettings from "@/pages/settings/WebhookSyncSettings";
import WatchProvidersSettings from "@/pages/settings/WatchProvidersSettings";
import SubtitleAppearanceSettings from "@/pages/settings/SubtitleAppearanceSettings";
import HomeScreenSettings from "@/pages/settings/HomeScreenSettings";
import ThemeEditorSettings from "@/pages/settings/ThemeEditorSettings";
import CardOverlaySettings from "@/pages/settings/CardOverlaySettings";
import PersonalizeSettings from "@/pages/settings/PersonalizeSettings";
import WatchTogetherJoin from "@/pages/WatchTogetherJoin";
import WatchTogetherRoomPage from "@/pages/WatchTogetherRoomPage";
import WatchRoute from "@/pages/WatchRoute";
import ProfileCustomizeHome from "@/pages/ProfileCustomizeHome";
import {
  WatchPlaybackBar,
  WatchPlaybackHost,
  WatchPlaybackProvider,
} from "@/playback/WatchPlaybackChrome";
import type { ReactNode } from "react";
import {
  buildLegacyBrowseCatalogHref,
  buildPersonalCatalogHref,
  buildQueryCatalogHref,
  buildUserCollectionCatalogHref,
} from "@/pages/catalogSearchParams";
import { buildLegacyWebhookSyncRedirectTarget } from "@/lib/webhookSync";
import { toast } from "sonner";

/** Scrolls to top on pathname change (custom replacement for ScrollRestoration which requires data router). */
function useScrollRestoration() {
  const { pathname } = useLocation();
  useEffect(() => {
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

function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading, setupLoading } = useAuth();
  if (loading || setupLoading) {
    return (
      <div className="p-8" role="status" aria-live="polite">
        <span className="sr-only">Loading application</span>
        Loading...
      </div>
    );
  }
  if (!user) return <Navigate to="/login" replace />;
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
  if (!profile) return <Navigate to="/profiles" replace />;
  return <>{children}</>;
}

function RequireAdmin({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  if (user?.role !== "admin") return <Navigate to="/" replace />;
  return <>{children}</>;
}

function RequirePrimaryOrAdmin({ children }: { children: ReactNode }) {
  const { user, profile } = useAuth();
  if (user?.role !== "admin" && profile?.is_primary !== true) {
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

  if (isPending || isError || !profile) return <>{children}</>;

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

function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/login/oauth-complete" element={<OAuthComplete />} />
      <Route path="/activate" element={<ActivateDevice />} />
      <Route path="/setup" element={<SetupWizard />} />
      <Route path="/signup" element={<Signup />} />
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
                  <Route path="libraries" element={<AdminLibraries />} />
                  <Route path="maintenance" element={<AdminMaintenance />} />
                  <Route path="collections" element={<AdminCollections />} />
                  <Route path="collections/new" element={<AdminCollectionEditor />} />
                  <Route path="collections/:id/edit" element={<AdminCollectionEditor />} />
                  <Route path="requests" element={<AdminRequests />} />
                  <Route path="history" element={<AdminPlaybackHistory />} />
                  <Route path="history-import" element={<AdminHistoryImport />} />
                  <Route path="users" element={<AdminUsers />} />
                  <Route path="users/:id" element={<AdminUserDetail />} />
                  <Route path="devices" element={<AdminDevices />} />
                  <Route path="devices/:userId/:deviceId" element={<AdminDevices />} />
                  <Route path="nodes" element={<AdminNodes />} />
                  <Route path="sections" element={<AdminSections />} />
                  <Route path="plugins" element={<AdminPlugins />} />
                  <Route path="settings" element={<AdminSettingsLayout />} />
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
                      <Layout>
                        <SettingsLayout />
                      </Layout>
                    </RequireProfile>
                  }
                >
                  <Route index element={<Navigate to="playback" replace />} />
                  <Route path="appearance" element={<AppearanceSettings />} />
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
                  <Route path="*" element={<Navigate to="/settings/playback" replace />} />
                </Route>
                <Route
                  path="/*"
                  element={
                    <RequireProfile>
                      <Layout>
                        <Routes>
                          <Route
                            path="/"
                            element={
                              <TasteSeedGate>
                                <Home />
                              </TasteSeedGate>
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
                          <Route
                            path="/profile/customize-home"
                            element={<ProfileCustomizeHome />}
                          />
                          <Route path="*" element={<Navigate to="/" replace />} />
                        </Routes>
                      </Layout>
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

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <QueryClientProvider client={queryClient}>
          <ErrorBoundary>
            <ThemeProvider>
              <CustomThemeProvider>
                <WatchPlaybackProvider>
                  <ScrollRestorationManager />
                  <RouteAnnouncer />
                  <QueryCacheManager />
                  <AppChrome />
                  <AppRoutes />
                  <WatchPlaybackHost />
                  <WatchPlaybackBar />
                  <Toaster />
                </WatchPlaybackProvider>
              </CustomThemeProvider>
            </ThemeProvider>
          </ErrorBoundary>
        </QueryClientProvider>
      </AuthProvider>
    </BrowserRouter>
  );
}
