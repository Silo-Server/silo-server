import { Link } from "react-router";
import { AlertTriangle, Info, XCircle } from "lucide-react";

import type { OperationalLogEntry } from "@/api/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useOperationalLogs } from "@/hooks/queries/admin/logs";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import { SectionError, UserSkeletonRows } from "../feedback";
import { useT, type Translate } from "@/i18n";

const ROW_LIMIT = 8;
const SEPARATOR = " · ";
/** One request for both levels — `level` takes a comma-separated list. */
const LEVELS = "error,warn";

/**
 * The newest error and warning lines from the operational log.
 *
 * Warnings sit beside errors on purpose: the log line that explains an error
 * is usually a warning logged moments earlier, and splitting them across two
 * widgets would hide that pairing.
 */
export function RecentErrorsWidget() {
  const t = useT();
  const logsQuery = useOperationalLogs({ level: LEVELS, limit: ROW_LIMIT });
  const entries = logsQuery.data?.entries ?? [];

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.dashboard.widgets.recentErrors.title")}
        </CardTitle>
        <Link
          to="/admin/logs"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          {t("web.admin.dashboard.widgets.recentErrors.allLogs")}
        </Link>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {logsQuery.isLoading ? (
          <UserSkeletonRows />
        ) : logsQuery.error ? (
          <SectionError message={t("web.admin.dashboard.widgets.recentErrors.loadFailed")} />
        ) : entries.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            {t("web.admin.dashboard.widgets.recentErrors.empty")}
          </div>
        ) : (
          <div className="space-y-0">
            {entries.slice(0, ROW_LIMIT).map((entry) => (
              <LogRow key={entry.id} entry={entry} />
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function LogRow({ entry }: { entry: OperationalLogEntry }) {
  const t = useT();
  const tone = levelTone(t, entry.level);
  const Icon = tone.icon;
  return (
    <div className="border-border/30 flex items-start gap-2.5 border-b py-2">
      {/* Icon + word: the level has to survive being read by someone who
          cannot tell the tints apart, and by a screenshot in grayscale. */}
      <span
        className={cn(
          "mt-0.5 flex flex-shrink-0 items-center gap-1 text-[10px] font-bold uppercase",
          tone.className,
        )}
      >
        <Icon className="h-3.5 w-3.5" aria-hidden="true" />
        {tone.label}
      </span>
      <div className="min-w-0 flex-1">
        <div className="truncate text-xs" title={entry.message}>
          {entry.message}
        </div>
        <div className="text-muted-foreground mt-0.5 text-[10px]">
          {entry.component || t("web.admin.dashboard.widgets.recentErrors.componentFallback")}
          {SEPARATOR}
          {formatRelativeTime(entry.timestamp, {
            rounding: "floor",
            justNowLabel: t("web.admin.dashboard.widgets.recentErrors.justNow"),
          }) ?? entry.timestamp}
        </div>
      </div>
    </div>
  );
}

function levelTone(t: Translate, level: string) {
  switch (level.toLowerCase()) {
    case "error":
    case "fatal":
      return {
        label: t("web.admin.dashboard.widgets.recentErrors.levelError"),
        icon: XCircle,
        className: "text-destructive",
      };
    case "warn":
    case "warning":
      return {
        label: t("web.admin.dashboard.widgets.recentErrors.levelWarn"),
        icon: AlertTriangle,
        className: "text-amber-500",
      };
    default:
      return {
        label: level || t("web.admin.dashboard.widgets.recentErrors.levelFallback"),
        icon: Info,
        className: "text-muted-foreground",
      };
  }
}
