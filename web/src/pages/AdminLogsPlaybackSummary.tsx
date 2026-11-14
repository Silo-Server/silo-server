import { useMemo } from "react";
import type { AuditLogEntry, OperationalLogEntry } from "@/api/types";
import { useT } from "@/i18n";
import { formatDateTime, shortID } from "./AdminLogs.format";

export function PlaybackSessionSummary({
  playbackSessionID,
  appRows,
  auditRows,
  component,
  onFilterFFmpeg,
}: {
  playbackSessionID: string;
  appRows: OperationalLogEntry[];
  auditRows: AuditLogEntry[];
  component: string;
  onFilterFFmpeg: () => void;
}) {
  const t = useT();
  const { appCount, ffmpegCount, auditCount, firstSeen, lastSeen, nodes } = useMemo(() => {
    const matchingAppRows = appRows.filter((row) => matchesPlaybackSession(row, playbackSessionID));
    const matchingAuditRows = auditRows.filter((row) =>
      matchesPlaybackSession(row, playbackSessionID),
    );
    const timestamps = [...matchingAppRows, ...matchingAuditRows]
      .map((row) => row.timestamp)
      .sort();

    return {
      appCount: matchingAppRows.length,
      ffmpegCount: matchingAppRows.filter((row) => row.component === "ffmpeg").length,
      auditCount: matchingAuditRows.length,
      firstSeen: timestamps[0],
      lastSeen: timestamps[timestamps.length - 1],
      nodes: new Set(
        [...matchingAppRows, ...matchingAuditRows].map((row) => row.node_id).filter(Boolean),
      ),
    };
  }, [appRows, auditRows, playbackSessionID]);

  return (
    <div className="bg-card border-border rounded-lg border p-4 text-sm">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-wrap gap-3 md:grid md:flex-1 md:grid-cols-6">
          <SummaryMetric
            label={t("web.admin.logs.summary.playbackSession")}
            value={shortID(playbackSessionID)}
            mono
          />
          <SummaryMetric
            label={t("web.admin.logs.summary.applicationLogs")}
            value={String(appCount)}
          />
          <SummaryMetric
            label={t("web.admin.logs.summary.ffmpegLogs")}
            value={String(ffmpegCount)}
          />
          <SummaryMetric label={t("web.admin.logs.summary.auditLogs")} value={String(auditCount)} />
          <SummaryMetric
            label={t("web.admin.logs.summary.firstSeen")}
            value={firstSeen ? formatDateTime(firstSeen) : "-"}
          />
          <SummaryMetric
            label={t("web.admin.logs.summary.nodesSeen")}
            value={nodes.size > 0 ? Array.from(nodes).join(", ") : "-"}
            mono={nodes.size > 0}
          />
          {lastSeen && (
            <SummaryMetric
              label={t("web.admin.logs.summary.lastSeen")}
              value={formatDateTime(lastSeen)}
            />
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onFilterFFmpeg}
            className={`rounded-md border px-3 py-2 text-xs font-medium ${
              component === "ffmpeg"
                ? "border-primary bg-primary/10 text-primary"
                : "border-border bg-background text-muted-foreground"
            }`}
          >
            {component === "ffmpeg"
              ? t("web.admin.logs.ffmpeg.showingOnly")
              : t("web.admin.logs.ffmpeg.open")}
          </button>
        </div>
      </div>
    </div>
  );
}

function SummaryMetric({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <div className="text-muted-foreground mb-1 text-xs">{label}</div>
      <div className={mono ? "font-mono text-xs break-all" : "text-sm"}>{value}</div>
    </div>
  );
}

function matchesPlaybackSession(
  entry: OperationalLogEntry | AuditLogEntry,
  playbackSessionID: string,
) {
  if (!playbackSessionID) return true;
  return entry.playback_session_id === playbackSessionID;
}
