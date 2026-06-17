import { useEffect, useMemo, useRef, useState } from "react";
import { ChevronRight } from "lucide-react";
import type { AudiobookGroup } from "@/api/types";
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
import { formatHoursMinutes } from "@/lib/audiobooks/duration";

// Number of groups rendered per reveal step (initial paint + each scroll grow).
const GROUPS_REVEAL_BATCH = 120;

interface AudiobookGroupsViewProps {
  libraryId: number;
  groupBy: AudiobookGroupBy;
  /** Called with the group name; the parent applies it as a Books filter. */
  onSelectGroup: (name: string) => void;
}

const GROUP_NOUN: Record<AudiobookGroupBy, string> = {
  author: "authors",
  narrator: "narrators",
  series: "series",
};

function groupStats(group: AudiobookGroup): string {
  const parts = [
    `${group.item_count} ${group.item_count === 1 ? "book" : "books"}`,
    formatHoursMinutes(group.total_duration_seconds),
  ];
  if (group.in_progress_count > 0) {
    parts.push(`${group.in_progress_count} in progress`);
  } else if (group.finished_count > 0 && group.finished_count === group.item_count) {
    parts.push("all finished");
  } else if (group.finished_count > 0) {
    parts.push(`${group.finished_count} finished`);
  }
  return parts.join(" · ");
}

function groupInitials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
}

/** Up to three overlapping square covers; falls back to an initials tile. */
function CoverStack({ group, large }: { group: AudiobookGroup; large?: boolean }) {
  const posters = group.poster_urls.slice(0, 3);
  if (posters.length === 0) {
    return (
      <div
        className={`bg-surface-raised text-muted-foreground flex shrink-0 items-center justify-center font-semibold ${
          large ? "aspect-square w-full rounded-xl text-2xl" : "h-14 w-14 rounded-lg text-sm"
        }`}
      >
        {groupInitials(group.name)}
      </div>
    );
  }

  if (!large) {
    return (
      <div className="flex shrink-0 -space-x-7">
        {posters.map((url, i) => (
          <img
            key={url}
            src={url}
            alt=""
            loading="lazy"
            className="border-background h-14 w-14 rounded-lg border object-cover shadow-md"
            style={{ zIndex: posters.length - i }}
          />
        ))}
      </div>
    );
  }

  return (
    <div className="relative aspect-square w-full">
      {posters
        .slice()
        .reverse()
        .map((url, i) => {
          // Render back-to-front; the first poster ends up on top, upright.
          const depth = posters.length - 1 - i;
          return (
            <img
              key={url}
              src={url}
              alt=""
              loading="lazy"
              className="absolute inset-0 h-full w-full rounded-xl object-cover shadow-lg transition-transform duration-[--duration-fast]"
              style={{
                transform:
                  depth === 0 ? undefined : `rotate(${depth * 3}deg) translateX(${depth * 4}px)`,
              }}
            />
          );
        })}
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
  // Authors/narrators default to most-books-first (collection heavy hitters);
  // series read more naturally alphabetized.
  const [sort, setSort] = useState<AudiobookGroupSort>(groupBy === "series" ? "name" : "count");
  const [filter, setFilter] = useState("");
  const { data, isLoading } = useAudiobookGroups(libraryId, groupBy, sort);

  const groups = useMemo(() => {
    const all = data?.groups ?? [];
    const needle = filter.trim().toLowerCase();
    if (!needle) return all;
    return all.filter((group) => group.name.toLowerCase().includes(needle));
  }, [data?.groups, filter]);

  // Incremental reveal: a large audiobook library can have tens of thousands of
  // authors/narrators. Rendering them all at once mounts tens of thousands of
  // DOM nodes (+ cover images) and freezes the main thread on load. Render a
  // capped window and grow it as the user scrolls toward the end, so initial
  // paint is bounded regardless of library size.
  const [visibleCount, setVisibleCount] = useState(GROUPS_REVEAL_BATCH);
  useEffect(() => {
    setVisibleCount(GROUPS_REVEAL_BATCH);
  }, [filter, sort, groupBy, libraryId]);
  const visibleGroups = useMemo(() => groups.slice(0, visibleCount), [groups, visibleCount]);
  const hasMore = visibleCount < groups.length;
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!hasMore) return;
    const el = sentinelRef.current;
    if (!el) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setVisibleCount((count) => count + GROUPS_REVEAL_BATCH);
        }
      },
      { rootMargin: "800px" },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [hasMore, groups.length]);

  const noun = GROUP_NOUN[groupBy];
  const isSeries = groupBy === "series";

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-3">
        <Input
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder={`Filter ${noun}…`}
          className="w-full max-w-xs"
        />
        <Select value={sort} onValueChange={(value) => setSort(value as AudiobookGroupSort)}>
          <SelectTrigger className="w-[160px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="name">Name</SelectItem>
            <SelectItem value="count">Most books</SelectItem>
            <SelectItem value="duration">Longest</SelectItem>
          </SelectContent>
        </Select>
        {data && (
          <span className="text-muted-foreground ml-auto text-sm">
            {groups.length === data.total ? data.total : `${groups.length} of ${data.total}`} {noun}
          </span>
        )}
      </div>

      {isLoading ? (
        isSeries ? (
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
            {Array.from({ length: 12 }).map((_, i) => (
              <div key={i}>
                <Skeleton className="aspect-square w-full rounded-xl" />
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
      ) : groups.length === 0 ? (
        <p className="text-muted-foreground py-10 text-center text-sm">
          {filter ? `No ${noun} match “${filter}”.` : `No ${noun} found in this library.`}
        </p>
      ) : isSeries ? (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
          {visibleGroups.map((group) => (
            <button
              key={group.name}
              type="button"
              onClick={() => onSelectGroup(group.name)}
              className="group/series text-left"
            >
              <div className="transition-transform duration-[--duration-fast] group-hover/series:scale-[1.02]">
                <CoverStack group={group} large />
              </div>
              <div className="mt-2.5 truncate px-0.5 text-[14px] font-semibold tracking-tight">
                {group.name}
              </div>
              <div className="text-muted-foreground truncate px-0.5 text-xs">
                {groupStats(group)}
              </div>
            </button>
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {visibleGroups.map((group) => (
            <button
              key={group.name}
              type="button"
              onClick={() => onSelectGroup(group.name)}
              className="surface-panel hover:bg-muted/40 flex items-center gap-4 rounded-xl border-0 px-4 py-3 text-left transition-colors duration-[--duration-fast]"
            >
              <CoverStack group={group} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-semibold">{group.name}</div>
                <div className="text-muted-foreground mt-0.5 truncate text-xs">
                  {groupStats(group)}
                </div>
              </div>
              <ChevronRight className="text-muted-foreground/50 h-4 w-4 shrink-0" />
            </button>
          ))}
        </div>
      )}
      {!isLoading && hasMore && <div ref={sentinelRef} aria-hidden className="h-1" />}
    </div>
  );
}
