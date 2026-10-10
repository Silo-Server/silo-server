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
import { COLLECTION_MAX_ITEMS } from "@/lib/collectionTemplates";
import {
  COLLECTION_SOURCE_ORDER,
  collectionDefaultSortOptions,
  selectValueToSortConfig,
  sortConfigToSelectValue,
} from "@/lib/collectionSortConfig";
import {
  MANUAL_ORDER_LINE,
  NO_LIMIT,
  ORDER_HELP,
  SYNCED_ORDER_HELP,
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

/** How each direction reads for a sort: "A–Z", "Newest first", "Highest first". */
function directionLabels(field: string): Record<QuerySortOrder, string> {
  if (ALPHABETICAL.has(field)) return { asc: "A–Z", desc: "Z–A" };
  if (BY_DATE.has(field)) return { desc: "Newest first", asc: "Oldest first" };
  return { desc: "Highest first", asc: "Lowest first" };
}

/** "Title A–Z", "Date Added, newest first": a stored default sort, as the stored-sort line names it. */
function sortLabel(field: string, order: string | undefined): string {
  const name = QUERY_SORT_OPTIONS.find((option) => option.value === field)?.label ?? field;
  const direction =
    directionLabels(field)[
      order === "asc" || order === "desc" ? order : getDefaultQuerySortOrder(field)
    ];
  return ALPHABETICAL.has(field) ? `${name} ${direction}` : `${name}, ${direction.toLowerCase()}`;
}

function storedDefaultSort(sortConfig: Record<string, unknown> | undefined) {
  const field = typeof sortConfig?.field === "string" ? sortConfig.field.trim() : "";
  return field ? sortLabel(field, sortConfig?.order as string | undefined) : null;
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
      /** Max titles shown but fixed, as on a legacy Trakt list. */
      limitLocked?: boolean;
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
 * the sort viewers land on and how many of the list's titles it keeps.
 */
export function OrderBlock(props: OrderBlockProps) {
  if (props.mode === "synced") return <SyncedOrder {...props} />;
  if (props.mode !== "smart") {
    return (
      <div className="border-border/70 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 border-t pt-4">
        <h3 className="text-[14.5px] font-semibold">Order</h3>
        <p className="text-muted-foreground text-[13.5px]">{MANUAL_ORDER_LINE}</p>
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
  const id = useId();
  const shownRatingSources = useShownRatingSources();
  const options = getQuerySortOptions({
    includePersonalized: allowPersonalized,
    relevanceScope: querySortScopeForMediaScope(rules.media_scope),
    shownRatingSources,
    keepSortField: rules.sort.field,
  });
  const directions = directionLabels(rules.sort.field);
  const stored = storedDefaultSort(sortConfig);

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
        Order
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
          <SelectTrigger aria-label="Sort by" className="h-11 w-full">
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
          <SelectTrigger aria-label="Direction" className="h-11 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="desc">{directions.desc}</SelectItem>
            <SelectItem value="asc">{directions.asc}</SelectItem>
          </SelectContent>
        </Select>
        <MaxTitlesInput limit={rules.limit} placeholder={NO_LIMIT} onCommit={commitLimit} />
      </div>
      {stored ? (
        <div className="bg-muted/50 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl px-3 py-2.5 text-[13px]">
          <Info aria-hidden className="text-muted-foreground size-4 shrink-0" />
          <p className="min-w-0 flex-1">{storedSortLine(stored)}</p>
          <Button
            type="button"
            size="sm"
            variant="outline"
            aria-label={`Clear the saved default sort, ${stored}`}
            onClick={() => onChange({ rules, sortConfig: clearDefaultSort(sortConfig) })}
          >
            Clear
          </Button>
        </div>
      ) : null}
      <p className="text-muted-foreground text-[13px]">{ORDER_HELP}</p>
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
  max,
  disabled,
  onCommit,
}: {
  limit: number | undefined;
  placeholder: string;
  max?: number;
  disabled?: boolean;
  onCommit: (input: HTMLInputElement) => void;
}) {
  return (
    <label className="border-border bg-background focus-within:border-ring focus-within:ring-ring/50 flex h-11 items-center gap-3 rounded-md border px-3 text-sm shadow-xs transition-[color,box-shadow] focus-within:ring-[3px] has-[input:disabled]:cursor-not-allowed has-[input:disabled]:opacity-50">
      <Input
        key={limit ?? ""}
        type="number"
        min={1}
        max={max}
        step={1}
        aria-label="Max titles"
        placeholder={placeholder}
        defaultValue={limit ?? ""}
        disabled={disabled}
        className="h-full min-w-0 flex-1 rounded-none border-0 bg-transparent p-0 shadow-none focus-visible:ring-0 disabled:opacity-100"
        onBlur={(event) => onCommit(event.currentTarget)}
        onKeyDown={(event) => {
          if (event.key === "Enter") event.currentTarget.blur();
        }}
      />
      <span
        aria-hidden
        className="border-border text-muted-foreground flex shrink-0 items-center self-stretch border-l pl-3"
      >
        max titles
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
  limitLocked,
}: Extract<OrderBlockProps, { mode: "synced" }>) {
  const id = useId();
  const shownRatingSources = useShownRatingSources();
  const value = sortConfigToSelectValue(sortConfig);
  const options = collectionDefaultSortOptions(
    allowPersonalized,
    shownRatingSources,
    value.split(":")[0],
  );

  function commitLimit(input: HTMLInputElement) {
    const typed = parsedLimit(input.value, limit);
    const next = typed === undefined ? undefined : Math.min(typed, COLLECTION_MAX_ITEMS);
    input.value = next === undefined ? "" : String(next);
    if (next !== limit) onLimitChange(next);
  }

  return (
    <section
      aria-labelledby={`${id}-heading`}
      className="border-border/70 grid gap-3 border-t pt-4"
    >
      <h3 id={`${id}-heading`} className="text-[14.5px] font-semibold">
        Order
      </h3>
      <div className="grid gap-2.5 sm:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
        <Select value={value} onValueChange={(next) => onSortChange(selectValueToSortConfig(next))}>
          <SelectTrigger aria-label="Default sort" className="h-11 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.value === COLLECTION_SOURCE_ORDER ? "List order" : option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <MaxTitlesInput
          limit={limit}
          placeholder="Whole list"
          max={COLLECTION_MAX_ITEMS}
          disabled={limitLocked}
          onCommit={commitLimit}
        />
      </div>
      <p className="text-muted-foreground text-[13px]">
        {SYNCED_ORDER_HELP} {ORDER_HELP}
      </p>
    </section>
  );
}
