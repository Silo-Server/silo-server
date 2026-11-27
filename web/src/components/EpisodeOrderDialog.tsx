// Per-series episode order: which provider and which of its orders number the
// show, how its seasons are laid out, and which order its file names follow.
// Opened from the series page's Manage menu (admins and metadata curators).
import { useMemo, useState, type ReactNode } from "react";
import { toast } from "sonner";
import {
  FileSearch,
  FileVideo,
  History,
  Info,
  Loader2,
  Sparkles,
  TriangleAlert,
} from "lucide-react";
import { V2ProblemError } from "@/api/v2/request";
import { ChoiceOption } from "@/components/ChoiceOption";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useEpisodeOrder,
  useEpisodeOrderPreview,
  useSaveEpisodeOrder,
  type EpisodeOrderChoice,
  type EpisodeOrderOption,
  type EpisodeOrderPreview,
  type EpisodeOrderState,
} from "@/hooks/queries/episodeOrder";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { joinList } from "@/lib/appLocale";
import { asProvider, fileOrderLabel, providerLabel, type Provider } from "@/lib/episodeOrder";
import { formatEpisodeCode } from "@/lib/episodeCardLabels";
import { seasonDisplayName } from "@/pages/ItemDetail/itemDetailLayout";
import { cn } from "@/lib/utils";

type Source = "library" | Provider;
type Layout = "seasons" | "absolute" | "custom";

interface Draft {
  source: Source;
  /** An order of the source provider; empty is its default order. */
  orderId: string;
  /** The provider whose default order the file names follow. */
  files: Provider;
  layout: Layout;
}

const otherProvider = (p: Provider): Provider => (p === "tvdb" ? "tmdb" : "tvdb");

const KIND_KEY: Record<string, MessageKey> = {
  aired: "web.item.episodeOrder.kind.aired",
  dvd: "web.item.episodeOrder.kind.dvd",
  absolute: "web.item.episodeOrder.kind.absolute",
  digital: "web.item.episodeOrder.kind.digital",
  story_arc: "web.item.episodeOrder.kind.storyArc",
  production: "web.item.episodeOrder.kind.production",
  tv: "web.item.episodeOrder.kind.tv",
};

function kindLabel(t: Translate, kind: string) {
  return t(KIND_KEY[kind] ?? "web.item.episodeOrder.kind.alternate");
}

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

export default function EpisodeOrderDialog({
  contentId,
  title,
  open,
  onOpenChange,
  onArrangeSeasons,
}: {
  contentId: string;
  title: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /**
   * Opens the season arranger: from the stored arrangement (null), or
   * starting over from the order the dialog shows.
   */
  onArrangeSeasons: (startFrom: EpisodeOrderChoice | null) => void;
}) {
  const t = useT();
  const stateQuery = useEpisodeOrder(contentId, open, true);
  const state = stateQuery.data;
  // The draft starts from the saved choice each time the dialog opens; a
  // cancelled draft is dropped on close.
  const [edited, setEdited] = useState<Draft | null>(null);
  const draft = edited ?? (state ? draftOf(state) : null);
  const setDraft = (update: (d: Draft | null) => Draft | null) =>
    setEdited((d) => update(d ?? draft));
  const changeOpen = (next: boolean) => {
    if (!next) setEdited(null);
    onOpenChange(next);
  };

  const libraryProvider = asProvider(state?.library_provider);
  const choice = draft ? choiceOf(draft, libraryProvider) : undefined;
  const preview = useEpisodeOrderPreview(contentId, choice, open);
  const save = useSaveEpisodeOrder(contentId);

  const savedChoice = state ? choiceOf(draftOf(state), libraryProvider) : undefined;
  const changed = !!choice && JSON.stringify(choice) !== JSON.stringify(savedChoice);

  function apply() {
    if (!choice || !state) return;
    save.mutate(
      { etag: state.etag, body: choice },
      {
        onSuccess: () => {
          changeOpen(false);
          toast.success(t("web.item.episodeOrder.applied", { title }), {
            description: t("web.item.episodeOrder.appliedBody"),
          });
        },
        onError: (err) => {
          // Someone saved in between: close so the next opening starts from
          // their choice instead of saving over it.
          if (err instanceof V2ProblemError && err.status === 412) {
            toast.error(t("web.item.episodeOrder.conflict"));
            changeOpen(false);
            return;
          }
          toast.error(t("web.item.episodeOrder.saveFailed"));
        },
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        className="flex max-w-6xl flex-col gap-0 overflow-hidden p-0 sm:max-w-6xl md:h-[min(88vh,820px)]"
        showCloseButton
      >
        <DialogHeader className="border-border/10 shrink-0 flex-row flex-wrap items-center gap-x-2.5 gap-y-1.5 border-b px-5 py-4 pe-12">
          <DialogTitle className="text-[0.9375rem] font-semibold">
            {t("web.item.episodeOrder.title")}
          </DialogTitle>
          <span className="bg-muted/50 text-muted-foreground rounded px-2 py-0.5 text-[0.6875rem]">
            {title}
          </span>
          <DialogDescription className="sr-only">
            {t("web.item.episodeOrder.description")}
          </DialogDescription>
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
          <div className="flex max-h-[70vh] min-h-0 flex-1 flex-col overflow-y-auto md:max-h-none md:flex-row md:overflow-hidden">
            <Settings state={state} draft={draft} setDraft={setDraft} preview={preview.data} />
            <PreviewPane
              title={title}
              draft={draft}
              setDraft={setDraft}
              libraryProvider={libraryProvider}
              preview={preview.data}
              loading={preview.isFetching}
              failed={preview.isError}
            />
          </div>
        )}

        <div className="border-border/10 flex shrink-0 flex-col gap-3 border-t px-5 py-3.5 sm:flex-row sm:items-center">
          <div className="text-muted-foreground flex min-w-0 flex-1 items-start gap-2 text-xs leading-relaxed">
            <History className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
            <span>{t("web.item.episodeOrder.footer")}</span>
          </div>
          <div className="flex shrink-0 gap-2">
            <Button variant="ghost" onClick={() => changeOpen(false)}>
              {t("common.cancel")}
            </Button>
            {draft?.layout === "custom" ? (
              <Button
                onClick={() => {
                  if (!choice || !savedChoice) return;
                  // A stored arrangement (kept while another layout is in
                  // use) opens for the same base order; another order starts
                  // over from its seasons.
                  const sameBase =
                    savedChoice.provider === choice.provider &&
                    savedChoice.order_id === choice.order_id &&
                    savedChoice.file_order === choice.file_order;
                  changeOpen(false);
                  onArrangeSeasons(sameBase ? null : choice);
                }}
              >
                {t("web.item.episodeOrder.arrange")}
              </Button>
            ) : (
              <Button disabled={!changed || save.isPending} onClick={apply}>
                {save.isPending && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
                {t("web.item.episodeOrder.apply")}
              </Button>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** The file order that places the most files, when it beats the chosen one. */
function suggestedFiles(
  preview: EpisodeOrderPreview | undefined,
  draft: Draft,
  structure: Provider,
): Provider | undefined {
  if (!preview || preview.file_count === 0) return undefined;
  let best: { provider: Provider; matched: number } | undefined;
  for (const fit of preview.file_order_fits) {
    const provider = fit.file_order ? asProvider(fit.file_order) : structure;
    if (!best || fit.matched_files > best.matched) best = { provider, matched: fit.matched_files };
  }
  if (!best || best.provider === draft.files || best.matched <= preview.matched_files)
    return undefined;
  return best.provider;
}

function filesPlacedBy(
  preview: EpisodeOrderPreview | undefined,
  provider: Provider,
  structure: Provider,
) {
  return preview?.file_order_fits.find(
    (fit) => (fit.file_order ? asProvider(fit.file_order) : structure) === provider,
  )?.matched_files;
}

function Settings({
  state,
  draft,
  setDraft,
  preview,
}: {
  state: EpisodeOrderState;
  draft: Draft;
  setDraft: (update: (d: Draft | null) => Draft | null) => void;
  preview: EpisodeOrderPreview | undefined;
}) {
  const t = useT();
  const libraryProvider = asProvider(state.library_provider);
  const structure = structureOf(draft, libraryProvider);
  const providerEntry = state.providers.find((p) => p.provider === structure);
  const orders = providerEntry?.orders ?? [];
  const defaultOrder = orders.find((o) => o.is_default);
  const selectedOrder =
    draft.source === "library"
      ? defaultOrder
      : (orders.find((o) => o.id === draft.orderId) ??
        (draft.orderId === "" ? defaultOrder : undefined));
  const suggestion = suggestedFiles(preview, draft, structure);
  const update = (patch: Partial<Draft>) => setDraft((d) => (d ? { ...d, ...patch } : d));

  return (
    <div className="border-border/10 min-h-0 shrink-0 px-5 py-5 md:w-[380px] md:overflow-y-auto md:border-e">
      <section className="space-y-2">
        <Label className="text-muted-foreground text-[0.75rem] font-medium">
          {t("web.item.episodeOrder.source.label")}
        </Label>
        <div role="radiogroup" className="space-y-2">
          <ChoiceOption
            name="episode-order-source"
            value="library"
            selected={draft.source === "library"}
            disabled={false}
            title={t("web.item.episodeOrder.source.library", {
              provider: providerLabel(libraryProvider),
            })}
            onSelect={() => update({ source: "library", orderId: "" })}
          >
            {t("web.item.episodeOrder.source.libraryBody", {
              provider: providerLabel(libraryProvider),
              other: providerLabel(otherProvider(libraryProvider)),
            })}
          </ChoiceOption>
          {(["tvdb", "tmdb"] as Provider[]).map((p) => (
            <ChoiceOption
              key={p}
              name="episode-order-source"
              value={p}
              selected={draft.source === p}
              disabled={!state.providers.some((entry) => entry.provider === p)}
              title={providerLabel(p)}
              onSelect={() =>
                update({
                  source: p,
                  orderId: "",
                  // Files keep the naming they had unless it followed the
                  // provider being replaced.
                  files: draft.files === structure ? p : draft.files,
                })
              }
            >
              {t(
                p === "tvdb"
                  ? "web.item.episodeOrder.source.tvdbBody"
                  : "web.item.episodeOrder.source.tmdbBody",
              )}
            </ChoiceOption>
          ))}
        </div>
      </section>

      <section className="mt-5 space-y-1.5">
        <Label className="text-muted-foreground text-[0.75rem] font-medium">
          {t("web.item.episodeOrder.order.label")}
        </Label>
        <Select
          value={selectedOrder?.id ?? ""}
          disabled={draft.source === "library" || orders.length === 0}
          onValueChange={(id) =>
            update({ orderId: orders.find((o) => o.id === id)?.is_default ? "" : id })
          }
        >
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent className="max-h-80">
            {orders.map((o) => (
              <SelectItem key={o.id} value={o.id}>
                <OrderOptionLabel order={o} />
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {providerEntry?.failed
            ? t("web.item.episodeOrder.order.failed")
            : providerEntry && !providerEntry.supported
              ? t("web.item.episodeOrder.order.unsupported")
              : t("web.item.episodeOrder.order.help")}
        </p>
        {draft.source === "library" && (
          <p className="text-muted-foreground text-xs leading-relaxed">
            {t("web.item.episodeOrder.order.helpLibrary")}
          </p>
        )}
      </section>

      <section className="mt-5 space-y-1.5">
        <Label
          id="episode-order-files"
          className="text-muted-foreground text-[0.75rem] font-medium"
        >
          {t("web.item.episodeOrder.files.label")}
        </Label>
        <Select value={draft.files} onValueChange={(v) => update({ files: asProvider(v) })}>
          <SelectTrigger className="w-full" aria-labelledby="episode-order-files">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(["tvdb", "tmdb"] as Provider[]).map((p) => {
              const placed = filesPlacedBy(preview, p, structure);
              return (
                <SelectItem key={p} value={p}>
                  <span className="flex items-center gap-2">
                    <span>{fileOrderLabel(t, p)}</span>
                    {preview && placed !== undefined && (
                      <span className="text-muted-foreground text-xs">
                        {placed === preview.file_count
                          ? t("web.item.episodeOrder.files.fitsAll")
                          : t("web.item.episodeOrder.files.fit", {
                              matched: placed,
                              total: preview.file_count,
                            })}
                      </span>
                    )}
                  </span>
                </SelectItem>
              );
            })}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {t("web.item.episodeOrder.files.help")}
        </p>
        {suggestion && (
          <div className="border-border bg-surface flex items-center gap-2.5 rounded-xl border px-3 py-2">
            <Sparkles className="text-primary size-3.5 shrink-0" aria-hidden="true" />
            <span className="min-w-0 flex-1 text-xs leading-snug">
              {t("web.item.episodeOrder.files.suggestion", {
                order: fileOrderLabel(t, suggestion),
              })}
            </span>
            <Button
              variant="outline"
              size="xs"
              className="shrink-0"
              onClick={() => update({ files: suggestion })}
            >
              {t("web.item.episodeOrder.files.useSuggestion")}
            </Button>
          </div>
        )}
      </section>

      <section className="mt-5 space-y-2">
        <Label className="text-muted-foreground text-[0.75rem] font-medium">
          {t("web.item.episodeOrder.layout.label")}
        </Label>
        <div role="radiogroup" className="space-y-2">
          <ChoiceOption
            name="episode-order-layout"
            value="seasons"
            selected={draft.layout === "seasons"}
            disabled={false}
            title={t("web.item.episodeOrder.layout.seasons")}
            onSelect={() => update({ layout: "seasons" })}
          >
            {t("web.item.episodeOrder.layout.seasonsBody", { provider: providerLabel(structure) })}
          </ChoiceOption>
          <ChoiceOption
            name="episode-order-layout"
            value="absolute"
            selected={draft.layout === "absolute"}
            disabled={false}
            title={t("web.item.episodeOrder.layout.absolute")}
            onSelect={() => update({ layout: "absolute" })}
          >
            {t("web.item.episodeOrder.layout.absoluteBody")}
          </ChoiceOption>
          <ChoiceOption
            name="episode-order-layout"
            value="custom"
            selected={draft.layout === "custom"}
            disabled={false}
            title={t("web.item.episodeOrder.layout.custom")}
            onSelect={() => update({ layout: "custom" })}
          >
            {t("web.item.episodeOrder.layout.customBody")}
          </ChoiceOption>
        </div>
      </section>
    </div>
  );
}

function OrderOptionLabel({ order }: { order: EpisodeOrderOption }) {
  const t = useT();
  const meta = [kindLabel(t, order.kind)];
  if (order.season_count > 0)
    meta.push(t("web.item.episodeOrder.order.seasons", { count: order.season_count }));
  if (order.episode_count > 0)
    meta.push(t("web.item.episodeOrder.order.episodes", { count: order.episode_count }));
  return (
    <span className="flex items-center gap-2">
      <span>{order.name}</span>
      <span className="text-muted-foreground text-xs">{meta.join(" · ")}</span>
    </span>
  );
}

function PreviewPane({
  title,
  draft,
  setDraft,
  libraryProvider,
  preview,
  loading,
  failed,
}: {
  title: string;
  draft: Draft;
  setDraft: (update: (d: Draft | null) => Draft | null) => void;
  libraryProvider: Provider;
  preview: EpisodeOrderPreview | undefined;
  loading: boolean;
  failed: boolean;
}) {
  const t = useT();
  const [seasonNumber, setSeasonNumber] = useState<number | null>(null);
  const structure = structureOf(draft, libraryProvider);
  const seasons = preview?.seasons ?? [];
  const shown =
    seasons.find((s) => s.number === seasonNumber) ??
    seasons.find((s) => s.file_count > 0) ??
    seasons[0];
  const regularSeasons = seasons.filter((s) => s.number !== 0).length;
  const suggestion = suggestedFiles(preview, draft, structure);
  const unmatched = useMemo(() => {
    const files = preview?.unmatched_files ?? [];
    const missing = (preview?.file_count ?? 0) - (preview?.matched_files ?? 0);
    const names = joinList(files.slice(0, 3));
    return missing > 3
      ? t("web.item.episodeOrder.preview.moreFiles", { names, count: missing - 3 })
      : names;
  }, [preview, t]);
  const missingFiles = (preview?.file_count ?? 0) - (preview?.matched_files ?? 0);
  return (
    <div className="flex min-w-0 flex-col bg-black/10 max-md:min-h-[560px] md:min-h-0 md:flex-1">
      <div className="shrink-0 px-5 pt-4 pb-3">
        <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <h3 className="flex items-center gap-2 text-sm font-semibold">
            {t("web.item.episodeOrder.preview.title")}
            {loading && (
              <Loader2 className="text-muted-foreground size-3.5 animate-spin" aria-hidden="true" />
            )}
          </h3>
          {preview && (
            <div className="text-muted-foreground flex gap-4 text-xs">
              <span>{t("web.item.episodeOrder.preview.seasons", { count: regularSeasons })}</span>
              <span>
                {t("web.item.episodeOrder.preview.episodes", { count: preview.episode_count })}
              </span>
              {preview.file_count > 0 && (
                <span
                  className={cn(
                    "font-medium",
                    missingFiles === 0 ? "text-green-500" : "text-amber-500",
                  )}
                >
                  {t("web.item.episodeOrder.preview.files", {
                    matched: preview.matched_files,
                    total: preview.file_count,
                  })}
                </span>
              )}
            </div>
          )}
        </div>
        <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
          {t("web.item.episodeOrder.preview.help", { title })}
        </p>
      </div>

      {!preview ? (
        <div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 px-5 pb-6 text-sm">
          {failed ? (
            t("web.item.episodeOrder.preview.failed")
          ) : (
            <>
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
              {t("web.item.episodeOrder.preview.loading")}
            </>
          )}
        </div>
      ) : preview.structure_provider === "" ? (
        <Notice tone="info" title={t("web.item.episodeOrder.preview.empty")} />
      ) : (
        <>
          {preview.structure_provider !== structure && (
            <Notice
              tone="info"
              title={t("web.item.episodeOrder.preview.fallback", {
                provider: providerLabel(structure),
                other: providerLabel(asProvider(preview.structure_provider)),
              })}
            />
          )}
          {missingFiles > 0 && (
            <Notice
              tone="warn"
              title={t("web.item.episodeOrder.preview.unmatchedTitle", { count: missingFiles })}
              action={
                suggestion
                  ? {
                      label: t("web.item.episodeOrder.preview.readAs", {
                        order: fileOrderLabel(t, suggestion),
                      }),
                      onClick: () => setDraft((d) => (d ? { ...d, files: suggestion } : d)),
                    }
                  : undefined
              }
            >
              {t("web.item.episodeOrder.preview.unmatchedBody", {
                files: unmatched,
                count: missingFiles,
              })}
            </Notice>
          )}

          <div className="flex shrink-0 items-center gap-2 px-5 pb-3">
            <Select
              value={shown ? String(shown.number) : undefined}
              onValueChange={(v) => setSeasonNumber(Number(v))}
            >
              <SelectTrigger
                className="min-w-0 flex-1"
                aria-label={t("web.item.episodeOrder.preview.seasonPicker")}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent className="max-h-80">
                {seasons.map((s) => {
                  // The number, then the season's own title when it has one.
                  const name = seasonDisplayName(s.number);
                  const titled = seasonDisplayName(s.number, s.title);
                  const label = titled === name ? name : [name, titled].join(" · ");
                  const meta = [
                    t("web.item.episodeOrder.preview.episodes", { count: s.episode_count }),
                  ];
                  if (s.file_count > 0)
                    meta.push(t("web.item.episodeOrder.preview.onDisk", { count: s.file_count }));
                  return (
                    <SelectItem key={s.number} value={String(s.number)}>
                      <span className="flex min-w-0 items-baseline gap-2">
                        <span className="truncate font-medium">{label}</span>
                        <span className="text-muted-foreground shrink-0 text-xs">
                          {meta.join(" · ")}
                        </span>
                      </span>
                    </SelectItem>
                  );
                })}
              </SelectContent>
            </Select>
          </div>

          <div className="border-border/10 mx-5 mb-4 flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border">
            <div className="text-muted-foreground border-border/10 bg-background/40 flex shrink-0 items-center gap-3 border-b px-3 py-2 text-[0.6875rem] font-medium tracking-wide uppercase">
              <span className="w-8 shrink-0 text-end">
                {t("web.item.episodeOrder.preview.colNumber")}
              </span>
              <span className="min-w-0 flex-1">
                {t("web.item.episodeOrder.preview.colEpisode")}
              </span>
              <span className="hidden w-[38%] shrink-0 sm:block">
                {t("web.item.episodeOrder.preview.colFile")}
              </span>
              <span className="w-[72px] shrink-0 text-end">
                {t("web.item.episodeOrder.preview.colFileNumber")}
              </span>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto py-1">
              {shown?.episodes.slice(0, 200).map((e) => {
                const fileNumber =
                  e.file_episode > 0 ? formatEpisodeCode(e.file_season, e.file_episode) : "–";
                return (
                  <div
                    key={`${shown.number}-${e.number}`}
                    className="hover:bg-accent/40 flex items-center gap-3 px-3 py-1.5 text-[0.8125rem]"
                  >
                    <span className="text-muted-foreground w-8 shrink-0 text-end font-mono text-xs tabular-nums">
                      {e.number}
                    </span>
                    <span
                      className={cn("min-w-0 flex-1 truncate", !e.file && "text-muted-foreground")}
                    >
                      {e.title}
                    </span>
                    <span className="hidden w-[38%] shrink-0 sm:block">
                      {e.file ? (
                        <span className="text-muted-foreground flex min-w-0 items-center gap-1.5 font-mono text-[0.6875rem]">
                          <FileVideo
                            className="size-3 shrink-0 text-green-500/80"
                            aria-hidden="true"
                          />
                          <span className="truncate">{e.file}</span>
                        </span>
                      ) : (
                        <span className="text-muted-foreground/60 text-[0.6875rem]">
                          {t("web.item.episodeOrder.preview.notOnDisk")}
                        </span>
                      )}
                    </span>
                    <span className="text-muted-foreground/70 w-[72px] shrink-0 text-end font-mono text-[0.6875rem]">
                      {fileNumber}
                    </span>
                  </div>
                );
              })}
              {shown && shown.episodes.length > 200 && (
                <div className="text-muted-foreground px-3 py-2 text-xs">
                  {t("web.item.episodeOrder.preview.moreEpisodes", {
                    count: shown.episodes.length - 200,
                  })}
                </div>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

// Matches the admin RestartBanner: icon tile, title and explanation, and the
// action as a button on the end (wrapping under the text when narrow).
function Notice({
  title,
  tone,
  action,
  children,
}: {
  title: string;
  tone: "warn" | "info";
  action?: { label: string; onClick: () => void };
  children?: ReactNode;
}) {
  const Icon = tone === "warn" ? TriangleAlert : Info;
  return (
    <div
      role="status"
      className={cn(
        "mx-5 mb-3 flex flex-wrap items-center gap-3 rounded-2xl border px-4 py-3",
        tone === "warn"
          ? "border-amber-500/30 bg-[linear-gradient(calc(90deg*var(--dir-sign)),color-mix(in_srgb,var(--warning)_16%,transparent),color-mix(in_srgb,var(--warning)_6%,transparent))]"
          : "border-border bg-surface",
      )}
    >
      <span
        className={cn(
          "grid size-7 shrink-0 place-items-center self-start rounded-lg",
          tone === "warn"
            ? "bg-amber-500/15 text-amber-600 dark:text-amber-400"
            : "bg-muted text-muted-foreground",
        )}
      >
        <Icon className="size-3.5" aria-hidden="true" />
      </span>
      <div className="min-w-0 flex-1 basis-64">
        <p className="text-[0.8125rem] font-medium">{title}</p>
        {children && (
          <p className="text-muted-foreground text-[0.71875rem] leading-relaxed">{children}</p>
        )}
      </div>
      {action && (
        <Button size="sm" className="shrink-0" onClick={action.onClick}>
          <FileSearch className="size-3.5" aria-hidden="true" />
          {action.label}
        </Button>
      )}
    </div>
  );
}
