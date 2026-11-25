import { createContext, useContext, useMemo, type ReactNode } from "react";
import { Check, LayoutDashboard } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";

import { findDashboardWidget } from "@/components/admin/dashboard/registry";
import type { WidgetId } from "@/components/admin/dashboard/types";
import { useDashboardLayout } from "@/components/admin/dashboard/useDashboardLayout";
import { Button } from "@/components/ui/button";
import { useAdminDashboardLayout } from "@/hooks/queries/admin/dashboardLayout";
import { useT } from "@/i18n";

import { insightsRangeToWidgetRange, type InsightsRange } from "./insightsHelpers";

interface InsightsDashboardPins {
  isPlaced: (id: WidgetId) => boolean;
  /**
   * False until the server layout has loaded, and while saving it is blocked
   * (a failed save, or a newer layout from another browser). An add then
   * could not reach the server, so the buttons stay hidden rather than
   * confirm a change the dashboard never gets.
   */
  canAdd: boolean;
  add: (id: WidgetId, range: InsightsRange) => void;
}

const InsightsDashboardPinsContext = createContext<InsightsDashboardPins | null>(null);

/**
 * Lets an Insights card put its chart on the admin's dashboard.
 *
 * The page holds one `useDashboardLayout`, the same hook the dashboard uses,
 * so an add rides its debounced, revision-checked save and lands where the
 * dashboard will read it. A second instance per card would keep its own copy
 * of the arrangement and the two would overwrite each other.
 */
export function InsightsDashboardPinsProvider({ children }: { children: ReactNode }) {
  const { entries, addWidget, setWidgetRange, serverSaveBlocked } = useDashboardLayout();
  const layoutLoaded = useAdminDashboardLayout().isSuccess;
  const canAdd = layoutLoaded && !serverSaveBlocked;
  const value = useMemo<InsightsDashboardPins>(() => {
    const placed = new Set(entries.map((entry) => entry.id));
    return {
      isPlaced: (id) => placed.has(id),
      canAdd,
      add: (id, range) => {
        addWidget(id);
        // Open the widget on the window the card was showing when the widget
        // offers it; a custom range has no widget equivalent.
        const widgetRange = insightsRangeToWidgetRange(range);
        if (widgetRange && findDashboardWidget(id)?.ranges?.allowed.includes(widgetRange)) {
          setWidgetRange(id, widgetRange);
        }
      },
    };
  }, [entries, canAdd, addWidget, setWidgetRange]);
  return (
    <InsightsDashboardPinsContext.Provider value={value}>
      {children}
    </InsightsDashboardPinsContext.Provider>
  );
}

/**
 * "Add to dashboard" for one Insights card. Once the widget is on the
 * dashboard it becomes a link there. The widget shows the whole server: the
 * page's library and account filters do not travel with it.
 */
export function AddToDashboardButton({
  widgetId,
  range,
  chartName,
}: {
  widgetId: WidgetId;
  range: InsightsRange;
  chartName: string;
}) {
  const t = useT();
  const pins = useContext(InsightsDashboardPinsContext);
  if (!pins || !findDashboardWidget(widgetId)) {
    return null;
  }
  if (pins.isPlaced(widgetId)) {
    return (
      <Link
        to="/admin"
        aria-label={t("web.admin.insights.addToDashboard.onDashboardLabel", { chart: chartName })}
        className="text-muted-foreground hover:text-primary inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md px-2 text-xs transition-colors"
      >
        <Check className="h-3.5 w-3.5" aria-hidden="true" />
        {t("web.admin.insights.addToDashboard.onDashboard")}
      </Link>
    );
  }
  if (!pins.canAdd) {
    return null;
  }
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      aria-label={t("web.admin.insights.addToDashboard.addLabel", { chart: chartName })}
      title={t("web.admin.insights.addToDashboard.hint")}
      className="text-muted-foreground h-7 shrink-0 gap-1.5 px-2 text-xs"
      onClick={() => {
        pins.add(widgetId, range);
        toast.success(t("web.admin.insights.addToDashboard.added", { chart: chartName }));
      }}
    >
      <LayoutDashboard className="h-3.5 w-3.5" aria-hidden="true" />
      {t("web.admin.insights.addToDashboard.add")}
    </Button>
  );
}
