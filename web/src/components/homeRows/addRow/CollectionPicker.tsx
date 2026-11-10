import { useId, useMemo, useState } from "react";
import { Check, Search } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { Input } from "@/components/ui/input";
import { RadioDot, RadioGroup } from "@/components/ui/radio-group";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { collectionKindOf, type CollectionKind } from "@/lib/collections/types";
import { collectionKind, titleCount } from "@/lib/homeRows/describe";
import type { RowCollections } from "@/lib/homeRows/types";
import { PosterArt } from "@/components/calm/PosterTile";

type Filter = "all" | CollectionKind;

const FILTERS: ReadonlyArray<[Filter, MessageKey]> = [
  ["all", "web.home.addRow.collection.filterAll"],
  ["manual", "web.home.addRow.collection.filterManual"],
  ["smart", "web.home.addRow.collection.filterSmart"],
  ["synced", "web.home.addRow.collection.filterSynced"],
];

function emptyText(t: Translate, collections: RowCollections, query: string): string {
  if (collections.loading) return t("web.home.addRow.collection.loading");
  if (collections.failed) return t("web.home.addRow.collection.failed");
  if (collections.options.length === 0) return t("web.home.addRow.collection.none");
  return query
    ? t("web.home.addRow.collection.noMatch", { query })
    : t("web.home.addRow.collection.noneOfType");
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
}: {
  collections: RowCollections;
  /** The collection the row shows now, or "". */
  value: string;
  onPick: (option: CollectionOption) => void;
  /** Collections a row on this page already shows. */
  onPageIds: ReadonlySet<string>;
  pageLabel: string;
}) {
  const t = useT();
  const labelId = useId();
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const { options } = collections;
  const needle = query.trim().toLowerCase();
  const shown = useMemo(
    () =>
      options.filter(
        (option) =>
          (filter === "all" ||
            (!!option.collection_type && collectionKindOf(option.collection_type) === filter)) &&
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
        {t("web.home.addRow.collection.label")}
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
              aria-label={t("web.home.addRow.collection.search")}
              placeholder={t("web.home.addRow.collection.searchPlaceholder", {
                count: options.length,
                count_text: String(options.length),
              })}
              className="h-9 border-0 bg-transparent pl-9 shadow-none focus-visible:ring-0"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
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
                onClick={() => setFilter(name)}
                className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 aria-pressed:bg-background aria-pressed:text-foreground h-8 rounded-lg px-2.5 text-[13px] font-medium outline-none focus-visible:ring-[3px] aria-pressed:shadow-sm"
              >
                {t(label)}
              </button>
            ))}
          </div>
        </div>
        {missing ? (
          <p
            role="status"
            className="border-warning/40 bg-warning/10 border-b px-4 py-2.5 text-[13px]"
          >
            {t("web.home.addRow.collection.missing")}
          </p>
        ) : null}
        {shown.length > 0 ? (
          <RadioGroup
            aria-labelledby={labelId}
            value={value}
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
            {emptyText(t, collections, query.trim())}
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
  const t = useT();
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
        <span aria-hidden className="text-muted-foreground block truncate text-[13px]">
          {meta}
        </span>
        <span id={metaId} className="sr-only">
          {onPage ? t("web.home.addRow.collection.metaOnPage", { meta, page: pageLabel }) : meta}
        </span>
      </span>
      {onPage ? (
        <span
          aria-hidden
          className="bg-background/70 ring-border inline-flex h-[22px] items-center gap-1 rounded-full px-2 text-xs font-semibold ring-1 ring-inset"
        >
          <Check className="text-success size-3" />
          {t("web.home.addRow.onPage", { page: pageLabel })}
        </span>
      ) : (
        <span />
      )}
    </RadioGroupPrimitive.Item>
  );
}
