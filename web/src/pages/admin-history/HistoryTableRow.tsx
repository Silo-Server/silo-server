import { Link } from "react-router";

import type { AdminPlaybackHistoryItem } from "@/api/types";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
import { Badge } from "@/components/ui/badge";
import { TableCell, TableRow } from "@/components/ui/table";
import { useT, type Translate } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import type { MessageKey } from "@/locales/keys";

import { activityMethodMeta, formatDecisionLabel } from "../adminActivityPresentation";
import {
  formatHistoryDuration,
  historyRowTitle,
  normalizeMethodKey,
  watchTimeSummary,
} from "./historyFormat";

const EM_DASH = "—";

const MEDIA_TYPE_LABELS: Record<string, MessageKey> = {
  movie: "web.admin.history.mediaType.movie",
  episode: "web.admin.history.mediaType.episode",
  series: "web.admin.history.mediaType.series",
};

/**
 * One finalized session in the history table. A click on the row outside its
 * links and buttons opens the session's detail panel.
 */
export function HistoryTableRow({
  row,
  selected,
  onSelect,
}: {
  row: AdminPlaybackHistoryItem;
  selected: boolean;
  onSelect: (sessionId: string, trigger?: HTMLButtonElement | null) => void;
}) {
  const t = useT();
  const watch = watchTimeSummary(row);
  const { title, detail } = historyRowTitle(row, t);
  const methodMeta = activityMethodMeta(normalizeMethodKey(row.play_method));
  const titleBlock = (
    <>
      {title}
      {detail ? <div className="text-muted-foreground font-normal">{detail}</div> : null}
    </>
  );
  const profileLabel = row.profile_name || row.profile_id;
  return (
    <TableRow
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("a, button")) return;
        onSelect(row.session_id);
      }}
      className={cn(
        "cursor-pointer transition-colors",
        selected && "bg-primary/8 hover:bg-primary/10",
      )}
    >
      <TableCell>
        <div className="max-w-[18rem] min-w-[11rem] space-y-1 break-words whitespace-normal">
          {row.media_item_id ? (
            <Link
              to={`/item/${encodeURIComponent(row.media_item_id)}`}
              className="hover:text-primary block font-medium transition-colors hover:underline"
            >
              {titleBlock}
            </Link>
          ) : (
            <div className="font-medium">{titleBlock}</div>
          )}
          <div className="text-muted-foreground text-xs">
            {t("web.admin.history.row.meta", {
              media_type: mediaTypeLabel(row.media_type, t),
              session: row.session_id.slice(0, 8),
            })}
          </div>
        </div>
      </TableCell>
      <TableCell>
        <Link
          to={`/admin/users/${row.user_id}`}
          className="hover:text-primary font-medium transition-colors hover:underline"
        >
          {row.username || t("web.admin.activity.userFallback", { id: row.user_id })}
        </Link>
      </TableCell>
      <TableCell>
        <div className="space-y-1">
          <Link
            to={`/admin/history?user_id=${row.user_id}&profile_id=${encodeURIComponent(row.profile_id)}`}
            className="hover:text-primary block transition-colors hover:underline"
          >
            {profileLabel}
          </Link>
          <div className="text-muted-foreground text-xs">{row.profile_id}</div>
        </div>
      </TableCell>
      <TableCell>
        <div className="space-y-0.5">
          <div className="flex items-center gap-1.5 text-xs font-medium">
            <span>{row.client_name ?? EM_DASH}</span>
            <JellyfinSessionPill session={row} />
          </div>
          {row.device_platform ? (
            <div className="text-muted-foreground text-[0.6875rem]">{row.device_platform}</div>
          ) : null}
        </div>
      </TableCell>
      <TableCell>
        <span
          className={cn(
            "inline-flex items-center rounded-md border px-2 py-0.5 text-[0.6875rem] font-semibold",
            methodMeta.badgeClass,
          )}
        >
          {formatDecisionLabel(row.play_method)}
        </span>
      </TableCell>
      <TableCell>
        <div className="space-y-1 tabular-nums">
          <div title={watch.estimated ? t("web.admin.history.row.estimatedTitle") : undefined}>
            {watch.estimated
              ? t("web.admin.history.row.estimatedDuration", {
                  duration: formatHistoryDuration(watch.seconds, t),
                })
              : formatHistoryDuration(watch.seconds, t)}
          </div>
          <div className="text-muted-foreground text-xs">
            {t("web.admin.history.row.ofDuration", {
              duration: formatHistoryDuration(row.duration_seconds, t),
            })}
          </div>
        </div>
      </TableCell>
      <TableCell>
        <Badge variant={row.completed ? "default" : "outline"}>
          {row.completed
            ? t("web.admin.history.row.completed")
            : t("web.admin.history.row.partial")}
        </Badge>
      </TableCell>
      <TableCell>
        <div className="space-y-1 tabular-nums">
          <div>{formatDateTime(row.ended_at)}</div>
          <div className="text-muted-foreground text-xs">
            {t("web.admin.history.row.started", {
              time: formatRelative(row.started_at),
            })}
          </div>
        </div>
      </TableCell>
      <TableCell className="text-end">
        <div className="flex items-center justify-end gap-3">
          <button
            type="button"
            onClick={(e) => onSelect(row.session_id, e.currentTarget)}
            className="text-primary cursor-pointer text-sm font-medium hover:underline"
          >
            {t("web.admin.activity.row.details")}
          </button>
          <Link
            to={`/admin/logs?playback_session_id=${encodeURIComponent(row.session_id)}&focus=playback`}
            className="text-primary text-sm font-medium"
          >
            {t("web.admin.activity.row.viewLogs")}
          </Link>
          <Link
            to={`/admin/logs?playback_session_id=${encodeURIComponent(row.session_id)}&focus=playback&component=ffmpeg`}
            className="text-primary text-sm"
          >
            {t("web.admin.activity.row.ffmpegLogs")}
          </Link>
        </div>
      </TableCell>
    </TableRow>
  );
}

function mediaTypeLabel(mediaType: string, t: Translate): string {
  if (!mediaType) return t("web.admin.history.mediaType.unknown");
  const key = MEDIA_TYPE_LABELS[mediaType];
  return key ? t(key) : mediaType;
}

function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatPreferredDateTime(date);
}

function formatRelative(value: string) {
  return formatRelativeTime(value, { rounding: "floor" }) ?? value;
}
