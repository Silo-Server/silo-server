import type { PersonalizedSorts } from "@/lib/querySortOptions";
import type { QueryDefinition } from "@/api/types";
import type { FilterConfig } from "@/api/types";
import FilterRuleEditor from "@/components/FilterRuleEditor";
import { useT } from "@/i18n";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import {
  normalizeQuerySortForScope,
  querySortScopeForMediaScope,
  type QuerySortRelevanceScope,
} from "@/lib/querySortOptions";
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
  allowPersonalizedSorts?: PersonalizedSorts;
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
  const t = useT();
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
              <Label>{t("web.collections.rules.mediaScope")}</Label>
              <Select
                value={value.media_scope ?? "all"}
                onValueChange={(next) => {
                  const nextSort = normalizeQuerySortForScope(value.sort, {
                    includePersonalized: allowPersonalizedSorts,
                    relevanceScope: querySortScopeForMediaScope(next),
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
                  <SelectItem value="all">{t("web.collections.rules.mediaAll")}</SelectItem>
                  <SelectItem value="video">{t("web.collections.rules.mediaVideo")}</SelectItem>
                  <SelectItem value="movie">{t("web.collections.rules.mediaMovies")}</SelectItem>
                  <SelectItem value="series">{t("web.collections.rules.mediaSeries")}</SelectItem>
                  <SelectItem value="episode">
                    {t("web.collections.rules.mediaEpisodes")}
                  </SelectItem>
                  <SelectItem value="audiobook">
                    {t("web.collections.rules.mediaAudiobooks")}
                  </SelectItem>
                  <SelectItem value="ebook">{t("web.collections.rules.mediaEbooks")}</SelectItem>
                  <SelectItem value="manga">{t("web.collections.rules.mediaManga")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          ) : null}

          {allowLibrarySelection ? (
            <div className="space-y-2">
              <Label>{t("web.collections.rules.libraries")}</Label>
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
        <Label>{t("web.collections.rules.ruleGroups")}</Label>
        <FilterRuleEditor
          value={filterConfig}
          allowPersonalizedFilters={allowPersonalizedFilters}
          allowPersonalizedSorts={allowPersonalizedSorts}
          sortRelevanceScope={sortRelevanceScope}
          mediaScope={value.media_scope ?? "all"}
          valueScope={{ libraryIds: value.library_ids, mediaScope: value.media_scope }}
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
