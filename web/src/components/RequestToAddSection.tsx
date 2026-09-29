import { useRef } from "react";
import type { MouseEvent } from "react";
import { Link } from "react-router";
import { Film, RefreshCw, Tv } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useRequestSearch } from "@/hooks/queries/useRequests";
import type { RequestMediaResult, RequestSearchMediaType } from "@/api/types";
import {
  formatMediaType,
  formatRequestDisplayState,
  formatRequestReason,
  REQUEST_DIALOG_SUGGESTION_LIMIT,
  requestDetailHref,
  requestDisplayState,
  requestSuggestions,
  tmdbImageURL,
  tmdbPageCount,
} from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";
import { RequestStatusBadge } from "./RequestStatusBadge";
import RequestResultsGrid, { RequestResultsPager } from "./RequestResultsGrid";

function cardKey(item: Pick<RequestMediaResult, "media_type" | "tmdb_id">): string {
  return `${item.media_type}-${item.tmdb_id}`;
}

function unavailableReasonLabel(item: RequestMediaResult): string {
  return item.request.reason ? formatRequestReason(item.request.reason) : "Blocked";
}

const GRID_LIMIT = 20;
const INTERACTIVE_SEARCH_GC_TIME_MS = 30_000;

/**
 * Lets a host combobox (the ⌘K dialog) treat the dialog rows as options of its
 * own: it owns the highlighted row and what picking a row does.
 */
export interface RequestSuggestionCombobox {
  listboxId: string;
  /** DOM id for the suggestion at this position, unique across the host's options. */
  optionId: (index: number) => string;
  /** Position of the highlighted suggestion, or -1 when none is highlighted. */
  selectedIndex: number;
  onPick: (item: RequestMediaResult) => void;
}

type RequestToAddSectionCommonProps = {
  query: string;
  /** The TMDB types to search, following the host's search scope. Default: movies and series. */
  mediaType?: RequestSearchMediaType;
  /** True when the library search returned at least one hit. Drives header copy. */
  libraryHadHits: boolean;
  /**
   * True only after the matching local-library query completed successfully.
   * Loading and failed searches must not be presented as confirmed absences.
   */
  libraryResultsKnown?: boolean;
};

export type RequestToAddSectionProps = RequestToAddSectionCommonProps &
  (
    | { variant: "dialog"; combobox?: RequestSuggestionCombobox }
    | {
        variant: "grid";
        /**
         * The TMDB results page to show. The host keeps it in navigation
         * (Catalog uses the URL), so Back and a reload return to it, and
         * starts over at 1 for another query or type.
         */
        page: number;
        onPageChange: (page: number) => void;
      }
  );

export function RequestToAddSection(props: RequestToAddSectionProps) {
  const { variant, query, mediaType = "all", libraryHadHits, libraryResultsKnown = true } = props;
  const { discoveryEnabled } = useCanRequest();
  const page = props.variant === "grid" ? props.page : 1;
  const search = useRequestSearch(mediaType, query, page, {
    enabled: discoveryEnabled,
    requireProfile: true,
    staleTime: 5 * 60 * 1000,
    gcTime: INTERACTIVE_SEARCH_GC_TIME_MS,
    retry: false,
    ...(variant === "grid" ? { keepPreviousPage: true } : {}),
  });

  if (!discoveryEnabled) return null;
  // A failed first read hides the section; a failed later page keeps it, so
  // the viewer can retry or go back instead of losing the results.
  const failed = search.isError && !search.data;
  if (failed && page === 1) return null;

  const limit = variant === "dialog" ? REQUEST_DIALOG_SUGGESTION_LIMIT : GRID_LIMIT;
  const visible = requestSuggestions(search.data?.results, limit);
  const totalPages = tmdbPageCount(search.data?.total_pages);
  // Any page can hold only library titles. The grid keeps such a page while
  // TMDB has others, so its pager can reach them; the dialog has no pager.
  const hasOtherPages = variant === "grid" && totalPages > 1;
  if (visible.length === 0 && page === 1 && !hasOtherPages) return null;

  if (props.variant === "dialog") {
    return (
      <DialogVariant
        items={visible}
        libraryHadHits={libraryHadHits}
        libraryResultsKnown={libraryResultsKnown}
        combobox={props.combobox}
      />
    );
  }
  return (
    <GridVariant
      query={query.trim()}
      items={visible}
      libraryHadHits={libraryHadHits}
      libraryResultsKnown={libraryResultsKnown}
      page={page}
      totalPages={totalPages}
      isChangingPage={search.isPlaceholderData}
      onPageChange={props.onPageChange}
      pageError={
        failed ? { onRetry: () => void search.refetch(), isRetrying: search.isFetching } : null
      }
    />
  );
}

function HeaderCopy({
  libraryHadHits,
  libraryResultsKnown,
  count,
}: {
  libraryHadHits: boolean;
  libraryResultsKnown: boolean;
  count: number;
}) {
  if (libraryHadHits) {
    return (
      <div className="text-muted-foreground flex items-center gap-2 px-3 pt-2 pb-1 text-[10px] font-medium tracking-[0.1em] uppercase">
        <span>Request to Add</span>
        <span className="bg-muted text-muted-foreground rounded-full px-1.5 text-[10px]">
          {count}
        </span>
      </div>
    );
  }

  if (!libraryResultsKnown) {
    return (
      <div className="text-muted-foreground px-3 pt-3 pb-1 text-[12px]">Discovery matches:</div>
    );
  }

  return (
    <div className="text-muted-foreground px-3 pt-3 pb-1 text-[12px]">Not in your library</div>
  );
}

function DialogVariant({
  items,
  libraryHadHits,
  libraryResultsKnown,
  combobox,
}: {
  items: RequestMediaResult[];
  libraryHadHits: boolean;
  libraryResultsKnown: boolean;
  combobox?: RequestSuggestionCombobox;
}) {
  return (
    <div className="border-border/60 border-t pt-1">
      <HeaderCopy
        libraryHadHits={libraryHadHits}
        libraryResultsKnown={libraryResultsKnown}
        count={items.length}
      />
      <div
        id={combobox?.listboxId}
        role="listbox"
        aria-label="Request suggestions"
        className="px-1 py-1"
      >
        {items.map((item, index) => (
          <DialogRow
            key={cardKey(item)}
            item={item}
            optionId={combobox?.optionId(index)}
            isSelected={combobox?.selectedIndex === index}
            onPick={combobox?.onPick}
          />
        ))}
      </div>
    </div>
  );
}

function DialogRow({
  item,
  optionId,
  isSelected,
  onPick,
}: {
  item: RequestMediaResult;
  optionId?: string;
  isSelected: boolean;
  onPick?: (item: RequestMediaResult) => void;
}) {
  const poster = tmdbImageURL(item.poster_path);
  const Icon = item.media_type === "series" ? Tv : Film;
  const requestable = item.request.requestable;
  const state = item.request.status
    ? requestDisplayState(item.request.status, undefined, item.request.state)
    : undefined;
  const reasonLabel = !state && !requestable ? unavailableReasonLabel(item) : null;

  // A plain click goes through the host so it can close the dialog; modified
  // clicks keep the link's own new-tab and new-window behavior.
  function handleClick(event: MouseEvent<HTMLAnchorElement>) {
    if (!onPick || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onPick(item);
  }

  // Keyboard focus stays in the host's search input, which points at this row
  // with aria-activedescendant, so the row is not a tab stop.
  return (
    <Link
      id={optionId}
      role="option"
      aria-selected={isSelected}
      aria-label={[
        item.title,
        item.year ? String(item.year) : null,
        formatMediaType(item.media_type),
        state ? formatRequestDisplayState(state) : reasonLabel,
      ]
        .filter(Boolean)
        .join(", ")}
      data-selected={isSelected || undefined}
      tabIndex={-1}
      to={requestDetailHref(item.media_type, item.tmdb_id)}
      onClick={handleClick}
      className="hover:bg-muted/80 data-[selected]:bg-accent flex w-full items-center gap-3 rounded-md px-3 py-2 text-left transition-colors"
    >
      <div
        className={cn(
          "bg-muted relative h-14 w-10 shrink-0 overflow-hidden rounded-md",
          !requestable && "opacity-70",
        )}
      >
        {poster ? (
          <img src={poster} alt="" className="h-full w-full object-cover" loading="lazy" />
        ) : (
          <div className="text-muted-foreground flex h-full items-center justify-center">
            <Icon className="h-4 w-4" />
          </div>
        )}
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{item.title}</div>
        <div className="text-muted-foreground text-xs">
          {item.year ? `${item.year} · ` : ""}
          {item.media_type === "series" ? "Series" : "Movie"}
        </div>
      </div>
      {state ? (
        <RequestStatusBadge state={state} className="shrink-0" />
      ) : reasonLabel ? (
        <span className="text-muted-foreground shrink-0 text-[11px]" title={reasonLabel}>
          {reasonLabel}
        </span>
      ) : null}
    </Link>
  );
}

function GridVariant({
  query,
  items,
  libraryHadHits,
  libraryResultsKnown,
  page,
  totalPages,
  isChangingPage,
  onPageChange,
  pageError,
}: {
  query: string;
  items: RequestMediaResult[];
  libraryHadHits: boolean;
  libraryResultsKnown: boolean;
  page: number;
  /** Readable pages; 0 while the page failed to load and the count is unknown. */
  totalPages: number;
  isChangingPage: boolean;
  onPageChange: (page: number) => void;
  /** Set when this (later) page failed to load. */
  pageError: { onRetry: () => void; isRetrying: boolean } | null;
}) {
  const sectionRef = useRef<HTMLElement>(null);

  function changePage(next: number) {
    onPageChange(next);
    // The pager sits below the grid; bring the new page's first row into view.
    const section = sectionRef.current;
    if (section && section.getBoundingClientRect().top < 0) {
      section.scrollIntoView?.({ block: "start", behavior: "smooth" });
    }
  }

  // Laid out like the People section above it: a plain heading over the results.
  return (
    <section ref={sectionRef} aria-label="Request to add" className="scroll-mt-4 space-y-3">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold">Request to add</h2>
        {libraryResultsKnown && !libraryHadHits ? (
          <p className="text-muted-foreground text-sm">
            Nothing in your library matches &ldquo;{query}&rdquo;.
          </p>
        ) : null}
      </div>
      {pageError ? (
        <div role="alert" className="flex flex-wrap items-center gap-x-3 gap-y-2 py-6">
          <p className="text-muted-foreground text-sm">Couldn&rsquo;t load page {page}.</p>
          <Button
            variant="outline"
            size="sm"
            onClick={pageError.onRetry}
            disabled={pageError.isRetrying}
          >
            <RefreshCw className="size-4" aria-hidden />
            Retry
          </Button>
          <Button variant="ghost" size="sm" onClick={() => changePage(1)}>
            Back to page 1
          </Button>
        </div>
      ) : items.length > 0 ? (
        <RequestResultsGrid
          results={items}
          className={cn("transition-opacity", isChangingPage && "opacity-60")}
        />
      ) : (
        <p className="text-muted-foreground text-sm">
          Every title on this page is already in your library.
        </p>
      )}
      <RequestResultsPager
        label="Request to add pages"
        position={totalPages > 0 ? `Page ${page} of ${totalPages}` : `Page ${page}`}
        hasPrevious={page > 1}
        hasNext={page < totalPages}
        onPrevious={() => changePage(page - 1)}
        onNext={() => changePage(page + 1)}
        disabled={isChangingPage}
      />
    </section>
  );
}
