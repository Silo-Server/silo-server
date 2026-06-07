import type { QueryDefinition } from "@/api/types";
import type { FilterConfig } from "@/api/types";
import FilterRuleEditor from "@/components/FilterRuleEditor";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { normalizeQuerySortForScope, type QuerySortRelevanceScope } from "@/lib/querySortOptions";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

interface CollectionRulesEditorProps {
  value: QueryDefinition;
  onChange: (value: QueryDefinition) => void;
  libraries?: Array<{ id: number; name: string }>;
  allowLibrarySelection?: boolean;
  showMediaScopeSelector?: boolean;
  allowPersonalizedFilters?: boolean;
  allowPersonalizedSorts?: boolean;
  sortRelevanceScope?: QuerySortRelevanceScope;
  readOnly?: boolean;
}

export default function CollectionRulesEditor({
  value,
  onChange,
  libraries = [],
  allowLibrarySelection = true,
  showMediaScopeSelector = true,
  allowPersonalizedFilters = false,
  allowPersonalizedSorts = false,
  sortRelevanceScope,
  readOnly = false,
}: CollectionRulesEditorProps) {
  const filterConfig: FilterConfig = {
    match: value.match,
    groups: value.groups,
    sort: value.sort.field,
    order: value.sort.order,
  };

  return (
    <div className="space-y-4">
      {showMediaScopeSelector || allowLibrarySelection ? (
        <div
          className={
            showMediaScopeSelector && allowLibrarySelection
              ? "grid gap-4 md:grid-cols-2"
              : "grid gap-4"
          }
        >
          {showMediaScopeSelector ? (
            <div className="space-y-2">
              <Label>Media Scope</Label>
              <Select
                value={value.media_scope ?? "all"}
                onValueChange={(next) => {
                  const nextRelevanceScope =
                    next === "all" ? "all" : (next as QuerySortRelevanceScope);
                  const nextSort = normalizeQuerySortForScope(value.sort, {
                    includePersonalized: allowPersonalizedSorts,
                    relevanceScope: nextRelevanceScope,
                  });

                  onChange({
                    ...value,
                    media_scope:
                      next === "all" ? undefined : (next as QueryDefinition["media_scope"]),
                    sort: nextSort,
                  });
                }}
                disabled={readOnly}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All Media</SelectItem>
                  <SelectItem value="movie">Movies</SelectItem>
                  <SelectItem value="series">Series</SelectItem>
                  <SelectItem value="episode">Episodes</SelectItem>
                  <SelectItem value="audiobook">Audiobooks</SelectItem>
                  <SelectItem value="ebook">Ebooks</SelectItem>
                </SelectContent>
              </Select>
            </div>
          ) : null}

          {allowLibrarySelection ? (
            <div className="space-y-2">
              <Label>Libraries</Label>
              <LibraryMultiSelect
                libraries={libraries}
                value={value.library_ids}
                onChange={(libraryIds) => onChange({ ...value, library_ids: libraryIds })}
              />
            </div>
          ) : null}
        </div>
      ) : null}

      <div className="space-y-2">
        <Label>Rule Groups</Label>
        <FilterRuleEditor
          value={filterConfig}
          allowPersonalizedFilters={allowPersonalizedFilters}
          allowPersonalizedSorts={allowPersonalizedSorts}
          sortRelevanceScope={sortRelevanceScope}
          onChange={(next) =>
            onChange({
              ...value,
              match: next.match,
              groups: next.groups,
              sort: {
                field: (next.sort ?? value.sort.field) as QueryDefinition["sort"]["field"],
                order: (next.order ?? value.sort.order) as QueryDefinition["sort"]["order"],
              },
            })
          }
        />
      </div>
    </div>
  );
}
