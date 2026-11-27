// Admin season arranger for a series on the "Custom seasons" layout: add,
// rename, reorder and delete seasons, and move episodes between them by drag
// and drop or selection. Only Silo's catalog changes; files on disk are never
// touched.
import {
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
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
import {
  SortableContext,
  arrayMove,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  EyeOff,
  FileVideo,
  GripVertical,
  History,
  ImagePlus,
  Info,
  Loader2,
  Pencil,
  Plus,
  RotateCcw,
  Scissors,
  Sparkles,
  Trash2,
  X,
} from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import {
  useCustomSeasons,
  useEpisodeOrder,
  useSaveCustomSeasons,
  useUploadCustomSeasonPoster,
  type CustomSeason,
  type CustomSeasonEpisode,
  type CustomSeasons,
  type EpisodeOrderChoice,
} from "@/hooks/queries/episodeOrder";
import { useGripDragSensors } from "@/hooks/useSortableList";
import { useT } from "@/i18n";
import { joinList } from "@/lib/appLocale";
import { providerLabel, episodeOrderLabel, isNumberedSeasonTitle } from "@/lib/episodeOrder";
import { cn } from "@/lib/utils";

const SPECIALS = "specials";
// Provider names are brands, the same in every language.

interface ArrangedSeason {
  /** Stable while the arranger is open; numbers follow the list order. */
  id: string;
  title: string;
  overview: string;
  /** An uploaded poster, which stays with the season wherever it moves. */
  poster?: CustomSeasonPosterUpload;
  episodeKeys: string[];
}

interface CustomSeasonPosterUpload {
  ref: NonNullable<CustomSeason["poster"]>;
  url?: string;
}

interface ProviderSeason {
  title: string;
  overview: string;
  posterUrl?: string;
}

let seq = 0;
const newId = () => `season-${Date.now().toString(36)}-${seq++}`;
const pad = (n: number) => String(n).padStart(2, "0");

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

function arrangedOf(data: CustomSeasons): { seasons: ArrangedSeason[]; specials: string[] } {
  const seasons: ArrangedSeason[] = [];
  let specials: string[] = [];
  for (const season of data.seasons) {
    const keys = season.episodes.map((e) => e.key);
    if (season.number === 0) specials = keys;
    else
      seasons.push({
        id: newId(),
        title: season.title,
        overview: season.overview,
        poster: season.poster ? { ref: season.poster, url: season.poster_url } : undefined,
        episodeKeys: keys,
      });
  }
  return { seasons, specials };
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
    const shown = season?.title || providerSeasons.get(index + 1)?.title || "";
    return shown && !isNumberedSeasonTitle(t, shown, index + 1)
      ? t("web.item.arrangeSeasons.seasonTitled", { number: index + 1, title: shown })
      : t("web.item.arrangeSeasons.season", { number: index + 1 });
  };
  const nameOf = (id: string) =>
    id === SPECIALS
      ? t("web.item.arrangeSeasons.specials")
      : seasonName(seasons.findIndex((s) => s.id === id));

  function setList(seasonId: string, keys: string[]) {
    if (seasonId === SPECIALS) setSpecials(keys);
    else setSeasons((all) => all.map((s) => (s.id === seasonId ? { ...s, episodeKeys: keys } : s)));
  }

  function moveEpisodes(keys: string[], target: string) {
    if (keys.length === 0) return;
    const moving = new Set(keys);
    setSeasons((all) =>
      all.map((s) => {
        const kept = s.episodeKeys.filter((k) => !moving.has(k));
        return s.id === target
          ? { ...s, episodeKeys: [...kept, ...keys] }
          : { ...s, episodeKeys: kept };
      }),
    );
    setSpecials((sp) => {
      const kept = sp.filter((k) => !moving.has(k));
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
      all.map((s) => ({ ...s, episodeKeys: s.episodeKeys.filter((k) => !moving.has(k)) })),
    );
    setSpecials((sp) => sp.filter((k) => !moving.has(k)));
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
        <DialogHeader className="border-border/10 shrink-0 gap-1 border-b px-5 py-4 pe-12 text-start">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
            <DialogTitle className="text-[0.9375rem] font-semibold">
              {t("web.item.arrangeSeasons.title")}
            </DialogTitle>
            <span className="bg-muted/50 text-muted-foreground rounded px-2 py-0.5 text-[0.6875rem]">
              {title}
            </span>
          </div>
          <DialogDescription className="text-muted-foreground text-xs">{summary}</DialogDescription>
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
                      names: joinList(
                        emptySeasons.map((i) =>
                          t("web.item.arrangeSeasons.season", { number: i + 1 }),
                        ),
                      ),
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
                        <SelectItem value={SPECIALS}>
                          {t("web.item.arrangeSeasons.specials")}
                        </SelectItem>
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
            <DragOverlay zIndex={60}>
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
            <SelectTrigger className="w-full sm:w-auto sm:max-w-[420px] sm:min-w-[220px]">
              <RotateCcw className="size-3.5" aria-hidden="true" />
              <SelectValue placeholder={t("web.item.arrangeSeasons.startOver")} />
            </SelectTrigger>
            <SelectContent>
              {startOverOptions.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
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
                  heir: heirOf(deleting)
                    ? nameOf(heirOf(deleting)!.id)
                    : t("web.item.arrangeSeasons.specials"),
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

function SeasonDetails({
  season,
  number,
  provider,
  inherited,
  contentId,
  onTitle,
  onOverview,
  onPoster,
}: {
  season: ArrangedSeason;
  number: number;
  provider: string;
  inherited?: ProviderSeason;
  contentId: string;
  onTitle: (title: string) => void;
  onOverview: (overview: string) => void;
  onPoster: (poster: CustomSeasonPosterUpload | undefined) => void;
}) {
  const t = useT();
  const [overviewKey, setOverviewKey] = useState(0);
  const from = t("web.item.arrangeSeasons.fromProvider", { provider, number });
  const input = useRef<HTMLInputElement>(null);
  const upload = useUploadCustomSeasonPoster(contentId);
  const poster = season.poster?.url ?? inherited?.posterUrl;
  return (
    <div className="surface-panel mb-4 flex flex-col gap-4 rounded-2xl p-4 sm:flex-row">
      <div className="shrink-0">
        <div className="bg-surface border-border relative aspect-[2/3] w-32 overflow-hidden rounded-xl border">
          {poster ? (
            <img src={poster} alt="" className="h-full w-full object-cover" />
          ) : (
            <div className="text-muted-foreground flex h-full items-center justify-center p-3 text-center text-xs font-medium">
              {season.title || inherited?.title || t("web.item.arrangeSeasons.season", { number })}
            </div>
          )}
          <span className="absolute start-1.5 bottom-1.5 rounded bg-black/70 px-1.5 py-0.5 text-[0.625rem] font-medium text-white/85">
            {season.poster
              ? t("web.item.arrangeSeasons.custom")
              : inherited?.posterUrl
                ? from
                : t("web.item.arrangeSeasons.posterNone")}
          </span>
          {upload.isPending && (
            <span className="absolute inset-0 grid place-items-center bg-black/50">
              <Loader2 className="size-5 animate-spin text-white" aria-hidden="true" />
            </span>
          )}
        </div>
        <input
          ref={input}
          type="file"
          accept="image/jpeg,image/png,image/webp"
          className="hidden"
          onChange={(e) => {
            const file = e.target.files?.[0];
            e.target.value = "";
            if (!file) return;
            upload.mutate(file, {
              onSuccess: (uploaded) => {
                onPoster({ ref: uploaded.poster, url: uploaded.poster_url });
                toast.success(t("web.item.arrangeSeasons.posterAdded"));
              },
              onError: () => toast.error(t("web.item.arrangeSeasons.posterFailed")),
            });
          }}
        />
        <div className="mt-2 flex w-32 flex-col gap-1.5">
          <Button
            size="sm"
            variant="outline"
            disabled={upload.isPending}
            onClick={() => input.current?.click()}
          >
            <ImagePlus className="size-3.5" aria-hidden="true" />
            {season.poster
              ? t("web.item.arrangeSeasons.posterReplace")
              : t("web.item.arrangeSeasons.posterUpload")}
          </Button>
          {season.poster && (
            <Button size="sm" variant="ghost" onClick={() => onPoster(undefined)}>
              <RotateCcw className="size-3.5" aria-hidden="true" />
              {inherited?.posterUrl
                ? t("web.item.arrangeSeasons.useProvider", { provider })
                : t("web.item.arrangeSeasons.posterRemove")}
            </Button>
          )}
        </div>
      </div>
      <div className="min-w-0 flex-1 space-y-3">
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <Label
              htmlFor={`season-title-${season.id}`}
              className="text-muted-foreground text-[0.75rem] font-medium"
            >
              {t("web.item.arrangeSeasons.titleLabel")}
            </Label>
            <span className="text-muted-foreground text-[0.6875rem]">
              {season.title ? t("web.item.arrangeSeasons.custom") : from}
            </span>
          </div>
          <Input
            id={`season-title-${season.id}`}
            defaultValue={season.title}
            placeholder={inherited?.title || t("web.item.arrangeSeasons.season", { number })}
            onBlur={(e) => onTitle(e.target.value.trim())}
            onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
          />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <Label
              htmlFor={`season-overview-${season.id}`}
              className="text-muted-foreground text-[0.75rem] font-medium"
            >
              {t("web.item.arrangeSeasons.descriptionLabel")}
            </Label>
            <span className="text-muted-foreground flex items-center gap-2 text-[0.6875rem]">
              {season.overview
                ? t("web.item.arrangeSeasons.custom")
                : inherited?.overview
                  ? from
                  : t("web.item.arrangeSeasons.none")}
              {season.overview && (
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => {
                    onOverview("");
                    setOverviewKey((k) => k + 1);
                  }}
                >
                  <RotateCcw className="size-3" aria-hidden="true" />
                  {inherited?.overview
                    ? t("web.item.arrangeSeasons.useProvider", { provider })
                    : t("web.item.arrangeSeasons.clear")}
                </Button>
              )}
            </span>
          </div>
          <Textarea
            id={`season-overview-${season.id}`}
            key={overviewKey}
            defaultValue={season.overview || inherited?.overview || ""}
            placeholder={t("web.item.arrangeSeasons.descriptionPlaceholder")}
            rows={4}
            style={{ fieldSizing: "content" } as CSSProperties}
            className="max-h-64 min-h-24 text-sm leading-relaxed"
            onBlur={(e) => {
              const text = e.target.value.trim();
              const providerText = (inherited?.overview ?? "").trim();
              onOverview(text === providerText ? "" : text);
            }}
          />
        </div>
        <p className="text-muted-foreground/80 flex gap-1.5 text-xs leading-relaxed">
          <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          {t("web.item.arrangeSeasons.metaNoteSeasons", { provider })}
        </p>
      </div>
    </div>
  );
}

/**
 * The episodes of one season, of which only the rows in view are mounted: a
 * long season (One Piece, Bleach on TMDB) would otherwise render hundreds of
 * sortable rows on every pointer move of a drag.
 */
function VirtualEpisodeList({
  keys,
  scrollElement,
  renderRow,
}: {
  keys: string[];
  scrollElement: () => HTMLElement | null;
  renderRow: (key: string, index: number) => ReactNode;
}) {
  const listRef = useRef<HTMLUListElement>(null);
  const [offset, setOffset] = useState(0);
  // The list starts below the season details, inside the scroll element.
  useLayoutEffect(() => {
    const list = listRef.current;
    const scroller = scrollElement();
    if (!list || !scroller) return;
    const measure = () =>
      setOffset(
        list.getBoundingClientRect().top -
          scroller.getBoundingClientRect().top +
          scroller.scrollTop,
      );
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(scroller);
    if (list.parentElement) observer.observe(list.parentElement);
    return () => observer.disconnect();
  }, [scrollElement]);
  const virtualizer = useVirtualizer({
    count: keys.length,
    getScrollElement: scrollElement,
    estimateSize: () => 60,
    overscan: 8,
    scrollMargin: offset,
    getItemKey: (index) => keys[index] ?? index,
  });
  return (
    <ul
      ref={listRef}
      className="surface-panel relative overflow-hidden rounded-2xl"
      style={{ height: virtualizer.getTotalSize() }}
    >
      {virtualizer.getVirtualItems().map((item) => (
        <li
          key={item.key}
          data-index={item.index}
          ref={virtualizer.measureElement}
          className="absolute inset-x-0 top-0"
          style={{ transform: `translateY(${item.start - offset}px)` }}
        >
          {renderRow(keys[item.index] ?? "", item.index)}
        </li>
      ))}
    </ul>
  );
}

function SeasonRow({
  season,
  name,
  providerTitle,
  posterUrl,
  active,
  editing,
  dropTarget,
  canDelete,
  onSelect,
  onRename,
  onEdit,
  onDelete,
}: {
  season: ArrangedSeason;
  name: string;
  providerTitle?: string;
  posterUrl?: string;
  active: boolean;
  editing: boolean;
  dropTarget: boolean;
  canDelete: boolean;
  onSelect: () => void;
  onRename: (title: string) => void;
  onEdit: (on: boolean) => void;
  onDelete: () => void;
}) {
  const t = useT();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging, isOver } =
    useSortable({
      id: `season:${season.id}`,
    });
  return (
    <li
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "group flex items-center gap-1.5 rounded-lg border border-transparent px-1.5 py-1.5 transition-colors",
        active ? "bg-primary/10 border-primary/30" : "hover:bg-accent/50",
        dropTarget && "border-border border-dashed",
        dropTarget && isOver && "border-primary bg-primary/15",
        isDragging && "opacity-60",
      )}
    >
      <button
        type="button"
        className="text-muted-foreground/60 hover:text-foreground cursor-grab touch-none p-1"
        aria-label={t("web.item.arrangeSeasons.drag", { name })}
        {...attributes}
        {...listeners}
      >
        <GripVertical className="size-4" aria-hidden="true" />
      </button>
      {editing ? (
        <Input
          autoFocus
          defaultValue={season.title}
          placeholder={providerTitle || name}
          className="h-8 flex-1 text-sm"
          onBlur={(e) => {
            onRename(e.target.value.trim());
            onEdit(false);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === "Escape") (e.target as HTMLInputElement).blur();
          }}
        />
      ) : (
        <button
          type="button"
          onClick={onSelect}
          className="flex min-w-0 flex-1 items-center gap-2.5 text-start"
        >
          <span className="bg-surface border-border/60 h-12 w-8 shrink-0 overflow-hidden rounded-md border">
            {posterUrl && (
              <img src={posterUrl} alt="" className="h-full w-full object-cover" loading="lazy" />
            )}
          </span>
          <span className="min-w-0 flex-1">
            <span className="block text-sm leading-snug font-medium text-pretty">{name}</span>
            <span className="text-muted-foreground block text-xs">
              {t("web.item.arrangeSeasons.episodeCount", { count: season.episodeKeys.length })}
            </span>
          </span>
        </button>
      )}
      {!editing && (
        <div className="flex opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
          <button
            type="button"
            onClick={() => onEdit(true)}
            className="text-muted-foreground hover:text-foreground p-1.5"
            aria-label={t("web.item.arrangeSeasons.rename")}
          >
            <Pencil className="size-3.5" aria-hidden="true" />
          </button>
          {canDelete && (
            <button
              type="button"
              onClick={onDelete}
              className="text-muted-foreground p-1.5 hover:text-red-400"
              aria-label={t("web.item.arrangeSeasons.delete")}
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
            </button>
          )}
        </div>
      )}
    </li>
  );
}

function SpecialsRow({
  count,
  active,
  dropTarget,
  onSelect,
}: {
  count: number;
  active: boolean;
  dropTarget: boolean;
  onSelect: () => void;
}) {
  const t = useT();
  const { setNodeRef, isOver } = useSortable({
    id: `season:${SPECIALS}`,
    disabled: { draggable: true },
  });
  return (
    <button
      ref={setNodeRef}
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-2 rounded-lg border border-transparent px-3 py-2 text-start",
        active ? "bg-primary/10 border-primary/30" : "hover:bg-accent/50",
        dropTarget && "border-border border-dashed",
        dropTarget && isOver && "border-primary bg-primary/15",
      )}
    >
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{t("web.item.arrangeSeasons.specials")}</div>
        <div className="text-muted-foreground text-xs">
          {t("web.item.arrangeSeasons.specialsMeta", { count })}
        </div>
      </div>
    </button>
  );
}

function EpisodeRow({
  episode,
  provider,
  number,
  selected,
  onToggle,
  onSplit,
}: {
  episode: CustomSeasonEpisode;
  provider: string;
  number: number;
  selected: boolean;
  onToggle: (shift: boolean) => void;
  onSplit?: () => void;
}) {
  const t = useT();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: `ep:${episode.key}`,
  });
  const origin = [
    provider,
    `S${pad(episode.provider_season)}E${pad(episode.provider_episode)}`,
  ].join(" ");
  const year = episode.air_date?.slice(0, 4);
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "group border-border/40 relative flex items-center gap-3 border-b px-3 py-2",
        selected && "bg-primary/8",
        isDragging && "opacity-0",
      )}
    >
      <input
        type="checkbox"
        checked={selected}
        onChange={() => undefined}
        onClick={(ev) => onToggle(ev.shiftKey)}
        className="size-4 shrink-0"
        style={{ accentColor: "var(--primary)" }}
        aria-label={t("web.item.arrangeSeasons.select", { title: episode.title })}
      />
      <button
        type="button"
        className="text-muted-foreground/50 hover:text-foreground cursor-grab touch-none"
        aria-label={t("web.item.arrangeSeasons.drag", { name: episode.title })}
        {...attributes}
        {...listeners}
      >
        <GripVertical className="size-4" aria-hidden="true" />
      </button>
      <span className="text-muted-foreground/70 w-8 shrink-0 text-center text-sm font-semibold tabular-nums">
        {number}
      </span>
      <span className="bg-muted/50 hidden h-11 w-[78px] shrink-0 overflow-hidden rounded-md sm:block">
        {episode.still_url && (
          <img
            src={episode.still_url}
            alt=""
            className="h-full w-full object-cover"
            loading="lazy"
          />
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-medium">{episode.title}</span>
          {episode.placed_automatically && (
            <span className="bg-primary/15 text-primary flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[0.625rem] font-medium">
              <Sparkles className="size-3" aria-hidden="true" />
              {t("web.item.arrangeSeasons.placedAutomatically")}
            </span>
          )}
        </div>
        <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
          <span className="shrink-0 font-mono">{origin}</span>
          {year && <span className="shrink-0">{year}</span>}
          {episode.file ? (
            <span className="flex min-w-0 items-center gap-1 font-mono text-[0.6875rem]">
              <FileVideo className="size-3 shrink-0" aria-hidden="true" />
              <span className="truncate">{episode.file}</span>
            </span>
          ) : (
            <span className="text-muted-foreground/60">{t("web.item.arrangeSeasons.noFile")}</span>
          )}
        </div>
      </div>
      {onSplit && (
        <button
          type="button"
          onClick={onSplit}
          className="bg-surface border-border text-muted-foreground hover:text-foreground absolute end-3 top-1/2 flex -translate-y-1/2 items-center gap-1 rounded-md border px-2 py-1 text-xs opacity-0 shadow-sm transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
          title={t("web.item.arrangeSeasons.splitTitle")}
        >
          <Scissors className="size-3.5" aria-hidden="true" />
          {t("web.item.arrangeSeasons.splitHere")}
        </button>
      )}
    </div>
  );
}
