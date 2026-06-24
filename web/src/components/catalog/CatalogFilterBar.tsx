import { Loader2, SlidersHorizontal } from "lucide-react";

import type { GuidedFormState } from "@/components/collections/CollectionGuidedRulesEditor";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getCollectionSortOptions } from "@/components/collections/collectionBuilderFields";
import {
  getDefaultQuerySortOrder,
  getQuerySortOptions,
  normalizeQuerySortForScope,
  type QuerySortRelevanceScope,
} from "@/lib/querySortOptions";

interface CatalogFilterBarProps {
  state: GuidedFormState;
  onUpdate: (patch: Partial<GuidedFormState>) => void;
  activeFilterCount: number;
  onOpenFilters: () => void;
  showMediaScopeSelector?: boolean;
  allowPersonalizedSorts?: boolean;
  sortRelevanceScope?: QuerySortRelevanceScope;
  resultCountLabel?: string;
  resultCountLoading?: boolean;
  sourceOrderLabel?: string;
  allowEpisodeMediaScope?: boolean;
}

export const CATALOG_SOURCE_ORDER_SORT_FIELD = "__source_order";

export const CATALOG_MEDIA_SCOPE_OPTIONS = [
  { value: "all", label: "All Media" },
  { value: "video", label: "Movies & Series" },
  { value: "movie", label: "Movies" },
  { value: "series", label: "Series" },
  { value: "episode", label: "Episodes" },
  { value: "audiobook", label: "Audiobooks" },
  { value: "ebook", label: "Ebooks" },
  { value: "manga", label: "Manga" },
] as const;

export default function CatalogFilterBar({
  state,
  onUpdate,
  activeFilterCount,
  onOpenFilters,
  showMediaScopeSelector = true,
  allowPersonalizedSorts = false,
  sortRelevanceScope,
  resultCountLabel,
  resultCountLoading = false,
  sourceOrderLabel,
  allowEpisodeMediaScope = true,
}: CatalogFilterBarProps) {
  const sortOptions = getCollectionSortOptions(allowPersonalizedSorts, sortRelevanceScope);
  const mediaScopeOptions = allowEpisodeMediaScope
    ? CATALOG_MEDIA_SCOPE_OPTIONS
    : CATALOG_MEDIA_SCOPE_OPTIONS.filter((option) => option.value !== "episode");
  const usesSourceOrder = Boolean(
    sourceOrderLabel && state.sortField === CATALOG_SOURCE_ORDER_SORT_FIELD,
  );
  const selectedSort = usesSourceOrder
    ? { field: CATALOG_SOURCE_ORDER_SORT_FIELD, order: state.sortOrder }
    : normalizeQuerySortForScope(
        { field: state.sortField, order: state.sortOrder },
        { includePersonalized: allowPersonalizedSorts, relevanceScope: sortRelevanceScope },
      );

  return (
    <div className="flex flex-wrap items-center gap-3">
      {showMediaScopeSelector ? (
        <Select
          value={state.mediaScope}
          onValueChange={(v) => {
            // "video" spans movie+series, so sorts valid for "all" stay valid.
            // Manga reuses the ebook sort universe (no dedicated sort scope).
            const nextRelevanceScope: QuerySortRelevanceScope =
              v === "all" || v === "video"
                ? "all"
                : v === "manga"
                  ? "ebook"
                  : (v as QuerySortRelevanceScope);
            if (usesSourceOrder) {
              onUpdate({ mediaScope: v as GuidedFormState["mediaScope"] });
              return;
            }
            const nextSort = normalizeQuerySortForScope(
              { field: state.sortField, order: state.sortOrder },
              {
                includePersonalized: allowPersonalizedSorts,
                relevanceScope: nextRelevanceScope,
              },
            );
            onUpdate({
              mediaScope: v as GuidedFormState["mediaScope"],
              sortField: nextSort.field,
              sortOrder: nextSort.order,
            });
          }}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {mediaScopeOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : null}

      {/* Sort By */}
      <Select
        value={selectedSort.field}
        onValueChange={(v) => {
          if (v === CATALOG_SOURCE_ORDER_SORT_FIELD) {
            onUpdate({ sortField: CATALOG_SOURCE_ORDER_SORT_FIELD });
            return;
          }
          const sortOption = getQuerySortOptions({
            includePersonalized: allowPersonalizedSorts,
          }).find((opt) => opt.value === v);
          const patch: Partial<GuidedFormState> = {
            sortField: v,
            sortOrder: getDefaultQuerySortOrder(v),
          };
          if (showMediaScopeSelector && sortOption && allowEpisodeMediaScope) {
            const scopeTypes: Array<Exclude<QuerySortRelevanceScope, "all">> | null =
              state.mediaScope === "all"
                ? null
                : state.mediaScope === "video"
                  ? ["movie", "series"]
                  : // Manga has no dedicated sort scope; it reuses the ebook
                    // sort universe (its chapters are ebook items).
                    state.mediaScope === "manga"
                    ? ["ebook"]
                    : [state.mediaScope];
            const currentApplicable =
              !scopeTypes ||
              scopeTypes.some((scope) => sortOption.applicableMediaScopes.includes(scope));
            if (
              sortOption.preferredMediaScope &&
              state.mediaScope !== sortOption.preferredMediaScope
            ) {
              patch.mediaScope = sortOption.preferredMediaScope as GuidedFormState["mediaScope"];
            } else if (!currentApplicable) {
              patch.mediaScope = sortOption
                .applicableMediaScopes[0] as GuidedFormState["mediaScope"];
            }
          }
          onUpdate(patch);
        }}
      >
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {sourceOrderLabel ? (
            <SelectItem value={CATALOG_SOURCE_ORDER_SORT_FIELD}>{sourceOrderLabel}</SelectItem>
          ) : null}
          {sortOptions.map((opt) => (
            <SelectItem key={opt.value} value={opt.value}>
              {opt.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      {/* Order */}
      {usesSourceOrder ? null : (
        <Select
          value={state.sortOrder}
          onValueChange={(v) => onUpdate({ sortOrder: v as "asc" | "desc" })}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="desc">Descending</SelectItem>
            <SelectItem value="asc">Ascending</SelectItem>
          </SelectContent>
        </Select>
      )}

      {/* Filters button */}
      <Button variant="outline" size="sm" onClick={onOpenFilters} className="gap-2">
        <SlidersHorizontal className="h-4 w-4" />
        Filters
        {activeFilterCount > 0 && (
          <Badge variant="default" className="ml-1 px-1.5 py-0 text-[10px]">
            {activeFilterCount}
          </Badge>
        )}
      </Button>
      {resultCountLoading ? (
        <Loader2
          className="text-muted-foreground size-4 animate-spin"
          aria-label="Loading item count"
        />
      ) : resultCountLabel ? (
        <span className="text-muted-foreground text-sm tabular-nums" aria-live="polite">
          {resultCountLabel}
        </span>
      ) : null}
    </div>
  );
}
