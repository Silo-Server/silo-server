import { useEffect, useMemo } from "react";
import { Link } from "react-router";
import { ChevronDown, ChevronUp, ExternalLink, Info, ScrollText, Terminal } from "lucide-react";

import type { AdminPlaybackHistoryItem, AdminSession } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { useT, type Translate } from "@/i18n";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";
import { formatCodecLabel } from "@/lib/mediaFormat";
import { cn } from "@/lib/utils";

import { episodeCode } from "../admin-users/detail/format";
import {
  activityMethodMeta,
  formatAudioSummary,
  formatDecisionLabel,
  formatDeliveredAudioSummary,
  formatDeliveredContainerSummary,
  formatDeliveredVideoSummary,
  formatSessionBitrate,
  formatSourceContainerSummary,
  formatToneMapSummary,
  formatTranscodeModeSummary,
  formatVideoSummary,
  getSessionRouteNodes,
} from "../adminActivityPresentation";
import { getAdaptationReasonsFromCodes } from "../adminActivityReasons";

export interface SessionDetailSheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  session: AdminPlaybackHistoryItem | null;
  isLoading?: boolean;
  notFound?: boolean;
  hasPrevious: boolean;
  hasNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
}

function historyItemAsAdminSession(item: AdminPlaybackHistoryItem): AdminSession {
  const audioTranscode =
    item.audio_decision === "transcode" ||
    (item.play_method === "transcode" && Boolean(item.target_audio_codec));
  return {
    session_id: item.session_id,
    user_id: item.user_id,
    username: item.username,
    profile_id: item.profile_id,
    profile_name: item.profile_name,
    media_item_id: item.media_item_id,
    media_file_id: item.media_file_id,
    requested_media_file_id: item.media_file_id,
    media_title: item.media_title,
    media_type: item.media_type,
    series_title: item.series_title || undefined,
    season_number: item.season_number ?? undefined,
    episode_number: item.episode_number ?? undefined,
    play_method: item.play_method,
    video_decision: item.video_decision ?? undefined,
    audio_decision: item.audio_decision ?? undefined,
    position_seconds: item.end_position_seconds,
    file_duration: item.duration_seconds ?? null,
    is_paused: false,
    started_at: item.started_at,
    updated_at: item.ended_at,
    client_ip: item.client_ip ?? undefined,
    client_name: item.client_name ?? undefined,
    client_version: item.client_version ?? undefined,
    device_id: item.device_id ?? undefined,
    device_platform: item.device_platform ?? undefined,
    is_jellyfin_client: item.is_jellyfin_client,
    audio_track_index: 0,
    transcode_audio: audioTranscode,
    target_video_codec: item.target_video_codec ?? undefined,
    target_resolution: item.target_resolution ?? undefined,
    target_audio_codec: item.target_audio_codec ?? undefined,
    target_audio_channels: item.target_audio_channels ?? undefined,
    output_container: item.output_container ?? undefined,
    output_protocol: item.output_protocol ?? undefined,
    target_bitrate_kbps: item.target_bitrate_kbps ?? null,
    transcode_hw_accel: item.transcode_hw_accel ?? undefined,
    tone_map_mode: item.tone_map_mode ?? undefined,
    adaptation_reasons: item.adaptation_reasons ?? [],
    reporting_node: item.reporting_node ?? "",
    stream_location: item.stream_location ?? undefined,
    routing_execution: item.routing_execution ?? undefined,
    routing_execution_node_id: item.routing_execution_node_id ?? undefined,
    routing_execution_node_name: item.routing_execution_node_name ?? undefined,
    routing_egress: item.routing_egress ?? undefined,
    routing_egress_node_id: item.routing_egress_node_id ?? undefined,
    routing_egress_node_name: item.routing_egress_node_name ?? undefined,
    source_video_codec: item.source_video_codec ?? undefined,
    source_video_resolution: item.source_resolution ?? undefined,
    source_audio_codec: item.source_audio_codec ?? undefined,
    source_audio_channels: item.source_audio_channels ?? null,
    source_container: item.source_container ?? undefined,
    source_bitrate_kbps: item.source_bitrate_kbps ?? null,
    stream_bitrate_kbps: item.target_bitrate_kbps ?? item.source_bitrate_kbps ?? null,
  };
}

function normalizeMethodKey(playMethod: string): string {
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

function formatDuration(seconds: number | null | undefined, t: Translate): string {
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

const EM_DASH = "—";

function formatTimestamp(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : formatPreferredDateTime(d);
}

function resolveTitle(item: AdminPlaybackHistoryItem, t: Translate) {
  if (item.series_title) {
    const epTitle = item.media_title === item.series_title ? "" : item.media_title;
    const subtitle = [episodeCode(item.season_number, item.episode_number), epTitle]
      .filter(Boolean)
      .join(" · ");
    return { title: item.series_title, subtitle };
  }
  return {
    title:
      item.media_title ||
      item.media_item_id ||
      t("web.common.activity.title.file", { id: item.media_file_id }),
    subtitle: "",
  };
}

function watchTimeSummary(item: AdminPlaybackHistoryItem) {
  if (item.played_seconds != null) {
    return { seconds: item.played_seconds, estimated: false };
  }
  const elapsed = (Date.parse(item.ended_at) - Date.parse(item.started_at)) / 1000;
  const capped = Number.isFinite(elapsed)
    ? Math.min(item.end_position_seconds, Math.max(elapsed, 0))
    : item.end_position_seconds;
  return { seconds: Math.max(capped, 0), estimated: true };
}

export function SessionDetailSheet({
  open,
  onOpenChange,
  session,
  isLoading = false,
  notFound = false,
  hasPrevious,
  hasNext,
  onPrevious,
  onNext,
}: SessionDetailSheetProps) {
  const t = useT();

  useEffect(() => {
    if (!open) return;
    function handleKeyDown(event: KeyboardEvent) {
      const target = event.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.tagName === "SELECT" ||
          target.isContentEditable)
      ) {
        return;
      }
      if (event.key === "ArrowUp" && hasPrevious) {
        event.preventDefault();
        onPrevious();
      } else if (event.key === "ArrowDown" && hasNext) {
        event.preventDefault();
        onNext();
      }
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, hasPrevious, hasNext, onPrevious, onNext]);

  const adminSession = useMemo(
    () => (session ? historyItemAsAdminSession(session) : null),
    [session],
  );

  const heading = session
    ? resolveTitle(session, t)
    : { title: t("web.admin.history.detail.title"), subtitle: "" };
  const methodMeta = session ? activityMethodMeta(normalizeMethodKey(session.play_method)) : null;
  const reasons = useMemo(
    () => getAdaptationReasonsFromCodes(session?.adaptation_reasons),
    [session?.adaptation_reasons],
  );
  const transcodeMode = adminSession ? formatTranscodeModeSummary(adminSession) : null;
  const toneMap = adminSession ? formatToneMapSummary(adminSession) : null;
  const routeNodes = adminSession ? getSessionRouteNodes(adminSession) : [];
  const watch = session ? watchTimeSummary(session) : null;

  const sourceBitrate = formatSessionBitrate(session?.source_bitrate_kbps);
  const deliveredBitrate =
    session && normalizeMethodKey(session.play_method) === "transcode"
      ? (formatSessionBitrate(session.target_bitrate_kbps) ?? sourceBitrate ?? EM_DASH)
      : (sourceBitrate ?? EM_DASH);

  const subtitleDisplay = session?.subtitle
    ? [
        session.subtitle.language?.toUpperCase(),
        session.subtitle.codec ? formatCodecLabel(session.subtitle.codec) : null,
        session.subtitle.format ? session.subtitle.format.toUpperCase() : null,
      ]
        .filter(Boolean)
        .join(" · ")
    : null;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-0 overflow-hidden p-0 sm:max-w-xl"
      >
        <SheetHeader className="border-border/70 border-b px-5 py-4">
          <div className="flex items-start justify-between gap-2 pe-8">
            <div className="min-w-0 space-y-1">
              <SheetTitle className="truncate text-base font-bold">{heading.title}</SheetTitle>
              <SheetDescription className="truncate text-xs">
                {heading.subtitle || (session ? session.session_id : "")}
              </SheetDescription>
            </div>
            {(hasPrevious || hasNext) && (
              <div className="flex shrink-0 items-center gap-1">
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="h-7 w-7"
                  disabled={!hasPrevious}
                  onClick={onPrevious}
                  aria-label={t("web.admin.history.detail.previousSession")}
                  title={t("web.admin.history.detail.keyboardHint")}
                >
                  <ChevronUp className="h-4 w-4" />
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="h-7 w-7"
                  disabled={!hasNext}
                  onClick={onNext}
                  aria-label={t("web.admin.history.detail.nextSession")}
                  title={t("web.admin.history.detail.keyboardHint")}
                >
                  <ChevronDown className="h-4 w-4" />
                </Button>
              </div>
            )}
          </div>

          {session && methodMeta ? (
            <div className="mt-2 flex flex-wrap items-center gap-1.5">
              <span
                className={cn(
                  "inline-flex items-center rounded-md border px-2 py-0.5 text-[11px] font-semibold",
                  methodMeta.badgeClass,
                )}
              >
                {formatDecisionLabel(session.play_method)}
              </span>
              {session.is_jellyfin_client ? (
                <Badge
                  variant="secondary"
                  title={t("web.admin.history.detail.jellyfinBadgeTitle")}
                  className="px-1.5 py-0 text-[10px] font-bold"
                >
                  {t("web.admin.history.detail.jellyfinBadge")}
                </Badge>
              ) : null}
              <Badge variant={session.completed ? "default" : "outline"} className="text-[11px]">
                {session.completed
                  ? t("web.admin.history.row.completed")
                  : t("web.admin.history.row.partial")}
              </Badge>
              {transcodeMode ? (
                <Badge variant="secondary" className="text-[11px]">
                  {transcodeMode}
                </Badge>
              ) : null}
              {toneMap ? (
                <Badge variant="secondary" className="text-[11px]">
                  {toneMap.badge}
                </Badge>
              ) : null}
            </div>
          ) : null}
        </SheetHeader>

        <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">
          {isLoading ? (
            <div className="space-y-3" aria-label={t("web.admin.history.detail.loading")}>
              <Skeleton className="h-24 w-full rounded-xl" />
              <Skeleton className="h-32 w-full rounded-xl" />
              <Skeleton className="h-28 w-full rounded-xl" />
            </div>
          ) : notFound || !session || !adminSession ? (
            <div className="text-muted-foreground py-12 text-center text-sm">
              {t("web.admin.history.detail.notFound")}
            </div>
          ) : (
            <>
              {session.route_facts_recorded === false ? (
                <div className="border-border/70 bg-muted/35 text-muted-foreground flex items-start gap-2.5 rounded-xl border px-3.5 py-2.5 text-xs leading-relaxed">
                  <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span>{t("web.admin.history.detail.preUpgradeNotice")}</span>
                </div>
              ) : null}

              {/* Source vs Delivered */}
              <section className="space-y-2">
                <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
                  {t("web.admin.history.detail.section.streams")}
                </h3>
                <div className="border-border/70 overflow-hidden rounded-xl border">
                  <table className="w-full text-xs">
                    <thead>
                      <tr className="border-border/60 bg-muted/30 text-muted-foreground border-b">
                        <th className="px-3 py-2 text-start font-medium">
                          {t("web.admin.history.detail.column.stream")}
                        </th>
                        <th className="px-3 py-2 text-start font-medium">
                          {t("web.admin.history.detail.column.source")}
                        </th>
                        <th className="px-3 py-2 text-start font-medium">
                          {t("web.admin.history.detail.column.delivered")}
                        </th>
                      </tr>
                    </thead>
                    <tbody className="divide-border/50 divide-y">
                      <tr>
                        <td className="text-muted-foreground px-3 py-2 font-medium">
                          {t("web.admin.history.detail.row.video")}
                        </td>
                        <td className="px-3 py-2">
                          <div className="flex flex-wrap items-center gap-1.5">
                            <span>{formatVideoSummary(adminSession)}</span>
                            {session.source_video_range_type ? (
                              <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                                {session.source_video_range_type}
                              </Badge>
                            ) : null}
                          </div>
                        </td>
                        <td className="px-3 py-2">{formatDeliveredVideoSummary(adminSession)}</td>
                      </tr>
                      <tr>
                        <td className="text-muted-foreground px-3 py-2 font-medium">
                          {t("web.admin.history.detail.row.audio")}
                        </td>
                        <td className="px-3 py-2">{formatAudioSummary(adminSession)}</td>
                        <td className="px-3 py-2">{formatDeliveredAudioSummary(adminSession)}</td>
                      </tr>
                      <tr>
                        <td className="text-muted-foreground px-3 py-2 font-medium">
                          {t("web.admin.history.detail.row.container")}
                        </td>
                        <td className="px-3 py-2">{formatSourceContainerSummary(adminSession)}</td>
                        <td className="px-3 py-2">
                          {formatDeliveredContainerSummary(adminSession)}
                        </td>
                      </tr>
                      <tr>
                        <td className="text-muted-foreground px-3 py-2 font-medium">
                          {t("web.admin.history.detail.row.bitrate")}
                        </td>
                        <td className="px-3 py-2 tabular-nums">{sourceBitrate ?? EM_DASH}</td>
                        <td className="px-3 py-2 tabular-nums">{deliveredBitrate}</td>
                      </tr>
                      <tr>
                        <td className="text-muted-foreground px-3 py-2 font-medium">
                          {t("web.admin.history.detail.row.subtitles")}
                        </td>
                        <td className="px-3 py-2">
                          {subtitleDisplay ?? t("web.admin.history.detail.subtitlesNone")}
                        </td>
                        <td className="px-3 py-2">
                          {!session.subtitle ? (
                            t("web.admin.history.detail.subtitlesNone")
                          ) : session.subtitle.delivery === "burn_in" ? (
                            <Badge
                              variant="outline"
                              className="border-warning/30 bg-warning/10 text-warning-label text-[10px]"
                            >
                              {t("web.admin.history.detail.subtitlesBurnIn")}
                            </Badge>
                          ) : (
                            t("web.admin.history.detail.subtitlesDirect")
                          )}
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </section>

              {/* Adaptation Reasons */}
              {reasons.length > 0 ? (
                <section className="space-y-2">
                  <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
                    {t("web.admin.history.detail.section.reasons")}
                  </h3>
                  <ul className="space-y-2">
                    {reasons.map((reason) => (
                      <li
                        key={reason.code}
                        className="border-border/60 bg-muted/25 rounded-xl border px-3 py-2"
                      >
                        <div className="text-xs font-semibold">{reason.label}</div>
                        <div className="text-muted-foreground mt-0.5 text-xs">
                          {reason.description}
                        </div>
                      </li>
                    ))}
                  </ul>
                </section>
              ) : null}

              {/* Timing & Watch Progress */}
              <section className="space-y-2">
                <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
                  {t("web.admin.history.detail.section.timing")}
                </h3>
                <dl className="border-border/70 grid grid-cols-2 gap-3 rounded-xl border p-3 text-xs">
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.started")}
                    </dt>
                    <dd className="mt-0.5 font-medium tabular-nums">
                      {formatTimestamp(session.started_at)}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.ended")}
                    </dt>
                    <dd className="mt-0.5 font-medium tabular-nums">
                      {formatTimestamp(session.ended_at)}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.watched")}
                    </dt>
                    <dd
                      className="mt-0.5 font-medium tabular-nums"
                      title={
                        watch?.estimated ? t("web.admin.history.row.estimatedTitle") : undefined
                      }
                    >
                      {watch?.estimated
                        ? t("web.admin.history.row.estimatedDuration", {
                            duration: formatDuration(watch.seconds, t),
                          })
                        : formatDuration(watch?.seconds, t)}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.endPosition")}
                    </dt>
                    <dd className="mt-0.5 font-medium tabular-nums">
                      {formatDuration(session.end_position_seconds, t)}
                      {session.duration_seconds ? (
                        <span className="text-muted-foreground ms-1">
                          {t("web.admin.history.row.ofDuration", {
                            duration: formatDuration(session.duration_seconds, t),
                          })}
                        </span>
                      ) : null}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.paused")}
                    </dt>
                    <dd className="mt-0.5 font-medium tabular-nums">
                      {session.paused_seconds != null
                        ? formatDuration(session.paused_seconds, t)
                        : EM_DASH}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.endReason")}
                    </dt>
                    <dd className="mt-0.5 font-medium">{session.end_reason ?? EM_DASH}</dd>
                  </div>
                </dl>
              </section>

              {/* Client, Device & Route */}
              <section className="space-y-2">
                <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
                  {t("web.admin.history.detail.section.clientRoute")}
                </h3>
                <dl className="border-border/70 grid grid-cols-2 gap-3 rounded-xl border p-3 text-xs">
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.account")}
                    </dt>
                    <dd className="mt-0.5 font-medium">
                      <Link
                        to={`/admin/users/${session.user_id}`}
                        className="hover:text-primary transition-colors hover:underline"
                      >
                        {session.username ||
                          t("web.admin.activity.userFallback", { id: session.user_id })}
                      </Link>
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.profile")}
                    </dt>
                    <dd className="mt-0.5 font-medium">
                      {session.profile_name || session.profile_id}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.library")}
                    </dt>
                    <dd className="mt-0.5 font-medium">{session.library_name ?? EM_DASH}</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.client")}
                    </dt>
                    <dd className="mt-0.5 font-medium">
                      {[session.client_name, session.client_version].filter(Boolean).join(" ") ||
                        EM_DASH}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.platform")}
                    </dt>
                    <dd className="mt-0.5 font-medium">{session.device_platform ?? EM_DASH}</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.network")}
                    </dt>
                    <dd className="mt-0.5 font-medium">
                      {session.stream_location === "remote"
                        ? t("web.admin.history.filter.locationRemote")
                        : session.stream_location === "local"
                          ? t("web.admin.history.filter.locationLocal")
                          : EM_DASH}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.clientIp")}
                    </dt>
                    <dd className="mt-0.5 font-mono text-[11px]">{session.client_ip ?? EM_DASH}</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.route")}
                    </dt>
                    <dd className="mt-0.5 font-medium">
                      {routeNodes.length > 0
                        ? routeNodes.map((n) => `${n.label}: ${n.name}`).join(" → ")
                        : EM_DASH}
                    </dd>
                  </div>
                  <div className="col-span-2">
                    <dt className="text-muted-foreground">
                      {t("web.admin.history.detail.field.sessionId")}
                    </dt>
                    <dd className="mt-0.5 font-mono text-[11px] break-all">{session.session_id}</dd>
                  </div>
                </dl>
              </section>
            </>
          )}
        </div>

        {session ? (
          <SheetFooter className="border-border/70 flex-row flex-wrap items-center justify-between gap-2 border-t px-5 py-3">
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="outline" size="sm" asChild>
                <Link
                  to={`/admin/logs?playback_session_id=${encodeURIComponent(session.session_id)}&focus=playback`}
                >
                  <ScrollText className="h-3.5 w-3.5" />
                  {t("web.admin.history.detail.action.sessionLogs")}
                </Link>
              </Button>
              <Button variant="outline" size="sm" asChild>
                <Link
                  to={`/admin/logs?playback_session_id=${encodeURIComponent(session.session_id)}&focus=playback&component=ffmpeg`}
                >
                  <Terminal className="h-3.5 w-3.5" />
                  {t("web.admin.history.detail.action.ffmpegLogs")}
                </Link>
              </Button>
            </div>
            {session.media_item_id ? (
              <Button variant="ghost" size="sm" asChild>
                <Link to={`/item/${encodeURIComponent(session.media_item_id)}`}>
                  <ExternalLink className="h-3.5 w-3.5" />
                  {t("web.admin.history.detail.action.openMedia")}
                </Link>
              </Button>
            ) : null}
          </SheetFooter>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
