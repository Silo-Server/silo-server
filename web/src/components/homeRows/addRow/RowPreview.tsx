import { Skeleton } from "@/components/ui/skeleton";
import type { PreviewState } from "@/hooks/queries/homeRows/useRowPreview";
import { PREVIEW_ITEM_LIMIT } from "@/hooks/queries/homeRows/useRowPreview";
import { rowKindGroup } from "@/lib/homeRows/catalog";
import { cn } from "@/lib/utils";
import { PosterTile } from "@/components/calm/PosterTile";
import { GROUP_ICONS, GROUP_TINTS } from "../rowIcons";

/** Why the strip has no titles to show, or null when it has (or is loading) some. */
function emptyMessage(state: PreviewState, offText: string): string | null {
  switch (state.status) {
    case "off":
      return offText;
    case "error":
      return "The preview didn't load. The row still saves.";
    case "ready":
      return state.items.length === 0
        ? "No titles match right now. The row stays empty until some do."
        : null;
    default:
      return null;
  }
}

/**
 * The step 2 / Edit row strip: the draft's first titles, or the group icon
 * with a line saying why there is nothing to show.
 */
export function RowPreview({
  title,
  sectionType,
  state,
  liveLabel,
  offText,
  countUpTo,
}: {
  title: string;
  sectionType: string;
  state: PreviewState;
  liveLabel: string;
  /** What the strip says when this surface has no preview. */
  offText: string;
  /**
   * Rule rows: once the preview loads, the header says how many titles
   * match and how many of them (up to this) the row shows.
   */
  countUpTo?: number;
}) {
  const group = rowKindGroup(sectionType);
  const Icon = GROUP_ICONS[group];
  const message = emptyMessage(state, offText);
  return (
    <section
      aria-label={`Preview of ${title || "this row"}`}
      aria-busy={state.status === "loading" || (state.status === "ready" && state.refreshing)}
      className="ring-border/85 relative overflow-hidden rounded-2xl bg-[radial-gradient(120%_140%_at_0%_0%,rgb(255_255_255/0.05),transparent_50%)] py-4 pl-4 ring-1 ring-inset sm:pl-[18px]"
    >
      <div className="mb-3 flex items-baseline justify-between gap-3 pr-4 sm:pr-[18px]">
        <span className="truncate text-base font-semibold tracking-[-0.015em]">
          {title || "Untitled row"}
        </span>
        {state.status === "off" ? null : countUpTo !== undefined && state.status === "ready" ? (
          <span
            className={cn(
              "text-muted-foreground shrink-0 text-xs transition-opacity",
              state.refreshing && "opacity-50",
            )}
          >
            {state.refreshing ? <span className="sr-only">Updating: </span> : null}
            <b className="text-foreground font-semibold">{state.totalCount}</b>
            {state.totalCount === 1 ? " title matches" : " titles match"}
            <span aria-hidden className="mx-1.5">
              ·
            </span>
            showing {Math.min(state.totalCount, countUpTo)}
          </span>
        ) : (
          <span className="text-muted-foreground inline-flex shrink-0 items-center gap-1.5 text-xs">
            <span
              aria-hidden
              className="bg-success ring-success/20 size-1.5 rounded-full ring-[3px]"
            />
            {liveLabel}
          </span>
        )}
      </div>
      {message ? (
        <div className="flex items-center gap-3 pr-4">
          <span
            aria-hidden
            className={cn(
              "grid size-12 shrink-0 place-items-center rounded-xl ring-1",
              GROUP_TINTS[group],
            )}
          >
            <Icon className="size-5" />
          </span>
          <p className="text-muted-foreground text-sm" role="status">
            {message}
          </p>
        </div>
      ) : (
        <div className="flex gap-2.5 overflow-hidden">
          {state.status === "ready"
            ? state.items.map((item) => (
                <PosterTile
                  key={item.id}
                  title={item.title}
                  posterUrl={item.posterUrl}
                  thumbhash={item.thumbhash}
                  className="w-[88px] sm:w-[104px]"
                />
              ))
            : Array.from({ length: PREVIEW_ITEM_LIMIT }, (_, index) => (
                <Skeleton
                  key={index}
                  className="aspect-[2/3] w-[88px] shrink-0 rounded-[10px] sm:w-[104px]"
                />
              ))}
        </div>
      )}
    </section>
  );
}
