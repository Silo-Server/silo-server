import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Plus, Trash2 } from "lucide-react";
import type { FilterConfig, FilterGroup, FilterRule } from "@/api/types";
import {
  COLLECTION_FIELD_OPTIONS,
  getCollectionSortOptions,
  getCollectionFieldOption,
} from "@/components/collections/collectionBuilderFields";
import { PersonSearchSelect } from "@/components/ui/person-search-select";
import {
  getDefaultQuerySortOrder,
  normalizeQuerySortForScope,
  type QuerySortRelevanceScope,
} from "@/lib/querySortOptions";

type FilterRuleMediaScope =
  | "all"
  | "video"
  | "movie"
  | "series"
  | "episode"
  | "audiobook"
  | "ebook"
  | "manga";

interface FilterRuleEditorProps {
  value: FilterConfig;
  onChange: (config: FilterConfig) => void;
  allowPersonalizedFilters?: boolean;
  allowPersonalizedSorts?: boolean;
  sortRelevanceScope?: QuerySortRelevanceScope;
  mediaScope?: FilterRuleMediaScope;
}

export function getFilterRuleFieldOptions(
  allowPersonalizedFilters = false,
  mediaScope: FilterRuleMediaScope = "all",
) {
  return COLLECTION_FIELD_OPTIONS.filter(
    (option) => allowPersonalizedFilters || !option.personalized,
  ).map((option) => {
    // Ebook and manga are read rather than watched, so relabel "watched".
    if (mediaScope !== "ebook" && mediaScope !== "manga") {
      return option;
    }
    switch (option.value) {
      case "watched":
        return { ...option, label: "Read" };
      default:
        return option;
    }
  });
}

export default function FilterRuleEditor({
  value,
  onChange,
  allowPersonalizedFilters = false,
  allowPersonalizedSorts = false,
  sortRelevanceScope,
  mediaScope = "all",
}: FilterRuleEditorProps) {
  const config = value || { match: "all", groups: [] };
  const sortOptions = getCollectionSortOptions(allowPersonalizedSorts, sortRelevanceScope);
  const selectedSort = normalizeQuerySortForScope(
    { field: config.sort, order: config.order },
    { includePersonalized: allowPersonalizedSorts, relevanceScope: sortRelevanceScope },
  );
  const fieldOptions = getFilterRuleFieldOptions(allowPersonalizedFilters, mediaScope);

  function getDefaultRuleValue(field: string, op: string): FilterRule["value"] {
    const fieldDef = getCollectionFieldOption(field);
    if (op === "between" && fieldDef?.supportsRange) {
      return ["", ""];
    }
    if (fieldDef?.inputType === "boolean") {
      return false;
    }
    if (fieldDef?.inputType === "number") {
      return 0;
    }
    return "";
  }

  function normalizeRuleValue(
    field: string,
    op: string,
    value: FilterRule["value"],
  ): FilterRule["value"] {
    const fieldDef = getCollectionFieldOption(field);
    if (!fieldDef) {
      return value;
    }
    if (op === "between" && fieldDef.supportsRange) {
      if (Array.isArray(value) && value.length === 2) {
        return value;
      }
      return ["", ""];
    }
    if (fieldDef.inputType === "boolean") {
      if (typeof value === "boolean") {
        return value;
      }
      return String(value) === "true";
    }
    return value;
  }

  function updateConfig(updates: Partial<FilterConfig>) {
    onChange({ ...config, ...updates });
  }

  function addGroup() {
    updateConfig({
      groups: [
        ...config.groups,
        {
          match: "all",
          rules: [{ field: "genre", op: "is", value: getDefaultRuleValue("genre", "is") }],
        },
      ],
    });
  }

  function removeGroup(groupIdx: number) {
    updateConfig({
      groups: config.groups.filter((_, i) => i !== groupIdx),
    });
  }

  function updateGroup(groupIdx: number, updates: Partial<FilterGroup>) {
    const newGroups = config.groups.map((g, i) => (i === groupIdx ? { ...g, ...updates } : g));
    updateConfig({ groups: newGroups });
  }

  function addRule(groupIdx: number) {
    const group = config.groups[groupIdx];
    if (!group) return;
    updateGroup(groupIdx, {
      rules: [
        ...group.rules,
        { field: "genre", op: "is", value: getDefaultRuleValue("genre", "is") },
      ],
    });
  }

  function removeRule(groupIdx: number, ruleIdx: number) {
    const group = config.groups[groupIdx];
    if (!group) return;
    const newRules = group.rules.filter((_, i) => i !== ruleIdx);
    if (newRules.length === 0) {
      removeGroup(groupIdx);
    } else {
      updateGroup(groupIdx, { rules: newRules });
    }
  }

  function updateRule(groupIdx: number, ruleIdx: number, updates: Partial<FilterRule>) {
    const group = config.groups[groupIdx];
    if (!group) return;
    const newRules = group.rules.map((r, i) => (i === ruleIdx ? { ...r, ...updates } : r));
    updateGroup(groupIdx, { rules: newRules });
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 text-sm">
        <span className="text-muted-foreground">Match</span>
        <Select
          value={config.match}
          onValueChange={(v) => updateConfig({ match: v as "all" | "any" })}
        >
          <SelectTrigger className="h-8 w-20">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">ALL</SelectItem>
            <SelectItem value="any">ANY</SelectItem>
          </SelectContent>
        </Select>
        <span className="text-muted-foreground">of the following groups</span>
      </div>

      {config.groups.map((group, groupIdx) => (
        <div key={groupIdx} className="border-border space-y-2 rounded-lg border p-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">Match</span>
              <Select
                value={group.match}
                onValueChange={(v) => updateGroup(groupIdx, { match: v as "all" | "any" })}
              >
                <SelectTrigger className="h-7 w-20 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">ALL</SelectItem>
                  <SelectItem value="any">ANY</SelectItem>
                </SelectContent>
              </Select>
              <span className="text-muted-foreground">rules</span>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive h-7 w-7 p-0"
              onClick={() => removeGroup(groupIdx)}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>

          {group.rules.map((rule, ruleIdx) => {
            const fieldDef = getCollectionFieldOption(rule.field);
            const operators = fieldDef?.operators ?? [];

            return (
              <div key={ruleIdx} className="flex items-center gap-2">
                <Select
                  value={rule.field}
                  onValueChange={(v) => {
                    const newDef = getCollectionFieldOption(v);
                    const defaultOp = newDef?.operators[0]?.value ?? "is";
                    updateRule(groupIdx, ruleIdx, {
                      field: v,
                      op: defaultOp,
                      value: getDefaultRuleValue(v, defaultOp),
                    });
                  }}
                >
                  <SelectTrigger className="h-8 w-36 text-xs">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {fieldOptions.map((f) => (
                      <SelectItem key={f.value} value={f.value}>
                        {f.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                <Select
                  value={rule.op}
                  onValueChange={(v) =>
                    updateRule(groupIdx, ruleIdx, {
                      op: v,
                      value: normalizeRuleValue(rule.field, v, rule.value),
                    })
                  }
                >
                  <SelectTrigger className="h-8 w-24 text-xs">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {operators.map((op) => (
                      <SelectItem key={op.value} value={op.value}>
                        {op.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                {fieldDef?.supportsRange && rule.op === "between" ? (
                  <div className="flex flex-1 items-center gap-2">
                    {[0, 1].map((index) => {
                      const rangeValue =
                        Array.isArray(rule.value) && rule.value.length === 2
                          ? rule.value
                          : ["", ""];
                      return (
                        <Input
                          key={index}
                          type={fieldDef.inputType === "number" ? "number" : "text"}
                          value={String(rangeValue[index] ?? "")}
                          onChange={(e) => {
                            const nextValue: [string | number, string | number] = [
                              rangeValue[0] ?? "",
                              rangeValue[1] ?? "",
                            ];
                            nextValue[index] =
                              fieldDef.inputType === "number" && e.target.value !== ""
                                ? Number(e.target.value)
                                : e.target.value;
                            updateRule(groupIdx, ruleIdx, { value: nextValue });
                          }}
                          className="h-8 flex-1 text-xs"
                          placeholder={index === 0 ? "From" : "To"}
                        />
                      );
                    })}
                  </div>
                ) : fieldDef?.inputType === "boolean" ? (
                  <Select
                    value={String(Boolean(rule.value))}
                    onValueChange={(v) => updateRule(groupIdx, ruleIdx, { value: v === "true" })}
                  >
                    <SelectTrigger className="h-8 flex-1 text-xs">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="true">True</SelectItem>
                      <SelectItem value="false">False</SelectItem>
                    </SelectContent>
                  </Select>
                ) : fieldDef?.inputType === "select" ? (
                  <Select
                    value={String(rule.value)}
                    onValueChange={(v) => updateRule(groupIdx, ruleIdx, { value: v })}
                  >
                    <SelectTrigger className="h-8 flex-1 text-xs">
                      <SelectValue placeholder="Select..." />
                    </SelectTrigger>
                    <SelectContent>
                      {fieldDef.selectOptions?.map((opt) => (
                        <SelectItem key={opt} value={opt}>
                          {opt}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : fieldDef?.inputType === "person_search" ? (
                  <PersonSearchSelect
                    value={String(rule.value ?? "")}
                    onChange={(v) => updateRule(groupIdx, ruleIdx, { value: v })}
                  />
                ) : (
                  <Input
                    type={fieldDef?.inputType === "number" ? "number" : "text"}
                    value={String(rule.value)}
                    onChange={(e) =>
                      updateRule(groupIdx, ruleIdx, {
                        value:
                          fieldDef?.inputType === "number"
                            ? Number(e.target.value)
                            : e.target.value,
                      })
                    }
                    className="h-8 flex-1 text-xs"
                    placeholder={rule.field === "added_at" ? "e.g. 30d, 2w" : "Value..."}
                  />
                )}

                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground hover:text-destructive h-7 w-7 shrink-0 p-0"
                  onClick={() => removeRule(groupIdx, ruleIdx)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            );
          })}

          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 text-xs"
            onClick={() => addRule(groupIdx)}
          >
            <Plus className="mr-1 h-3 w-3" /> Add Rule
          </Button>
        </div>
      ))}

      <Button type="button" variant="outline" size="sm" onClick={addGroup}>
        <Plus className="mr-1 h-3.5 w-3.5" /> Add Group
      </Button>

      {/* Sort controls */}
      <div className="border-border flex items-center gap-2 border-t pt-2">
        <span className="text-muted-foreground text-sm">Sort by</span>
        <Select
          value={selectedSort.field}
          onValueChange={(v) => updateConfig({ sort: v, order: getDefaultQuerySortOrder(v) })}
        >
          <SelectTrigger className="h-8 w-32 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {sortOptions.map((sortOption) => (
              <SelectItem key={sortOption.value} value={sortOption.value}>
                {sortOption.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={config.order || "desc"} onValueChange={(v) => updateConfig({ order: v })}>
          <SelectTrigger className="h-8 w-28 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="desc">Descending</SelectItem>
            <SelectItem value="asc">Ascending</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}
