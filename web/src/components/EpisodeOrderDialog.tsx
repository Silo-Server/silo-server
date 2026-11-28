// Per-series episode order in one dialog: which provider and order number the
// show and which order its file names follow (the header), and how its seasons
// are laid out: as the order lists them, as one absolute list, or arranged by
// hand. Opened from the series page's Manage → Arrange Show (admins and
// metadata curators).
import { useState } from "react";
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
  type EpisodeOrderState,
} from "@/hooks/queries/episodeOrder";
import { useT } from "@/i18n";
import { asProvider, type Provider } from "@/lib/episodeOrder";

function draftOf(state: EpisodeOrderState): Draft {
  const { choice } = state;
  const source: Source =
    choice.provider === "tvdb" || choice.provider === "tmdb" ? choice.provider : "library";
  const structure = source === "library" ? asProvider(state.library_provider) : source;
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
  const saved = state ? draftOf(state) : null;
  const draft = edited ?? saved;
  const libraryProvider = asProvider(state?.library_provider);
  const choice = draft ? choiceOf(draft, libraryProvider) : undefined;
  const savedChoice = saved ? choiceOf(saved, libraryProvider) : undefined;
  const custom = draft?.layout === "custom";

  // The arranger has unsaved edits: changing the order, starting over or
  // closing asks first. It stays mounted while edited, so switching layouts
  // back and forth keeps them.
  const [arrangerEdited, setArrangerEdited] = useState(false);
  const [confirm, setConfirm] = useState<
    { kind: "draft"; next: Draft } | { kind: "startOver" } | { kind: "close" } | null
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
  const stored = useCustomSeasons(contentId, arrangerOn, null);
  const startFrom: EpisodeOrderChoice | null =
    choice && stored.data && (restarts > 0 || !sameBase(stored.data.choice, choice))
      ? { ...choice, layout: "custom" }
      : null;
  const fresh = useCustomSeasons(contentId, arrangerOn && startFrom !== null, startFrom);
  const arrangement = startFrom ? fresh.data : stored.data;
  const arrangementLoading = !arrangement || (startFrom !== null && fresh.isFetching);
  const arrangementFailed = stored.isError || fresh.isError;

  function update(patch: Partial<Draft>) {
    if (!draft) return;
    const next = { ...draft, ...patch };
    const baseChanged = !!choice && !sameBase(choiceOf(next, libraryProvider), choice);
    if (arrangerEdited && baseChanged) setConfirm({ kind: "draft", next });
    else setEdited(next);
  }

  function startOver() {
    setArrangerEdited(false);
    setRestarts((n) => n + 1);
    // The same start over again reloads the same query.
    if (startFrom) void fresh.refetch();
  }

  function requestClose() {
    if (arrangerEdited) setConfirm({ kind: "close" });
    else onClose();
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
            {!custom && (
              <EpisodeOrderSeasonsView
                contentId={contentId}
                choice={choice!}
                structure={structure}
                files={draft.files}
                preview={preview.data}
                onReadFilesAs={(files) => update({ files })}
                onArrange={() => update({ layout: "custom" })}
              />
            )}
            {custom && arrangementLoading && (
              <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 p-10 text-sm">
                {arrangementFailed ? (
                  t("web.item.arrangeSeasons.failed")
                ) : (
                  <Loader2 className="size-5 animate-spin" aria-hidden="true" />
                )}
              </div>
            )}
            {arrangerOn && arrangement && !arrangementLoading && (
              <SeasonArranger
                // A start over or another base order starts from new data.
                key={`${restarts}:${JSON.stringify(startFrom)}`}
                contentId={contentId}
                title={title}
                data={arrangement}
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
              <Button disabled={!changed || save.isPending} onClick={apply}>
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
