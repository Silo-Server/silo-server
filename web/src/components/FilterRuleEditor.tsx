import type { PersonalizedSorts } from "@/lib/querySortOptions";
import { useShownRatingSources } from "@/hooks/queries/ratingsCapability";
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
import { cn } from "@/lib/utils";
import type { FilterConfig, FilterGroup, FilterRule } from "@/api/types";
import {
  COLLECTION_FIELD_OPTIONS,
  getCollectionSortOptions,
  getCollectionFieldOption,
  getDefaultRuleValue,
  newFilterRule,
  type CollectionFieldOption,
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
  allowPersonalizedSorts?: PersonalizedSorts;
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

/**
 * Whether the rule's field, operator and value fit the editor's controls.
 * Any other rule is shown read-only and kept exactly as saved unless removed.
 * Many such rules are valid, for example ones the guided editor writes for
 * fields these controls do not offer, so the label must not call them broken.
 */
function canEditRule(
  rule: FilterRule,
  fieldDef: CollectionFieldOption | undefined,
  allowPersonalizedFilters: boolean,
): boolean {
  if (!fieldDef || (fieldDef.personalized && !allowPersonalizedFilters)) return false;
  if (!fieldDef.operators.some((op) => op.value === rule.op)) return false;
  if (rule.op === "between") return Array.isArray(rule.value) && rule.value.length === 2;
  if (fieldDef.inputType === "boolean") return typeof rule.value === "boolean";
  return true;
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

interface FilterRuleRowProps {
  rule: FilterRule;
  fieldOptions: CollectionFieldOption[];
  allowPersonalizedFilters: boolean;
  onChange: (updates: Partial<FilterRule>) => void;
  onRemove: () => void;
  /** Taller controls that wrap onto a second line when narrow, for roomier forms. */
  roomy?: boolean;
  /**
   * Names the rule's controls as a group ("Rule 2"), so a screen reader can
   * tell which rule a Field, Value or Remove rule control belongs to.
   */
  label?: string;
}

const RULE_ROW_SIZES = {
  compact: {
    row: "flex items-center gap-2",
    control: "h-8 text-xs",
    field: "w-36",
    op: "w-24",
    value: "flex-1",
  },
  roomy: {
    row: "flex flex-wrap items-center gap-2",
    control: "h-9 text-sm",
    field: "w-40",
    op: "w-36",
    value: "min-w-40 flex-1",
  },
};

/**
 * One rule: field, condition and value, then a remove button. A rule these
 * controls can't represent shows read-only and stays as saved until removed.
 */
export function FilterRuleRow({
  rule,
  fieldOptions,
  allowPersonalizedFilters,
  onChange,
  onRemove,
  roomy = false,
  label,
}: FilterRuleRowProps) {
  const size = RULE_ROW_SIZES[roomy ? "roomy" : "compact"];
  const fieldDef = getCollectionFieldOption(rule.field);
  const operators = fieldDef?.operators ?? [];

  if (!canEditRule(rule, fieldDef, allowPersonalizedFilters)) {
    return (
      <div
        role="group"
        aria-label="Rule not editable here"
        className="border-border flex items-center gap-2 rounded-md border border-dashed px-2 py-1 text-xs"
      >
        <span className="flex-1">
          <span className="font-medium">Not editable here</span>{" "}
          <code className="text-muted-foreground">
            {rule.field} {rule.op} {JSON.stringify(rule.value)}
          </code>
        </span>
        <Button type="button" variant="ghost" size="sm" className="h-7 text-xs" onClick={onRemove}>
          Remove
        </Button>
      </div>
    );
  }

  return (
    <div role={label ? "group" : undefined} aria-label={label} className={size.row}>
      <Select
        value={rule.field}
        onValueChange={(v) => {
          const newDef = getCollectionFieldOption(v);
          const defaultOp = newDef?.operators[0]?.value ?? "is";
          onChange({ field: v, op: defaultOp, value: getDefaultRuleValue(v, defaultOp) });
        }}
      >
        <SelectTrigger aria-label="Field" className={cn(size.control, size.field)}>
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
          onChange({ op: v, value: normalizeRuleValue(rule.field, v, rule.value) })
        }
      >
        <SelectTrigger aria-label="Condition" className={cn(size.control, size.op)}>
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
        <div className={cn("flex items-center gap-2", size.value)}>
          {[0, 1].map((index) => {
            const rangeValue =
              Array.isArray(rule.value) && rule.value.length === 2 ? rule.value : ["", ""];
            return (
              <Input
                key={index}
                type={fieldDef.inputType === "number" ? "number" : "text"}
                aria-label={index === 0 ? "From" : "To"}
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
                  onChange({ value: nextValue });
                }}
                className={cn(size.control, "min-w-0 flex-1")}
                placeholder={index === 0 ? "From" : "To"}
              />
            );
          })}
        </div>
      ) : fieldDef?.inputType === "boolean" ? (
        <Select
          value={String(Boolean(rule.value))}
          onValueChange={(v) => onChange({ value: v === "true" })}
        >
          <SelectTrigger aria-label="Value" className={cn(size.control, size.value)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="true">True</SelectItem>
            <SelectItem value="false">False</SelectItem>
          </SelectContent>
        </Select>
      ) : fieldDef?.inputType === "select" ? (
        <Select value={String(rule.value)} onValueChange={(v) => onChange({ value: v })}>
          <SelectTrigger aria-label="Value" className={cn(size.control, size.value)}>
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
          onChange={(v) => onChange({ value: v })}
        />
      ) : (
        <Input
          type={fieldDef?.inputType === "number" ? "number" : "text"}
          aria-label="Value"
          value={String(rule.value)}
          onChange={(e) =>
            onChange({
              value: fieldDef?.inputType === "number" ? Number(e.target.value) : e.target.value,
            })
          }
          className={cn(size.control, size.value)}
          placeholder={rule.field === "added_at" ? "e.g. 30d, 2w" : "Value..."}
        />
      )}

      <Button
        type="button"
        variant="ghost"
        size="sm"
        aria-label="Remove rule"
        className="text-muted-foreground hover:text-destructive h-7 w-7 shrink-0 p-0"
        onClick={onRemove}
      >
        <Trash2 className="h-3.5 w-3.5" />
      </Button>
    </div>
  );
}

interface FilterSortControlsProps {
  sort?: string;
  order?: string;
  /** A new field sends both; a new direction sends only the order. */
  onChange: (next: { sort?: string; order: string }) => void;
  allowPersonalizedSorts?: PersonalizedSorts;
  sortRelevanceScope?: QuerySortRelevanceScope;
}

/** "Sort by" a field, then ascending or descending. */
export function FilterSortControls({
  sort,
  order,
  onChange,
  allowPersonalizedSorts = false,
  sortRelevanceScope,
}: FilterSortControlsProps) {
  const shownRatingSources = useShownRatingSources();
  const sortOptions = getCollectionSortOptions(
    allowPersonalizedSorts,
    sortRelevanceScope,
    shownRatingSources,
    sort,
  );
  const selectedSort = normalizeQuerySortForScope(
    { field: sort, order },
    {
      includePersonalized: allowPersonalizedSorts,
      relevanceScope: sortRelevanceScope,
      shownRatingSources,
      keepSortField: sort,
    },
  );
  return (
    <>
      <Select
        value={selectedSort.field}
        onValueChange={(v) => onChange({ sort: v, order: getDefaultQuerySortOrder(v) })}
      >
        <SelectTrigger aria-label="Sort by" className="h-8 w-32 text-xs">
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
      <Select value={order || "desc"} onValueChange={(v) => onChange({ order: v })}>
        <SelectTrigger aria-label="Direction" className="h-8 w-28 text-xs">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="desc">Descending</SelectItem>
          <SelectItem value="asc">Ascending</SelectItem>
        </SelectContent>
      </Select>
    </>
  );
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
  const fieldOptions = getFilterRuleFieldOptions(allowPersonalizedFilters, mediaScope);

  function updateConfig(updates: Partial<FilterConfig>) {
    onChange({ ...config, ...updates });
  }

  function addGroup() {
    updateConfig({
      groups: [...config.groups, { match: "all", rules: [newFilterRule()] }],
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
    updateGroup(groupIdx, { rules: [...group.rules, newFilterRule()] });
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

          {group.rules.map((rule, ruleIdx) => (
            <FilterRuleRow
              key={ruleIdx}
              rule={rule}
              fieldOptions={fieldOptions}
              allowPersonalizedFilters={allowPersonalizedFilters}
              onChange={(updates) => updateRule(groupIdx, ruleIdx, updates)}
              onRemove={() => removeRule(groupIdx, ruleIdx)}
              label={`Rule ${ruleIdx + 1}`}
            />
          ))}

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
        <FilterSortControls
          sort={config.sort}
          order={config.order}
          onChange={updateConfig}
          allowPersonalizedSorts={allowPersonalizedSorts}
          sortRelevanceScope={sortRelevanceScope}
        />
      </div>
    </div>
  );
}
