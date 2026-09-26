import { useCallback, useMemo, useState } from "react";
import { ChevronDown, ChevronUp, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { BrowseItem } from "@/api/types";
import type { PickerEntry } from "@/api/v2/watchTogetherPicker";
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
const chips: { id: Chip; label: string }[] = [
  { id: "all", label: "All" },
  { id: "movies", label: "Movies" },
  { id: "series", label: "Series" },
  { id: "together", label: "Together" },
  { id: "watchlists", label: "Watchlists" },
  { id: "recent", label: "Recent" },
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

const emptyMessage: Record<Chip, string> = {
  all: "Nothing to browse yet. Search for something above.",
  movies: "No movies in the library yet. Search for one above.",
  series: "No series in the library yet. Search for one above.",
  together: "Nothing in progress for two or more of you yet.",
  watchlists: "Nobody in the room has anything on their watchlist.",
  recent: "Nothing added recently.",
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
  collapsedLabel = "Browse",
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
          <span className="block text-sm font-semibold">{collapsedLabel}</span>
          <span className="text-muted-foreground block text-xs">
            Search or browse what the room has in common to {verb} something.
          </span>
        </span>
        <ChevronDown className="text-muted-foreground size-4 shrink-0" />
      </button>
    );
  }

  return (
    <section
      className="flex min-w-0 flex-col gap-4"
      aria-label={verb === "pick" ? "Browse to pick" : "Browse to suggest"}
    >
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-56 flex-1">
          <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2" />
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search movies, series, episodes…"
            aria-label="Search movies and series"
            className="border-border bg-surface placeholder:text-muted-foreground h-10 w-full rounded-lg border py-2 pr-3 pl-9 text-sm outline-none focus:border-white/30"
          />
        </div>
        <div role="tablist" aria-label="Filter" className="flex flex-wrap gap-1">
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
              {c.label}
            </button>
          ))}
        </div>
        {collapsible ? (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={() => onOpenChange(false)}
            aria-label="Hide browse"
          >
            <ChevronUp className="size-4" />
          </Button>
        ) : null}
      </div>

      {search.q ? (
        <section className="flex flex-col gap-2" aria-label="Search results">
          <div className="flex items-baseline gap-2">
            <h3 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
              Search results
            </h3>
            {!search.isFetching ? (
              <span className="text-muted-foreground text-[11px]">· {searchItems.length}</span>
            ) : null}
          </div>
          {search.isError ? (
            <div
              role="alert"
              className="text-muted-foreground flex items-center gap-2 py-6 text-sm"
            >
              Could not search.
              <Button variant="outline" size="sm" onClick={() => void search.refetch()}>
                Retry
              </Button>
            </div>
          ) : searchItems.length === 0 && !search.isFetching ? (
            <p className="text-muted-foreground py-6 text-sm">
              No matches for “{search.q}”. Try another title, or clear the filter.
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
              title="Continue watching together"
              hint="in progress for 2+ of you"
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
              title="On your watchlists"
              hint="anyone in the room"
              entries={watchlistEntries}
              members={members}
              selectedId={selectedId}
              onSelect={(entry) => onSelect({ card: cardOf(entry.item) })}
            />
          ) : null}
          {homeSections.map((section) => (
            <PickerRowSection key={section.id} title={section.title}>
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
            <PickerRowSection title="Recently added movies" loading={recentMovies.isFetching}>
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
            <PickerRowSection title="Recently added series" loading={recentSeries.isFetching}>
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
            <p className="text-muted-foreground py-8 text-center text-sm">{emptyMessage[chip]}</p>
          ) : null}
        </>
      )}
    </section>
  );
}
