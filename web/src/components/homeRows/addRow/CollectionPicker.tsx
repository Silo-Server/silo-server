import { useId, useMemo, useState } from "react";
import { Check, Search } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { Input } from "@/components/ui/input";
import { RadioDot, RadioGroup } from "@/components/ui/radio-group";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { collectionKind, titleCount } from "@/lib/homeRows/describe";
import type { RowCollections } from "@/lib/homeRows/types";
import { PosterArt } from "@/components/calm/PosterTile";

const FILTERS = ["All", "Manual", "Smart", "Synced list"] as const;
type Filter = (typeof FILTERS)[number];

function emptyText(collections: RowCollections, query: string): string {
  if (collections.loading) return "Loading collections…";
  if (collections.failed) return "Collections didn't load. Close this and try again.";
  if (collections.options.length === 0) return "No collections yet. Make one in Collections first.";
  return query ? `No collections match "${query}".` : "No collections of this type.";
}

/**
 * Step 2 of a collection row: search, a Manual / Smart / Synced list filter,
 * and one radio per collection with its poster, type and title count.
 * Collections already shown on this page are marked.
 */
export function CollectionPicker({
  collections,
  value,
  onPick,
  onPageIds,
  pageLabel,
  locked = false,
}: {
  collections: RowCollections;
  /** The collection the row shows now, or "". */
  value: string;
  onPick: (option: CollectionOption) => void;
  /** Collections a row on this page already shows. */
  onPageIds: ReadonlySet<string>;
  pageLabel: string;
  /** A legacy Trakt row: the server refuses a different collection. */
  locked?: boolean;
}) {
  const labelId = useId();
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<Filter>("All");
  const { options } = collections;
  const needle = query.trim().toLowerCase();
  const shown = useMemo(
    () =>
      options.filter(
        (option) =>
          (filter === "All" || collectionKind(option.collection_type) === filter) &&
          option.title.toLowerCase().includes(needle),
      ),
    [options, filter, needle],
  );
  const missing =
    value !== "" &&
    !collections.loading &&
    !collections.failed &&
    !options.some((option) => option.id === value);

  return (
    <div className="grid gap-2">
      <span id={labelId} className="text-sm font-medium">
        Collection
      </span>
      <div className="border-border overflow-hidden rounded-2xl border">
        <div className="border-border flex flex-wrap items-center gap-2 border-b p-2">
          <div className="relative min-w-40 flex-1">
            <Search
              aria-hidden
              className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2"
            />
            <Input
              type="search"
              aria-label="Search collections"
              placeholder={`Search ${options.length} ${options.length === 1 ? "collection" : "collections"}`}
              className="h-9 border-0 bg-transparent pl-9 shadow-none focus-visible:ring-0"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <div
            role="group"
            aria-label="Collection type"
            className="bg-muted/60 inline-flex rounded-[10px] p-0.5"
          >
            {FILTERS.map((name) => (
              <button
                key={name}
                type="button"
                aria-pressed={filter === name}
                onClick={() => setFilter(name)}
                className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 aria-pressed:bg-background aria-pressed:text-foreground h-8 rounded-lg px-2.5 text-[13px] font-medium outline-none focus-visible:ring-[3px] aria-pressed:shadow-sm"
              >
                {name}
              </button>
            ))}
          </div>
        </div>
        {locked ? (
          <p className="text-muted-foreground border-border border-b px-4 py-2.5 text-[13px]">
            This row follows a Trakt list, so its collection can't change.
          </p>
        ) : missing ? (
          <p
            role="status"
            className="border-warning/40 bg-warning/10 border-b px-4 py-2.5 text-[13px]"
          >
            This row's collection isn't in this list. The row keeps it until you pick another.
          </p>
        ) : null}
        {shown.length > 0 ? (
          <RadioGroup
            aria-labelledby={labelId}
            value={value}
            disabled={locked}
            onValueChange={(id) => {
              const picked = options.find((option) => option.id === id);
              if (picked) onPick(picked);
            }}
            className="max-h-[280px] gap-0.5 overflow-y-auto p-1.5"
          >
            {shown.map((option) => (
              <CollectionRadio
                key={option.id}
                option={option}
                onPage={onPageIds.has(option.id)}
                pageLabel={pageLabel}
              />
            ))}
          </RadioGroup>
        ) : (
          <p role="status" className="text-muted-foreground px-4 py-6 text-sm">
            {emptyText(collections, query.trim())}
          </p>
        )}
      </div>
    </div>
  );
}

function CollectionRadio({
  option,
  onPage,
  pageLabel,
}: {
  option: CollectionOption;
  onPage: boolean;
  pageLabel: string;
}) {
  const metaId = useId();
  const kind = collectionKind(option.collection_type);
  const meta = [kind, option.item_count === undefined ? undefined : titleCount(option.item_count)]
    .filter(Boolean)
    .join(" · ");
  return (
    <RadioGroupPrimitive.Item
      value={option.id}
      aria-label={option.title}
      aria-describedby={metaId}
      className="group/radio hover:bg-accent/60 focus-visible:ring-ring/50 data-[state=checked]:bg-accent grid grid-cols-[18px_36px_minmax(0,1fr)_auto] items-center gap-3 rounded-xl px-3 py-2 text-left outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-60"
    >
      <RadioDot />
      <PosterArt
        posterUrl={option.poster_url || undefined}
        thumbhash={option.poster_thumbhash}
        className="ring-border/60 aspect-[2/3] w-9 rounded-md ring-1 ring-inset"
      />
      <span className="min-w-0">
        <span className="block truncate text-sm font-semibold">{option.title}</span>
        <span aria-hidden className="text-muted-foreground block truncate text-[12.5px]">
          {meta}
        </span>
        <span id={metaId} className="sr-only">
          {onPage ? `${meta} · On ${pageLabel}` : meta}
        </span>
      </span>
      {onPage ? (
        <span
          aria-hidden
          className="bg-background/70 ring-border inline-flex h-[22px] items-center gap-1 rounded-full px-2 text-[11.5px] font-semibold ring-1 ring-inset"
        >
          <Check className="text-success size-3" />
          On {pageLabel}
        </span>
      ) : (
        <span />
      )}
    </RadioGroupPrimitive.Item>
  );
}
