import { useCallback, useMemo, useState } from "react";
import { ChevronDown, ChevronUp, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { BrowseItem } from "@/api/types";
import type { PickerEntry } from "@/api/v2/watchTogetherPicker";
import { useT, type MessageKey } from "@/i18n";
import { useSectionTitle } from "@/lib/sectionTitle";
import type { WatchTogetherRoomMember } from "@/lib/watchTogether";
import { PickerRowSection, PosterTile, TogetherRow, type PickerCard } from "../picker/PickerRow";
import {
  useHomeShelfSections,
  usePickerRows,
  usePickerSearch,
  useRecentlyAdded,
  type ShelfItem,
} from "../picker/usePickerData";

/** What the shelf hands up: a card the viewer wants to look at on the stage. */
export interface BrowseSelection {
  card: PickerCard;
  /** For a series from "continue together": the season the room is on. */
  season?: number;
}

type Chip = "all" | "movies" | "series" | "together" | "watchlists" | "recent";
const chips: { id: Chip; label: MessageKey }[] = [
  { id: "all", label: "web.watchTogether.browse.chipAll" },
  { id: "movies", label: "web.watchTogether.browse.chipMovies" },
  { id: "series", label: "web.watchTogether.browse.chipSeries" },
  { id: "together", label: "web.watchTogether.browse.chipTogether" },
  { id: "watchlists", label: "web.watchTogether.browse.chipWatchlists" },
  { id: "recent", label: "web.watchTogether.browse.chipRecent" },
];

export function cardOf(item: ShelfItem | BrowseItem | PickerEntry["item"]): PickerCard {
  return {
    content_id: item.content_id,
    type: item.type,
    title: item.title,
    year: item.year,
    poster_url: item.poster_url,
    poster_thumbhash: item.poster_thumbhash,
  };
}

const emptyMessage: Record<Chip, MessageKey> = {
  all: "web.watchTogether.browse.emptyAll",
  movies: "web.watchTogether.browse.emptyMovies",
  series: "web.watchTogether.browse.emptySeries",
  together: "web.watchTogether.browse.emptyTogether",
  watchlists: "web.watchTogether.browse.emptyWatchlists",
  recent: "web.watchTogether.browse.emptyRecent",
};

/**
 * The room's browse shelf: search plus the rows the server computes over the
 * room's members, sitting under the stage. Choosing a tile does not commit
 * anything; it puts the item on the stage as a candidate. The shelf can fold
 * down to one line when the stage should own the screen.
 */
export function BrowseShelf({
  roomId,
  roomToken,
  members,
  selectedId,
  verb,
  collapsible = false,
  collapsedLabel,
  open,
  onOpenChange,
  onSelect,
}: {
  roomId: string;
  roomToken: string;
  members: WatchTogetherRoomMember[];
  /** The candidate or staged item, highlighted in the rows. */
  selectedId?: string;
  /** What choosing does, for the collapsed hint: "pick" or "suggest". */
  verb: "pick" | "suggest";
  collapsible?: boolean;
  collapsedLabel?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSelect: (selection: BrowseSelection) => void;
}) {
  const t = useT();
  const sectionTitle = useSectionTitle();
  const [query, setQuery] = useState("");
  const [chip, setChip] = useState<Chip>("all");
  const expanded = !collapsible || open;

  const rows = usePickerRows(roomId, roomToken, members);
  const search = usePickerSearch(query);
  const browsing = expanded && search.q === "";
  const home = useHomeShelfSections(
    browsing && (chip === "all" || chip === "movies" || chip === "series"),
  );
  const recentMovies = useRecentlyAdded(
    browsing && (chip === "all" || chip === "movies" || chip === "recent"),
    "movie",
  );
  const recentSeries = useRecentlyAdded(
    browsing && (chip === "all" || chip === "series" || chip === "recent"),
    "series",
  );

  const filterChip = useCallback(
    <T extends { type: string }>(items: T[]) =>
      items.filter((item) =>
        chip === "movies"
          ? item.type === "movie"
          : chip === "series"
            ? item.type === "series"
            : true,
      ),
    [chip],
  );
  const showRow = (id: Chip) => chip === "all" || chip === id;
  const togetherEntries = rows.data?.continue_together ?? [];
  const watchlistEntries = rows.data?.watchlist_union ?? [];
  const homeSections = useMemo(
    () =>
      home.sections
        .map((section) => ({ ...section, items: filterChip(section.items) }))
        .filter((section) => section.items.length > 0),
    [filterChip, home.sections],
  );
  const showRecentMovies = chip === "all" || chip === "movies" || chip === "recent";
  const showRecentSeries = chip === "all" || chip === "series" || chip === "recent";
  const searchItems = useMemo(() => filterChip(search.items), [filterChip, search.items]);
  const visibleCount =
    (showRow("together") ? togetherEntries.length : 0) +
    (showRow("watchlists") ? watchlistEntries.length : 0) +
    homeSections.reduce((count, section) => count + section.items.length, 0) +
    (showRecentMovies ? recentMovies.items.length : 0) +
    (showRecentSeries ? recentSeries.items.length : 0);
  const browseFetching = home.isFetching || recentMovies.isFetching || recentSeries.isFetching;
  const browseEmpty = !search.q && rows.isSuccess && !browseFetching && visibleCount === 0;

  if (!expanded) {
    return (
      <button
        type="button"
        onClick={() => onOpenChange(true)}
        className="surface-panel-subtle hover:border-ring/40 flex w-full items-center gap-3 rounded-xl border border-transparent px-4 py-3 text-left transition-colors"
        data-testid="browse-shelf-collapsed"
      >
        <Search className="text-muted-foreground size-4 shrink-0" />
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-semibold">
            {collapsedLabel ?? t("web.watchTogether.browse.collapsed")}
          </span>
          <span className="text-muted-foreground block text-xs">
            {verb === "pick"
              ? t("web.watchTogether.browse.collapsedHintPick")
              : t("web.watchTogether.browse.collapsedHintSuggest")}
          </span>
        </span>
        <ChevronDown className="text-muted-foreground size-4 shrink-0" />
      </button>
    );
  }

  return (
    <section
      className="flex min-w-0 flex-col gap-4"
      aria-label={
        verb === "pick"
          ? t("web.watchTogether.browse.sectionPick")
          : t("web.watchTogether.browse.sectionSuggest")
      }
    >
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-56 flex-1">
          <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2" />
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("web.watchTogether.browse.searchPlaceholder")}
            aria-label={t("web.watchTogether.browse.searchLabel")}
            className="border-border bg-surface placeholder:text-muted-foreground h-10 w-full rounded-lg border py-2 pr-3 pl-9 text-sm outline-none focus:border-white/30"
          />
        </div>
        <div
          role="tablist"
          aria-label={t("web.watchTogether.browse.filterLabel")}
          className="flex flex-wrap gap-1"
        >
          {chips.map((c) => (
            <button
              key={c.id}
              type="button"
              role="tab"
              aria-selected={chip === c.id}
              onClick={() => setChip(c.id)}
              className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                chip === c.id
                  ? "bg-foreground text-background"
                  : "bg-surface-raised text-muted-foreground hover:text-foreground"
              }`}
            >
              {t(c.label)}
            </button>
          ))}
        </div>
        {collapsible ? (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={() => onOpenChange(false)}
            aria-label={t("web.watchTogether.browse.hide")}
          >
            <ChevronUp className="size-4" />
          </Button>
        ) : null}
      </div>

      {search.q ? (
        <section
          className="flex flex-col gap-2"
          aria-label={t("web.watchTogether.browse.searchResults")}
        >
          <div className="flex items-baseline gap-2">
            <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
              {t("web.watchTogether.browse.searchResults")}
            </h3>
            {!search.isFetching ? (
              <span className="text-muted-foreground text-[11px]">
                {t("web.watchTogether.browse.resultCount", { count: String(searchItems.length) })}
              </span>
            ) : null}
          </div>
          {search.isError ? (
            <div
              role="alert"
              className="text-muted-foreground flex items-center gap-2 py-6 text-sm"
            >
              {t("web.watchTogether.browse.searchFailed")}
              <Button variant="outline" size="sm" onClick={() => void search.refetch()}>
                {t("common.retry")}
              </Button>
            </div>
          ) : searchItems.length === 0 && !search.isFetching ? (
            <p className="text-muted-foreground py-6 text-sm">
              {t("web.watchTogether.browse.noMatches", { query: search.q })}
            </p>
          ) : (
            // Results are scanned, not flicked through: a wrapping grid.
            <div className="flex flex-wrap gap-3">
              {searchItems.map((item) => (
                <PosterTile
                  key={item.content_id}
                  card={cardOf(item)}
                  selected={selectedId === item.content_id}
                  onClick={() => onSelect({ card: cardOf(item) })}
                />
              ))}
            </div>
          )}
        </section>
      ) : (
        <>
          {showRow("together") ? (
            <TogetherRow
              title={t("web.watchTogether.browse.togetherTitle")}
              hint={t("web.watchTogether.browse.togetherHint")}
              entries={togetherEntries}
              members={members}
              selectedId={selectedId}
              onSelect={(entry) =>
                onSelect({
                  card: { ...cardOf(entry.item), library_id: undefined },
                  season: entry.next_up?.season_number,
                })
              }
            />
          ) : null}
          {showRow("watchlists") ? (
            <TogetherRow
              title={t("web.watchTogether.browse.watchlistsTitle")}
              hint={t("web.watchTogether.browse.watchlistsHint")}
              entries={watchlistEntries}
              members={members}
              selectedId={selectedId}
              onSelect={(entry) => onSelect({ card: cardOf(entry.item) })}
            />
          ) : null}
          {homeSections.map((section) => (
            <PickerRowSection key={section.id} title={sectionTitle(section)}>
              {section.items.map((item) => (
                <PosterTile
                  key={item.content_id}
                  card={cardOf(item)}
                  selected={selectedId === item.content_id}
                  onClick={() => onSelect({ card: cardOf(item) })}
                />
              ))}
            </PickerRowSection>
          ))}
          {showRecentMovies && (recentMovies.items.length > 0 || recentMovies.isFetching) ? (
            <PickerRowSection
              title={t("web.watchTogether.browse.recentMovies")}
              loading={recentMovies.isFetching}
            >
              {recentMovies.items.map((item) => (
                <PosterTile
                  key={item.content_id}
                  card={cardOf(item)}
                  selected={selectedId === item.content_id}
                  onClick={() => onSelect({ card: cardOf(item) })}
                />
              ))}
            </PickerRowSection>
          ) : null}
          {showRecentSeries && (recentSeries.items.length > 0 || recentSeries.isFetching) ? (
            <PickerRowSection
              title={t("web.watchTogether.browse.recentSeries")}
              loading={recentSeries.isFetching}
            >
              {recentSeries.items.map((item) => (
                <PosterTile
                  key={item.content_id}
                  card={cardOf(item)}
                  selected={selectedId === item.content_id}
                  onClick={() => onSelect({ card: cardOf(item) })}
                />
              ))}
            </PickerRowSection>
          ) : null}
          {browseEmpty ? (
            <p className="text-muted-foreground py-8 text-center text-sm">
              {t(emptyMessage[chip])}
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}
