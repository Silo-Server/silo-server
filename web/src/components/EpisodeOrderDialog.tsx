// Per-series episode order in one dialog: which provider and order number the
// show and which order its file names follow (the header), and how its seasons
// are laid out: as the order lists them, as one absolute list, or arranged by
// hand. Opened from the series page's Manage → Arrange Show (admins and
// metadata curators).
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { History, Loader2 } from "lucide-react";
import { V2ProblemError } from "@/api/v2/request";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { EpisodeOrderSeasonsView } from "@/components/EpisodeOrderSeasonsView";
import {
  FilesPicker,
  LayoutSwitch,
  OrderPicker,
  type Draft,
  type Source,
} from "@/components/EpisodeOrderPickers";
import { SeasonArranger } from "@/components/SeasonArranger";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  useCustomSeasons,
  useEpisodeOrder,
  useEpisodeOrderPreview,
  useSaveEpisodeOrder,
  type EpisodeOrderChoice,
} from "@/hooks/queries/episodeOrder";
import { useT } from "@/i18n";
import { asProvider, type Provider } from "@/lib/episodeOrder";

function draftOf(choice: EpisodeOrderChoice, libraryProvider: Provider): Draft {
  const source: Source =
    choice.provider === "tvdb" || choice.provider === "tmdb" ? choice.provider : "library";
  const structure = source === "library" ? libraryProvider : source;
  return {
    source,
    orderId: choice.order_id,
    files: choice.file_order ? asProvider(choice.file_order) : structure,
    layout: choice.layout === "absolute" || choice.layout === "custom" ? choice.layout : "seasons",
  };
}

/** The provider that numbers the series under draft. */
function structureOf(draft: Draft, libraryProvider: Provider): Provider {
  return draft.source === "library" ? libraryProvider : draft.source;
}

function choiceOf(draft: Draft, libraryProvider: Provider): EpisodeOrderChoice {
  const structure = structureOf(draft, libraryProvider);
  return {
    provider: draft.source === "library" ? "" : draft.source,
    order_id: draft.source === "library" ? "" : draft.orderId,
    file_order: draft.files === structure ? "" : draft.files,
    layout: draft.layout,
  };
}

/** Whether two choices start from the same order and file naming. */
const sameBase = (a: EpisodeOrderChoice, b: EpisodeOrderChoice) =>
  a.provider === b.provider && a.order_id === b.order_id && a.file_order === b.file_order;

export default function EpisodeOrderDialog({
  contentId,
  title,
  open,
  onOpenChange,
}: {
  contentId: string;
  title: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  // Mounted only while open, so every opening starts from the saved order.
  if (!open) return null;
  return <Editor contentId={contentId} title={title} onClose={() => onOpenChange(false)} />;
}

function Editor({
  contentId,
  title,
  onClose,
}: {
  contentId: string;
  title: string;
  onClose: () => void;
}) {
  const t = useT();
  const stateQuery = useEpisodeOrder(contentId, true, true);
  const state = stateQuery.data;
  const [edited, setEdited] = useState<Draft | null>(null);
  const libraryProvider = asProvider(state?.library_provider);
  const saved = state ? draftOf(state.choice, libraryProvider) : null;
  const draft = edited ?? saved;
  const choice = draft ? choiceOf(draft, libraryProvider) : undefined;
  const savedChoice = saved ? choiceOf(saved, libraryProvider) : undefined;
  const custom = draft?.layout === "custom";

  // The arranger has unsaved edits: changing the order, starting over or
  // closing asks first. It stays mounted while edited, so switching layouts
  // back and forth keeps them.
  const [arrangerEdited, setArrangerEdited] = useState(false);
  const [confirm, setConfirm] = useState<
    | { kind: "draft"; next: Draft }
    | { kind: "startOver" }
    | { kind: "close" }
    | { kind: "apply" }
    | null
  >(null);
  const [restarts, setRestarts] = useState(0);

  // The previewed choice; the Custom layout previews its base order for the
  // file fits.
  const previewChoice =
    choice && (choice.layout === "custom" ? { ...choice, layout: "seasons" as const } : choice);
  const preview = useEpisodeOrderPreview(contentId, previewChoice, true);
  const save = useSaveEpisodeOrder(contentId);

  // The stored arrangement opens when it was made from the chosen order;
  // another order, or a start over, builds a fresh one from its seasons.
  const arrangerOn = custom || arrangerEdited;
  // The chosen order's own seasons load with the dialog: the read-only
  // layouts show them and a fresh arrangement starts from them. The stored
  // arrangement loads once Custom is in view, or about to be (hover, focus).
  const [storedWanted, setStoredWanted] = useState(state?.choice.layout === "custom");
  const stored = useCustomSeasons(contentId, storedWanted || custom, null);
  // Switched to Custom before the stored arrangement arrived: it reopens on
  // its own order when it does, unless the order changed meanwhile.
  const [reopenPending, setReopenPending] = useState(false);
  // The read-only layout Custom was opened from stays on screen, dimmed,
  // until the arrangement can show.
  const [heldLayout, setHeldLayout] = useState<"seasons" | "absolute" | null>(null);
  if (reopenPending && stored.data) {
    setReopenPending(false);
    if (stored.data.saved && draft?.layout === "custom" && !arrangerEdited)
      setEdited({ ...draftOf(stored.data.choice, libraryProvider), layout: "custom" });
  }
  // Cached arrangements carry the revision of when they loaded; a later
  // opening loads them again.
  const queryClient = useQueryClient();
  useEffect(
    () => () => queryClient.removeQueries({ queryKey: ["custom-seasons", contentId] }),
    [queryClient, contentId],
  );
  const orderBase: EpisodeOrderChoice | null = choice ? { ...choice, layout: "custom" } : null;
  const fresh = useCustomSeasons(contentId, orderBase !== null, orderBase);
  const startFrom =
    stored.data && (restarts > 0 || !sameBase(stored.data.choice, orderBase ?? stored.data.choice))
      ? orderBase
      : null;
  const arrangement = startFrom ? fresh.data : stored.data;
  const arrangementFailed = stored.isError || fresh.isError;
  // The arranger keeps showing the last arrangement, dimmed, while the next
  // one loads, instead of blanking the dialog.
  const arrangerKey = `${restarts}:${JSON.stringify(startFrom)}`;
  const [shownArrangement, setShownArrangement] = useState<{
    key: string;
    data: NonNullable<typeof arrangement>;
  } | null>(null);
  if (arrangement && shownArrangement?.key !== arrangerKey)
    setShownArrangement({ key: arrangerKey, data: arrangement });
  const arrangementSwitching = !!shownArrangement && shownArrangement.key !== arrangerKey;

  function update(patch: Partial<Draft>) {
    if (!draft) return;
    let next = { ...draft, ...patch };
    // Switching to Custom reopens a saved arrangement on the order it was
    // made from; changing Based on afterwards starts over.
    const toCustom = patch.layout === "custom" && draft.layout !== "custom";
    if (toCustom && stored.data?.saved)
      next = { ...draftOf(stored.data.choice, libraryProvider), layout: "custom" };
    // Only a real change settles a pending reopen: clicking Custom again
    // while it loads keeps waiting for the saved arrangement.
    if (toCustom) {
      setReopenPending(!stored.data);
      setHeldLayout(draft.layout === "absolute" ? "absolute" : "seasons");
    } else if (JSON.stringify(next) !== JSON.stringify(draft)) setReopenPending(false);
    const baseChanged = !!choice && !sameBase(choiceOf(next, libraryProvider), choice);
    if (arrangerEdited && baseChanged) setConfirm({ kind: "draft", next });
    else setEdited(next);
  }

  function startOver() {
    setArrangerEdited(false);
    setRestarts((n) => n + 1);
  }

  function requestClose() {
    if (arrangerEdited) setConfirm({ kind: "close" });
    else onClose();
  }

  // Applying a read-only layout drops arranger edits kept behind it.
  function requestApply() {
    if (arrangerEdited) setConfirm({ kind: "apply" });
    else apply();
  }

  const changed = !!choice && JSON.stringify(choice) !== JSON.stringify(savedChoice);

  function apply() {
    if (!choice || !state) return;
    save.mutate(
      { etag: state.etag, body: choice },
      {
        onSuccess: () => {
          onClose();
          toast.success(t("web.item.episodeOrder.applied", { title }), {
            description: t("web.item.episodeOrder.appliedBody"),
          });
        },
        onError: (err) => {
          // Someone saved in between: close so the next opening starts from
          // their choice instead of saving over it.
          if (err instanceof V2ProblemError && err.status === 412) {
            toast.error(t("web.item.episodeOrder.conflict"));
            onClose();
            return;
          }
          toast.error(t("web.item.episodeOrder.saveFailed"));
        },
      },
    );
  }

  const structure = draft ? structureOf(draft, libraryProvider) : libraryProvider;

  return (
    <Dialog open onOpenChange={(next) => !next && requestClose()}>
      <DialogContent
        className="flex max-w-[min(1760px,calc(100vw-3rem))] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(1760px,calc(100vw-3rem))] md:h-[min(calc(100dvh-3rem),1080px)]"
        showCloseButton
      >
        <DialogHeader className="border-border/10 shrink-0 flex-col items-stretch gap-x-4 gap-y-3 border-b px-5 py-3.5 pe-12 text-start sm:flex-row sm:flex-wrap sm:items-center">
          <div className="min-w-[10rem] space-y-0.5">
            <DialogTitle className="text-[0.9375rem] font-semibold">
              {t("web.item.episodeOrder.title")}
            </DialogTitle>
            <p className="text-muted-foreground truncate text-xs">{title}</p>
            <DialogDescription className="sr-only">
              {t("web.item.episodeOrder.description")}
            </DialogDescription>
          </div>
          {state && draft && (
            <div className="flex min-w-0 flex-1 flex-wrap items-stretch gap-2.5">
              <OrderPicker
                state={state}
                draft={draft}
                structure={structure}
                basedOn={custom}
                onChange={update}
              />
              <FilesPicker
                files={draft.files}
                structure={structure}
                preview={preview.data}
                onChange={(files) => update({ files })}
              />
              <LayoutSwitch
                layout={draft.layout}
                structure={structure}
                onChange={(layout) => update({ layout })}
                onCustomIntent={() => setStoredWanted(true)}
              />
            </div>
          )}
        </DialogHeader>

        {!state || !draft ? (
          <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 p-10 text-sm">
            {stateQuery.isError ? (
              t("web.item.episodeOrder.order.failed")
            ) : (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            )}
          </div>
        ) : (
          <>
            {(!custom || (heldLayout && !shownArrangement && !arrangementFailed)) && (
              <EpisodeOrderSeasonsView
                contentId={contentId}
                choice={custom && heldLayout ? { ...choice!, layout: heldLayout } : choice!}
                busy={custom}
                structure={structure}
                files={draft.files}
                preview={preview.data}
                onReadFilesAs={(files) => update({ files })}
                onArrange={() => {
                  setStoredWanted(true);
                  update({ layout: "custom" });
                }}
              />
            )}
            {custom &&
              ((!shownArrangement && (!heldLayout || arrangementFailed)) ||
                (arrangementSwitching && arrangementFailed)) && (
                <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 p-10 text-sm">
                  {arrangementFailed ? (
                    t("web.item.arrangeSeasons.failed")
                  ) : (
                    <Loader2 className="size-5 animate-spin" aria-hidden="true" />
                  )}
                </div>
              )}
            {arrangerOn && shownArrangement && !(arrangementSwitching && arrangementFailed) && (
              <SeasonArranger
                // A start over or another base order starts from new data.
                key={shownArrangement.key}
                contentId={contentId}
                title={title}
                data={shownArrangement.data}
                busy={arrangementSwitching}
                dirtyFromStart={startFrom !== null || state.choice.layout !== "custom"}
                hidden={!custom}
                onDirtyChange={setArrangerEdited}
                onStartOver={() =>
                  arrangerEdited ? setConfirm({ kind: "startOver" }) : startOver()
                }
                onCancel={requestClose}
                onDone={onClose}
              />
            )}
          </>
        )}

        {!custom && (
          <div className="border-border/10 flex shrink-0 flex-col gap-3 border-t px-5 py-3.5 sm:flex-row sm:items-center">
            <div className="text-muted-foreground flex min-w-0 flex-1 items-start gap-2 text-xs leading-relaxed">
              <History className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
              <span>{t("web.item.episodeOrder.footer")}</span>
            </div>
            <div className="flex shrink-0 gap-2">
              <Button variant="ghost" onClick={requestClose}>
                {t("common.cancel")}
              </Button>
              <Button disabled={!changed || save.isPending} onClick={requestApply}>
                {save.isPending && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
                {t("web.item.episodeOrder.apply")}
              </Button>
            </div>
          </div>
        )}

        <ConfirmDialog
          open={confirm !== null}
          onOpenChange={(next) => !next && setConfirm(null)}
          title={t("web.item.arrangeSeasons.discardTitle")}
          description={t(
            confirm?.kind === "close"
              ? "web.item.arrangeSeasons.discardBody"
              : "web.item.arrangeSeasons.startOverBody",
          )}
          confirmLabel={t("web.item.arrangeSeasons.discard")}
          cancelLabel={t("web.item.arrangeSeasons.keepEditing")}
          variant="destructive"
          onConfirm={() => {
            const pending = confirm;
            setConfirm(null);
            if (pending?.kind === "close") onClose();
            else if (pending?.kind === "apply") apply();
            else if (pending?.kind === "startOver") startOver();
            else if (pending?.kind === "draft") {
              setArrangerEdited(false);
              setEdited(pending.next);
            }
          }}
        />
      </DialogContent>
    </Dialog>
  );
}
