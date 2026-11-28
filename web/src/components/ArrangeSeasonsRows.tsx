// The arranger's season, Specials and episode rows, and its virtualized episode list.
import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { CSS } from "@dnd-kit/utilities";
import { FileVideo, GripVertical, Pencil, Scissors, Sparkles, Trash2 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { type CustomSeasonEpisode } from "@/hooks/queries/episodeOrder";
import { useMotionSafeSortable } from "@/hooks/useSortableList";
import { useT } from "@/i18n";
import { formatEpisodeCode } from "@/lib/episodeCardLabels";
import { seasonDisplayName } from "@/pages/ItemDetail/itemDetailLayout";
import { cn } from "@/lib/utils";

import { SPECIALS, type ArrangedSeason } from "@/lib/arrangeSeasons";

/**
 * The episodes of one season, of which only the rows in view are mounted: a
 * long season (One Piece, Bleach on TMDB) would otherwise render hundreds of
 * sortable rows on every pointer move of a drag.
 */
export function VirtualEpisodeList({
  keys,
  scrollElement,
  renderRow,
}: {
  keys: string[];
  scrollElement: () => HTMLElement | null;
  renderRow: (key: string, index: number) => ReactNode;
}) {
  const listRef = useRef<HTMLUListElement>(null);
  const [offset, setOffset] = useState(0);
  // The scroll element can still be detached when the list mounts (the
  // arranger remounts in its dialog on a start over), so it is looked up
  // again a frame later; finding it renders the list, and the virtualizer
  // reads the scroll element on every render.
  const [scroller, setScroller] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => {
    const found = scrollElement();
    if (found) {
      setScroller(found);
      return;
    }
    const frame = requestAnimationFrame(() => setScroller(scrollElement()));
    return () => cancelAnimationFrame(frame);
  }, [scrollElement]);
  // The list starts below the season details, inside the scroll element.
  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list || !scroller) return;
    // Read live: a resize can move scrolling between the pane and the body.
    const measure = () => {
      const current = scrollElement() ?? scroller;
      setOffset(
        list.getBoundingClientRect().top - current.getBoundingClientRect().top + current.scrollTop,
      );
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(scroller);
    if (list.parentElement) observer.observe(list.parentElement);
    return () => observer.disconnect();
  }, [scroller, scrollElement]);
  const virtualizer = useVirtualizer({
    count: keys.length,
    getScrollElement: scrollElement,
    estimateSize: () => 60,
    overscan: 8,
    scrollMargin: offset,
    getItemKey: (index) => keys[index] ?? index,
  });
  return (
    <ul
      ref={listRef}
      className="surface-panel relative overflow-hidden rounded-2xl"
      style={{ height: virtualizer.getTotalSize() }}
    >
      {virtualizer.getVirtualItems().map((item) => (
        <li
          key={item.key}
          data-index={item.index}
          ref={virtualizer.measureElement}
          className="absolute inset-x-0 top-0"
          style={{ transform: `translateY(${item.start - offset}px)` }}
        >
          {renderRow(keys[item.index] ?? "", item.index)}
        </li>
      ))}
    </ul>
  );
}

export function SeasonRow({
  season,
  name,
  providerTitle,
  posterUrl,
  active,
  editing,
  dropTarget,
  canDelete,
  onSelect,
  onRename,
  onEdit,
  onDelete,
}: {
  season: ArrangedSeason;
  name: string;
  providerTitle?: string;
  posterUrl?: string;
  active: boolean;
  editing: boolean;
  dropTarget: boolean;
  canDelete: boolean;
  onSelect: () => void;
  onRename: (title: string) => void;
  onEdit: (on: boolean) => void;
  onDelete: () => void;
}) {
  const t = useT();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging, isOver } =
    useMotionSafeSortable({
      id: `season:${season.id}`,
    });
  return (
    <li
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "group flex items-center gap-1.5 rounded-lg border border-transparent px-1.5 py-1.5 transition-colors",
        active ? "bg-primary/10 border-primary/30" : "hover:bg-accent/50",
        dropTarget && "border-border border-dashed",
        dropTarget && isOver && "border-primary bg-primary/15",
        isDragging && "opacity-60",
      )}
    >
      <button
        type="button"
        className="text-muted-foreground/60 hover:text-foreground cursor-grab touch-none p-1"
        aria-label={t("web.item.arrangeSeasons.drag", { name })}
        {...attributes}
        {...listeners}
      >
        <GripVertical className="size-4" aria-hidden="true" />
      </button>
      {editing ? (
        <Input
          autoFocus
          defaultValue={season.title}
          placeholder={providerTitle || name}
          className="h-8 flex-1 text-sm"
          onBlur={(e) => {
            onRename(e.target.value.trim());
            onEdit(false);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === "Escape") (e.target as HTMLInputElement).blur();
          }}
        />
      ) : (
        <button
          type="button"
          onClick={onSelect}
          className="flex min-w-0 flex-1 items-center gap-2.5 text-start"
        >
          <span className="bg-surface border-border/60 h-12 w-8 shrink-0 overflow-hidden rounded-md border">
            {posterUrl && (
              <img src={posterUrl} alt="" className="h-full w-full object-cover" loading="lazy" />
            )}
          </span>
          <span className="min-w-0 flex-1">
            <span className="block text-sm leading-snug font-medium text-pretty">{name}</span>
            <span className="text-muted-foreground block text-xs">
              {t("web.item.arrangeSeasons.episodeCount", { count: season.episodeKeys.length })}
            </span>
          </span>
        </button>
      )}
      {!editing && (
        <div className="flex opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
          <button
            type="button"
            onClick={() => onEdit(true)}
            className="text-muted-foreground hover:text-foreground p-1.5"
            aria-label={t("web.item.arrangeSeasons.rename")}
          >
            <Pencil className="size-3.5" aria-hidden="true" />
          </button>
          {canDelete && (
            <button
              type="button"
              onClick={onDelete}
              className="text-muted-foreground p-1.5 hover:text-red-400"
              aria-label={t("web.item.arrangeSeasons.delete")}
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
            </button>
          )}
        </div>
      )}
    </li>
  );
}

export function SpecialsRow({
  count,
  active,
  dropTarget,
  onSelect,
}: {
  count: number;
  active: boolean;
  dropTarget: boolean;
  onSelect: () => void;
}) {
  const t = useT();
  const { setNodeRef, isOver } = useMotionSafeSortable({
    id: `season:${SPECIALS}`,
    disabled: { draggable: true },
  });
  return (
    <button
      ref={setNodeRef}
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-2 rounded-lg border border-transparent px-3 py-2 text-start",
        active ? "bg-primary/10 border-primary/30" : "hover:bg-accent/50",
        dropTarget && "border-border border-dashed",
        dropTarget && isOver && "border-primary bg-primary/15",
      )}
    >
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{seasonDisplayName(0)}</div>
        <div className="text-muted-foreground text-xs">
          {t("web.item.arrangeSeasons.specialsMeta", { count })}
        </div>
      </div>
    </button>
  );
}

export function EpisodeRow({
  episode,
  provider,
  number,
  selected,
  onToggle,
  onSplit,
}: {
  episode: CustomSeasonEpisode;
  provider: string;
  number: number;
  selected: boolean;
  onToggle: (shift: boolean) => void;
  onSplit?: () => void;
}) {
  const t = useT();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useMotionSafeSortable({
      id: `ep:${episode.key}`,
    });
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "group border-border/40 relative flex items-center gap-3 border-b px-3 py-2",
        selected && "bg-primary/8",
        isDragging && "opacity-0",
      )}
    >
      <input
        type="checkbox"
        checked={selected}
        onChange={() => undefined}
        onClick={(ev) => onToggle(ev.shiftKey)}
        className="size-4 shrink-0"
        style={{ accentColor: "var(--primary)" }}
        aria-label={t("web.item.arrangeSeasons.select", { title: episode.title })}
      />
      <button
        type="button"
        className="text-muted-foreground/50 hover:text-foreground cursor-grab touch-none"
        aria-label={t("web.item.arrangeSeasons.drag", { name: episode.title })}
        {...attributes}
        {...listeners}
      >
        <GripVertical className="size-4" aria-hidden="true" />
      </button>
      <EpisodeSummary episode={episode} provider={provider} number={number} />
      {onSplit && (
        <button
          type="button"
          onClick={onSplit}
          className="bg-surface border-border text-muted-foreground hover:text-foreground absolute end-3 top-1/2 flex -translate-y-1/2 items-center gap-1 rounded-md border px-2 py-1 text-xs opacity-0 shadow-sm transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
          title={t("web.item.arrangeSeasons.splitTitle")}
        >
          <Scissors className="size-3.5" aria-hidden="true" />
          {t("web.item.arrangeSeasons.splitHere")}
        </button>
      )}
    </div>
  );
}

/** An episode's number, still, title and where it comes from, with its file. */
export function EpisodeSummary({
  episode,
  provider,
  number,
}: {
  episode: CustomSeasonEpisode;
  provider: string;
  number: number;
}) {
  const t = useT();
  const origin = [
    provider,
    formatEpisodeCode(episode.provider_season, episode.provider_episode),
  ].join(" ");
  const year = episode.air_date?.slice(0, 4);
  return (
    <>
      <span className="text-muted-foreground/70 w-8 shrink-0 text-center text-sm font-semibold tabular-nums">
        {number}
      </span>
      <span className="bg-muted/50 hidden h-11 w-[78px] shrink-0 overflow-hidden rounded-md sm:block">
        {episode.still_url && (
          <img
            src={episode.still_url}
            alt=""
            className="h-full w-full object-cover"
            loading="lazy"
          />
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-medium">{episode.title}</span>
          {episode.placed_automatically && (
            <span className="bg-primary/15 text-primary flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[0.625rem] font-medium">
              <Sparkles className="size-3" aria-hidden="true" />
              {t("web.item.arrangeSeasons.placedAutomatically")}
            </span>
          )}
        </div>
        <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
          <span className="shrink-0 font-mono">{origin}</span>
          {year && <span className="shrink-0">{year}</span>}
          {episode.file ? (
            <span className="flex min-w-0 items-center gap-1 font-mono text-[0.6875rem]">
              <FileVideo className="size-3 shrink-0" aria-hidden="true" />
              <span className="truncate">{episode.file}</span>
            </span>
          ) : (
            <span className="text-muted-foreground/60">{t("web.item.arrangeSeasons.noFile")}</span>
          )}
        </div>
      </div>
    </>
  );
}
