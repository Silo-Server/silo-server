import { Fragment, useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  ChevronDown,
  ChevronRight,
  Loader2,
  Search,
} from "lucide-react";

import { formatWatchTime } from "@/components/admin/dashboard/format";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAdminInsightsPeopleInfinite } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";

import { AddToDashboardButton } from "./InsightsDashboardPins";
import {
  buildHistoryDrilldownHref,
  buildInsightsWindowQueryParams,
  formatRatePercentNumber,
  formatShortTimestamp,
  insightsHrefWith,
  type InsightsFilterState,
  insightRefreshProps,
} from "./insightsHelpers";
import { InsightKpiCard, InsightNoticeBanner, InsightSectionError } from "./insightsShared";

type PeopleSort = "watch_time" | "plays" | "direct_pct" | "completion_pct" | "last_seen";
type SortDir = "asc" | "desc";
const EM_DASH = "—";

export function PeopleTab({ filters }: { filters: InsightsFilterState }) {
  const t = useT();
  const [searchParams] = useSearchParams();
  const [searchInput, setSearchInput] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [sort, setSort] = useState<PeopleSort>("watch_time");
  const [dir, setDir] = useState<SortDir>("desc");
  const [expandedUsers, setExpandedUsers] = useState<Record<string, boolean>>({});

  useEffect(() => {
    const trimmed = searchInput.trim();
    const timer = window.setTimeout(() => {
      setDebouncedSearch(trimmed);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [searchInput]);

  const queryParams = useMemo(
    () => ({
      ...buildInsightsWindowQueryParams(filters, { omitUser: true }),
      ...(debouncedSearch ? { search: debouncedSearch } : {}),
      sort,
      dir,
    }),
    [filters, debouncedSearch, sort, dir],
  );

  const query = useAdminInsightsPeopleInfinite(queryParams);
  const data = query.data?.pages[0];
  const accounts = useMemo(() => {
    const pages = query.data?.pages ?? [];
    const seen = new Set<string>();
    const merged: NonNullable<typeof data>["accounts"] = [];
    for (const page of pages) {
      for (const account of page.accounts) {
        if (!seen.has(account.user_id)) {
          seen.add(account.user_id);
          merged.push(account);
        }
      }
    }
    return merged;
  }, [query.data?.pages]);

  function handleSort(column: PeopleSort) {
    if (sort === column) {
      setDir((prev) => (prev === "desc" ? "asc" : "desc"));
    } else {
      setSort(column);
      setDir("desc");
    }
  }

  function toggleExpanded(userId: string) {
    setExpandedUsers((prev) => ({ ...prev, [userId]: !prev[userId] }));
  }

  function renderSortIcon(column: PeopleSort) {
    if (sort !== column) {
      return <ArrowUpDown className="text-muted-foreground/60 h-3 w-3" />;
    }
    return dir === "desc" ? (
      <ArrowDown className="text-primary h-3 w-3" />
    ) : (
      <ArrowUp className="text-primary h-3 w-3" />
    );
  }

  // A new sort, search, or filter keeps the previous rows on screen until its
  // first page arrives.
  const refreshing = query.isPlaceholderData;
  const coverage = data?.coverage;
  const showMeasuredBanner = Boolean(
    coverage && coverage.total_sessions > 0 && coverage.measured_sessions < coverage.total_sessions,
  );

  return (
    <div className="space-y-5">
      {showMeasuredBanner && coverage ? (
        <InsightNoticeBanner
          message={t("web.admin.insights.coverage.watchTimePartial", {
            measured: coverage.measured_sessions.toLocaleString(formatLocale()),
            total: coverage.total_sessions.toLocaleString(formatLocale()),
            total_count: coverage.total_sessions,
          })}
        />
      ) : null}

      <div {...insightRefreshProps(refreshing, "space-y-5")}>
        {data ? (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <InsightKpiCard
              label={t("web.admin.insights.people.kpi.activeAccounts")}
              value={data.active_accounts.toLocaleString(formatLocale())}
            />
            <InsightKpiCard
              label={t("web.admin.insights.people.kpi.activeProfiles")}
              value={data.active_profiles.toLocaleString(formatLocale())}
            />
            <InsightKpiCard
              label={t("web.admin.insights.people.kpi.totalWatchTime")}
              value={formatWatchTime(data.total_watch_seconds)}
            />
          </div>
        ) : query.isLoading ? (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-24 rounded-2xl" />
            ))}
          </div>
        ) : null}

        {/* Keep the search input card mounted across refetches so typing never loses focus. */}
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-3 space-y-0">
            <div className="relative w-full max-w-xs">
              <Search className="text-muted-foreground pointer-events-none absolute start-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2" />
              <Input
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                placeholder={t("web.admin.insights.people.searchPlaceholder")}
                className="h-8 ps-8 pe-8 text-xs"
              />
              {refreshing ? (
                <Loader2
                  aria-hidden="true"
                  className="text-muted-foreground pointer-events-none absolute end-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 animate-spin"
                />
              ) : null}
            </div>
            <AddToDashboardButton
              widgetId="top-profiles"
              range={filters.range}
              chartName={t("web.admin.dashboard.widgetCatalog.topProfiles.title")}
            />
          </CardHeader>
          <CardContent>
            {query.isLoading ? (
              <div className="space-y-3">
                <Skeleton className="h-9 w-full rounded-lg" />
                {Array.from({ length: 5 }).map((_, i) => (
                  <Skeleton key={i} className="h-12 w-full rounded-lg" />
                ))}
              </div>
            ) : query.error || !data ? (
              <InsightSectionError
                error={query.error}
                onRetry={() => {
                  void query.refetch();
                }}
              />
            ) : accounts.length === 0 ? (
              <div className="text-muted-foreground py-12 text-center text-sm">
                {t("web.admin.insights.people.empty")}
              </div>
            ) : (
              <>
                <div className="overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t("web.admin.insights.people.column.account")}</TableHead>
                        <TableHead className="text-end">
                          <button
                            type="button"
                            onClick={() => handleSort("watch_time")}
                            className="hover:text-foreground inline-flex cursor-pointer items-center gap-1"
                          >
                            {t("web.admin.insights.people.column.watchTime")}
                            {renderSortIcon("watch_time")}
                          </button>
                        </TableHead>
                        <TableHead className="text-end">
                          <button
                            type="button"
                            onClick={() => handleSort("plays")}
                            className="hover:text-foreground inline-flex cursor-pointer items-center gap-1"
                          >
                            {t("web.admin.insights.people.column.plays")}
                            {renderSortIcon("plays")}
                          </button>
                        </TableHead>
                        <TableHead className="text-end">
                          <button
                            type="button"
                            onClick={() => handleSort("direct_pct")}
                            className="hover:text-foreground inline-flex cursor-pointer items-center gap-1"
                          >
                            {t("web.admin.insights.people.column.directRate")}
                            {renderSortIcon("direct_pct")}
                          </button>
                        </TableHead>
                        <TableHead className="text-end">
                          <button
                            type="button"
                            onClick={() => handleSort("completion_pct")}
                            className="hover:text-foreground inline-flex cursor-pointer items-center gap-1"
                          >
                            {t("web.admin.insights.people.column.completionRate")}
                            {renderSortIcon("completion_pct")}
                          </button>
                        </TableHead>
                        <TableHead className="text-end">
                          <button
                            type="button"
                            onClick={() => handleSort("last_seen")}
                            className="hover:text-foreground inline-flex cursor-pointer items-center gap-1"
                          >
                            {t("web.admin.insights.people.column.lastSeen")}
                            {renderSortIcon("last_seen")}
                          </button>
                        </TableHead>
                        <TableHead className="text-end">
                          {t("web.admin.insights.people.column.actions")}
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {accounts.map((account) => {
                        const hasProfiles = account.profiles.length > 0;
                        const isExpanded = Boolean(expandedUsers[account.user_id]);
                        const accountHistoryHref = buildHistoryDrilldownHref({
                          userId: account.user_id,
                          libraryId: filters.libraryId,
                          startedAfter: data.from,
                          startedBefore: data.to,
                        });
                        return (
                          <Fragment key={account.user_id}>
                            <TableRow>
                              <TableCell>
                                <div className="flex items-center gap-2">
                                  {hasProfiles ? (
                                    <button
                                      type="button"
                                      onClick={() => toggleExpanded(account.user_id)}
                                      aria-expanded={isExpanded}
                                      aria-label={t(
                                        isExpanded
                                          ? "web.admin.insights.people.collapseProfiles"
                                          : "web.admin.insights.people.expandProfiles",
                                        { account: account.username },
                                      )}
                                      className="text-muted-foreground hover:text-foreground hover:bg-muted/60 flex h-6 w-6 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors"
                                    >
                                      {isExpanded ? (
                                        <ChevronDown className="h-3.5 w-3.5" />
                                      ) : (
                                        <ChevronRight className="h-3.5 w-3.5" />
                                      )}
                                    </button>
                                  ) : (
                                    <span className="w-6 shrink-0" />
                                  )}
                                  <div className="min-w-0 space-y-0.5">
                                    <div className="flex flex-wrap items-center gap-1.5">
                                      <Link
                                        to={`/admin/users/${account.user_id}`}
                                        className="hover:text-primary font-semibold transition-colors hover:underline"
                                      >
                                        {account.username}
                                      </Link>
                                      {account.is_live ? (
                                        <span className="border-success/30 bg-success/15 text-success-label inline-flex items-center gap-1 rounded-full border px-1.5 py-0 text-[0.625rem] font-semibold">
                                          <span className="bg-success h-1.5 w-1.5 rounded-full" />
                                          {t("web.admin.insights.people.liveBadge")}
                                        </span>
                                      ) : null}
                                      {account.profiles.length > 1 ? (
                                        <Badge variant="outline" className="text-[0.625rem]">
                                          {t("web.admin.insights.people.profilesCount", {
                                            count: account.profiles.length,
                                          })}
                                        </Badge>
                                      ) : null}
                                    </div>
                                    {account.top_client ? (
                                      <div className="text-muted-foreground text-[0.6875rem]">
                                        {account.top_client}
                                      </div>
                                    ) : null}
                                  </div>
                                </div>
                              </TableCell>
                              <TableCell className="text-end font-semibold tabular-nums">
                                {formatWatchTime(account.watch_seconds)}
                              </TableCell>
                              <TableCell className="text-end tabular-nums">
                                {account.plays.toLocaleString(formatLocale())}
                              </TableCell>
                              <TableCell className="text-end tabular-nums">
                                {t("web.admin.insights.percent", {
                                  value: formatRatePercentNumber(account.direct_rate),
                                })}
                              </TableCell>
                              <TableCell className="text-end tabular-nums">
                                {t("web.admin.insights.percent", {
                                  value: formatRatePercentNumber(account.completion_rate),
                                })}
                              </TableCell>
                              <TableCell
                                className="text-muted-foreground text-end text-xs tabular-nums"
                                title={formatShortTimestamp(account.last_seen_at)}
                              >
                                {account.last_seen_at
                                  ? (formatRelativeTime(account.last_seen_at, {
                                      rounding: "floor",
                                    }) ?? formatShortTimestamp(account.last_seen_at))
                                  : EM_DASH}
                              </TableCell>
                              <TableCell className="text-end">
                                <div className="flex items-center justify-end gap-3">
                                  <Link
                                    to={insightsHrefWith(searchParams, {
                                      person: String(account.user_id),
                                      profile: null,
                                      title: null,
                                    })}
                                    aria-label={t("web.admin.insights.people.action.detailsFor", {
                                      name: account.username,
                                    })}
                                    className="text-primary text-xs font-medium hover:underline"
                                  >
                                    {t("web.admin.insights.people.action.details")}
                                  </Link>
                                  <Link
                                    to={accountHistoryHref}
                                    className="text-primary text-xs font-medium hover:underline"
                                  >
                                    {t("web.admin.insights.openHistory")}
                                  </Link>
                                </div>
                              </TableCell>
                            </TableRow>

                            {isExpanded &&
                              account.profiles.map((profile) => {
                                const profileDirectRate =
                                  profile.plays > 0 ? profile.direct_plays / profile.plays : 0;
                                const profileLabel =
                                  profile.profile_name ||
                                  t("web.admin.insights.people.unnamedProfile", {
                                    id: profile.profile_id.slice(0, 8),
                                  });
                                const profileHistoryHref = buildHistoryDrilldownHref({
                                  userId: account.user_id,
                                  profileId: profile.profile_id,
                                  libraryId: filters.libraryId,
                                  startedAfter: data.from,
                                  startedBefore: data.to,
                                });
                                return (
                                  <TableRow
                                    key={`${account.user_id}:${profile.profile_id}`}
                                    className="bg-muted/25 hover:bg-muted/35"
                                  >
                                    <TableCell className="ps-10">
                                      <div className="flex items-center gap-1.5 text-xs">
                                        <span className="font-medium">{profileLabel}</span>
                                        {profile.is_live ? (
                                          <span
                                            className={cn(
                                              "bg-success inline-block h-1.5 w-1.5 rounded-full",
                                            )}
                                            title={t("web.admin.insights.people.liveBadge")}
                                          />
                                        ) : null}
                                      </div>
                                    </TableCell>
                                    <TableCell className="text-end text-xs tabular-nums">
                                      {formatWatchTime(profile.watch_seconds)}
                                    </TableCell>
                                    <TableCell className="text-end text-xs tabular-nums">
                                      {profile.plays.toLocaleString(formatLocale())}
                                    </TableCell>
                                    <TableCell className="text-end text-xs tabular-nums">
                                      {t("web.admin.insights.percent", {
                                        value: formatRatePercentNumber(profileDirectRate),
                                      })}
                                    </TableCell>
                                    <TableCell className="text-end text-xs tabular-nums">
                                      {t("web.admin.insights.percent", {
                                        value: formatRatePercentNumber(profile.completion_rate),
                                      })}
                                    </TableCell>
                                    <TableCell
                                      className="text-muted-foreground text-end text-xs tabular-nums"
                                      title={formatShortTimestamp(profile.last_seen_at)}
                                    >
                                      {profile.last_seen_at
                                        ? (formatRelativeTime(profile.last_seen_at, {
                                            rounding: "floor",
                                          }) ?? formatShortTimestamp(profile.last_seen_at))
                                        : EM_DASH}
                                    </TableCell>
                                    <TableCell className="text-end">
                                      <div className="flex items-center justify-end gap-3">
                                        <Link
                                          to={insightsHrefWith(searchParams, {
                                            person: String(account.user_id),
                                            profile: profile.profile_id,
                                            title: null,
                                          })}
                                          aria-label={t(
                                            "web.admin.insights.people.action.detailsFor",
                                            {
                                              name: profileLabel,
                                            },
                                          )}
                                          className="text-primary text-xs hover:underline"
                                        >
                                          {t("web.admin.insights.people.action.details")}
                                        </Link>
                                        <Link
                                          to={profileHistoryHref}
                                          className="text-primary text-xs hover:underline"
                                        >
                                          {t("web.admin.insights.openHistory")}
                                        </Link>
                                      </div>
                                    </TableCell>
                                  </TableRow>
                                );
                              })}
                          </Fragment>
                        );
                      })}
                    </TableBody>
                  </Table>
                </div>

                {query.hasNextPage && !refreshing ? (
                  <div className="flex justify-center pt-4">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={query.isFetchingNextPage}
                      onClick={() => {
                        void query.fetchNextPage();
                      }}
                    >
                      {t("web.admin.insights.people.loadMore")}
                    </Button>
                  </div>
                ) : null}
              </>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
