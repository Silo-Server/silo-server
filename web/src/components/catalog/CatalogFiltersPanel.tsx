import { useMemo, useState } from "react";

import { createEmptyQueryDefinition, type QueryDefinition } from "@/api/types";
import {
  queryDefinitionToGuidedState,
  guidedStateToQueryDefinition,
  type GuidedFormState,
} from "@/components/collections/CollectionGuidedRulesEditor";
import { useCatalogFilters } from "@/hooks/queries/catalog";
import type { QuerySortRelevanceScope } from "@/lib/querySortOptions";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";

import ActiveFilterBadges from "./ActiveFilterBadges";
import CatalogFilterBar, { CATALOG_SOURCE_ORDER_SORT_FIELD } from "./CatalogFilterBar";
import CatalogFilterSheet from "./CatalogFilterSheet";
import { countActiveFilters, getActiveFilterBadges } from "./catalogFilterBadges";

export interface CatalogFiltersPanelProps {
  state: CatalogSearchState;
  onStateChange: (nextState: CatalogSearchState) => void;
  libraries?: Array<{ id: number; name: string }>;
  allowLibrarySelection?: boolean;
  showMediaScopeSelector?: boolean;
  allowPersonalizedFilters?: boolean;
  allowPersonalizedSorts?: boolean;
  sortRelevanceScope?: QuerySortRelevanceScope;
  resultCountLabel?: string;
  resultCountLoading?: boolean;
  libraryType?: string;
}

export default function CatalogFiltersPanel({
  state,
  onStateChange,
  libraries,
  allowLibrarySelection = true,
  showMediaScopeSelector = true,
  allowPersonalizedFilters = false,
  allowPersonalizedSorts = false,
  sortRelevanceScope,
  resultCountLabel,
  resultCountLoading = false,
  libraryType,
}: CatalogFiltersPanelProps) {
  const [sheetOpen, setSheetOpen] = useState(false);
  const [editorMode, setEditorMode] = useState<"guided" | "advanced">("guided");

  const isLocked = state.source === "section";
  const isCollectionSource =
    state.source === "library_collection" || state.source === "user_collection";
  const usesSourceOrder = isCollectionSource && state.uses_source_order;

  const qd = state.query_definition ?? createEmptyQueryDefinition();
  const guidedState = useMemo(() => queryDefinitionToGuidedState(qd), [qd]);
  const toolbarGuidedState = usesSourceOrder
    ? { ...guidedState, sortField: CATALOG_SOURCE_ORDER_SORT_FIELD }
    : guidedState;
  const isAudiobookLibrary =
    libraryType === "audiobook" ||
    libraryType === "audiobooks" ||
    guidedState.mediaScope === "audiobook";
  const badges = useMemo(
    () => getActiveFilterBadges(guidedState, { isAudiobookLibrary }),
    [guidedState, isAudiobookLibrary],
  );
  const activeCount = useMemo(() => countActiveFilters(guidedState), [guidedState]);

  // Section surfaces are generated blocks and do not expose an overlay editor.
  if (isLocked) {
    return (
      <section className="bg-card space-y-2 rounded-lg border p-4">
        <h2 className="text-sm font-medium">Filters</h2>
        <p className="text-muted-foreground text-sm">Filters are locked to this source.</p>
      </section>
    );
  }

  const libraryOptions =
    libraries ?? state.query_definition.library_ids.map((id) => ({ id, name: `Library ${id}` }));

  function update(patch: Partial<GuidedFormState>) {
    const next = { ...toolbarGuidedState, ...patch };
    const nextUsesSourceOrder =
      isCollectionSource && next.sortField === CATALOG_SOURCE_ORDER_SORT_FIELD;
    const nextForQuery = nextUsesSourceOrder
      ? { ...next, sortField: guidedState.sortField, sortOrder: guidedState.sortOrder }
      : next;
    const nextQd = guidedStateToQueryDefinition(nextForQuery, qd);
    onStateChange({
      ...state,
      uses_source_order: nextUsesSourceOrder,
      query_definition: nextQd,
    });
  }

  return (
    <div className="space-y-3">
      <CatalogFilterBar
        state={toolbarGuidedState}
        onUpdate={update}
        activeFilterCount={activeCount}
        onOpenFilters={() => setSheetOpen(true)}
        showMediaScopeSelector={showMediaScopeSelector}
        allowPersonalizedSorts={allowPersonalizedSorts}
        sortRelevanceScope={sortRelevanceScope}
        resultCountLabel={resultCountLabel}
        resultCountLoading={resultCountLoading}
        sourceOrderLabel={isCollectionSource ? "Collection Order" : undefined}
        allowEpisodeMediaScope={!isCollectionSource}
      />

      <ActiveFilterBadges badges={badges} onClear={update} />

      {sheetOpen ? (
        <CatalogFilterSheetContainer
          state={state}
          open={sheetOpen}
          onOpenChange={setSheetOpen}
          guidedState={guidedState}
          onUpdate={update}
          libraries={libraryOptions}
          allowLibrarySelection={allowLibrarySelection}
          showMediaScopeSelector={showMediaScopeSelector}
          allowPersonalizedFilters={allowPersonalizedFilters}
          allowPersonalizedSorts={allowPersonalizedSorts}
          sortRelevanceScope={sortRelevanceScope}
          editorMode={editorMode}
          onEditorModeChange={setEditorMode}
          queryDefinition={qd}
          onQueryDefinitionChange={(nextQd) =>
            onStateChange({ ...state, query_definition: nextQd })
          }
          libraryType={libraryType}
        />
      ) : null}
    </div>
  );
}

export function CatalogFilterSheetContainer({
  state,
  open,
  onOpenChange,
  guidedState,
  onUpdate,
  libraries,
  allowLibrarySelection,
  showMediaScopeSelector,
  allowPersonalizedFilters,
  allowPersonalizedSorts,
  sortRelevanceScope,
  editorMode,
  onEditorModeChange,
  queryDefinition,
  onQueryDefinitionChange,
  libraryType,
}: {
  state: CatalogSearchState;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  guidedState: GuidedFormState;
  onUpdate: (patch: Partial<GuidedFormState>) => void;
  libraries: Array<{ id: number; name: string }>;
  allowLibrarySelection: boolean;
  showMediaScopeSelector?: boolean;
  allowPersonalizedFilters: boolean;
  allowPersonalizedSorts: boolean;
  sortRelevanceScope?: QuerySortRelevanceScope;
  editorMode: "guided" | "advanced";
  onEditorModeChange: (mode: "guided" | "advanced") => void;
  queryDefinition: QueryDefinition;
  onQueryDefinitionChange: (nextQd: QueryDefinition) => void;
  libraryType?: string;
}) {
  const filtersQuery = useCatalogFilters(state, {
    enabled: editorMode === "guided",
    includeTechnical: false,
  });

  return (
    <CatalogFilterSheet
      open={open}
      onOpenChange={onOpenChange}
      state={guidedState}
      onUpdate={onUpdate}
      libraries={libraries}
      allowLibrarySelection={allowLibrarySelection}
      showMediaScopeSelector={showMediaScopeSelector}
      allowPersonalizedFilters={allowPersonalizedFilters}
      allowPersonalizedSorts={allowPersonalizedSorts}
      sortRelevanceScope={sortRelevanceScope}
      editorMode={editorMode}
      onEditorModeChange={onEditorModeChange}
      queryDefinition={queryDefinition}
      onQueryDefinitionChange={onQueryDefinitionChange}
      filters={filtersQuery.data}
      filtersLoading={filtersQuery.isLoading}
      libraryType={libraryType}
      catalogState={state}
    />
  );
}
