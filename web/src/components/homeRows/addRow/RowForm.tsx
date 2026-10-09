import { useId } from "react";
import { queryDefinitionFromSectionConfig } from "@/api/types";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { PreviewState } from "@/hooks/queries/homeRows/useRowPreview";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import type { RecipeCatalogResponse } from "@/lib/recipes";
import { ruleSortSummary } from "@/lib/homeRows/describe";
import {
  savedTitle,
  withCollection,
  withRules,
  withTitle,
  type RowDraft,
} from "@/lib/homeRows/rowDraft";
import { BULK_LIBRARY_LIMIT } from "@/lib/homeRows/bulkCopy";
import type { LibraryPage, RowCollections } from "@/lib/homeRows/types";
import { kindLocked, variantFamily } from "@/lib/homeRows/variants";
import { FILTER_SECTION_TYPES } from "@/lib/sectionTypes";
import { CollectionPicker } from "./CollectionPicker";
import { CuratedTitlesEditor } from "./CuratedTitlesEditor";
import { LibraryPageChips } from "./LibraryPageChips";
import { ParamFields, type ParamLibrary } from "./ParamFields";
import { MoreOptions } from "./MoreOptions";
import { RowPreview } from "./RowPreview";
import { RuleBuilder, RuleSortField } from "./RuleBuilder";
import { ShowsSummary } from "./ShowsSummary";
import { VariantChoice } from "./VariantChoice";

export interface ShowsLine {
  label: string;
  sentence: string;
  onChange?: () => void;
}

/** Where a collection row's collection comes from, and which are already on the page. */
export interface CollectionChoices {
  collections: RowCollections;
  onPageIds: ReadonlySet<string>;
  pageLabel: string;
  /** The collection the draft shows now as this surface reads it, or "". */
  value: string;
  /** The collection the draft shows now, when it is in the list. */
  current: CollectionOption | undefined;
}

/** Kinds built with their own control, which comes before the preview. */
const CONTROL_KINDS: ReadonlySet<string> = new Set([
  "collection",
  "admin_curated_list",
  ...FILTER_SECTION_TYPES,
]);

function itemIds(config: Record<string, unknown>): string[] {
  return Array.isArray(config.item_ids)
    ? config.item_ids.filter((id): id is string => typeof id === "string")
    : [];
}

/**
 * The control a kind is built with, when it is more than a variant: a
 * collection picker, the rule builder (with its order under More options),
 * or an Editor's Picks title list. These come before the preview, which
 * shows what they produce.
 */
function KindControl({
  draft,
  onChange,
  choices,
  libraries,
}: {
  draft: RowDraft;
  onChange: (draft: RowDraft) => void;
  choices: CollectionChoices;
  libraries: ParamLibrary[];
}) {
  if (draft.sectionType === "collection") {
    return (
      <CollectionPicker
        collections={choices.collections}
        value={choices.value}
        onPageIds={choices.onPageIds}
        pageLabel={choices.pageLabel}
        locked={kindLocked(draft.config)}
        onPick={(option) => onChange(withCollection(draft, option, choices.current?.title))}
      />
    );
  }
  if (FILTER_SECTION_TYPES.has(draft.sectionType)) {
    return (
      <RuleBuilder
        value={queryDefinitionFromSectionConfig(draft.config)}
        onChange={(query) => onChange(withRules(draft, query))}
        libraries={libraries}
        allowPersonalized
      />
    );
  }
  if (draft.sectionType === "admin_curated_list") {
    return (
      <CuratedTitlesEditor
        itemIds={itemIds(draft.config)}
        onChange={(ids) => onChange({ ...draft, config: { ...draft.config, item_ids: ids } })}
      />
    );
  }
  return null;
}

/**
 * Step 2 and Edit row, top to bottom: the live preview, (Edit) what the row
 * shows, the one choice that matters, the row's name, (Add row on a library
 * page) the other library pages, then More options.
 * Collection, rule and Editor's Picks rows put their control above the
 * preview instead.
 */
export function RowForm({
  draft,
  onChange,
  catalog,
  preview,
  liveLabel,
  previewOffText,
  shows,
  variantLocked,
  libraries,
  onLibraryPage,
  onVariant,
  collectionChoices,
  libraryPages,
  contentLocked = false,
}: {
  draft: RowDraft;
  onChange: (draft: RowDraft) => void;
  catalog: RecipeCatalogResponse | undefined;
  preview: PreviewState;
  liveLabel: string;
  previewOffText: string;
  /** Edit row only. */
  shows?: ShowsLine;
  /** No variant control: legacy Trakt rows and Continue Reading rows. */
  variantLocked: boolean;
  libraries: ParamLibrary[];
  onLibraryPage: boolean;
  onVariant: (presetKey: string) => void;
  collectionChoices: CollectionChoices;
  /** Add row on a library page, for a kind that can be copied: the pages it may also go to. */
  libraryPages?: { pages: LibraryPage[]; currentId: number };
  /**
   * What the row shows can't change (a server row on Settings > Home Screen):
   * no collection picker, rules or title list, and no rule order.
   */
  contentLocked?: boolean;
}) {
  const nameId = useId();
  const family = variantLocked ? undefined : variantFamily(draft.sectionType);
  const rules = FILTER_SECTION_TYPES.has(draft.sectionType);
  const control = contentLocked ? null : (
    <KindControl
      draft={draft}
      onChange={onChange}
      choices={collectionChoices}
      libraries={libraries}
    />
  );
  const setConfig = (config: Record<string, unknown>) => onChange({ ...draft, config });
  const fieldProps = {
    sectionType: draft.sectionType,
    config: draft.config,
    onChange: setConfig,
    libraries,
    onLibraryPage,
  };
  const fallbackTitle = collectionChoices.current?.title;
  let nameHelp: string | null = null;
  if (draft.sectionType === "collection" && draft.titleFollowsVariant) {
    nameHelp = "Starts as the collection's name.";
  } else if (family && draft.titleFollowsVariant) {
    nameHelp = `Follows ${family.follows} until you type your own name.`;
  }
  const previewStrip = (
    <RowPreview
      title={draft.title.trim() || savedTitle(draft, catalog, fallbackTitle)}
      sectionType={draft.sectionType}
      state={preview}
      liveLabel={liveLabel}
      offText={previewOffText}
      countUpTo={rules ? draft.itemLimit : undefined}
    />
  );
  const showsLine = shows ? <ShowsSummary sectionType={draft.sectionType} {...shows} /> : null;
  return (
    <div className="grid gap-5">
      {CONTROL_KINDS.has(draft.sectionType) ? (
        <>
          {showsLine}
          {control}
          {previewStrip}
        </>
      ) : (
        <>
          {previewStrip}
          {showsLine}
        </>
      )}
      {family ? (
        <VariantChoice sectionType={draft.sectionType} config={draft.config} onChange={onVariant} />
      ) : null}
      <ParamFields {...fieldProps} slot="primary" />
      <div className="grid gap-2">
        <Label htmlFor={nameId}>Row name</Label>
        <Input
          id={nameId}
          className="h-10 text-[15px]"
          value={draft.title}
          placeholder={savedTitle({ ...draft, title: "" }, catalog, fallbackTitle)}
          onChange={(event) => onChange(withTitle(draft, event.target.value))}
        />
        {nameHelp ? <p className="text-muted-foreground text-[13px]">{nameHelp}</p> : null}
      </div>
      {libraryPages ? (
        <LibraryPageChips
          pages={libraryPages.pages}
          currentId={libraryPages.currentId}
          currentNote="this page"
          selectedIds={draft.extraLibraryIds ?? []}
          onChange={(extraLibraryIds) => onChange({ ...draft, extraLibraryIds })}
          // The new row on this page is one of the pages the request adds to.
          maxSelected={BULK_LIBRARY_LIMIT - 1}
          disabled={draft.hero}
          help={
            draft.hero
              ? "Turn off the hero banner to add this row to other pages."
              : "Each page gets its own copy, so you can change or remove it there later."
          }
        />
      ) : null}
      <MoreOptions
        itemLimit={draft.itemLimit}
        hero={draft.hero}
        summary={rules ? ruleSortSummary(draft.config) : undefined}
        onItemLimitChange={(itemLimit) => onChange({ ...draft, itemLimit })}
        // A hero row stays on this page alone, so turning it on drops the other pages.
        onHeroChange={(hero) =>
          onChange({ ...draft, hero, ...(hero ? { extraLibraryIds: [] } : {}) })
        }
      >
        {rules && !contentLocked ? (
          <RuleSortField
            value={queryDefinitionFromSectionConfig(draft.config)}
            onChange={(query) => onChange(withRules(draft, query))}
            allowPersonalized
          />
        ) : null}
        <ParamFields {...fieldProps} slot="more" />
      </MoreOptions>
    </div>
  );
}
