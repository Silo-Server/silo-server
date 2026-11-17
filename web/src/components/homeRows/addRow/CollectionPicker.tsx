import { useId } from "react";
import { Check } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { RadioDot, RadioGroup } from "@/components/ui/radio-group";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { useT } from "@/i18n";
import type { RowCollections } from "@/lib/homeRows/types";
import { CollectionOptionBody, CollectionSearchBar } from "./collectionChooser";
import { collectionEmptyText, collectionOptionMeta, useCollectionSearch } from "./collectionSearch";

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
  const { options } = collections;
  const search = useCollectionSearch(options);
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
        <CollectionSearchBar
          count={options.length}
          query={search.query}
          onQueryChange={search.setQuery}
          filter={search.filter}
          onFilterChange={search.setFilter}
        />
        {missing ? (
          <p
            role="status"
            className="border-warning/40 bg-warning/10 border-b px-4 py-2.5 text-[13px]"
          >
            {t("web.home.addRow.collection.missing")}
          </p>
        ) : null}
        {search.shown.length > 0 ? (
          <RadioGroup
            aria-labelledby={labelId}
            value={value}
            onValueChange={(id) => {
              const picked = options.find((option) => option.id === id);
              if (picked) onPick(picked);
            }}
            className="max-h-[280px] gap-0.5 overflow-y-auto p-1.5"
          >
            {search.shown.map((option) => (
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
            {collectionEmptyText(t, collections, search.query.trim())}
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
  const meta = collectionOptionMeta(option);
  return (
    <RadioGroupPrimitive.Item
      value={option.id}
      aria-label={option.title}
      aria-describedby={metaId}
      className="group/radio hover:bg-accent/60 focus-visible:ring-ring/50 data-[state=checked]:bg-accent grid grid-cols-[18px_36px_minmax(0,1fr)_auto] items-center gap-3 rounded-xl px-3 py-2 text-start outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-60"
    >
      <RadioDot />
      <CollectionOptionBody
        title={option.title}
        posterUrl={option.poster_url}
        thumbhash={option.poster_thumbhash}
        meta={meta}
        metaId={metaId}
        srMeta={
          onPage ? t("web.home.addRow.collection.metaOnPage", { meta, page: pageLabel }) : meta
        }
      />
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
