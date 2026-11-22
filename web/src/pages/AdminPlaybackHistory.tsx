import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Flame, History, RefreshCw, Search, X } from "lucide-react";

import { PageHeader } from "@/components/page/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useAdminPlaybackHistoryEntry,
  useAdminPlaybackHistoryPage,
  useAdminUserProfiles,
} from "@/hooks/queries/admin/history";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useT, type Translate } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { formatRelativeTime } from "@/lib/date";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";
import { formatCodecLabel } from "@/lib/mediaFormat";
import { cn } from "@/lib/utils";
import type { MessageKey } from "@/locales/keys";

import {
  formatHistoryDuration,
  historyRowLabel,
  historyRowTitle,
  normalizeMethodKey,
  watchTimeSummary,
} from "./admin-history/historyFormat";
import { SessionDetailSheet } from "./admin-history/SessionDetailSheet";
import { InsightKpiCard } from "./admin-insights/insightsShared";
import { activityMethodMeta, formatDecisionLabel } from "./adminActivityPresentation";
import { getAdaptationReasonByCode } from "./adminActivityReasons";

const ALL_VALUE = "all";
const EM_DASH = "—";
const PAGE_SIZE_OPTIONS = ["25", "50", "100"] as const;
const REFRESH_SPINNER_MIN_VISIBLE_MS = 1_000;

const MEDIA_TYPE_LABELS: Record<string, MessageKey> = {
  movie: "web.admin.history.mediaType.movie",
  episode: "web.admin.history.mediaType.episode",
  series: "web.admin.history.mediaType.series",
};

interface ActiveFilterChip {
  key: string;
  label: string;
  removeKeys: string[];
}

export default function AdminPlaybackHistory() {
  const t = useT();
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: users = [] } = useAdminUsers();
  const { data: libraries = [] } = useAdminLibraries();

  const [pageSize, setPageSize] = useState(25);
  const manualRefreshStartedAtRef = useRef<number | null>(null);
  const [isManualRefreshPending, setIsManualRefreshPending] = useState(false);
  const detailTriggerRef = useRef<HTMLButtonElement | null>(null);

  const selectedUser = searchParams.get("user_id") ?? ALL_VALUE;
  const selectedProfile = searchParams.get("profile_id") ?? ALL_VALUE;
  const selectedLibrary = searchParams.get("library_id") ?? ALL_VALUE;
  const selectedMethod = searchParams.get("play_method") ?? ALL_VALUE;
  const selectedCompleted = searchParams.get("completed") ?? ALL_VALUE;
  const selectedLocation = searchParams.get("stream_location") ?? ALL_VALUE;
  const selectedBurnIn = searchParams.get("subtitle_burn_in") === "true";
  const selectedMediaItemId = searchParams.get("media_item_id")?.trim() ?? "";
  const selectedClientName = searchParams.get("client_name")?.trim() ?? "";
  const selectedPlatform = searchParams.get("device_platform")?.trim() ?? "";
  const selectedCodec = searchParams.get("source_video_codec")?.trim() ?? "";
  const selectedReason = searchParams.get("adaptation_reason")?.trim() ?? "";
  const selectedStartedAfter = searchParams.get("started_after")?.trim() ?? "";
  const selectedStartedBefore = searchParams.get("started_before")?.trim() ?? "";
  const urlSearch = searchParams.get("search")?.trim() ?? "";
  const selectedSessionId = searchParams.get("session")?.trim() || null;

  // Bind the cursor stack to the effective filter fingerprint so any URL
  // navigation (profile link clicks, Back/Forward, filter edits, page size)
  // immediately starts from page 1 rather than sending a mismatched cursor.
  const filterKey = JSON.stringify([
    selectedUser,
    selectedProfile,
    selectedLibrary,
    selectedMethod,
    selectedCompleted,
    selectedLocation,
    selectedBurnIn,
    selectedMediaItemId,
    selectedClientName,
    selectedPlatform,
    selectedCodec,
    selectedReason,
    selectedStartedAfter,
    selectedStartedBefore,
    urlSearch,
    pageSize,
  ]);
  const [cursorState, setCursorState] = useState<{ key: string; stack: string[] }>({
    key: filterKey,
    stack: [],
  });
  const cursorStack = useMemo(
    () => (cursorState.key === filterKey ? cursorState.stack : []),
    [cursorState, filterKey],
  );

  const [searchDraft, setSearchDraft] = useState(urlSearch);
  const [syncedUrlSearch, setSyncedUrlSearch] = useState(urlSearch);
  if (syncedUrlSearch !== urlSearch) {
    setSyncedUrlSearch(urlSearch);
    setSearchDraft(urlSearch);
  }

  useEffect(() => {
    const trimmed = searchDraft.trim();
    if (trimmed === urlSearch) return;
    const timer = window.setTimeout(() => {
      const next = new URLSearchParams(searchParams);
      if (trimmed) {
        next.set("search", trimmed);
      } else {
        next.delete("search");
      }
      setSearchParams(next, { replace: true });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [searchDraft, urlSearch, searchParams, setSearchParams]);

  const selectedUserId = selectedUser !== ALL_VALUE ? Number(selectedUser) : undefined;
  const selectedLibraryId = selectedLibrary !== ALL_VALUE ? Number(selectedLibrary) : undefined;
  const activeCursor = cursorStack.length > 0 ? cursorStack[cursorStack.length - 1] : undefined;

  const history = useAdminPlaybackHistoryPage({
    userId: selectedUserId,
    profileId: selectedProfile !== ALL_VALUE ? selectedProfile : undefined,
    libraryId: selectedLibraryId,
    mediaItemId: selectedMediaItemId || undefined,
    playMethod: selectedMethod !== ALL_VALUE ? selectedMethod : undefined,
    completed:
      selectedCompleted === "true" || selectedCompleted === "false" ? selectedCompleted : "all",
    streamLocation:
      selectedLocation === "local" || selectedLocation === "remote" ? selectedLocation : "all",
    subtitleBurnIn: selectedBurnIn ? "true" : "all",
    clientName: selectedClientName || undefined,
    devicePlatform: selectedPlatform || undefined,
    sourceVideoCodec: selectedCodec || undefined,
    adaptationReason: selectedReason || undefined,
    startedAfter: selectedStartedAfter || undefined,
    startedBefore: selectedStartedBefore || undefined,
    search: urlSearch || undefined,
    cursor: activeCursor,
    limit: pageSize,
  });

  const profiles = useAdminUserProfiles(selectedUserId);
  const { refetch: refetchHistory } = history;

  const refreshHistory = useCallback(async () => {
    manualRefreshStartedAtRef.current = Date.now();
    setIsManualRefreshPending(true);
    try {
      await refetchHistory();
    } finally {
      const startedAt = manualRefreshStartedAtRef.current;
      if (startedAt !== null) {
        const remaining = REFRESH_SPINNER_MIN_VISIBLE_MS - (Date.now() - startedAt);
        if (remaining > 0) {
          await delay(remaining);
        }
      }
      manualRefreshStartedAtRef.current = null;
      setIsManualRefreshPending(false);
    }
  }, [refetchHistory]);

  // Only clear an orphan profile_id when no account is selected. When an
  // account IS selected, preserve historical/deleted profile_id filters from
  // Insights drill-downs instead of silently widening to the whole account.
  useEffect(() => {
    if (selectedUser === ALL_VALUE && selectedProfile !== ALL_VALUE) {
      const next = new URLSearchParams(searchParams);
      next.delete("profile_id");
      setSearchParams(next, { replace: true });
    }
  }, [searchParams, selectedProfile, selectedUser, setSearchParams]);

  const updateFilter = useCallback(
    (key: string, value: string) => {
      const next = new URLSearchParams(searchParams);
      if (!value || value === ALL_VALUE) {
        next.delete(key);
      } else {
        next.set(key, value);
      }
      if (key === "user_id") {
        next.delete("profile_id");
      }
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams],
  );

  const removeFilterKeys = useCallback(
    (keys: string[]) => {
      const next = new URLSearchParams(searchParams);
      for (const k of keys) next.delete(k);
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams],
  );

  const resetFilters = useCallback(() => {
    setSearchDraft("");
    setSearchParams(new URLSearchParams(), { replace: true });
  }, [setSearchParams]);

  const selectSession = useCallback(
    (sessionId: string | null, trigger?: HTMLButtonElement | null) => {
      if (trigger) {
        detailTriggerRef.current = trigger;
      }
      const next = new URLSearchParams(searchParams);
      if (sessionId) {
        next.set("session", sessionId);
      } else {
        next.delete("session");
      }
      setSearchParams(next, { replace: true });
      if (!sessionId && detailTriggerRef.current) {
        const el = detailTriggerRef.current;
        detailTriggerRef.current = null;
        window.requestAnimationFrame(() => el.focus());
      }
    },
    [searchParams, setSearchParams],
  );

  const rows = useMemo(() => history.data?.items ?? [], [history.data?.items]);
  const hasMore = history.data?.hasMore ?? false;
  const nextCursor = history.data?.nextCursor ?? null;

  const missingProfileFallbackLabel = useMemo(() => {
    if (selectedUser === ALL_VALUE || selectedProfile === ALL_VALUE) return null;
    if ((profiles.data ?? []).some((p) => p.id === selectedProfile)) return null;
    const fromRow = rows.find((r) => r.profile_id === selectedProfile)?.profile_name;
    return fromRow || selectedProfile;
  }, [profiles.data, rows, selectedProfile, selectedUser]);

  const selectedRowIndex = useMemo(
    () => (selectedSessionId ? rows.findIndex((r) => r.session_id === selectedSessionId) : -1),
    [rows, selectedSessionId],
  );
  const inPageSession = selectedRowIndex >= 0 ? (rows[selectedRowIndex] ?? null) : null;
  const detailQuery = useAdminPlaybackHistoryEntry(
    selectedSessionId,
    Boolean(selectedSessionId) && !inPageSession,
  );
  const activeSession = inPageSession ?? detailQuery.data ?? null;

  const activeChips = useMemo<ActiveFilterChip[]>(() => {
    const chips: ActiveFilterChip[] = [];
    if (selectedClientName) {
      chips.push({
        key: "client_name",
        label: t("web.admin.history.filter.activeChip.client", { value: selectedClientName }),
        removeKeys: ["client_name"],
      });
    }
    if (selectedPlatform) {
      chips.push({
        key: "device_platform",
        label: t("web.admin.history.filter.activeChip.platform", { value: selectedPlatform }),
        removeKeys: ["device_platform"],
      });
    }
    if (selectedCodec) {
      chips.push({
        key: "source_video_codec",
        label: t("web.admin.history.filter.activeChip.codec", {
          value: formatCodecLabel(selectedCodec) || selectedCodec.toUpperCase(),
        }),
        removeKeys: ["source_video_codec"],
      });
    }
    if (selectedReason) {
      const reasonLabel = getAdaptationReasonByCode(selectedReason)?.label ?? selectedReason;
      chips.push({
        key: "adaptation_reason",
        label: t("web.admin.history.filter.activeChip.reason", { value: reasonLabel }),
        removeKeys: ["adaptation_reason"],
      });
    }
    if (selectedStartedAfter && selectedStartedBefore) {
      chips.push({
        key: "date_range",
        label: t("web.admin.history.filter.activeChip.dateRange", {
          from: formatChipDate(selectedStartedAfter),
          to: formatChipDate(selectedStartedBefore),
        }),
        removeKeys: ["started_after", "started_before"],
      });
    } else if (selectedStartedAfter) {
      chips.push({
        key: "started_after",
        label: t("web.admin.history.filter.activeChip.fromDate", {
          from: formatChipDate(selectedStartedAfter),
        }),
        removeKeys: ["started_after"],
      });
    } else if (selectedStartedBefore) {
      chips.push({
        key: "started_before",
        label: t("web.admin.history.filter.activeChip.toDate", {
          to: formatChipDate(selectedStartedBefore),
        }),
        removeKeys: ["started_before"],
      });
    }
    return chips;
  }, [
    selectedClientName,
    selectedPlatform,
    selectedCodec,
    selectedReason,
    selectedStartedAfter,
    selectedStartedBefore,
    t,
  ]);

  const activeMediaItemLabel = rows[0] ? historyRowLabel(rows[0], t) : selectedMediaItemId;

  return (
    <div className="space-y-6 pb-4 sm:pb-6">
      <PageHeader
        title={t("web.admin.history.title")}
        subtitle={t("web.admin.history.subtitle")}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={resetFilters}>
              {t("web.admin.history.filter.reset")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="min-w-[8.25rem] justify-center"
              onClick={() => {
                void refreshHistory();
              }}
              disabled={isManualRefreshPending}
              aria-busy={isManualRefreshPending}
            >
              <RefreshCw className={cn("h-3.5 w-3.5", isManualRefreshPending && "animate-spin")} />
              {isManualRefreshPending
                ? t("web.admin.activity.page.refreshing")
                : t("web.admin.activity.page.refresh")}
            </Button>
          </div>
        }
      />

      {/* Multi-facet Filter Bar */}
      <div className="surface-panel space-y-3 rounded-2xl border-0 p-3.5">
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-[220px] flex-1">
            <Search className="text-muted-foreground pointer-events-none absolute start-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2" />
            <Input
              value={searchDraft}
              onChange={(e) => setSearchDraft(e.target.value)}
              placeholder={t("web.admin.history.filter.searchPlaceholder")}
              className="h-9 ps-8 text-xs"
            />
          </div>

          <Select value={selectedUser} onValueChange={(value) => updateFilter("user_id", value)}>
            <SelectTrigger className="h-9 w-[160px] text-xs">
              <SelectValue placeholder={t("web.admin.history.filter.allUsers")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_VALUE}>{t("web.admin.history.filter.allUsers")}</SelectItem>
              {users.map((user) => (
                <SelectItem key={user.id} value={String(user.id)}>
                  {user.username}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select
            value={selectedProfile}
            onValueChange={(value) => updateFilter("profile_id", value)}
            disabled={selectedUser === ALL_VALUE}
          >
            <SelectTrigger className="h-9 w-[155px] text-xs">
              <SelectValue
                placeholder={
                  selectedUser === ALL_VALUE
                    ? t("web.admin.history.filter.chooseUserFirst")
                    : t("web.admin.history.filter.allProfiles")
                }
              />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_VALUE}>{t("web.admin.history.filter.allProfiles")}</SelectItem>
              {missingProfileFallbackLabel ? (
                <SelectItem value={selectedProfile}>{missingProfileFallbackLabel}</SelectItem>
              ) : null}
              {(profiles.data ?? []).map((profile) => (
                <SelectItem key={profile.id} value={profile.id}>
                  {profile.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select
            value={selectedLibrary}
            onValueChange={(value) => updateFilter("library_id", value)}
          >
            <SelectTrigger className="h-9 w-[155px] text-xs">
              <SelectValue placeholder={t("web.admin.history.filter.allLibraries")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_VALUE}>
                {t("web.admin.history.filter.allLibraries")}
              </SelectItem>
              {libraries.map((lib) => (
                <SelectItem key={lib.id} value={String(lib.id)}>
                  {lib.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select
            value={selectedMethod}
            onValueChange={(value) => updateFilter("play_method", value)}
          >
            <SelectTrigger className="h-9 w-[140px] text-xs">
              <SelectValue placeholder={t("web.admin.history.filter.allMethods")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_VALUE}>{t("web.admin.history.filter.allMethods")}</SelectItem>
              <SelectItem value="direct">{t("web.admin.activity.decision.direct")}</SelectItem>
              <SelectItem value="remux">{t("web.admin.activity.decision.remux")}</SelectItem>
              <SelectItem value="transcode">
                {t("web.admin.activity.decision.transcode")}
              </SelectItem>
            </SelectContent>
          </Select>

          <Select
            value={selectedCompleted}
            onValueChange={(value) => updateFilter("completed", value)}
          >
            <SelectTrigger className="h-9 w-[135px] text-xs">
              <SelectValue placeholder={t("web.admin.history.filter.allAttempts")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_VALUE}>{t("web.admin.history.filter.allAttempts")}</SelectItem>
              <SelectItem value="true">{t("web.admin.history.filter.completed")}</SelectItem>
              <SelectItem value="false">{t("web.admin.history.filter.partial")}</SelectItem>
            </SelectContent>
          </Select>

          <Select
            value={selectedLocation}
            onValueChange={(value) => updateFilter("stream_location", value)}
          >
            <SelectTrigger className="h-9 w-[135px] text-xs">
              <SelectValue placeholder={t("web.admin.history.filter.allLocations")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_VALUE}>
                {t("web.admin.history.filter.allLocations")}
              </SelectItem>
              <SelectItem value="local">{t("web.admin.history.filter.locationLocal")}</SelectItem>
              <SelectItem value="remote">{t("web.admin.history.filter.locationRemote")}</SelectItem>
            </SelectContent>
          </Select>

          <Button
            type="button"
            variant={selectedBurnIn ? "secondary" : "outline"}
            size="sm"
            aria-pressed={selectedBurnIn}
            onClick={() => updateFilter("subtitle_burn_in", selectedBurnIn ? ALL_VALUE : "true")}
            className="h-9 gap-1.5 text-xs"
          >
            <Flame className="h-3.5 w-3.5" />
            {t("web.admin.history.filter.burnInOnly")}
          </Button>
        </div>

        {(selectedMediaItemId || activeChips.length > 0) && (
          <div className="flex flex-wrap items-center gap-2 pt-0.5">
            {selectedMediaItemId ? (
              <div className="border-border bg-muted/40 inline-flex items-center gap-2 rounded-md border px-2.5 py-1 text-xs">
                <span className="text-muted-foreground font-medium">
                  {t("web.admin.history.itemFilterActive")}
                </span>
                <Link
                  to={`/item/${encodeURIComponent(selectedMediaItemId)}`}
                  className="hover:text-primary font-semibold transition-colors hover:underline"
                >
                  {activeMediaItemLabel}
                </Link>
                <button
                  type="button"
                  onClick={() => removeFilterKeys(["media_item_id"])}
                  aria-label={t("web.admin.history.filter.removeChip", {
                    label: activeMediaItemLabel,
                  })}
                  className="text-muted-foreground hover:text-foreground cursor-pointer"
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
            ) : null}
            {activeChips.map((chip) => (
              <span
                key={chip.key}
                className="border-border bg-muted/40 inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs"
              >
                <span>{chip.label}</span>
                <button
                  type="button"
                  onClick={() => removeFilterKeys(chip.removeKeys)}
                  aria-label={t("web.admin.history.filter.removeChip", { label: chip.label })}
                  className="text-muted-foreground hover:text-foreground cursor-pointer"
                >
                  <X className="h-3 w-3" />
                </button>
              </span>
            ))}
          </div>
        )}
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <InsightKpiCard
          label={t("web.admin.history.stats.visibleRows")}
          value={rows.length.toLocaleString(formatLocale())}
        />
        <InsightKpiCard
          label={t("web.admin.history.stats.completed")}
          value={rows.filter((row) => row.completed).length.toLocaleString(formatLocale())}
        />
        <InsightKpiCard
          label={t("web.admin.history.stats.partial")}
          value={rows.filter((row) => !row.completed).length.toLocaleString(formatLocale())}
        />
      </div>

      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="flex flex-row items-center justify-between space-y-0">
          <CardTitle className="text-sm font-bold">
            {t("web.admin.history.recent.heading")}
          </CardTitle>
          <div className="text-muted-foreground text-xs">
            {history.isFetching
              ? t("web.admin.activity.page.refreshing")
              : t("web.admin.history.recent.autoRefreshing")}
          </div>
        </CardHeader>
        <CardContent>
          {history.isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-10 w-full rounded-lg" />
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-12 w-full rounded-lg" />
              ))}
            </div>
          ) : history.error ? (
            <div className="text-destructive py-8 text-center text-sm">
              {history.error instanceof Error
                ? history.error.message
                : t("web.admin.history.loadError")}
            </div>
          ) : (
            <>
              {rows.length === 0 ? (
                <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
                  <History className="text-muted-foreground/50 h-10 w-10" />
                  <div className="space-y-1">
                    <p className="text-sm font-medium">{t("web.admin.history.empty.title")}</p>
                    <p className="text-muted-foreground max-w-sm text-xs">
                      {t("web.admin.history.empty.body")}
                    </p>
                  </div>
                </div>
              ) : (
                <div className="overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t("web.admin.history.column.media")}</TableHead>
                        <TableHead>{t("web.admin.history.column.user")}</TableHead>
                        <TableHead>{t("web.admin.history.column.profile")}</TableHead>
                        <TableHead>{t("web.admin.history.column.client")}</TableHead>
                        <TableHead>{t("web.admin.history.column.method")}</TableHead>
                        <TableHead>{t("web.admin.history.column.watchTime")}</TableHead>
                        <TableHead>{t("web.admin.history.column.status")}</TableHead>
                        <TableHead>{t("web.admin.history.column.ended")}</TableHead>
                        <TableHead className="text-end">
                          {t("web.admin.history.column.logs")}
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {rows.map((row) => {
                        const watch = watchTimeSummary(row);
                        const { title, detail } = historyRowTitle(row, t);
                        const isSelected = row.session_id === selectedSessionId;
                        const methodMeta = activityMethodMeta(normalizeMethodKey(row.play_method));
                        const titleBlock = (
                          <>
                            {title}
                            {detail ? (
                              <div className="text-muted-foreground font-normal">{detail}</div>
                            ) : null}
                          </>
                        );
                        const profileLabel = row.profile_name || row.profile_id;
                        return (
                          <TableRow
                            key={row.session_id}
                            onClick={(e) => {
                              if ((e.target as HTMLElement).closest("a, button")) return;
                              selectSession(row.session_id);
                            }}
                            className={cn(
                              "cursor-pointer transition-colors",
                              isSelected && "bg-primary/8 hover:bg-primary/10",
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
                                {row.username ||
                                  t("web.admin.activity.userFallback", { id: row.user_id })}
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
                                <div className="text-muted-foreground text-xs">
                                  {row.profile_id}
                                </div>
                              </div>
                            </TableCell>
                            <TableCell>
                              <div className="space-y-0.5">
                                <div className="flex items-center gap-1.5 text-xs font-medium">
                                  <span>{row.client_name ?? EM_DASH}</span>
                                  {row.is_jellyfin_client ? (
                                    <Badge
                                      variant="secondary"
                                      title={t("web.admin.history.detail.jellyfinBadgeTitle")}
                                      className="px-1 py-0 text-[10px] font-bold"
                                    >
                                      {t("web.admin.history.detail.jellyfinBadge")}
                                    </Badge>
                                  ) : null}
                                </div>
                                {row.device_platform ? (
                                  <div className="text-muted-foreground text-[11px]">
                                    {row.device_platform}
                                  </div>
                                ) : null}
                              </div>
                            </TableCell>
                            <TableCell>
                              <span
                                className={cn(
                                  "inline-flex items-center rounded-md border px-2 py-0.5 text-[11px] font-semibold",
                                  methodMeta.badgeClass,
                                )}
                              >
                                {formatDecisionLabel(row.play_method)}
                              </span>
                            </TableCell>
                            <TableCell>
                              <div className="space-y-1 tabular-nums">
                                <div
                                  title={
                                    watch.estimated
                                      ? t("web.admin.history.row.estimatedTitle")
                                      : undefined
                                  }
                                >
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
                                  onClick={(e) => selectSession(row.session_id, e.currentTarget)}
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
                      })}
                    </TableBody>
                  </Table>
                </div>
              )}

              {rows.length > 0 || cursorStack.length > 0 ? (
                <div className="flex flex-wrap items-center justify-between gap-3 px-2 py-4">
                  <div className="flex items-center gap-4">
                    <span className="text-muted-foreground text-sm tabular-nums">
                      {t("web.admin.history.pagination.summary", {
                        count: rows.length,
                        count_text: rows.length.toLocaleString(formatLocale()),
                      })}
                    </span>
                    <Select
                      value={String(pageSize)}
                      onValueChange={(v) => {
                        setPageSize(Number(v));
                      }}
                    >
                      <SelectTrigger className="h-8 min-w-[100px]">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {PAGE_SIZE_OPTIONS.map((size) => (
                          <SelectItem key={size} value={size}>
                            {t("web.admin.history.pagination.pageSize", { count: Number(size) })}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="flex gap-2">
                    {cursorStack.length > 1 ? (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => setCursorState({ key: filterKey, stack: [] })}
                      >
                        {t("web.admin.history.pagination.firstPage")}
                      </Button>
                    ) : null}
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() =>
                        setCursorState({
                          key: filterKey,
                          stack: cursorStack.slice(0, -1),
                        })
                      }
                      disabled={cursorStack.length === 0}
                    >
                      {t("web.admin.history.pagination.previous")}
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        if (nextCursor) {
                          setCursorState({
                            key: filterKey,
                            stack: [...cursorStack, nextCursor],
                          });
                        }
                      }}
                      disabled={!hasMore || !nextCursor}
                    >
                      {t("web.admin.history.pagination.next")}
                    </Button>
                  </div>
                </div>
              ) : null}
            </>
          )}
        </CardContent>
      </Card>

      <SessionDetailSheet
        open={Boolean(selectedSessionId)}
        onOpenChange={(open) => {
          if (!open) selectSession(null);
        }}
        session={activeSession}
        isLoading={Boolean(selectedSessionId) && !inPageSession && detailQuery.isLoading}
        loadError={Boolean(selectedSessionId) && !inPageSession ? detailQuery.error : undefined}
        onRetry={() => {
          void detailQuery.refetch();
        }}
        notFound={
          Boolean(selectedSessionId) &&
          !inPageSession &&
          !detailQuery.isLoading &&
          !detailQuery.isError &&
          activeSession === null
        }
        hasPrevious={selectedRowIndex > 0}
        hasNext={selectedRowIndex >= 0 && selectedRowIndex < rows.length - 1}
        onPrevious={() => {
          if (selectedRowIndex > 0) {
            const prev = rows[selectedRowIndex - 1];
            if (prev) selectSession(prev.session_id);
          }
        }}
        onNext={() => {
          if (selectedRowIndex >= 0 && selectedRowIndex < rows.length - 1) {
            const next = rows[selectedRowIndex + 1];
            if (next) selectSession(next.session_id);
          }
        }}
      />
    </div>
  );
}

function formatChipDate(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleDateString(formatLocale(), {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
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

function delay(ms: number) {
  return new Promise<void>((resolve) => {
    window.setTimeout(resolve, ms);
  });
}
