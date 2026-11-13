import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import type { FilterRule, QueryDefinition, QueryGroup } from "@/api/types";
import { newFilterRule } from "@/components/collections/collectionBuilderFields";
import {
  FilterRuleRow,
  FilterSortControls,
  getFilterRuleFieldOptions,
} from "@/components/FilterRuleEditor";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { useQueryRuleCapabilities } from "@/hooks/queries/personSearch";
import { useShownRatingSources } from "@/hooks/queries/ratingsCapability";
import { Button } from "@/components/ui/button";
import { useT, type MessageKey } from "@/i18n";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { normalizeQuerySortForScope, querySortScopeForMediaScope } from "@/lib/querySortOptions";

type MediaScope = NonNullable<QueryDefinition["media_scope"]> | "all";

// Lower-case: each name reads inside the sentence ("Show movies from …").
const SCOPES: ReadonlyArray<[MediaScope, MessageKey]> = [
  ["all", "web.home.addRow.rules.scopeAll"],
  ["video", "web.home.addRow.rules.scopeVideo"],
  ["movie", "web.home.addRow.rules.scopeMovie"],
  ["series", "web.home.addRow.rules.scopeSeries"],
  ["episode", "web.home.addRow.rules.scopeEpisode"],
  ["audiobook", "web.home.addRow.rules.scopeAudiobook"],
  ["ebook", "web.home.addRow.rules.scopeEbook"],
  ["manga", "web.home.addRow.rules.scopeManga"],
];

/** A word of a sentence of controls, as its own flex item. */
const word = (chunks: ReactNode[]) => <span>{chunks}</span>;

const INLINE_TRIGGER = "h-9 w-auto gap-1.5 px-3 text-sm font-medium";

function MatchSelect({
  label,
  value,
  onChange,
  names,
}: {
  label: string;
  value: "all" | "any";
  onChange: (value: "all" | "any") => void;
  /** What "all" and "any" read as. */
  names: { all: MessageKey; any: MessageKey };
}) {
  const t = useT();
  return (
    <Select value={value} onValueChange={(next) => onChange(next as "all" | "any")}>
      <SelectTrigger aria-label={label} className={INLINE_TRIGGER}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">{t(names.all)}</SelectItem>
        <SelectItem value="any">{t(names.any)}</SelectItem>
      </SelectContent>
    </Select>
  );
}

const RULE_NAMES: { all: MessageKey; any: MessageKey } = {
  all: "web.home.addRow.rules.matchAll",
  any: "web.home.addRow.rules.matchAny",
};
const GROUP_JOIN_NAMES: { all: MessageKey; any: MessageKey } = {
  all: "web.home.addRow.rules.joinAnd",
  any: "web.home.addRow.rules.joinOr",
};

/**
 * Step 2 of a rule row, and a Smart collection's rules: "Show [movies] from
 * the [Movies, 4K Movies] libraries that match [all/any] of these:" ("from
 * [all libraries]" when none are picked), then one line per rule. More groups keep a stored multi-group
 * filter intact; a new group joins with "or" unless the stored groups
 * already join with "and". Rules these controls can't show stay read-only
 * until removed.
 */
export function RuleBuilder({
  value,
  onChange,
  libraries,
  allowPersonalized = false,
  context = "homeRow",
  allLibrariesLabel: allLibrariesProp,
  librariesId,
}: {
  value: QueryDefinition;
  onChange: (value: QueryDefinition) => void;
  libraries: Array<{ id: number; name: string }>;
  /** Rows resolve per viewer, so the server accepts personalized rules and sorts. */
  allowPersonalized?: boolean;
  /** What the rules fill: a Home row or a Smart collection. */
  context?: "homeRow" | "collection";
  /** How the sentence names every library, when none is picked. */
  allLibrariesLabel?: string;
  /** An id for the libraries control, so another part of the page can move focus to it. */
  librariesId?: string;
}) {
  const t = useT();
  const allLibrariesLabel = allLibrariesProp ?? t("web.home.addRow.rules.allLibraries");
  const { groups } = value;
  const scope: MediaScope = value.media_scope ?? "all";
  const shownRatingSources = useShownRatingSources();
  const capabilities = useQueryRuleCapabilities();
  const fieldOptions = getFilterRuleFieldOptions(
    allowPersonalized,
    scope,
    shownRatingSources,
    capabilities,
  );
  const several = groups.length > 1;
  // Follows the picker's summary: "library"/"libraries" only follows a summary that shows a
  // name, and counts every chosen library, named or not.
  const summaryShowsAName = value.library_ids.some((id) =>
    libraries.some((library) => library.id === id),
  );

  // The rule just added, "group:rule", whose value control takes focus.
  const [focusRule, setFocusRule] = useState<string | null>(null);
  const valueScope = { libraryIds: value.library_ids, mediaScope: scope };

  const setGroups = (next: QueryGroup[]) => onChange({ ...value, groups: next });
  const setGroup = (index: number, group: QueryGroup) =>
    setGroups(groups.map((entry, at) => (at === index ? group : entry)));

  function setScope(next: string) {
    onChange({
      ...value,
      media_scope: next === "all" ? undefined : (next as QueryDefinition["media_scope"]),
      sort: normalizeQuerySortForScope(value.sort, {
        includePersonalized: allowPersonalized,
        relevanceScope: querySortScopeForMediaScope(next),
      }),
    });
  }

  function addRule(index: number) {
    const group = groups[index];
    if (!group) {
      setGroups([{ match: "all", rules: [newFilterRule()] }]);
      setFocusRule("0:0");
      return;
    }
    setGroup(index, { ...group, rules: [...group.rules, newFilterRule()] });
    setFocusRule(`${index}:${group.rules.length}`);
  }

  function updateRule(index: number, ruleIndex: number, updates: Partial<FilterRule>) {
    const group = groups[index]!;
    setGroup(index, {
      ...group,
      rules: group.rules.map((rule, at) => (at === ruleIndex ? { ...rule, ...updates } : rule)),
    });
  }

  function removeRule(index: number, ruleIndex: number) {
    const group = groups[index]!;
    const rules = group.rules.filter((_, at) => at !== ruleIndex);
    setFocusRule(null);
    setGroups(
      rules.length === 0
        ? groups.filter((_, at) => at !== index)
        : groups.map((entry, at) => (at === index ? { ...group, rules } : entry)),
    );
  }

  function addGroup() {
    // With one group, how groups join changes nothing yet, so the new one joins with "or".
    onChange({
      ...value,
      match: several ? value.match : "any",
      groups: [...groups, { match: "all", rules: [newFilterRule()] }],
    });
    setFocusRule(`${groups.length}:0`);
  }

  const joinsWithAnd = several && value.match === "all";
  const addGroupButton = (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className="text-muted-foreground"
      onClick={addGroup}
    >
      <Plus aria-hidden className="size-4" />
      {joinsWithAnd
        ? t("web.home.addRow.rules.addAndGroup")
        : t("web.home.addRow.rules.addOrGroup")}
    </Button>
  );

  return (
    <div className="grid gap-3">
      <div
        role="group"
        aria-label={
          context === "collection"
            ? t("web.home.addRow.rules.whatCollectionShows")
            : t("web.home.addRow.rules.whatRowShows")
        }
        className="flex flex-wrap items-center gap-x-2 gap-y-2 text-[15px]"
      >
        {t(
          summaryShowsAName
            ? "web.home.addRow.rules.sentenceNamed"
            : value.library_ids.length > 0
              ? "web.home.addRow.rules.sentencePicked"
              : "web.home.addRow.rules.sentenceAll",
          {
            count: value.library_ids.length,
            rules: groups.length > 0 ? "yes" : "no",
            w: word,
            scope: () => (
              <Select value={scope} onValueChange={setScope}>
                <SelectTrigger
                  aria-label={t("web.home.addRow.rules.kindOfTitles")}
                  className={INLINE_TRIGGER}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SCOPES.map(([key, name]) => (
                    <SelectItem key={key} value={key}>
                      {t(name)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ),
            libraries: () => (
              <span id={librariesId} className="inline-flex">
                <LibraryMultiSelect
                  libraries={libraries}
                  value={value.library_ids}
                  onChange={(libraryIds) => onChange({ ...value, library_ids: libraryIds })}
                  emptyLabel={allLibrariesLabel}
                  allOptionLabel={
                    allLibrariesLabel.charAt(0).toUpperCase() + allLibrariesLabel.slice(1)
                  }
                  triggerLabel={t("web.home.addRow.libraries")}
                  triggerClassName={`${INLINE_TRIGGER} justify-between`}
                />
              </span>
            ),
            // The mockup starts the matching clause on its own line.
            break: () => <span aria-hidden className="h-0 basis-full" />,
            match: () =>
              groups[0] ? (
                <MatchSelect
                  label={t("web.home.addRow.rules.howRulesCombine")}
                  value={groups[0].match}
                  names={RULE_NAMES}
                  onChange={(match) => setGroup(0, { ...groups[0]!, match })}
                />
              ) : null,
          },
        )}
      </div>

      <div className="border-border grid gap-2.5 rounded-2xl border p-3 sm:p-4">
        {groups.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {context === "collection"
              ? t("web.home.addRow.rules.noRulesCollectionYet")
              : t("web.home.addRow.rules.noRulesRow")}
          </p>
        ) : null}
        {groups.map((group, index) => (
          <div
            key={index}
            role={several ? "group" : undefined}
            aria-label={
              several ? t("web.home.addRow.rules.group", { number: index + 1 }) : undefined
            }
            className="grid gap-2"
          >
            {index > 0 ? (
              <div className="border-border flex flex-wrap items-center gap-2 border-t pt-3 text-sm">
                {t("web.home.addRow.rules.groupSentence", {
                  w: word,
                  join: () => (
                    <MatchSelect
                      label={t("web.home.addRow.rules.howGroupsCombine")}
                      value={value.match}
                      names={GROUP_JOIN_NAMES}
                      onChange={(match) => onChange({ ...value, match })}
                    />
                  ),
                  match: () => (
                    <MatchSelect
                      label={t("web.home.addRow.rules.groupMatchLabel", {
                        number: index + 1,
                      })}
                      value={group.match}
                      names={RULE_NAMES}
                      onChange={(match) => setGroup(index, { ...group, match })}
                    />
                  ),
                })}
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("web.home.addRow.rules.removeGroup", {
                    number: index + 1,
                  })}
                  className="text-muted-foreground ms-auto size-8"
                  onClick={() => {
                    setFocusRule(null);
                    setGroups(groups.filter((_, at) => at !== index));
                  }}
                >
                  <X aria-hidden className="size-4" />
                </Button>
              </div>
            ) : null}
            {group.rules.map((rule, ruleIndex) => (
              <FilterRuleRow
                key={ruleIndex}
                rule={rule}
                fieldOptions={fieldOptions}
                allowPersonalizedFilters={allowPersonalized}
                onChange={(updates) => updateRule(index, ruleIndex, updates)}
                onRemove={() => removeRule(index, ruleIndex)}
                roomy
                label={t("web.home.addRow.rules.rule", { number: ruleIndex + 1 })}
                valueScope={valueScope}
                focusValue={focusRule === `${index}:${ruleIndex}`}
              />
            ))}
            <div className="flex flex-wrap items-center gap-1">
              <Button type="button" variant="ghost" size="sm" onClick={() => addRule(index)}>
                <Plus aria-hidden className="size-4" />
                {t("web.home.addRow.rules.addRule")}
              </Button>
              {index === groups.length - 1 ? addGroupButton : null}
            </div>
          </div>
        ))}
        {groups.length === 0 ? (
          <div>
            <Button type="button" variant="ghost" size="sm" onClick={() => addRule(0)}>
              <Plus aria-hidden className="size-4" />
              {t("web.home.addRow.rules.addRule")}
            </Button>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** A rule row's order, under More options. */
export function RuleSortField({
  value,
  onChange,
  allowPersonalized = false,
}: {
  value: QueryDefinition;
  onChange: (value: QueryDefinition) => void;
  allowPersonalized?: boolean;
}) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div>
        <span className="text-sm font-medium">{t("web.home.addRow.order")}</span>
        <p className="text-muted-foreground mt-1 text-[13px]">
          {t("web.home.addRow.rules.orderHint")}
        </p>
      </div>
      <div className="flex items-center gap-2">
        <FilterSortControls
          sort={value.sort.field}
          order={value.sort.order}
          allowPersonalizedSorts={allowPersonalized}
          sortRelevanceScope={querySortScopeForMediaScope(value.media_scope)}
          onChange={(next) =>
            onChange({
              ...value,
              sort: {
                field: (next.sort ?? value.sort.field) as QueryDefinition["sort"]["field"],
                order: next.order as QueryDefinition["sort"]["order"],
              },
            })
          }
        />
      </div>
    </div>
  );
}
