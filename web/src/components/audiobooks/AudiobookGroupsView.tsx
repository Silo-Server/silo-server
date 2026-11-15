import { useCallback, useMemo, useState, type CSSProperties } from "react";
import { ChevronRight } from "lucide-react";
import type { AudiobookGroup } from "@/api/types";
import { loadErrorDescription } from "@/components/loadErrorDescription";
import PageUnavailable from "@/components/PageUnavailable";
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
  useAudiobookGroups,
  type AudiobookGroupBy,
  type AudiobookGroupSort,
} from "@/hooks/queries/audiobookGroups";
import { useT, type Translate } from "@/i18n";
import { useDebounce } from "@/hooks/useDebounce";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";
import { formatHoursMinutes } from "@/lib/audiobooks/duration";

interface AudiobookGroupsViewProps {
  libraryId: number;
  groupBy: AudiobookGroupBy;
  /** Called with the group name; the parent applies it as a Books filter. */
  onSelectGroup: (name: string) => void;
}

/** "12 books · 61 hr": the group's size. */
function groupTotals(t: Translate, group: AudiobookGroup): string {
  return [
    t("web.audiobooks.groups.statBooks", {
      count: group.item_count,
      count_text: group.item_count,
    }),
    formatHoursMinutes(group.total_duration_seconds),
  ].join(" · ");
}

/** The profile's listening across the group, or nothing before they start it. */
function groupListening(t: Translate, group: AudiobookGroup): string | undefined {
  if (group.in_progress_count > 0) {
    return t("web.audiobooks.groups.statInProgress", {
      count: group.in_progress_count,
      count_text: group.in_progress_count,
    });
  }
  if (group.finished_count > 0 && group.finished_count === group.item_count) {
    return t("web.audiobooks.groups.statAllFinished");
  }
  if (group.finished_count > 0) {
    return t("web.audiobooks.groups.statFinished", {
      count: group.finished_count,
      count_text: group.finished_count,
    });
  }
  return undefined;
}

function groupInitials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
}

/** A group's cover; one that fails to load leaves a plain tile, not a broken image. */
function GroupCover({
  url,
  className,
  style,
}: {
  url: string;
  className: string;
  style?: CSSProperties;
}) {
  const [failed, setFailed] = useState(false);
  if (failed) {
    return <div aria-hidden="true" className={`bg-surface-raised ${className}`} style={style} />;
  }
  return (
    <img
      src={url}
      alt=""
      loading="lazy"
      decoding="async"
      onError={() => setFailed(true)}
      className={`bg-muted object-cover ${className}`}
      style={style}
    />
  );
}

function InitialsTile({ name, className }: { name: string; className: string }) {
  return (
    <div
      className={`bg-surface-raised text-muted-foreground flex items-center justify-center font-semibold ${className}`}
    >
      {groupInitials(name)}
    </div>
  );
}

/**
 * A series' first covers side by side, each partly under the one before it,
 * so the series reads as a run of books.
 */
function SeriesFan({ group }: { group: AudiobookGroup }) {
  const posters = group.poster_urls.slice(0, 3);
  if (posters.length === 0) {
    return <InitialsTile name={group.name} className="aspect-[3/2] w-full rounded-xl text-2xl" />;
  }
  return (
    <div className="relative aspect-[3/2] w-full">
      {posters.map((url, i) => (
        <GroupCover
          key={url}
          url={url}
          className="absolute top-0 aspect-square w-[66%] rounded-xl shadow-[0_14px_26px_-12px_rgb(0_0_0/0.85),-6px_0_14px_-6px_rgb(0_0_0/0.6)]"
          style={{
            insetInlineStart: `${i * 17}%`,
            zIndex: posters.length - i,
            filter: i === 0 ? undefined : `brightness(${1 - i * 0.17})`,
          }}
        />
      ))}
    </div>
  );
}

/** Up to four of an author's or narrator's covers in a square. */
function CoverMosaic({ group }: { group: AudiobookGroup }) {
  const posters = group.poster_urls.slice(0, 4);
  if (posters.length === 0) {
    return <InitialsTile name={group.name} className="h-16 w-16 shrink-0 rounded-xl text-sm" />;
  }
  const single = posters.length === 1;
  return (
    <div
      className={`bg-surface-raised grid h-16 w-16 shrink-0 gap-0.5 overflow-hidden rounded-xl ${
        single ? "grid-cols-1" : "grid-cols-2"
      }`}
    >
      {posters.map((url) => (
        <GroupCover key={url} url={url} className="h-full w-full" />
      ))}
    </div>
  );
}

/**
 * Grouped Library-tab browse for audiobook libraries: Authors and Narrators
 * render as rows with cover stacks and listening stats; Series render as a
 * card grid. Selecting a group drops back into the Books grid filtered to it.
 */
export default function AudiobookGroupsView({
  libraryId,
  groupBy,
  onSelectGroup,
}: AudiobookGroupsViewProps) {
  const t = useT();
  // Authors/narrators default to most-books-first (collection heavy hitters);
  // series read more naturally alphabetized.
  const [sort, setSort] = useState<AudiobookGroupSort>(groupBy === "series" ? "name" : "count");
  const [filter, setFilter] = useState("");
  const debouncedFilter = useDebounce(filter.trim(), 250);
  const {
    data,
    error,
    fetchNextPage,
    hasNextPage,
    isError,
    isFetching,
    isFetchingNextPage,
    isLoading,
    refetch,
  } = useAudiobookGroups(libraryId, groupBy, sort, debouncedFilter);

  const groups = useMemo(() => data?.pages.flatMap((page) => page.groups) ?? [], [data?.pages]);
  const firstPage = data?.pages[0];
  const total = firstPage?.total ?? 0;
  const totalExact = firstPage?.total_exact ?? false;
  const isInitialLoading = isLoading && groups.length === 0;
  // A failed read with nothing loaded is not an empty library. Groups that
  // did load stay on screen when a later refetch fails.
  const failedWithNothingLoaded = isError && groups.length === 0;
  const loadNextPage = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);
  const sentinelRef = useIntersectionObserver({
    onIntersect: loadNextPage,
    enabled: Boolean(hasNextPage) && !isFetchingNextPage,
  });

  const isSeries = groupBy === "series";
  const countLabel = totalExact
    ? groups.length === total
      ? t("web.audiobooks.groups.countAll", {
          group_by: groupBy,
          count: total,
          total: total,
        })
      : t("web.audiobooks.groups.countPartial", {
          group_by: groupBy,
          shown: groups.length,
          count: total,
          total: total,
        })
    : hasNextPage
      ? t("web.audiobooks.groups.countAtLeast", {
          group_by: groupBy,
          count: groups.length,
          loaded: groups.length,
        })
      : t("web.audiobooks.groups.countAll", {
          group_by: groupBy,
          count: groups.length,
          total: groups.length,
        });

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-3">
        <Input
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder={t("web.audiobooks.groups.searchPlaceholder", { group_by: groupBy })}
          className="w-full max-w-xs"
        />
        <Select value={sort} onValueChange={(value) => setSort(value as AudiobookGroupSort)}>
          <SelectTrigger className="w-[160px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="name">{t("web.audiobooks.groups.sortName")}</SelectItem>
            <SelectItem value="count">{t("web.audiobooks.groups.sortCount")}</SelectItem>
            <SelectItem value="duration">{t("web.audiobooks.groups.sortDuration")}</SelectItem>
          </SelectContent>
        </Select>
        {firstPage && <span className="text-muted-foreground ms-auto text-sm">{countLabel}</span>}
      </div>

      {isInitialLoading ? (
        isSeries ? (
          <div className="grid grid-cols-2 gap-x-4 gap-y-7 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 2xl:grid-cols-6">
            {Array.from({ length: 12 }).map((_, i) => (
              <div key={i}>
                <Skeleton className="aspect-[3/2] w-full rounded-xl" />
                <Skeleton className="mt-2 h-4 w-3/4 rounded" />
              </div>
            ))}
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
            {Array.from({ length: 9 }).map((_, i) => (
              <Skeleton key={i} className="h-20 rounded-xl" />
            ))}
          </div>
        )
      ) : failedWithNothingLoaded ? (
        <PageUnavailable
          title={t("web.audiobooks.groups.loadFailed", { group_by: groupBy })}
          description={loadErrorDescription(error)}
          onRetry={() => void refetch()}
          retrying={isFetching}
        />
      ) : groups.length === 0 ? (
        <p className="text-muted-foreground py-10 text-center text-sm">
          {filter
            ? t("web.audiobooks.groups.noMatch", { group_by: groupBy, filter })
            : t("web.audiobooks.groups.empty", { group_by: groupBy })}
        </p>
      ) : isSeries ? (
        <>
          <div className="grid grid-cols-2 gap-x-4 gap-y-7 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 2xl:grid-cols-6">
            {groups.map((group) => {
              const listening = groupListening(t, group);
              return (
                <button
                  key={group.name}
                  type="button"
                  onClick={() => onSelectGroup(group.name)}
                  className="group/series grid content-start gap-0.5 text-start"
                >
                  <div className="mb-2 transition-transform duration-(--duration-fast) group-hover/series:-translate-y-0.5">
                    <SeriesFan group={group} />
                  </div>
                  <div className="truncate px-0.5 text-sm font-semibold tracking-tight">
                    {group.name}
                  </div>
                  <div className="text-muted-foreground truncate px-0.5 text-xs">
                    {groupTotals(t, group)}
                  </div>
                  {listening && (
                    <div className="text-foreground truncate px-0.5 text-xs font-medium">
                      {listening}
                    </div>
                  )}
                </button>
              );
            })}
          </div>
          <div ref={sentinelRef} className="h-8">
            {isFetchingNextPage ? <Skeleton className="mx-auto h-3 w-28 rounded" /> : null}
          </div>
        </>
      ) : (
        <>
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
            {groups.map((group) => {
              const listening = groupListening(t, group);
              return (
                <button
                  key={group.name}
                  type="button"
                  onClick={() => onSelectGroup(group.name)}
                  className="surface-panel hover:bg-muted/40 flex items-center gap-4 rounded-2xl border-0 p-3 text-start transition-colors duration-(--duration-fast)"
                >
                  <CoverMosaic group={group} />
                  <div className="grid min-w-0 flex-1 gap-0.5">
                    <div className="truncate text-sm font-semibold">{group.name}</div>
                    <div className="text-muted-foreground truncate text-xs">
                      {groupTotals(t, group)}
                    </div>
                    {listening && (
                      <div className="text-foreground truncate text-xs font-medium">
                        {listening}
                      </div>
                    )}
                  </div>
                  <ChevronRight className="text-muted-foreground/50 h-4 w-4 shrink-0 rtl:rotate-180" />
                </button>
              );
            })}
          </div>
          <div ref={sentinelRef} className="h-8">
            {isFetchingNextPage ? <Skeleton className="mx-auto h-3 w-28 rounded" /> : null}
          </div>
        </>
      )}
    </div>
  );
}
