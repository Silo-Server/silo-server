import { AlertTriangle } from "lucide-react";

import type { LibraryRealtimeMonitoringEntry } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { useT } from "@/i18n";

import {
  realtimeMonitoringNeedsAttention,
  realtimeMonitoringStateLabel,
} from "./realtimeMonitoring";

/** The libraries list's warning badge; renders nothing unless monitoring needs attention. */
export function RealtimeMonitoringBadge({
  entry,
}: {
  entry: LibraryRealtimeMonitoringEntry | undefined;
}) {
  const t = useT();
  if (!realtimeMonitoringNeedsAttention(entry)) return null;
  const label = realtimeMonitoringStateLabel(entry.state);
  const detail = entry.detail.trim();
  return (
    <Badge
      variant="outline"
      className="border-warning/40 text-warning"
      title={detail || t("web.admin.common.libraryEditor.monitoringBadge.title", { state: label })}
    >
      <AlertTriangle aria-hidden="true" />
      {t("web.admin.common.libraryEditor.monitoringBadge.label", { state: label })}
      {detail ? (
        <span className="sr-only">
          {t("web.admin.common.libraryEditor.monitoringBadge.srDetail", { detail })}
        </span>
      ) : null}
    </Badge>
  );
}
