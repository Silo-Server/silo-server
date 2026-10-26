import type { PersonalizedSorts } from "@/lib/querySortOptions";
import { useMemo, useState } from "react";

import {
  createEmptyQueryDefinition,
  type QueryDefinition,
  type QueryGroup,
  type QuerySort,
} from "@/api/types";
import {
  queryDefinitionToGuidedState,
  guidedStateToQueryDefinition,
  type GuidedFormState,
} from "@/components/collections/CollectionGuidedRulesEditor";
import { isGuidedRepresentable } from "@/components/collections/guidedRepresentable";
import { useCatalogFilters } from "@/hooks/queries/catalog";
import { querySortToSelectValue } from "@/lib/collectionSortConfig";
import type { QuerySortRelevanceScope } from "@/lib/querySortOptions";
import {
  catalogSourceSupportsSourceOrder,
  type CatalogSearchState,
} from "@/pages/catalogSearchParams";

import ActiveFilterBadges from "./ActiveFilterBadges";
import CatalogFilterBar, { CATALOG_SOURCE_ORDER_SORT_FIELD } from "./CatalogFilterBar";
import CatalogFilterSheet from "./CatalogFilterSheet";
import { getActiveFilterBadges } from "./catalogFilterBadges";

export interface CatalogFiltersPanelProps {
  state: CatalogSearchState;
  onStateChange: (nextState: CatalogSearchState) => void;
  libraries?: Array<{ id: number; name: string }>;
  allowLibrarySelection?: boolean;
  showMediaScopeSelector?: boolean;
  allowPersonalizedFilters?: boolean;
  allowPersonalizedSorts?: PersonalizedSorts;
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
  const supportsSourceOrder = catalogSourceSupportsSourceOrder(state.source);
  const usesSourceOrder = supportsSourceOrder && state.uses_source_order;

  const qd = state.query_definition ?? createEmptyQueryDefinition();
  const guidedState = useMemo(() => queryDefinitionToGuidedState(qd), [qd]);
  const guidedAvailable = useMemo(() => isGuidedRepresentable(qd), [qd]);
  // Rules Guided can't show move the sheet to Advanced. It stays there once
  // they're fixed, so the view doesn't jump back to Guided mid-edit.
  if (!guidedAvailable && editorMode === "guided") {
    setEditorMode("advanced");
  }
  const toolbarGuidedState = usesSourceOrder
    ? { ...guidedState, sortField: CATALOG_SOURCE_ORDER_SORT_FIELD }
    : guidedState;
  const isAudiobookLibrary =
    libraryType === "audiobook" ||
    libraryType === "audiobooks" ||
    guidedState.mediaScope === "audiobook";
  // Guided badges list every rule only when Guided can show them all. Otherwise
  // they would hide rules, and clearing one would rebuild the definition
  // without them, so the count comes from the rules and no badges show.
  const badges = useMemo(
    () => (guidedAvailable ? getActiveFilterBadges(guidedState, { isAudiobookLibrary }) : []),
    [guidedAvailable, guidedState, isAudiobookLibrary],
  );
  const activeCount = guidedAvailable
    ? badges.length
    : qd.groups.reduce((count, group) => count + group.rules.length, 0);

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

  // The toolbar sets only the media type and sort, so it patches those onto
  // the definition and leaves the rules as they are. The one exception is
  // narrator rules, which the server rejects for ebooks.
  function updateToolbar(patch: Partial<GuidedFormState>) {
    const next = { ...toolbarGuidedState, ...patch };
    const nextUsesSourceOrder =
      supportsSourceOrder && next.sortField === CATALOG_SOURCE_ORDER_SORT_FIELD;
    const mediaScope = next.mediaScope === "all" ? undefined : next.mediaScope;
    onStateChange({
      ...state,
      uses_source_order: nextUsesSourceOrder,
      query_definition: {
        ...qd,
        media_scope: mediaScope,
        groups:
          mediaScope === "ebook" && qd.media_scope !== "ebook"
            ? withoutNarratorRules(qd.groups)
            : qd.groups,
        sort: nextUsesSourceOrder
          ? qd.sort
          : { field: next.sortField as QuerySort["field"], order: next.sortOrder },
      },
    });
  }

  // The sheet edits the whole definition, and Advanced has its own Sort by.
  // A sort picked there leaves source order as one picked in the toolbar
  // does; under source order the URL carries no sort, so it would be lost.
  function updateDefinition(nextQd: QueryDefinition) {
    const sortChanged =
      usesSourceOrder && querySortToSelectValue(nextQd.sort) !== querySortToSelectValue(qd.sort);
    onStateChange({
      ...state,
      ...(sortChanged ? { uses_source_order: false } : null),
      query_definition: nextQd,
    });
  }

  // Badge removal and Clear All rebuild the definition through Guided. That
  // loses nothing: badges show only when Guided can show every rule, and
  // Clear All removes them all.
  function updateGuided(patch: Partial<GuidedFormState>) {
    onStateChange({
      ...state,
      query_definition: guidedStateToQueryDefinition({ ...guidedState, ...patch }, qd),
    });
  }

  return (
    <div className="space-y-3">
      <CatalogFilterBar
        state={toolbarGuidedState}
        onUpdate={updateToolbar}
        activeFilterCount={activeCount}
        onOpenFilters={() => setSheetOpen(true)}
        showMediaScopeSelector={showMediaScopeSelector}
        allowPersonalizedSorts={allowPersonalizedSorts}
        sortRelevanceScope={sortRelevanceScope}
        resultCountLabel={resultCountLabel}
        resultCountLoading={resultCountLoading}
        sourceOrderLabel={
          isCollectionSource
            ? "Collection Order"
            : state.source === "history"
              ? "Watch History"
              : supportsSourceOrder
                ? "List Order"
                : undefined
        }
        allowEpisodeMediaScope={!isCollectionSource}
      />

      <ActiveFilterBadges badges={badges} onClear={updateGuided} />

      {sheetOpen ? (
        <CatalogFilterSheetContainer
          state={state}
          open={sheetOpen}
          onOpenChange={setSheetOpen}
          guidedState={guidedState}
          onUpdate={updateGuided}
          libraries={libraryOptions}
          allowLibrarySelection={allowLibrarySelection}
          showMediaScopeSelector={showMediaScopeSelector}
          allowPersonalizedFilters={allowPersonalizedFilters}
          allowPersonalizedSorts={allowPersonalizedSorts}
          sortRelevanceScope={sortRelevanceScope}
          editorMode={editorMode}
          onEditorModeChange={setEditorMode}
          guidedAvailable={guidedAvailable}
          queryDefinition={qd}
          onQueryDefinitionChange={updateDefinition}
          libraryType={libraryType}
        />
      ) : null}
    </div>
  );
}

function withoutNarratorRules(groups: QueryGroup[]): QueryGroup[] {
  return groups
    .map((group) => ({ ...group, rules: group.rules.filter((rule) => rule.field !== "narrator") }))
    .filter((group) => group.rules.length > 0);
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
  guidedAvailable,
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
  allowPersonalizedSorts: PersonalizedSorts;
  sortRelevanceScope?: QuerySortRelevanceScope;
  editorMode: "guided" | "advanced";
  onEditorModeChange: (mode: "guided" | "advanced") => void;
  guidedAvailable?: boolean;
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
      guidedAvailable={guidedAvailable}
      queryDefinition={queryDefinition}
      onQueryDefinitionChange={onQueryDefinitionChange}
      filters={filtersQuery.data}
      filtersLoading={filtersQuery.isLoading}
      libraryType={libraryType}
      catalogState={state}
    />
  );
}
