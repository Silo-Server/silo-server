import { useState } from "react";

import { createEmptyQueryDefinition, type QueryDefinition } from "@/api/types";
import type { GuidedFormState } from "@/components/collections/CollectionGuidedRulesEditor";
import CollectionGuidedRulesEditor from "@/components/collections/CollectionGuidedRulesEditor";
import CollectionRulesEditor from "@/components/collections/CollectionRulesEditor";
import type { QuerySortRelevanceScope } from "@/lib/querySortOptions";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";
import { Button } from "@/components/ui/button";
import { PortalContainerContext } from "@/components/ui/portal-container-context";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import type { CatalogFiltersResponse } from "@/api/types";

/** Default secondary filter values for "Clear All". */
const EMPTY_SECONDARY_FILTERS: Partial<GuidedFormState> = {
  libraryIds: [],
  genres: [],
  decade: "",
  yearFrom: "",
  yearTo: "",
  minRating: "",
  contentRating: "",
  originalLanguages: [],
  actor: "",
  director: "",
  writer: "",
  producer: "",
  author: "",
  narrator: "",
  series: "",
  studio: "",
  network: "",
  country: "",
  status: "",
  watchStatus: "",
  addedInLast: "",
  releasedInLast: "",
  fourK: false,
  hdr: false,
  dolbyVision: false,
};

interface CatalogFilterSheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  state: GuidedFormState;
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
  onQueryDefinitionChange: (qd: QueryDefinition) => void;
  filters?: CatalogFiltersResponse;
  filtersLoading?: boolean;
  libraryType?: string;
  // Forwarded into the editor so audiobook-native facet sections can
  // typeahead-search /api/v1/catalog/filters/search at the same scope.
  catalogState?: CatalogSearchState;
}

export default function CatalogFilterSheet({
  open,
  onOpenChange,
  onUpdate,
  libraries,
  allowLibrarySelection,
  showMediaScopeSelector = true,
  allowPersonalizedFilters,
  allowPersonalizedSorts,
  sortRelevanceScope,
  editorMode,
  onEditorModeChange,
  queryDefinition,
  onQueryDefinitionChange,
  filters,
  filtersLoading = false,
  libraryType,
  catalogState,
}: CatalogFilterSheetProps) {
  // Portal container inside the Sheet's DOM so that react-remove-scroll
  // (activated by the Dialog/Sheet) does not block scroll events inside
  // dropdown listboxes opened by filter controls.
  const [portalContainer, setPortalContainer] = useState<HTMLElement | null>(null);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col sm:max-w-md" showCloseButton={false}>
        {/* Invisible portal mount point — must be inside SheetContent so that
            react-remove-scroll considers popover scroll events as "inside" the
            dialog and does not cancel them. */}
        <div
          ref={setPortalContainer}
          aria-hidden="true"
          className="pointer-events-none absolute size-0"
        />

        <PortalContainerContext.Provider value={portalContainer}>
          <SheetHeader className="border-b pb-3">
            <div className="flex items-start justify-between gap-4">
              <div>
                <SheetTitle>Filters</SheetTitle>
                <SheetDescription>Refine your catalog results</SheetDescription>
              </div>
              <div className="flex items-center gap-1">
                <Button
                  type="button"
                  variant={editorMode === "guided" ? "default" : "outline"}
                  size="xs"
                  onClick={() => onEditorModeChange("guided")}
                >
                  Guided
                </Button>
                <Button
                  type="button"
                  variant={editorMode === "advanced" ? "default" : "outline"}
                  size="xs"
                  onClick={() => onEditorModeChange("advanced")}
                >
                  Advanced
                </Button>
              </div>
            </div>
          </SheetHeader>

          <ScrollArea className="flex-1">
            <div className="space-y-4 px-4 pb-4">
              {editorMode === "advanced" ? (
                <CollectionRulesEditor
                  value={queryDefinition ?? createEmptyQueryDefinition()}
                  onChange={onQueryDefinitionChange}
                  libraries={libraries}
                  allowLibrarySelection={allowLibrarySelection}
                  showMediaScopeSelector={showMediaScopeSelector}
                  allowPersonalizedFilters={allowPersonalizedFilters}
                  allowPersonalizedSorts={allowPersonalizedSorts}
                  sortRelevanceScope={sortRelevanceScope}
                />
              ) : (
                <CollectionGuidedRulesEditor
                  value={queryDefinition ?? createEmptyQueryDefinition()}
                  onChange={onQueryDefinitionChange}
                  libraries={libraries}
                  allowLibrarySelection={allowLibrarySelection}
                  showMediaScopeSelector={showMediaScopeSelector}
                  allowPersonalizedFilters={allowPersonalizedFilters}
                  allowPersonalizedSorts={allowPersonalizedSorts}
                  sortRelevanceScope={sortRelevanceScope}
                  showSortControls={false}
                  filters={filters}
                  filtersLoading={filtersLoading}
                  libraryType={libraryType}
                  catalogState={catalogState}
                />
              )}
            </div>
          </ScrollArea>

          <SheetFooter className="flex-row justify-between border-t pt-4">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => onUpdate(EMPTY_SECONDARY_FILTERS)}
            >
              Clear All
            </Button>
            <SheetClose asChild>
              <Button type="button" size="sm">
                Done
              </Button>
            </SheetClose>
          </SheetFooter>
        </PortalContainerContext.Provider>
      </SheetContent>
    </Sheet>
  );
}
