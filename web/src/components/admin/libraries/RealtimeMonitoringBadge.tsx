import { AlertTriangle } from "lucide-react";

import type { LibraryRealtimeMonitoringEntry } from "@/api/types";
import { Badge } from "@/components/ui/badge";

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
  if (!realtimeMonitoringNeedsAttention(entry)) return null;
  const label = realtimeMonitoringStateLabel(entry.state);
  const detail = entry.detail.trim();
  return (
    <Badge
      variant="outline"
      className="border-warning/40 text-warning"
      title={detail || `Real-time monitoring: ${label}`}
    >
      <AlertTriangle aria-hidden="true" />
      Monitoring: {label}
      {detail ? <span className="sr-only">. {detail}</span> : null}
    </Badge>
  );
}
