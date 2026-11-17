import { Search } from "lucide-react";
import { PosterArt } from "@/components/calm/PosterTile";
import { Input } from "@/components/ui/input";
import { useT, type MessageKey } from "@/i18n";
import type { CollectionFilter } from "./collectionSearch";

/**
 * What the collection pickers of Add row share: the search box, the
 * Manual / Smart / Synced filter, and how one collection reads in the list.
 * A collection row picks one (CollectionPicker); a row of collections adds
 * several (CollectionsRowEditor).
 */

const FILTERS: ReadonlyArray<[CollectionFilter, MessageKey]> = [
  ["all", "web.home.addRow.collection.filterAll"],
  ["manual", "web.home.addRow.collection.filterManual"],
  ["smart", "web.home.addRow.collection.filterSmart"],
  ["synced", "web.home.addRow.collection.filterSynced"],
];

/** The bar over a collection list: the search box and the type filter. */
export function CollectionSearchBar({
  count,
  query,
  onQueryChange,
  filter,
  onFilterChange,
}: {
  /** How many collections there are, for the placeholder. */
  count: number;
  query: string;
  onQueryChange: (query: string) => void;
  filter: CollectionFilter;
  onFilterChange: (filter: CollectionFilter) => void;
}) {
  const t = useT();
  return (
    <div className="border-border flex flex-wrap items-center gap-2 border-b p-2">
      <div className="relative min-w-40 flex-1">
        <Search
          aria-hidden
          className="text-muted-foreground absolute start-3 top-1/2 size-4 -translate-y-1/2"
        />
        <Input
          type="search"
          aria-label={t("web.home.addRow.collection.search")}
          placeholder={t("web.home.addRow.collection.searchPlaceholder", {
            count,
            count_text: count,
          })}
          className="h-9 border-0 bg-transparent ps-9 shadow-none focus-visible:ring-0"
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
        />
      </div>
      <div
        role="group"
        aria-label={t("web.home.addRow.collection.type")}
        className="bg-muted/60 inline-flex rounded-[10px] p-0.5"
      >
        {FILTERS.map(([name, label]) => (
          <button
            key={name}
            type="button"
            aria-pressed={filter === name}
            onClick={() => onFilterChange(name)}
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 aria-pressed:bg-background aria-pressed:text-foreground h-8 rounded-lg px-2.5 text-[13px] font-medium outline-none focus-visible:ring-[3px] aria-pressed:shadow-sm"
          >
            {t(label)}
          </button>
        ))}
      </div>
    </div>
  );
}

/** A collection's poster, name and meta line, as every collection list shows them. */
export function CollectionOptionBody({
  title,
  posterUrl,
  thumbhash,
  meta,
  metaId,
  srMeta,
}: {
  title: string;
  posterUrl?: string;
  thumbhash?: string;
  meta: string;
  /** The id of the meta line screen readers hear, for the item's aria-describedby. */
  metaId: string;
  /** What screen readers hear in place of `meta`; `meta` when absent. */
  srMeta?: string;
}) {
  return (
    <>
      <PosterArt
        posterUrl={posterUrl || undefined}
        thumbhash={thumbhash}
        className="ring-border/60 aspect-[2/3] w-9 rounded-md ring-1 ring-inset"
      />
      <span className="min-w-0">
        <span className="block truncate text-sm font-semibold">{title}</span>
        <span aria-hidden className="text-muted-foreground block truncate text-[13px]">
          {meta}
        </span>
        <span id={metaId} className="sr-only">
          {srMeta ?? meta}
        </span>
      </span>
    </>
  );
}
