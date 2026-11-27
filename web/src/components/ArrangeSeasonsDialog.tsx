// Admin season arranger for a series on the "Custom seasons" layout: add,
// rename, reorder and delete seasons, and move episodes between them by drag
// and drop or selection. Only Silo's catalog changes; files on disk are never
// touched.
import { useCallback, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { toast } from "sonner";
import {
  DndContext,
  DragOverlay,
  closestCenter,
  pointerWithin,
  type CollisionDetection,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import { SortableContext, arrayMove, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { EyeOff, GripVertical, History, Loader2, Plus, RotateCcw, X } from "lucide-react";
import { V2ProblemError } from "@/api/v2/request";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useCustomSeasons,
  useEpisodeOrder,
  useSaveCustomSeasons,
  type CustomSeasonEpisode,
  type CustomSeasons,
  type EpisodeOrderChoice,
} from "@/hooks/queries/episodeOrder";
import { useGripDragSensors, useMotionSafeOverlay } from "@/hooks/useSortableList";
import { useT } from "@/i18n";
import { joinList } from "@/lib/appLocale";
import { providerLabel, episodeOrderLabel } from "@/lib/episodeOrder";
import { seasonDisplayName } from "@/pages/ItemDetail/itemDetailLayout";

import {
  SPECIALS,
  arrangedOf,
  newId,
  withoutKeys,
  type ArrangedSeason,
  type ProviderSeason,
} from "@/lib/arrangeSeasons";
import { SeasonDetails } from "@/components/ArrangeSeasonDetails";
import {
  EpisodeRow,
  SeasonRow,
  SpecialsRow,
  VirtualEpisodeList,
} from "@/components/ArrangeSeasonsRows";

export default function ArrangeSeasonsDialog({
  contentId,
  title,
  open,
  onOpenChange,
  startFrom = null,
  activate = false,
}: {
  contentId: string;
  title: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Start from this order's seasons instead of the stored arrangement. */
  startFrom?: EpisodeOrderChoice | null;
  /** The series does not use its arrangement yet; saving switches it to it. */
  activate?: boolean;
}) {
  // Mounted only while open, so every opening starts from the saved seasons.
  if (!open) return null;
  return (
    <Loader
      contentId={contentId}
      title={title}
      startFrom={startFrom}
      activate={activate}
      onClose={() => onOpenChange(false)}
    />
  );
}

function Loader({
  contentId,
  title,
  startFrom,
  activate,
  onClose,
}: {
  contentId: string;
  title: string;
  startFrom: EpisodeOrderChoice | null;
  activate: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const [base, setBase] = useState<EpisodeOrderChoice | null>(startFrom);
  const query = useCustomSeasons(contentId, true, base);
  const data = query.data;
  if (!data) {
    return (
      <Dialog open onOpenChange={(next) => !next && onClose()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t("web.item.arrangeSeasons.title")}</DialogTitle>
            <DialogDescription>
              {query.isError ? t("web.item.arrangeSeasons.failed") : title}
            </DialogDescription>
          </DialogHeader>
          {!query.isError && (
            <div className="flex justify-center p-6">
              <Loader2 className="text-muted-foreground size-5 animate-spin" aria-hidden="true" />
            </div>
          )}
        </DialogContent>
      </Dialog>
    );
  }
  return (
    <Arranger
      // A start over loads new data; the arranger starts from it.
      key={JSON.stringify(base)}
      contentId={contentId}
      title={title}
      data={data}
      dirtyFromStart={base !== null || activate}
      onStartOver={setBase}
      onClose={onClose}
    />
  );
}

function Arranger({
  contentId,
  title,
  data,
  dirtyFromStart,
  onStartOver,
  onClose,
}: {
  contentId: string;
  title: string;
  data: CustomSeasons & { etag: string };
  dirtyFromStart: boolean;
  onStartOver: (base: EpisodeOrderChoice) => void;
  onClose: () => void;
}) {
  const t = useT();
  const initial = useMemo(() => arrangedOf(data), [data]);
  const episodes = useMemo(() => {
    const map = new Map<string, CustomSeasonEpisode>();
    for (const season of data.seasons) for (const e of season.episodes) map.set(e.key, e);
    return map;
  }, [data]);
  const providerSeasons = useMemo(() => {
    const map = new Map<number, ProviderSeason>();
    for (const s of data.seasons)
      map.set(s.number, {
        title: s.provider_title,
        overview: s.provider_overview,
        posterUrl: s.provider_poster_url,
      });
    return map;
  }, [data]);
  const orders = useEpisodeOrder(contentId, true, true).data;
  const save = useSaveCustomSeasons(contentId);
  const provider = providerLabel(data.structure_provider);

  const [seasons, setSeasons] = useState<ArrangedSeason[]>(initial.seasons);
  const [specials, setSpecials] = useState<string[]>(initial.specials);
  const [activeId, setActiveId] = useState<string>(initial.seasons[0]?.id ?? SPECIALS);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [editing, setEditing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<ArrangedSeason | null>(null);
  const [dragging, setDragging] = useState<string | null>(null);
  const [dirty, setDirty] = useState(dirtyFromStart || !data.saved);
  const [discarding, setDiscarding] = useState(false);
  const sensors = useGripDragSensors();
  const overlayMotion = useMotionSafeOverlay();
  // The episode list scrolls in the details pane on wide screens and with
  // the whole dialog body on narrow ones; the virtual list follows whichever.
  const bodyRef = useRef<HTMLDivElement>(null);
  const paneRef = useRef<HTMLElement>(null);
  const scrollElement = useCallback(() => {
    const pane = paneRef.current;
    return pane && getComputedStyle(pane).overflowY !== "visible" ? pane : bodyRef.current;
  }, []);

  const activeIndex = seasons.findIndex((s) => s.id === activeId);
  const activeSeason = seasons[activeIndex];
  const activeEpisodes = activeId === SPECIALS ? specials : (activeSeason?.episodeKeys ?? []);
  const touch = () => setDirty(true);

  const seasonName = (index: number) => {
    const season = seasons[index];
    const number = index + 1;
    const named = seasonDisplayName(number, season?.title || providerSeasons.get(number)?.title);
    const plain = seasonDisplayName(number);
    return named === plain
      ? plain
      : t("web.item.arrangeSeasons.seasonTitled", { number, title: named });
  };
  const nameOf = (id: string) =>
    id === SPECIALS ? seasonDisplayName(0) : seasonName(seasons.findIndex((s) => s.id === id));

  function setList(seasonId: string, keys: string[]) {
    if (seasonId === SPECIALS) setSpecials(keys);
    else setSeasons((all) => all.map((s) => (s.id === seasonId ? { ...s, episodeKeys: keys } : s)));
  }

  function moveEpisodes(keys: string[], target: string) {
    if (keys.length === 0) return;
    const moving = new Set(keys);
    setSeasons((all) =>
      all.map((s) => {
        const kept = withoutKeys(s.episodeKeys, moving);
        return { ...s, episodeKeys: s.id === target ? [...kept, ...keys] : kept };
      }),
    );
    setSpecials((sp) => {
      const kept = withoutKeys(sp, moving);
      return target === SPECIALS ? [...kept, ...keys] : kept;
    });
    setSelected(new Set());
    touch();
    toast(t("web.item.arrangeSeasons.moved", { count: keys.length, season: nameOf(target) }));
  }

  function addSeason(afterId?: string, keys: string[] = []) {
    const season: ArrangedSeason = { id: newId(), title: "", overview: "", episodeKeys: keys };
    setSeasons((all) => {
      const idx = afterId ? all.findIndex((s) => s.id === afterId) : all.length - 1;
      const next = [...all];
      next.splice(idx + 1, 0, season);
      return next;
    });
    setEditing(season.id);
    setActiveId(season.id);
    touch();
  }

  function splitAt(key: string) {
    if (activeId === SPECIALS || !activeSeason) return;
    const idx = activeSeason.episodeKeys.indexOf(key);
    if (idx <= 0) return;
    setList(activeId, activeSeason.episodeKeys.slice(0, idx));
    addSeason(activeId, activeSeason.episodeKeys.slice(idx));
  }

  function heirOf(season: ArrangedSeason) {
    const idx = seasons.findIndex((s) => s.id === season.id);
    return seasons[idx - 1] ?? seasons[idx + 1];
  }

  function deleteSeason(season: ArrangedSeason) {
    const idx = seasons.findIndex((s) => s.id === season.id);
    const heir = heirOf(season);
    setSeasons((all) =>
      all
        .filter((s) => s.id !== season.id)
        .map((s) =>
          heir && s.id === heir.id
            ? {
                ...s,
                episodeKeys:
                  idx > 0
                    ? [...s.episodeKeys, ...season.episodeKeys]
                    : [...season.episodeKeys, ...s.episodeKeys],
              }
            : s,
        ),
    );
    if (activeId === season.id) setActiveId(heir?.id ?? SPECIALS);
    touch();
  }

  function submit() {
    save.mutate(
      {
        etag: data.etag,
        body: {
          provider: data.choice.provider,
          order_id: data.choice.order_id,
          file_order: data.choice.file_order,
          seasons: [
            ...seasons.map((s, i) => ({
              number: i + 1,
              title: s.title,
              overview: s.overview,
              poster: s.poster?.ref,
              episodes: s.episodeKeys,
            })),
            { number: 0, title: "", overview: "", episodes: specials },
          ],
        },
      },
      {
        onSuccess: () => {
          toast.success(t("web.item.arrangeSeasons.saved", { count: seasons.length, title }), {
            description: t("web.item.arrangeSeasons.savedBody"),
          });
          onClose();
        },
        onError: (err) => {
          // Someone saved in between: close so the next opening starts from
          // their seasons instead of saving over them.
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

  function requestClose() {
    if (dirty) setDiscarding(true);
    else onClose();
  }

  // --- drag and drop -------------------------------------------------------
  const collision: CollisionDetection = (args) => {
    const pointer = pointerWithin(args);
    const seasonHit = pointer.find((c) => String(c.id).startsWith("season:"));
    if (seasonHit && String(args.active.id).startsWith("ep:")) return [seasonHit];
    return pointer.length > 0 ? pointer : closestCenter(args);
  };

  function onDragStart(e: DragStartEvent) {
    setDragging(String(e.active.id));
  }

  function onDragEnd(e: DragEndEvent) {
    setDragging(null);
    const a = String(e.active.id);
    const o = e.over ? String(e.over.id) : null;
    if (!o || a === o) return;
    if (a.startsWith("season:") && o.startsWith("season:")) {
      const from = seasons.findIndex((s) => `season:${s.id}` === a);
      const to = seasons.findIndex((s) => `season:${s.id}` === o);
      if (from >= 0 && to >= 0) {
        setSeasons(arrayMove(seasons, from, to));
        touch();
      }
      return;
    }
    if (a.startsWith("ep:")) {
      const key = a.slice(3);
      if (o.startsWith("season:")) {
        const target = o.slice(7);
        if (target === activeId) return;
        moveEpisodes(
          selected.has(key) ? activeEpisodes.filter((k) => selected.has(k)) : [key],
          target,
        );
        return;
      }
      if (o.startsWith("ep:")) {
        const from = activeEpisodes.indexOf(key);
        const to = activeEpisodes.indexOf(o.slice(3));
        if (from >= 0 && to >= 0) {
          setList(activeId, arrayMove(activeEpisodes, from, to));
          touch();
        }
      }
    }
  }

  function toggle(key: string, index: number, shift: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (shift && prev.size > 0) {
        const anchor = activeEpisodes.findIndex((k) => prev.has(k));
        const [a, b] = anchor < index ? [anchor, index] : [index, anchor];
        activeEpisodes.slice(a, b + 1).forEach((k) => next.add(k));
      } else if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function newSeasonFromSelection() {
    const keys = activeEpisodes.filter((k) => selected.has(k));
    const moving = new Set(keys);
    setSeasons((all) =>
      all.map((s) => ({ ...s, episodeKeys: withoutKeys(s.episodeKeys, moving) })),
    );
    setSpecials((sp) => withoutKeys(sp, moving));
    setSelected(new Set());
    addSeason(activeId === SPECIALS ? undefined : activeId, keys);
  }

  const draggedKey = dragging?.startsWith("ep:") ? dragging.slice(3) : undefined;
  const dragged = draggedKey ? episodes.get(draggedKey) : undefined;
  const dragCount = draggedKey && selected.has(draggedKey) ? selected.size : 1;
  const totalEpisodes = seasons.reduce((n, s) => n + s.episodeKeys.length, 0);
  const emptySeasons = seasons
    .map((s, i) => (s.episodeKeys.length === 0 ? i : -1))
    .filter((i) => i >= 0);
  const orderName = episodeOrderLabel(
    t,
    data.choice.provider || data.structure_provider,
    data.choice.order_id,
    orders?.order_name,
  );
  const summary = [
    t("web.item.arrangeSeasons.from", { order: orderName }),
    t("web.item.arrangeSeasons.seasonCount", { count: seasons.length }),
    t("web.item.arrangeSeasons.episodeCount", { count: totalEpisodes }),
  ].join(" · ");
  const startOverOptions =
    orders?.providers.flatMap((p) =>
      p.orders.map((o) => ({
        value: JSON.stringify({ provider: p.provider, order_id: o.is_default ? "" : o.id }),
        label: t("web.item.arrangeSeasons.startOverOption", {
          provider: providerLabel(p.provider),
          order: o.name,
        }),
      })),
    ) ?? [];

  return (
    <Dialog open onOpenChange={(next) => !next && requestClose()}>
      <DialogContent
        className="flex max-w-[min(1760px,calc(100vw-3rem))] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(1760px,calc(100vw-3rem))] md:h-[min(calc(100dvh-3rem),1080px)]"
        showCloseButton
      >
        <DialogHeader className="border-border/10 shrink-0 flex-col gap-3 border-b px-5 py-4 pe-12 text-start sm:flex-row sm:items-center">
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
              <DialogTitle className="text-[0.9375rem] font-semibold">
                {t("web.item.arrangeSeasons.title")}
              </DialogTitle>
              <span className="bg-muted/50 text-muted-foreground rounded px-2 py-0.5 text-[0.6875rem]">
                {title}
              </span>
            </div>
            <DialogDescription className="text-muted-foreground text-xs">
              {summary}
            </DialogDescription>
          </div>
          <div className="shrink-0">
            <Select
              value=""
              onValueChange={(value) => {
                const picked = JSON.parse(value) as { provider: string; order_id: string };
                // Files keep the naming they have: an empty file order meant
                // the provider being replaced.
                const files = data.choice.file_order || data.structure_provider;
                onStartOver({
                  provider: picked.provider,
                  order_id: picked.order_id,
                  file_order: files === picked.provider ? "" : files,
                  layout: "custom",
                });
              }}
            >
              <SelectTrigger className="w-full sm:w-auto sm:min-w-[240px]">
                <RotateCcw className="size-3.5" aria-hidden="true" />
                <SelectValue placeholder={t("web.item.arrangeSeasons.startOver")} />
              </SelectTrigger>
              {/* Below the trigger and as wide as the longest order name, so a
                long TMDB episode group name is not cut off. */}
              <SelectContent
                position="popper"
                align="end"
                className="w-max max-w-[min(36rem,calc(100vw-2rem))]"
              >
                {startOverOptions.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </DialogHeader>

        <DndContext
          sensors={sensors}
          collisionDetection={collision}
          onDragStart={onDragStart}
          onDragEnd={onDragEnd}
        >
          <div
            ref={bodyRef}
            className="flex max-h-[70vh] min-h-0 flex-1 flex-col overflow-y-auto md:max-h-none md:flex-row md:overflow-hidden"
          >
            <aside className="border-border/10 min-h-0 shrink-0 p-3 md:w-[420px] md:overflow-y-auto md:border-e">
              <div className="flex items-center justify-between px-1.5 pb-2">
                <h2 className="text-sm font-semibold">{t("web.item.arrangeSeasons.seasons")}</h2>
                <span className="text-muted-foreground text-xs">
                  {t("web.item.arrangeSeasons.seasonsHint")}
                </span>
              </div>
              <SortableContext
                items={seasons.map((s) => `season:${s.id}`)}
                strategy={verticalListSortingStrategy}
              >
                <ul className="space-y-1">
                  {seasons.map((s, i) => (
                    <SeasonRow
                      key={s.id}
                      season={s}
                      name={seasonName(i)}
                      posterUrl={s.poster?.url ?? providerSeasons.get(i + 1)?.posterUrl}
                      providerTitle={providerSeasons.get(i + 1)?.title}
                      active={activeId === s.id}
                      editing={editing === s.id}
                      dropTarget={!!dragging?.startsWith("ep:") && activeId !== s.id}
                      canDelete={seasons.length > 1}
                      onSelect={() => {
                        setActiveId(s.id);
                        setSelected(new Set());
                      }}
                      onRename={(value) => {
                        setSeasons((all) =>
                          all.map((x) => (x.id === s.id ? { ...x, title: value } : x)),
                        );
                        touch();
                      }}
                      onEdit={(on) => setEditing(on ? s.id : null)}
                      onDelete={() =>
                        s.episodeKeys.length === 0 ? deleteSeason(s) : setDeleting(s)
                      }
                    />
                  ))}
                </ul>
              </SortableContext>
              <button
                type="button"
                onClick={() => addSeason()}
                className="text-muted-foreground hover:text-foreground hover:bg-accent/60 mt-1 flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-sm"
              >
                <Plus className="size-4" aria-hidden="true" />
                {t("web.item.arrangeSeasons.addSeason")}
              </button>
              <div className="border-border/60 mt-2 border-t pt-2">
                <SpecialsRow
                  count={specials.length}
                  active={activeId === SPECIALS}
                  dropTarget={!!dragging?.startsWith("ep:") && activeId !== SPECIALS}
                  onSelect={() => {
                    setActiveId(SPECIALS);
                    setSelected(new Set());
                  }}
                />
              </div>
              {emptySeasons.length > 0 && (
                <div
                  role="status"
                  className="mt-3 flex items-start gap-2.5 rounded-xl border border-amber-500/30 bg-amber-500/10 px-3 py-2.5"
                >
                  <span className="grid size-6 shrink-0 place-items-center rounded-md bg-amber-500/15 text-amber-600 dark:text-amber-400">
                    <EyeOff className="size-3.5" aria-hidden="true" />
                  </span>
                  <p className="text-muted-foreground min-w-0 text-xs leading-relaxed">
                    {t("web.item.arrangeSeasons.empty", {
                      count: emptySeasons.length,
                      names: joinList(emptySeasons.map((i) => seasonDisplayName(i + 1))),
                    })}
                  </p>
                </div>
              )}
            </aside>

            <section ref={paneRef} className="min-h-0 min-w-0 flex-1 p-5 md:overflow-y-auto">
              <div className="mb-3 flex flex-wrap items-center gap-3">
                <h2 className="text-xl font-semibold tracking-tight">{nameOf(activeId)}</h2>
                <span className="text-muted-foreground text-sm">
                  {t("web.item.arrangeSeasons.episodeCount", { count: activeEpisodes.length })}
                </span>
              </div>

              {activeSeason && (
                <SeasonDetails
                  key={activeSeason.id}
                  season={activeSeason}
                  number={activeIndex + 1}
                  provider={provider}
                  inherited={providerSeasons.get(activeIndex + 1)}
                  contentId={contentId}
                  onPoster={(poster) => {
                    setSeasons((all) =>
                      all.map((x) => (x.id === activeSeason.id ? { ...x, poster } : x)),
                    );
                    touch();
                  }}
                  onTitle={(value) => {
                    if (value === activeSeason.title) return;
                    setSeasons((all) =>
                      all.map((x) => (x.id === activeSeason.id ? { ...x, title: value } : x)),
                    );
                    touch();
                  }}
                  onOverview={(value) => {
                    if (value === activeSeason.overview) return;
                    setSeasons((all) =>
                      all.map((x) => (x.id === activeSeason.id ? { ...x, overview: value } : x)),
                    );
                    touch();
                  }}
                />
              )}

              {selected.size > 0 && (
                <div className="bg-surface border-border sticky top-0 z-20 mb-3 flex flex-wrap items-center gap-2 rounded-xl border px-3 py-2 shadow-lg">
                  <span className="text-sm font-medium">
                    {t("web.item.arrangeSeasons.selected", { count: selected.size })}
                  </span>
                  <Select
                    onValueChange={(target) =>
                      target === "__new"
                        ? newSeasonFromSelection()
                        : moveEpisodes(
                            activeEpisodes.filter((k) => selected.has(k)),
                            target,
                          )
                    }
                  >
                    <SelectTrigger className="h-8 w-[190px]">
                      <SelectValue placeholder={t("web.item.arrangeSeasons.moveTo")} />
                    </SelectTrigger>
                    <SelectContent>
                      {seasons
                        .filter((s) => s.id !== activeId)
                        .map((s) => (
                          <SelectItem key={s.id} value={s.id}>
                            {nameOf(s.id)}
                          </SelectItem>
                        ))}
                      {activeId !== SPECIALS && (
                        <SelectItem value={SPECIALS}>{seasonDisplayName(0)}</SelectItem>
                      )}
                      <SelectItem value="__new">
                        {t("web.item.arrangeSeasons.newSeason")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>
                    <X className="size-3.5" aria-hidden="true" />
                    {t("web.item.arrangeSeasons.clear")}
                  </Button>
                  <span className="text-muted-foreground ms-auto text-xs">
                    {t("web.item.arrangeSeasons.dragTip")}
                  </span>
                </div>
              )}

              {activeEpisodes.length === 0 ? (
                <div className="border-border text-muted-foreground rounded-xl border border-dashed p-10 text-center text-sm">
                  {t("web.item.arrangeSeasons.noEpisodes")}
                </div>
              ) : (
                <SortableContext
                  items={activeEpisodes.map((k) => `ep:${k}`)}
                  strategy={verticalListSortingStrategy}
                >
                  <VirtualEpisodeList
                    // Each season scrolls from its top.
                    key={activeId}
                    keys={activeEpisodes}
                    scrollElement={scrollElement}
                    renderRow={(key, i) => {
                      const episode = episodes.get(key);
                      return episode ? (
                        <EpisodeRow
                          episode={episode}
                          provider={provider}
                          number={i + 1}
                          selected={selected.has(key)}
                          onToggle={(shift) => toggle(key, i, shift)}
                          onSplit={activeId !== SPECIALS && i > 0 ? () => splitAt(key) : undefined}
                        />
                      ) : null;
                    }}
                  />
                </SortableContext>
              )}
            </section>
          </div>
          {/* The dialog is transformed, which would offset a fixed overlay
              inside it; render the drag preview on the body instead. */}
          {createPortal(
            <DragOverlay zIndex={60} {...overlayMotion}>
              {dragged ? (
                <div className="bg-surface border-border inline-flex max-w-xs items-center gap-2 rounded-lg border px-3 py-2 text-sm shadow-2xl">
                  <GripVertical className="text-muted-foreground size-4" aria-hidden="true" />
                  {dragCount > 1
                    ? t("web.item.arrangeSeasons.dragMany", { count: dragCount })
                    : dragged.title}
                </div>
              ) : null}
            </DragOverlay>,
            document.body,
          )}
        </DndContext>

        <div className="border-border/10 flex shrink-0 flex-col gap-3 border-t px-5 py-3.5 sm:flex-row sm:items-center">
          <div className="text-muted-foreground flex min-w-0 flex-1 items-start gap-2 text-xs leading-relaxed">
            <History className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
            <span>{t("web.item.arrangeSeasons.footer")}</span>
          </div>
          <div className="flex shrink-0 gap-2">
            <Button variant="ghost" onClick={requestClose}>
              {t("common.cancel")}
            </Button>
            <Button onClick={submit} disabled={!dirty || save.isPending}>
              {save.isPending && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
              {t("web.item.arrangeSeasons.save")}
            </Button>
          </div>
        </div>

        <ConfirmDialog
          open={deleting !== null}
          onOpenChange={(next) => !next && setDeleting(null)}
          title={t("web.item.arrangeSeasons.deleteTitle", {
            season: deleting ? nameOf(deleting.id) : "",
          })}
          description={
            deleting
              ? t("web.item.arrangeSeasons.deleteBody", {
                  count: deleting.episodeKeys.length,
                  heir: heirOf(deleting) ? nameOf(heirOf(deleting)!.id) : seasonDisplayName(0),
                })
              : ""
          }
          confirmLabel={t("web.item.arrangeSeasons.deleteConfirm")}
          variant="destructive"
          onConfirm={() => {
            if (deleting) deleteSeason(deleting);
            setDeleting(null);
          }}
        />
        <ConfirmDialog
          open={discarding}
          onOpenChange={setDiscarding}
          title={t("web.item.arrangeSeasons.discardTitle")}
          description={t("web.item.arrangeSeasons.discardBody")}
          confirmLabel={t("web.item.arrangeSeasons.discard")}
          cancelLabel={t("web.item.arrangeSeasons.keepEditing")}
          variant="destructive"
          onConfirm={() => {
            setDiscarding(false);
            onClose();
          }}
        />
      </DialogContent>
    </Dialog>
  );
}
