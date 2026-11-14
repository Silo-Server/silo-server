import { useId } from "react";
import { Info } from "lucide-react";

import type { QueryDefinition } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useShownRatingSources } from "@/hooks/queries/ratingsCapability";
import { useT, type Translate } from "@/i18n";
import { COLLECTION_SOURCE_ORDER } from "@/lib/collectionSortConfig";
import {
  manualOrderLine,
  noLimit,
  orderHelp,
  syncedOrderHelp,
  storedSortLine,
} from "@/lib/collections/copy";
import { clearDefaultSort } from "@/lib/collections/draft";
import {
  getDefaultQuerySortOrder,
  getQuerySortOptions,
  querySortScopeForMediaScope,
  QUERY_SORT_OPTIONS,
  type QuerySortOrder,
} from "@/lib/querySortOptions";

const ALPHABETICAL = new Set(["title", "author", "narrator", "series"]);
const BY_DATE = new Set([
  "added_at",
  "release_date",
  "last_air_date",
  "latest_episode_added",
  "year",
  "date_viewed",
]);

type Direction = "az" | "za" | "newest" | "oldest" | "highest" | "lowest";

/** Which way each direction reads for a sort: A–Z, newest first, highest first. */
function directionsOf(field: string): Record<QuerySortOrder, Direction> {
  if (ALPHABETICAL.has(field)) return { asc: "az", desc: "za" };
  if (BY_DATE.has(field)) return { desc: "newest", asc: "oldest" };
  return { desc: "highest", asc: "lowest" };
}

/** How each direction reads for a sort: "A–Z", "Newest first", "Highest first". */
function directionLabels(t: Translate, field: string): Record<QuerySortOrder, string> {
  const directions = directionsOf(field);
  return {
    asc: t("web.collections.fields.direction", { direction: directions.asc }),
    desc: t("web.collections.fields.direction", { direction: directions.desc }),
  };
}

/** "Title A–Z", "Date Added, newest first": a stored default sort, as the stored-sort line names it. */
function sortLabel(t: Translate, field: string, order: QuerySortOrder): string {
  const name = QUERY_SORT_OPTIONS.find((option) => option.value === field)?.label ?? field;
  const direction = directionsOf(field)[order];
  return ALPHABETICAL.has(field)
    ? t("web.collections.fields.sortAlphabetical", { name, direction })
    : t("web.collections.fields.sortWithDirection", { name, direction });
}

/** A sort_config's field ("" keeps the source order) and direction, the field's default when unset. */
function storedSort(sortConfig: Record<string, unknown> | undefined) {
  const field = typeof sortConfig?.field === "string" ? sortConfig.field.trim() : "";
  const order: QuerySortOrder =
    sortConfig?.order === "asc" || sortConfig?.order === "desc"
      ? sortConfig.order
      : getDefaultQuerySortOrder(field);
  return { field, order };
}

function storedDefaultSort(t: Translate, sortConfig: Record<string, unknown> | undefined) {
  const { field, order } = storedSort(sortConfig);
  return field ? sortLabel(t, field, order) : null;
}

/** Commits the typed limit: blank is no limit; anything not a positive whole number keeps the old one. */
function parsedLimit(text: string, current: number | undefined): number | undefined {
  const trimmed = text.trim();
  if (trimmed === "") return undefined;
  // Whole digits only: parseInt would read "2.5" as 2 and "1e2" as 1.
  if (!/^\d+$/.test(trimmed)) return current;
  const parsed = Number(trimmed);
  return parsed > 0 ? parsed : current;
}

export type OrderBlockProps =
  | { mode?: "manual" }
  | {
      mode: "synced";
      /** The default sort viewers land on; `{}` keeps the list's own order. */
      sortConfig: Record<string, unknown> | undefined;
      /** Blank takes the whole list. */
      limit: number | undefined;
      onSortChange: (sortConfig: Record<string, unknown>) => void;
      onLimitChange: (limit: number | undefined) => void;
      allowPersonalized: boolean;
    }
  | {
      mode: "smart";
      rules: QueryDefinition;
      sortConfig: Record<string, unknown> | undefined;
      /** Order changes clear a stored default sort, which would otherwise win over them. */
      onChange: (next: { rules: QueryDefinition; sortConfig?: Record<string, unknown> }) => void;
      allowPersonalized: boolean;
    };

/**
 * The last block of a Contents panel: how the titles are ordered. Manual is a
 * read-only line; Smart picks a sort, its direction and a maximum, and shows a
 * stored default sort (which wins over them) with Clear; a Synced list picks
 * the sort viewers land on, its direction, and how many of the list's titles
 * it keeps.
 */
export function OrderBlock(props: OrderBlockProps) {
  const t = useT();
  if (props.mode === "synced") return <SyncedOrder {...props} />;
  if (props.mode !== "smart") {
    return (
      <div className="border-border/70 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 border-t pt-4">
        <h3 className="text-[14.5px] font-semibold">{t("web.collections.fields.order")}</h3>
        <p className="text-muted-foreground text-[13.5px]">{manualOrderLine()}</p>
      </div>
    );
  }
  return <SmartOrder {...props} />;
}

function SmartOrder({
  rules,
  sortConfig,
  onChange,
  allowPersonalized,
}: Extract<OrderBlockProps, { mode: "smart" }>) {
  const t = useT();
  const id = useId();
  const shownRatingSources = useShownRatingSources();
  const options = getQuerySortOptions({
    includePersonalized: allowPersonalized,
    relevanceScope: querySortScopeForMediaScope(rules.media_scope),
    shownRatingSources,
    keepSortField: rules.sort.field,
  });
  const directions = directionLabels(t, rules.sort.field);
  const stored = storedDefaultSort(t, sortConfig);

  function setSort(sort: QueryDefinition["sort"]) {
    onChange({
      rules: { ...rules, sort },
      sortConfig: stored ? clearDefaultSort(sortConfig) : undefined,
    });
  }

  function commitLimit(input: HTMLInputElement) {
    const limit = parsedLimit(input.value, rules.limit);
    input.value = limit === undefined ? "" : String(limit);
    if (limit !== rules.limit) onChange({ rules: { ...rules, limit } });
  }

  return (
    <section
      aria-labelledby={`${id}-heading`}
      className="border-border/70 grid gap-3 border-t pt-4"
    >
      <h3 id={`${id}-heading`} className="text-[14.5px] font-semibold">
        {t("web.collections.fields.order")}
      </h3>
      <div className="grid gap-2.5 sm:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_minmax(0,1fr)]">
        <Select
          value={rules.sort.field}
          onValueChange={(field) =>
            setSort({
              field: field as QueryDefinition["sort"]["field"],
              order: getDefaultQuerySortOrder(field),
            })
          }
        >
          <SelectTrigger aria-label={t("web.collections.fields.sortBy")} className="h-11 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={rules.sort.order}
          onValueChange={(order) =>
            setSort({ ...rules.sort, order: order as QueryDefinition["sort"]["order"] })
          }
        >
          <SelectTrigger
            aria-label={t("web.collections.fields.directionLabel")}
            className="h-11 w-full"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="desc">{directions.desc}</SelectItem>
            <SelectItem value="asc">{directions.asc}</SelectItem>
          </SelectContent>
        </Select>
        <MaxTitlesInput limit={rules.limit} placeholder={noLimit()} onCommit={commitLimit} />
      </div>
      {stored ? (
        <div className="bg-muted/50 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl px-3 py-2.5 text-[13px]">
          <Info aria-hidden className="text-muted-foreground size-4 shrink-0" />
          <p className="min-w-0 flex-1">{storedSortLine(stored)}</p>
          <Button
            type="button"
            size="sm"
            variant="outline"
            aria-label={t("web.collections.fields.clearStoredSort", { sort: stored })}
            onClick={() => onChange({ rules, sortConfig: clearDefaultSort(sortConfig) })}
          >
            {t("web.collections.fields.clear")}
          </Button>
        </div>
      ) : null}
      <p className="text-muted-foreground text-[13px]">{orderHelp()}</p>
    </section>
  );
}

/**
 * "[ 250   | max titles ]", drawn like the Sort selects beside it: commits on
 * blur or Enter.
 */
function MaxTitlesInput({
  limit,
  placeholder,
  onCommit,
}: {
  limit: number | undefined;
  placeholder: string;
  onCommit: (input: HTMLInputElement) => void;
}) {
  const t = useT();
  return (
    <label className="border-border bg-background focus-within:border-ring focus-within:ring-ring/50 flex h-11 items-center gap-3 rounded-md border px-3 text-sm shadow-xs transition-[color,box-shadow] focus-within:ring-[3px]">
      <Input
        key={limit ?? ""}
        type="number"
        min={1}
        step={1}
        aria-label={t("web.collections.copy.fieldLimit")}
        placeholder={placeholder}
        defaultValue={limit ?? ""}
        className="h-full min-w-0 flex-1 rounded-none border-0 bg-transparent p-0 shadow-none focus-visible:ring-0"
        onBlur={(event) => onCommit(event.currentTarget)}
        onKeyDown={(event) => {
          if (event.key === "Enter") event.currentTarget.blur();
        }}
      />
      <span
        aria-hidden
        className="border-border text-muted-foreground flex shrink-0 items-center self-stretch border-s ps-3"
      >
        {t("web.collections.fields.maxTitlesSuffix")}
      </span>
    </label>
  );
}

function SyncedOrder({
  sortConfig,
  limit,
  onSortChange,
  onLimitChange,
  allowPersonalized,
}: Extract<OrderBlockProps, { mode: "synced" }>) {
  const t = useT();
  const id = useId();
  const shownRatingSources = useShownRatingSources();
  const { field, order } = storedSort(sortConfig);
  const options = getQuerySortOptions({
    includePersonalized: allowPersonalized,
    shownRatingSources,
    keepSortField: field,
  });
  const directions = directionLabels(t, field);

  function commitLimit(input: HTMLInputElement) {
    const next = parsedLimit(input.value, limit);
    input.value = next === undefined ? "" : String(next);
    if (next !== limit) onLimitChange(next);
  }

  return (
    <section
      aria-labelledby={`${id}-heading`}
      className="border-border/70 grid gap-3 border-t pt-4"
    >
      <h3 id={`${id}-heading`} className="text-[14.5px] font-semibold">
        {t("web.collections.fields.order")}
      </h3>
      <div className="grid gap-2.5 sm:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_minmax(0,1fr)]">
        <Select
          value={field || COLLECTION_SOURCE_ORDER}
          onValueChange={(next) =>
            onSortChange(
              next === COLLECTION_SOURCE_ORDER
                ? {}
                : { field: next, order: getDefaultQuerySortOrder(next) },
            )
          }
        >
          <SelectTrigger
            aria-label={t("web.collections.fields.defaultSort")}
            className="h-11 w-full"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={COLLECTION_SOURCE_ORDER}>
              {t("web.collections.fields.listOrder")}
            </SelectItem>
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {/* The list's own order has one direction: the one its source publishes. */}
        <Select
          value={field ? order : COLLECTION_SOURCE_ORDER}
          disabled={!field}
          onValueChange={(next) => onSortChange({ field, order: next })}
        >
          <SelectTrigger
            aria-label={t("web.collections.fields.directionLabel")}
            className="h-11 w-full"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {field ? (
              <>
                <SelectItem value="desc">{directions.desc}</SelectItem>
                <SelectItem value="asc">{directions.asc}</SelectItem>
              </>
            ) : (
              <SelectItem value={COLLECTION_SOURCE_ORDER}>
                {t("web.collections.fields.asListed")}
              </SelectItem>
            )}
          </SelectContent>
        </Select>
        <MaxTitlesInput
          limit={limit}
          placeholder={t("web.collections.fields.wholeList")}
          onCommit={commitLimit}
        />
      </div>
      <p className="text-muted-foreground text-[13px]">
        {syncedOrderHelp()} {orderHelp()}
      </p>
    </section>
  );
}
