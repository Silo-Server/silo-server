import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AdminSectionCommandDialog } from "@/components/AdminSectionCommandDialog";
import { DashboardGrid } from "@/components/admin/dashboard/DashboardGrid";
import { useDashboardLayout } from "@/components/admin/dashboard/useDashboardLayout";
import { fetchAdminStats, useAdminSessions, useAdminStats } from "@/hooks/queries/admin/stats";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { usePolicyCapability } from "@/hooks/queries/admin/policy";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useAdminLibraries, useScanAllLibraries } from "@/hooks/queries/admin/libraries";
import { Button } from "@/components/ui/button";
import { LayoutDashboard, Plus, RefreshCw, ScanLine } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { adminKeys } from "@/hooks/queries/keys";
import { usePageActivity } from "@/hooks/usePageActivity";
import { buildAdminCommandNavSections } from "@/lib/adminNavigation";

// Query prefixes the dashboard's stats/sessions/libraries/users refetch does
// not already cover. Widgets fetch these themselves, so Refresh only has to
// mark them stale; mounted widgets refetch, hidden ones stay cheap.
const DASHBOARD_WIDGET_QUERY_PREFIXES = [
  adminKeys.dashboardTimeseriesRoot(),
  adminKeys.playbackActivityRoot(),
  adminKeys.topActivityRoot(),
  adminKeys.downloadsStatsRoot(),
  adminKeys.serverStatus(),
  adminKeys.systemResources(),
  adminKeys.nodes(),
  adminKeys.autoscanStatus(),
  adminKeys.autoscanScansRoot(),
  adminKeys.operationalLogsRoot(),
];

const REFRESH_SPINNER_MIN_VISIBLE_MS = 1_000;
const DASHBOARD_AUTO_REFRESH_MS = 60_000;
const RELATIVE_UPDATED_LABEL_TICK_MS = 30_000;

export default function AdminDashboard() {
  const queryClient = useQueryClient();
  const statsQuery = useAdminStats();
  const sessionsQuery = useAdminSessions();
  const librariesQuery = useAdminLibraries();
  const usersQuery = useAdminUsers();
  const { data: adminInstallations } = useAdminPluginInstallations();
  const policyCapability = usePolicyCapability();
  const scanAll = useScanAllLibraries();
  const pageActivity = usePageActivity();
  const layout = useDashboardLayout();
  const manualRefreshStartedAtRef = useRef<number | null>(null);
  const wasDashboardPollingPausedRef = useRef(!pageActivity.canPollDashboard);
  const [isManualRefreshPending, setIsManualRefreshPending] = useState(false);
  const [lastDashboardUpdatedAt, setLastDashboardUpdatedAt] = useState<number | null>(null);
  const [relativeUpdatedNow, setRelativeUpdatedNow] = useState(() => Date.now());
  const [isAddPanelOpen, setIsAddPanelOpen] = useState(false);

  const libraries = librariesQuery.data ?? [];
  const { refetch: refetchSessions } = sessionsQuery;
  const { refetch: refetchLibraries } = librariesQuery;
  const { refetch: refetchUsers } = usersQuery;
  const hasDashboardData =
    statsQuery.data !== undefined &&
    sessionsQuery.data !== undefined &&
    librariesQuery.data !== undefined &&
    usersQuery.data !== undefined;
  const hasStaleDashboardData =
    statsQuery.isStale || sessionsQuery.isStale || librariesQuery.isStale || usersQuery.isStale;
  const dashboardDataUpdatedAt = Math.max(
    statsQuery.dataUpdatedAt,
    sessionsQuery.dataUpdatedAt,
    librariesQuery.dataUpdatedAt,
    usersQuery.dataUpdatedAt,
  );
  const lastUpdatedLabel = lastDashboardUpdatedAt
    ? formatRelativeUpdatedLabel(relativeUpdatedNow, lastDashboardUpdatedAt)
    : null;
  const adminSearchSections = useMemo(
    () =>
      buildAdminCommandNavSections(adminInstallations, {
        policyEditorAvailable: policyCapability.data?.editor_available === true,
      }),
    [adminInstallations, policyCapability.data?.editor_available],
  );

  useEffect(() => {
    if (!lastDashboardUpdatedAt) {
      return;
    }

    const interval = window.setInterval(() => {
      setRelativeUpdatedNow(Date.now());
    }, RELATIVE_UPDATED_LABEL_TICK_MS);

    return () => {
      window.clearInterval(interval);
    };
  }, [lastDashboardUpdatedAt]);

  useEffect(() => {
    if (hasDashboardData && dashboardDataUpdatedAt > 0) {
      setLastDashboardUpdatedAt(dashboardDataUpdatedAt);
    }
  }, [dashboardDataUpdatedAt, hasDashboardData]);

  const refreshDashboard = useCallback(
    async ({ manual }: { manual: boolean }) => {
      if (manual) {
        manualRefreshStartedAtRef.current = Date.now();
        setIsManualRefreshPending(true);
      }
      try {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: adminKeys.stats(), refetchType: "none" }),
          queryClient.invalidateQueries({ queryKey: adminKeys.sessions(), refetchType: "none" }),
          queryClient.invalidateQueries({ queryKey: adminKeys.libraries(), refetchType: "none" }),
          queryClient.invalidateQueries({ queryKey: adminKeys.users(), refetchType: "none" }),
          // Widgets that own their own data: invalidate by prefix so every
          // window variant is covered, and let the default refetchType refetch
          // the ones actually mounted. Nothing here is refetched by hand — the
          // widget's own hook does that when its query goes stale.
          ...DASHBOARD_WIDGET_QUERY_PREFIXES.map((queryKey) =>
            queryClient.invalidateQueries({ queryKey }),
          ),
        ]);
        const nextStats = await fetchAdminStats({ refresh: true });
        queryClient.setQueryData(adminKeys.stats(), nextStats);
        await Promise.all([refetchSessions(), refetchLibraries(), refetchUsers()]);
        const refreshedAt = Date.now();
        setLastDashboardUpdatedAt(refreshedAt);
        setRelativeUpdatedNow(refreshedAt);
      } finally {
        if (manual) {
          const startedAt = manualRefreshStartedAtRef.current;
          if (startedAt !== null) {
            const elapsed = Date.now() - startedAt;
            const remaining = REFRESH_SPINNER_MIN_VISIBLE_MS - elapsed;
            if (remaining > 0) {
              await delay(remaining);
            }
          }
          manualRefreshStartedAtRef.current = null;
          setIsManualRefreshPending(false);
        }
      }
    },
    [queryClient, refetchLibraries, refetchSessions, refetchUsers],
  );

  useEffect(() => {
    if (!pageActivity.canPollDashboard) {
      wasDashboardPollingPausedRef.current = true;
      return;
    }
    if (isManualRefreshPending) {
      return;
    }
    const resumedPolling = wasDashboardPollingPausedRef.current;
    wasDashboardPollingPausedRef.current = false;
    if (
      resumedPolling &&
      hasDashboardData &&
      (hasStaleDashboardData ||
        !lastDashboardUpdatedAt ||
        Date.now() - lastDashboardUpdatedAt >= DASHBOARD_AUTO_REFRESH_MS)
    ) {
      void refreshDashboard({ manual: true });
      return;
    }

    if (
      lastDashboardUpdatedAt &&
      Date.now() - lastDashboardUpdatedAt >= DASHBOARD_AUTO_REFRESH_MS
    ) {
      void refreshDashboard({ manual: false });
    }

    const interval = window.setInterval(() => {
      void refreshDashboard({ manual: false });
    }, DASHBOARD_AUTO_REFRESH_MS);

    return () => {
      window.clearInterval(interval);
    };
  }, [
    hasDashboardData,
    hasStaleDashboardData,
    isManualRefreshPending,
    lastDashboardUpdatedAt,
    pageActivity.canPollDashboard,
    refreshDashboard,
  ]);

  const toggleCustomizing = useCallback(() => {
    layout.setCustomizing(!layout.isCustomizing);
    if (layout.isCustomizing) {
      setIsAddPanelOpen(false);
    }
  }, [layout]);

  return (
    <div className="space-y-6 lg:space-y-8">
      <AdminSectionCommandDialog sections={adminSearchSections} />

      {/* Page header */}
      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3.25rem)]">Dashboard</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Live sessions, content health, and server activity in one view.
          </p>
        </div>
        <div className="grid w-full gap-2 sm:flex sm:w-auto sm:flex-wrap sm:items-center">
          <div className="grid gap-2 sm:flex sm:items-center">
            {lastUpdatedLabel && (
              <span className="text-muted-foreground text-xs sm:whitespace-nowrap">
                Updated {lastUpdatedLabel}
              </span>
            )}
            <Button
              variant="outline"
              size="sm"
              className="min-w-[8.25rem] justify-center sm:w-auto"
              onClick={() => {
                void refreshDashboard({ manual: true });
              }}
              disabled={isManualRefreshPending}
              aria-busy={isManualRefreshPending}
            >
              <RefreshCw
                className={`h-3.5 w-3.5 ${isManualRefreshPending ? "animate-spin" : ""}`}
              />
              {isManualRefreshPending ? "Refreshing..." : "Refresh"}
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="cursor-pointer justify-center sm:w-auto"
              onClick={toggleCustomizing}
              aria-pressed={layout.isCustomizing}
            >
              <LayoutDashboard className="h-3.5 w-3.5" />
              {layout.isCustomizing ? "Done" : "Customize"}
            </Button>
          </div>
          <Button
            variant="default"
            size="sm"
            className="w-full cursor-pointer sm:w-auto"
            onClick={() => {
              if (libraries.length > 0) {
                scanAll.mutate();
              }
            }}
            disabled={scanAll.isPending || libraries.length === 0}
          >
            <ScanLine className="h-3.5 w-3.5" />
            Scan All Libraries
          </Button>
        </div>
      </div>

      {layout.isCustomizing && (
        <div className="surface-panel-subtle flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl px-4 py-2.5">
          <Button
            variant="outline"
            size="sm"
            className="cursor-pointer"
            onClick={() => setIsAddPanelOpen(true)}
          >
            <Plus className="h-3.5 w-3.5" />
            Add widget
          </Button>
          <span className="text-muted-foreground text-xs">
            Drag a widget to move it · drag its corner to resize · × removes it
          </span>
          <button
            type="button"
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring ml-auto cursor-pointer text-xs underline-offset-2 transition-colors hover:underline focus-visible:ring-2 focus-visible:outline-none"
            onClick={() => layout.resetLayout()}
          >
            Reset to default layout
          </button>
        </div>
      )}

      <DashboardGrid
        layout={layout}
        isAddPanelOpen={isAddPanelOpen}
        onAddPanelOpenChange={setIsAddPanelOpen}
      />
    </div>
  );
}

// --- Helpers ---

function formatRelativeUpdatedLabel(now: number, updatedAt: number) {
  const elapsedMinutes = Math.floor(Math.max(0, now - updatedAt) / 60_000);
  if (elapsedMinutes < 1) {
    return "less than 1 minute ago";
  }
  if (elapsedMinutes === 1) {
    return "1 minute ago";
  }
  return `${elapsedMinutes.toLocaleString()} minutes ago`;
}

function delay(ms: number) {
  return new Promise<void>((resolve) => {
    window.setTimeout(resolve, ms);
  });
}
