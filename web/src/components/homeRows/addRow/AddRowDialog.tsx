import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Plus, Search, Trash2 } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";
import { StepCount, StepDialog } from "@/components/calm/StepDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useRowPreview } from "@/hooks/queries/homeRows/useRowPreview";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import {
  isPersonalRowKind,
  pickerGroups,
  rowKindLabel,
  rowKindSentence,
  type PickerCard,
} from "@/lib/homeRows/catalog";
import { canCopyToLibraries, copyTargetPages, libraryCopyIds } from "@/lib/homeRows/bulkCopy";
import { pageLabel as labelOfPage, libraryPagesOf } from "@/lib/homeRows/pages";
import { collectionIdOf } from "@/lib/homeRows/payloads";
import {
  canSaveDraft,
  DRAFT_FIELD_LABELS,
  draftForPreset,
  draftFromRow,
  findRecipe,
  mergeReloadedDraft,
  previewWaitText,
  savedTitle,
  withVariant,
  type DraftField,
  type RowDraft,
} from "@/lib/homeRows/rowDraft";
import { searchPickerGroups } from "@/lib/homeRows/search";
import {
  RowChangedError,
  type EditSession,
  type HomeRowsAdapter,
  type RowCollections,
} from "@/lib/homeRows/types";
import { kindLocked, showsLabel, variantLocked } from "@/lib/homeRows/variants";
import type { RecipeCatalogResponse } from "@/lib/recipes";
import { cn } from "@/lib/utils";
import type { ParamLibrary } from "./ParamFields";
import { RowForm, type CollectionChoices } from "./RowForm";
import { RowPicker } from "./RowPicker";

export interface AddRowDialogProps {
  adapter: HomeRowsAdapter;
  catalog: RecipeCatalogResponse | undefined;
  catalogFailed?: boolean;
  libraries: ParamLibrary[];
  /** Edit row when set, Add row otherwise. */
  session: EditSession | null;
  /** Add row opened from a link on a draft: starts at step 2 with it. */
  initialSeed?: {
    draft: RowDraft;
    /** Replaces step 2's back link to the picker. */
    back?: { label: string; onClick: () => void };
  };
  onClose: () => void;
  /** After a row is added or saved, with the ids of new rows. */
  onSaved: (newIds: string[]) => void;
  onDelete?: (session: EditSession) => void;
  /** The footer's delete button, when "Delete row…" isn't what it does. */
  deleteLabel?: string;
}

const NO_COLLECTIONS: RowCollections = { options: [], loading: false, failed: false, href: "" };

function sentenceWithoutStop(sentence: string) {
  return sentence.replace(/\.$/, "");
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

/**
 * Add row (step 1: pick a kind; step 2: preview, variant, name, More
 * options) and Edit row (step 2 with Shows · Change and Delete row…) in one
 * dialog. Below 1024px it is a bottom sheet, and step 2 is full height.
 */
export function AddRowDialog({
  adapter,
  catalog,
  catalogFailed,
  libraries,
  session: initialSession,
  initialSeed,
  onClose,
  onSaved,
  onDelete,
  deleteLabel = "Delete row…",
}: AddRowDialogProps) {
  const editing = initialSession !== null;
  const narrow = useMediaQuery("(max-width: 1023px)");
  const phone = useMediaQuery("(max-width: 639px)");
  const page = labelOfPage(adapter.page, adapter.pages);
  const [step, setStep] = useState<"pick" | "form">(editing || initialSeed ? "form" : "pick");
  const [query, setQuery] = useState("");
  const [session, setSession] = useState(initialSession);
  const [original, setOriginal] = useState<RowDraft | null>(() =>
    initialSession ? draftFromRow(initialSession.row, catalog) : null,
  );
  const [draft, setDraft] = useState<RowDraft | null>(original ?? initialSeed?.draft ?? null);
  // A row opened before the kinds of rows loaded couldn't tell whether its name
  // is still its variant's preset name; work that out once they arrive.
  const [namedWithCatalog, setNamedWithCatalog] = useState(catalog !== undefined);
  if (catalog && !namedWithCatalog) {
    setNamedWithCatalog(true);
    if (session && original && draft) {
      const settled = draftFromRow(session.row, catalog);
      setOriginal(settled);
      if (draft.title === original.title) {
        setDraft({ ...draft, titleFollowsVariant: settled.titleFollowsVariant });
      }
    }
  }
  const [conflict, setConflict] = useState(false);
  const [changedUpstream, setChangedUpstream] = useState<DraftField[]>([]);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const mounted = useRef(true);
  const searchRef = useRef<HTMLInputElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const firstStep = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // Moving between steps puts focus where the new step starts.
  useEffect(() => {
    if (firstStep.current) {
      firstStep.current = false;
      return;
    }
    if (step === "pick") searchRef.current?.focus();
    else headingRef.current?.focus();
  }, [step]);

  const groups = useMemo(() => searchPickerGroups(pickerGroups(catalog), query), [catalog, query]);
  const typesOnPage = useMemo(
    () => new Set(adapter.rows.map((row) => row.sectionType)),
    [adapter.rows],
  );
  const collections = adapter.collections ?? NO_COLLECTIONS;
  const collectionChoices = useMemo<CollectionChoices>(() => {
    const value =
      draft?.sectionType === "collection" ? collectionIdOf(draft.config, adapter.surface) : "";
    return {
      collections,
      pageLabel: page,
      onPageIds: new Set(
        adapter.rows
          .filter((row) => row.sectionType === "collection")
          .map((row) => collectionIdOf(row.config, adapter.surface)),
      ),
      value,
      current: collections.options.find((option) => option.id === value),
    };
  }, [collections, page, adapter.rows, adapter.surface, draft]);
  const previewWait = draft ? previewWaitText(draft, adapter.surface) : null;
  // Why the strip shows no titles: this surface has no preview, or the draft can't have one yet.
  let previewOffText = "Previews aren't available on this server.";
  if (adapter.surface === "profile")
    previewOffText = editing
      ? `You'll see your changes on ${page} after you save.`
      : `You'll see it on ${page} after you add it.`;
  else if (adapter.capabilities.draftPreview && previewWait) previewOffText = previewWait;
  const libraryPages = useMemo(() => libraryPagesOf(adapter.pages), [adapter.pages]);
  // A new row on a library page may go to other library pages too, when its
  // settings can be copied as they are, but only to pages it fits.
  const targetPages =
    !editing &&
    adapter.capabilities.libraryCopies &&
    adapter.page.kind === "library" &&
    draft !== null &&
    canCopyToLibraries(draft)
      ? copyTargetPages(draft, libraryPages, adapter.page.libraryId)
      : [];
  const copyPages =
    adapter.page.kind === "library" && targetPages.length > 1
      ? { pages: targetPages, currentId: adapter.page.libraryId }
      : undefined;
  const copies = copyPages && draft ? libraryCopyIds(draft, adapter.page, libraryPages) : [];
  const preview = useRowPreview(
    draft ?? { sectionType: "", config: {} },
    adapter.page,
    adapter.capabilities.draftPreview && step === "form" && draft !== null && !previewWait,
  );

  const def = draft ? findRecipe(catalog, draft.sectionType) : undefined;
  const locked = draft ? variantLocked(draft.sectionType, draft.config) : false;
  const canSave = draft !== null && canSaveDraft(draft, adapter.surface);

  function pick(card: PickerCard, presetKey?: string) {
    const preset = card.def.presets.find((entry) => entry.key === presetKey) ?? card.def.presets[0];
    const fresh = draftForPreset(card.def, preset);
    setDraft(
      editing && draft
        ? {
            ...fresh,
            // A new kind starts from its own settings; the name stays unless it
            // was still following the old variant.
            title: draft.titleFollowsVariant ? fresh.title : draft.title,
            titleFollowsVariant: draft.titleFollowsVariant,
            itemLimit: draft.itemLimit,
            hero: draft.hero,
          }
        : fresh,
    );
    setStep("form");
  }

  async function run(action: () => Promise<void>) {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    try {
      await action();
    } finally {
      busyRef.current = false;
      if (mounted.current) setBusy(false);
    }
  }

  function submit() {
    if (!draft || conflict || !canSave) return;
    const finished = {
      ...draft,
      title: savedTitle(draft, catalog, collectionChoices.current?.title),
      extraLibraryIds: copies,
    };
    void run(async () => {
      try {
        // Closed while saving: the page may have opened another dialog since,
        // which this one's onSaved and onClose would act on.
        if (session) {
          await adapter.save(session, finished);
          if (!mounted.current) return;
          onSaved([]);
        } else {
          const { newIds } = await adapter.create(finished);
          if (!mounted.current) return;
          onSaved(newIds);
        }
        onClose();
      } catch (error) {
        if (error instanceof RowChangedError) {
          if (mounted.current) setConflict(true);
          return;
        }
        toast.error(
          errorMessage(error, session ? "Could not save this row" : "Could not add this row"),
        );
      }
    });
  }

  function reloadRow() {
    if (!session || !draft || !original) return;
    void run(async () => {
      try {
        const next = await adapter.reloadEdit(session);
        if (!mounted.current) return;
        const upstream = draftFromRow(next.row, catalog);
        const merged = mergeReloadedDraft(original, draft, upstream);
        setSession(next);
        setOriginal(upstream);
        setDraft(merged.draft);
        setChangedUpstream(merged.changedUpstream);
        setConflict(false);
      } catch (error) {
        toast.error(errorMessage(error, "Could not reload this row"));
      }
    });
  }

  const changing = editing && step === "pick";
  // Step 2 of Add row goes back to the picker; the Change picker goes back to Edit row.
  const back =
    step === "form" && !editing
      ? (initialSeed?.back ?? { label: "All rows", onClick: () => setStep("pick") })
      : changing
        ? { label: "Edit row", onClick: () => setStep("form") }
        : null;
  let title: string;
  let description: ReactNode;
  if (step === "pick") {
    title = changing ? "Change what this row shows" : `Add a row to ${page}`;
    description = changing
      ? "Pick another kind of row. Its settings start fresh; the name and More options stay."
      : phone
        ? `Pick what it shows. It goes to the bottom of ${page}.`
        : "Pick what the row shows. Next you'll see a preview and can name it.";
  } else if (editing) {
    title = "Edit row";
    // A profile save stores only the fields that profile changed, so an
    // admin's edit reaches every profile except in what it changed itself.
    description =
      adapter.surface === "profile"
        ? "Changes apply only to this profile."
        : `Changes apply to everyone on ${page}. A profile that changed this row keeps its own changes.`;
  } else {
    const type = draft?.sectionType ?? "";
    title = rowKindLabel(type);
    if (type === "collection") {
      description = (
        <>
          Show one of your collections as a row. Make or change collections in{" "}
          <Link to={collections.href} className="text-foreground underline underline-offset-2">
            Collections
          </Link>
          .
        </>
      );
    } else if (type === "custom_filter") {
      description = "Describe the titles you want. New matches show up on their own.";
    } else {
      description = `${rowKindSentence(type)}${isPersonalRowKind(type) ? " Different for each viewer." : ""}`;
    }
  }

  // The footer's left side: progress while adding, Delete row… while editing.
  let footerStart: ReactNode = <span />;
  if (!editing) {
    footerStart =
      step === "pick" ? (
        <StepCount step={1}>{`New rows go to the bottom of ${page}`}</StepCount>
      ) : (
        <StepCount step={2}>
          {copies.length > 0 ? "Goes to the bottom of each page" : `Goes to the bottom of ${page}`}
        </StepCount>
      );
  } else if (step === "form") {
    footerStart = (
      <Button
        type="button"
        variant="ghost"
        className="text-destructive hover:text-destructive -ml-3"
        disabled={busy || !onDelete}
        onClick={() => session && onDelete?.(session)}
      >
        <Trash2 aria-hidden className="size-4" />
        {deleteLabel}
      </Button>
    );
  }

  return (
    // Focus goes back to whatever opened the dialog: Add row, or the row's ⋯.
    <StepDialog
      size={step === "pick" ? "picker" : "form"}
      onClose={onClose}
      onOpenFocus={() => {
        if (step === "pick") searchRef.current?.focus();
        else headingRef.current?.focus();
      }}
      back={back}
      title={title}
      titleRef={headingRef}
      description={description}
      header={
        step === "pick" ? (
          <div className="relative mt-3">
            <Search
              aria-hidden
              className="text-muted-foreground absolute top-1/2 left-3.5 size-4 -translate-y-1/2"
            />
            <Input
              ref={searchRef}
              type="search"
              aria-label="Search rows"
              placeholder={phone ? "Search rows" : "Search, e.g. trending, 4K, Ghibli, Christmas"}
              className="h-11 pl-10"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
        ) : null
      }
      notice={
        step === "form" && editing && (conflict || changedUpstream.length > 0) ? (
          <div
            role={conflict ? "alert" : "status"}
            className={cn(
              "flex items-center gap-3 border-t px-5 py-3 text-sm sm:px-7",
              conflict ? "border-warning/40 bg-warning/10" : "border-border",
            )}
          >
            <p className="min-w-0 flex-1">
              {conflict
                ? "This row changed since you opened it. Your changes are kept. Reload the row to see what changed, then save again."
                : `Changed elsewhere: ${changedUpstream.map((field) => DRAFT_FIELD_LABELS[field]).join(", ")}`}
            </p>
            {conflict ? (
              <Button type="button" size="sm" variant="outline" disabled={busy} onClick={reloadRow}>
                Reload row
              </Button>
            ) : null}
          </div>
        ) : null
      }
      footerStart={footerStart}
      actions={
        step === "form" ? (
          <Button type="button" disabled={busy || conflict || !canSave} onClick={submit}>
            {editing ? (
              "Save"
            ) : (
              <>
                <Plus aria-hidden className="size-4" />
                {copies.length > 0 ? `Add to ${copies.length + 1} pages` : "Add row"}
              </>
            )}
          </Button>
        ) : null
      }
    >
      {step === "pick" ? (
        <div className="border-border flex min-h-0 flex-1 flex-col border-t pt-3 lg:pt-0">
          {catalog ? (
            <RowPicker
              groups={groups}
              query={query}
              onClearSearch={() => {
                setQuery("");
                searchRef.current?.focus();
              }}
              pageLabel={page}
              typesOnPage={typesOnPage}
              narrow={narrow}
              onPick={pick}
            />
          ) : (
            <p role="status" className="text-muted-foreground px-7 py-10 text-sm">
              {catalogFailed
                ? "The kinds of rows didn't load. Close this and try again."
                : "Loading kinds of rows…"}
            </p>
          )}
        </div>
      ) : draft ? (
        <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-6 sm:px-7">
          <RowForm
            draft={draft}
            onChange={setDraft}
            catalog={catalog}
            preview={preview}
            liveLabel={editing ? "Live preview" : "Live preview from your libraries"}
            previewOffText={previewOffText}
            collectionChoices={collectionChoices}
            shows={
              editing
                ? {
                    label: showsLabel(draft.sectionType, draft.config),
                    sentence: sentenceWithoutStop(rowKindSentence(draft.sectionType)),
                    onChange:
                      kindLocked(draft.config) || session?.kindLocked
                        ? undefined
                        : () => setStep("pick"),
                  }
                : undefined
            }
            variantLocked={locked}
            libraries={libraries}
            onLibraryPage={adapter.page.kind === "library"}
            onVariant={(presetKey) => setDraft(withVariant(draft, def, presetKey))}
            libraryPages={copyPages}
            contentLocked={session?.kindLocked}
          />
        </div>
      ) : null}
    </StepDialog>
  );
}
