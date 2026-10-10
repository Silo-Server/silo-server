import { useEffect, useRef, useState } from "react";
import type { PersonalizedSorts } from "@/lib/querySortOptions";
import { useShownRatingSources } from "@/hooks/queries/ratingsCapability";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Plus, Trash2 } from "lucide-react";
import { cn } from "@/lib/utils";
import type { FilterConfig, FilterGroup, FilterRule } from "@/api/types";
import {
  COLLECTION_FIELD_GROUPS,
  RELATIVE_DATE_OPERATORS,
  availableCollectionFields,
  getCollectionSortOptions,
  getCollectionFieldOption,
  getDefaultRuleValue,
  newFilterRule,
  type CollectionFieldOption,
  type CollectionLanguageSource,
} from "@/components/collections/collectionBuilderFields";
import { FacetValuePicker } from "@/components/ui/facet-value-picker";
import { PersonSearchSelect } from "@/components/ui/person-search-select";
import { type FacetValueScope, useRuleLanguages } from "@/hooks/queries/facetValues";
import { useExtendedQueryRules } from "@/hooks/queries/personSearch";
import { formatLanguage } from "@/lib/languageDisplay";
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
  /** Where picked values come from; without it, all of the viewer's titles. */
  valueScope?: FacetValueScope;
}

/** The scopes a show can match in, where fields about its episodes apply. */
const SHOW_SCOPES: ReadonlySet<FilterRuleMediaScope> = new Set(["all", "video", "series"]);

/**
 * The fields a rule row offers. `shownRatingSources` leaves out ratings an
 * administrator hid; unset offers every rating. `extendedRules` false leaves
 * out what only a server advertising `extended_query_rules` takes.
 */
export function getFilterRuleFieldOptions(
  allowPersonalizedFilters = false,
  mediaScope: FilterRuleMediaScope = "all",
  shownRatingSources?: ReadonlySet<string>,
  extendedRules = true,
) {
  return availableCollectionFields(allowPersonalizedFilters, shownRatingSources, extendedRules)
    .filter(
      (option) =>
        (!option.showsOnly || SHOW_SCOPES.has(mediaScope)) &&
        (!option.noEpisodeValue || mediaScope !== "episode"),
    )
    .map((option) => {
      // Ebook and manga are read rather than watched, so relabel "watched".
      if (mediaScope !== "ebook" && mediaScope !== "manga") {
        return option;
      }
      switch (option.value) {
        case "watched":
          return { ...option, label: "Read" };
        case "last_watched":
          return { ...option, label: "Last read" };
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

const ISO_DATE = /^\d{4}-\d{2}-\d{2}$/;
/** "30d": the server trims and ignores case, and reads m as months. */
const IN_LAST = /^\s*(\d+)\s*([hdwmy])\s*$/i;
const IN_LAST_UNITS: ReadonlyArray<[string, string]> = [
  ["d", "days"],
  ["w", "weeks"],
  ["m", "months"],
  ["y", "years"],
];

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
  if (Array.isArray(value)) {
    // Leaving "between" keeps where the range started, if the new condition
    // takes such a value.
    value = value[0] ?? getDefaultRuleValue(field, op);
  }
  if (fieldDef.inputType === "boolean") {
    if (typeof value === "boolean") {
      return value;
    }
    return String(value) === "true";
  }
  if (fieldDef.inputType === "date") {
    // A date and "in the last 30 days" don't convert into each other.
    const text = String(value ?? "");
    return (RELATIVE_DATE_OPERATORS.has(op) ? IN_LAST : ISO_DATE).test(text) ? text : "";
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
  /** Where picked values come from; without it, all of the viewer's titles. */
  valueScope?: FacetValueScope;
  /** Moves focus to the value control, for a rule just added. */
  focusValue?: boolean;
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
    field: "w-48",
    op: "w-36",
    value: "min-w-40 flex-1",
  },
};

type RuleRowSize = (typeof RULE_ROW_SIZES)["roomy"];

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
  valueScope,
  focusValue = false,
}: FilterRuleRowProps) {
  const size = RULE_ROW_SIZES[roomy ? "roomy" : "compact"];
  const fieldDef = getCollectionFieldOption(rule.field);
  const valueRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (focusValue) valueRef.current?.querySelector<HTMLElement>("button, input")?.focus();
  }, [focusValue]);

  if (!fieldDef || !canEditRule(rule, fieldDef, allowPersonalizedFilters)) {
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

  // Status and genre "contains" are offered only to a rule that already uses
  // them. A field this scope, the server's shown ratings or an older server
  // leave out stays offered to a rule that already compares it.
  const offeredDef = fieldOptions.find((f) => f.value === fieldDef.value);
  const offered = offeredDef ? fieldOptions : [...fieldOptions, fieldDef];
  const fields = offered.filter((f) => !f.hidden || f.value === fieldDef.value);
  const operators = (offeredDef ?? fieldDef).operators.filter(
    (op) => !op.hidden || op.value === rule.op,
  );

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
          {COLLECTION_FIELD_GROUPS.map(([group, name]) => {
            const inGroup = fields.filter((f) => f.group === group);
            return inGroup.length > 0 ? (
              <SelectGroup key={group}>
                <SelectLabel>{name}</SelectLabel>
                {inGroup.map((f) => (
                  <SelectItem key={f.value} value={f.value}>
                    {f.label}
                  </SelectItem>
                ))}
              </SelectGroup>
            ) : null;
          })}
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

      <div ref={valueRef} className="contents">
        <RuleValueControl
          rule={rule}
          fieldDef={fieldDef}
          size={size}
          valueScope={valueScope}
          onChange={(value) => onChange({ value })}
        />
      </div>

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

/** The control a rule's value takes, by its field's input type and condition. */
function RuleValueControl({
  rule,
  fieldDef,
  size,
  valueScope,
  onChange,
}: {
  rule: FilterRule;
  fieldDef: CollectionFieldOption;
  size: RuleRowSize;
  valueScope: FacetValueScope | undefined;
  onChange: (value: FilterRule["value"]) => void;
}) {
  const control = cn(size.control, "min-w-0");
  const unit = fieldDef.unit ? (
    <span className="text-muted-foreground shrink-0 text-sm">{fieldDef.unit}</span>
  ) : null;

  if (fieldDef.supportsRange && rule.op === "between") {
    const range = Array.isArray(rule.value) && rule.value.length === 2 ? rule.value : ["", ""];
    return (
      <div className={cn("flex items-center gap-2", size.value)}>
        {(["From", "To"] as const).map((name, index) => {
          const set = (next: string | number) => {
            const value: [string | number, string | number] = [range[0] ?? "", range[1] ?? ""];
            value[index] = next;
            onChange(value);
          };
          return fieldDef.inputType === "date" ? (
            <DateInput
              key={name}
              label={name}
              value={range[index]}
              onChange={set}
              className={cn(control, "flex-1")}
            />
          ) : (
            <Input
              key={name}
              type={fieldDef.inputType === "number" ? "number" : "text"}
              aria-label={name}
              placeholder={name}
              value={String(range[index] ?? "")}
              onChange={(e) => set(inputValue(fieldDef, e.target.value))}
              className={cn(control, "flex-1")}
            />
          );
        })}
        {unit}
      </div>
    );
  }

  switch (fieldDef.inputType) {
    case "boolean":
      return (
        <Select value={String(Boolean(rule.value))} onValueChange={(v) => onChange(v === "true")}>
          <SelectTrigger aria-label="Value" className={cn(size.control, size.value)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="true">Yes</SelectItem>
            <SelectItem value="false">No</SelectItem>
          </SelectContent>
        </Select>
      );
    case "select":
      return (
        <Select
          value={String(rule.value)}
          onValueChange={(v) => onChange(fieldDef.valueType === "number" ? Number(v) : v)}
        >
          <SelectTrigger aria-label="Value" className={cn(size.control, size.value)}>
            <SelectValue placeholder="Pick one" />
          </SelectTrigger>
          <SelectContent>
            {fieldDef.selectOptions?.map((opt) => (
              <SelectItem key={opt.value} value={opt.value}>
                {opt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      );
    case "person_search":
      return <PersonSearchSelect value={String(rule.value ?? "")} onChange={onChange} />;
    case "facet":
      return (
        <FacetValuePicker
          facet={fieldDef.facet ?? "genre"}
          value={String(rule.value ?? "")}
          onChange={onChange}
          scope={valueScope}
          placeholder={fieldDef.placeholder ?? "Pick a value"}
          searchLabel={fieldDef.searchLabel ?? "Search values"}
          className={cn(size.control, size.value)}
        />
      );
    case "language":
      return (
        <LanguageValueSelect
          source={fieldDef.languageSource ?? "original"}
          scope={valueScope ?? {}}
          value={String(rule.value ?? "")}
          onChange={onChange}
          className={cn(size.control, size.value)}
        />
      );
    case "date":
      return RELATIVE_DATE_OPERATORS.has(rule.op) ? (
        <InLastInput
          value={rule.value}
          onChange={onChange}
          className={cn("flex items-center gap-2", size.value)}
          control={control}
        />
      ) : (
        <DateInput
          label="Value"
          value={rule.value}
          onChange={onChange}
          className={cn(size.control, size.value)}
        />
      );
    default:
      return (
        <div className={cn("flex items-center gap-2", size.value)}>
          <Input
            type={fieldDef.inputType === "number" ? "number" : "text"}
            aria-label="Value"
            value={String(rule.value)}
            onChange={(e) => onChange(inputValue(fieldDef, e.target.value))}
            className={cn(control, "flex-1")}
            placeholder="Value"
          />
          {unit}
        </div>
      );
  }
}

/** A typed value: a number for number fields, except that a cleared field stays empty. */
function inputValue(fieldDef: CollectionFieldOption, text: string): string | number {
  return fieldDef.inputType === "number" && text !== "" ? Number(text) : text;
}

const LANGUAGE_LISTS = {
  original: "original_languages",
  audio: "audio_languages",
  subtitle: "subtitle_languages",
} as const;

/**
 * A language the titles in the rule's scope have, by name. Original language
 * comes from the titles' metadata; audio and subtitle languages from their
 * files. A saved language no title has any more stays listed so the rule
 * keeps reading right.
 */
function LanguageValueSelect({
  source,
  scope,
  value,
  onChange,
  className,
}: {
  source: CollectionLanguageSource;
  scope: FacetValueScope;
  value: string;
  onChange: (value: string) => void;
  className: string;
}) {
  const filters = useRuleLanguages(scope, { includeTechnical: source !== "original" });
  const codes = new Set(filters.data?.[LANGUAGE_LISTS[source]] ?? []);
  if (value) codes.add(value);
  const options = [...codes]
    .map((code) => ({ code, name: formatLanguage(code) || code }))
    .sort((a, b) => a.name.localeCompare(b.name));
  let placeholder = "Pick a language";
  if (filters.isLoading) placeholder = "Loading languages";
  else if (filters.isError && !filters.data) placeholder = "Couldn’t load languages";
  let status: string | null = null;
  if (filters.isError) {
    status = filters.data ? "Couldn’t refresh languages" : "Couldn’t load languages";
  } else if (options.length === 0) {
    status = "No languages yet";
  }

  return (
    <Select
      value={value}
      onValueChange={onChange}
      onOpenChange={(open) => {
        // Opening the picker after a failed load asks again.
        if (open && filters.isError) void filters.refetch();
      }}
    >
      <SelectTrigger aria-label="Value" className={className}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.code} value={option.code}>
            {option.name}
          </SelectItem>
        ))}
        {status ? (
          <p role="status" className="text-muted-foreground px-2 py-1.5 text-sm">
            {status}
          </p>
        ) : null}
      </SelectContent>
    </Select>
  );
}

/** A calendar date; a saved value that isn't YYYY-MM-DD stays editable as text. */
function DateInput({
  label,
  value,
  onChange,
  className,
}: {
  label: string;
  value: FilterRule["value"] | undefined;
  onChange: (value: string) => void;
  className: string;
}) {
  const text = String(value ?? "");
  return (
    <Input
      type={text === "" || ISO_DATE.test(text) ? "date" : "text"}
      aria-label={label}
      value={text}
      onChange={(e) => onChange(e.target.value)}
      className={className}
    />
  );
}

/**
 * "in the last [30] [days]", written as "30d". A saved value in hours keeps
 * an hours choice; one that doesn't read as a number and unit stays as text.
 */
function InLastInput({
  value,
  onChange,
  className,
  control,
}: {
  value: FilterRule["value"];
  onChange: (value: string) => void;
  className: string;
  control: string;
}) {
  const text = String(value ?? "");
  const parsed = IN_LAST.exec(text);
  // The unit picked while the number is blank, which writes nothing yet.
  const [unitDraft, setUnitDraft] = useState("d");

  if (text.trim() !== "" && !parsed) {
    return (
      <div className={className}>
        <Input
          aria-label="Value"
          value={text}
          onChange={(e) => onChange(e.target.value)}
          className={cn(control, "flex-1")}
        />
      </div>
    );
  }

  const amount = parsed?.[1] ?? "";
  const unit = parsed?.[2]?.toLowerCase() ?? unitDraft;
  const units = unit === "h" ? [["h", "hours"] as const, ...IN_LAST_UNITS] : IN_LAST_UNITS;
  const write = (nextAmount: string, nextUnit: string) => {
    setUnitDraft(nextUnit);
    // The server rejects a span of 0, so an amount below 1 writes nothing.
    const count = /^\d+$/.test(nextAmount) ? Number(nextAmount) : 0;
    onChange(count >= 1 ? `${count}${nextUnit}` : "");
  };

  return (
    <div className={className}>
      <Input
        type="number"
        min={1}
        step={1}
        aria-label="Amount"
        placeholder="30"
        value={amount}
        onChange={(e) => write(e.target.value.trim(), unit)}
        className={cn(control, "w-20 flex-none")}
      />
      <Select value={unit} onValueChange={(next) => write(amount, next)}>
        <SelectTrigger aria-label="Unit" className={cn(control, "flex-1")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {units.map(([value, name]) => (
            <SelectItem key={value} value={value}>
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
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
  valueScope,
}: FilterRuleEditorProps) {
  const config = value || { match: "all", groups: [] };
  const shownRatingSources = useShownRatingSources();
  const extendedRules = useExtendedQueryRules();
  const fieldOptions = getFilterRuleFieldOptions(
    allowPersonalizedFilters,
    mediaScope,
    shownRatingSources,
    extendedRules,
  );

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
              valueScope={valueScope}
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
