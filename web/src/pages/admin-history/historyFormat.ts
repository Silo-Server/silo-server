import type { AdminPlaybackHistoryItem } from "@/api/types";
import type { Translate } from "@/i18n";

import { episodeCode } from "../admin-users/detail/format";

export function normalizeMethodKey(playMethod: string): string {
  switch (playMethod) {
    case "direct":
    case "direct_play":
      return "direct";
    case "remux":
    case "direct_stream":
      return "remux";
    case "transcode":
      return "transcode";
    default:
      return "unknown";
  }
}

export function formatHistoryDuration(seconds: number | null | undefined, t: Translate): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds <= 0) {
    return t("web.admin.history.duration.none");
  }
  const rounded = Math.max(0, Math.floor(seconds));
  const hours = Math.floor(rounded / 3600);
  const minutes = Math.floor((rounded % 3600) / 60);
  const secs = rounded % 60;
  if (hours > 0) {
    return t("web.admin.history.duration.hoursMinutes", {
      hours: String(hours),
      minutes: String(minutes),
    });
  }
  if (minutes > 0) {
    return t("web.admin.history.duration.minutesSeconds", {
      minutes: String(minutes),
      seconds: String(secs),
    });
  }
  return t("web.admin.history.duration.seconds", { seconds: String(secs) });
}

/**
 * An episode leads with its series title, with "S02E04 · Episode title" as the
 * detail line. A row without a series title keeps its own title.
 */
export function historyRowTitle(
  row: AdminPlaybackHistoryItem,
  t: Translate,
): { title: string; detail: string } {
  if (row.series_title) {
    const episodeTitle = row.media_title === row.series_title ? "" : row.media_title;
    const detail = [episodeCode(row.season_number, row.episode_number), episodeTitle]
      .filter(Boolean)
      .join(" · ");
    return { title: row.series_title, detail };
  }
  return {
    title:
      row.media_title ||
      row.media_item_id ||
      t("web.common.activity.title.file", { id: row.media_file_id }),
    detail: "",
  };
}

export function historyRowLabel(row: AdminPlaybackHistoryItem, t: Translate): string {
  const { title, detail } = historyRowTitle(row, t);
  return detail ? `${title} · ${detail}` : title;
}

/**
 * Measured watch time, or for a play recorded before it was measured the
 * server's estimate: the end position capped at the session's length.
 */
export function watchTimeSummary(row: AdminPlaybackHistoryItem): {
  seconds: number;
  estimated: boolean;
} {
  if (row.played_seconds != null) {
    return { seconds: row.played_seconds, estimated: false };
  }
  const elapsed = (Date.parse(row.ended_at) - Date.parse(row.started_at)) / 1000;
  const capped = Number.isFinite(elapsed)
    ? Math.min(row.end_position_seconds, Math.max(elapsed, 0))
    : row.end_position_seconds;
  return { seconds: Math.max(capped, 0), estimated: true };
}
