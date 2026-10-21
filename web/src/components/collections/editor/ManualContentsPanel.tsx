import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
  type UniqueIdentifier,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Check, GripVertical, Loader2, Plus, Search, Undo2, X } from "lucide-react";

import type { BrowseItem } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminCollectionCapabilities } from "@/hooks/queries/admin/collections";
import {
  createCatalogSearchState,
  fetchCatalogItems,
  fetchCatalogPage,
} from "@/hooks/queries/catalog";
import { putCollectionItem } from "@/hooks/queries/collectionScope";
import {
  useCollectionCapabilities,
  useCollectionItemOrderSnapshot,
  COLLECTION_ITEMS_PAGE,
  useCollectionItems,
  useReorderCollectionItems,
} from "@/hooks/queries/collections";
import { catalogKeys } from "@/hooks/queries/keys";
import { useDebounce } from "@/hooks/useDebounce";
import { v2 } from "@/api/v2/request";
import {
  ALL_YOUR_LIBRARIES,
  IN_THIS_COLLECTION,
  NOT_CREATED_YET,
  TITLES_CAPTION,
  TITLES_EMPTY,
  TITLES_SAVED,
  createdButNotAdded,
  joinNames,
  removedTitle,
} from "@/lib/collections/copy";
import type { CollectionScope } from "@/lib/collections/scope";
import { cn } from "@/lib/utils";

import { OrderBlock } from "../fields/OrderBlock";

const SEARCH_LIMIT = 12;
const DEBOUNCE_MS = 250;
/** Libraries searched at once when a server collection spans several. */
const SEARCH_CONCURRENCY = 4;
/** Titles listed before "Show all N". */
const COLLAPSED_COUNT = 10;
const UNDO_MS = 6000;

/** What a title row shows, from a search hit or the collection's catalog page. */
interface TitleInfo {
  title: string;
  year?: number;
  type?: string;
  posterUrl?: string;
}

const TYPE_LABEL: Record<string, string> = {
  movie: "Movie",
  series: "Show",
  season: "Season",
  episode: "Episode",
};

function infoOf(item: BrowseItem): TitleInfo {
  return {
    title: item.title,
    year: item.year || undefined,
    type: item.type,
    posterUrl: item.poster_url || undefined,
  };
}

function metaOf(info: TitleInfo | undefined) {
  return [info?.year, info?.type ? (TYPE_LABEL[info.type] ?? info.type) : undefined]
    .filter(Boolean)
    .join(" · ");
}

/** Runs `run` over `values` with at most `limit` in flight, keeping the input order. */
async function mapLimit<T, R>(values: readonly T[], limit: number, run: (value: T) => Promise<R>) {
  const results = new Array<R>(values.length);
  let next = 0;
  async function worker() {
    while (next < values.length) {
      const index = next++;
      results[index] = await run(values[index]!);
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, values.length) }, worker));
  return results;
}

/** One list from several ranked lists: first hits of each, then second hits, and so on. */
function mergeByRank(lists: readonly BrowseItem[][], limit: number): BrowseItem[] {
  const seen = new Set<string>();
  const merged: BrowseItem[] = [];
  for (let rank = 0; merged.length < limit; rank++) {
    if (lists.every((list) => rank >= list.length)) break;
    for (const list of lists) {
      const item = list[rank];
      if (!item || seen.has(item.content_id)) continue;
      seen.add(item.content_id);
      merged.push(item);
      if (merged.length === limit) break;
    }
  }
  return merged;
}

/**
 * Searches the viewer's catalog, so it only offers titles the viewer may see.
 * One library sends its `library_id`; several search each library (at most
 * four at once) and merge by rank; none searches every library the viewer has.
 */
function searchTitles(query: string, libraryIds: readonly number[], signal?: AbortSignal) {
  const page = (libraryId?: number) =>
    fetchCatalogPage(
      createCatalogSearchState("query", {
        q: query,
        library_id: libraryId,
        // Text-search ranking, not a library's default Date Added order.
        query_definition: {
          ...createCatalogSearchState("query").query_definition,
          sort: { field: "relevance", order: "desc" },
        },
      }),
      SEARCH_LIMIT,
      0,
      { signal },
      false,
    ).then((result) => result.items);
  if (libraryIds.length === 0) return page();
  if (libraryIds.length === 1) return page(libraryIds[0]);
  return mapLimit(libraryIds, SEARCH_CONCURRENCY, page).then((lists) =>
    mergeByRank(lists, SEARCH_LIMIT),
  );
}

function TitleSearch({
  libraries,
  existing,
  onAdd,
  disabled,
}: {
  libraries: Array<{ id: number; name: string }>;
  existing: ReadonlySet<string>;
  onAdd: (item: BrowseItem) => void;
  disabled?: boolean;
}) {
  const listId = useId();
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);
  const debounced = useDebounce(query.trim(), DEBOUNCE_MS);
  const libraryIds = useMemo(() => libraries.map((library) => library.id), [libraries]);
  const scopeText =
    libraries.length > 0 ? libraries.map((library) => library.name).join(", ") : ALL_YOUR_LIBRARIES;

  const results = useQuery({
    queryKey: [...catalogKeys.all, "collectionTitleSearch", libraryIds, debounced],
    queryFn: ({ signal }) => searchTitles(debounced, libraryIds, signal),
    enabled: debounced.length > 0,
    staleTime: 30 * 1000,
  });
  const found = results.data ?? [];
  const showList = open && debounced.length > 0;
  const active = showList ? found[Math.min(highlight, found.length - 1)] : undefined;

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      setOpen(true);
      if (found.length === 0) return;
      const step = event.key === "ArrowDown" ? 1 : -1;
      setHighlight((index) => (index + step + found.length) % found.length);
    } else if (event.key === "Enter") {
      event.preventDefault();
      if (active && !existing.has(active.content_id)) onAdd(active);
    } else if (event.key === "Escape") {
      if (showList) {
        event.preventDefault();
        setOpen(false);
      }
    }
  }

  let status: string | null = null;
  if (showList) {
    if (results.isLoading) status = "Searching…";
    else if (!results.isError && found.length === 0) status = "No titles match.";
  }

  return (
    <div className="relative grid gap-2">
      <div className="border-border bg-background/40 focus-within:ring-ring/50 flex h-11 items-center gap-2.5 rounded-xl border px-3 focus-within:ring-[3px]">
        <Search aria-hidden className="text-muted-foreground size-4 shrink-0" />
        <input
          role="combobox"
          aria-label={
            libraries.length > 0
              ? `Add a title from ${joinNames(libraries.map((l) => l.name))}`
              : "Add a title"
          }
          aria-expanded={showList}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={active ? `${listId}-${active.content_id}` : undefined}
          placeholder="Add a title"
          autoComplete="off"
          disabled={disabled}
          data-title-search
          className="placeholder:text-muted-foreground min-w-0 flex-1 bg-transparent text-[14.5px] outline-none"
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setOpen(true);
            setHighlight(0);
          }}
          onKeyDown={onKeyDown}
        />
        <span className="text-muted-foreground hidden shrink-0 text-[12.5px] sm:inline">
          {scopeText}
        </span>
      </div>
      {status ? (
        <p role="status" className="text-muted-foreground px-1 text-[13px]">
          {status}
        </p>
      ) : null}
      {showList && results.isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 px-1 text-[13px]">
          <span>The search didn't work.</span>
          <Button type="button" size="sm" variant="outline" onClick={() => void results.refetch()}>
            Try again
          </Button>
        </div>
      ) : null}
      <ul
        id={listId}
        role="listbox"
        aria-label="Titles to add"
        hidden={!showList || found.length === 0}
        className="border-border bg-popover grid max-h-80 gap-0.5 overflow-y-auto rounded-xl border p-1.5"
      >
        {found.map((item) => {
          const already = existing.has(item.content_id);
          const highlighted = item === active;
          return (
            <li
              key={item.content_id}
              id={`${listId}-${item.content_id}`}
              role="option"
              aria-selected={highlighted}
              aria-disabled={already || undefined}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => (already ? undefined : onAdd(item))}
              className={cn(
                "flex cursor-pointer items-center gap-3 rounded-[10px] px-2.5 py-2",
                highlighted && "bg-accent",
                already && "cursor-default",
              )}
            >
              <Poster url={item.poster_url} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[14px] font-semibold">{item.title}</span>
                <span className="text-muted-foreground block truncate text-[12.5px]">
                  {metaOf(infoOf(item))}
                </span>
              </span>
              <span
                className={cn(
                  "flex shrink-0 items-center gap-1 text-[13px]",
                  already ? "text-muted-foreground" : "font-medium",
                )}
              >
                {already ? (
                  <Check aria-hidden className="text-success size-3.5" />
                ) : (
                  <Plus aria-hidden className="size-3.5" />
                )}
                {already ? IN_THIS_COLLECTION : "Add"}
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function Poster({ url }: { url?: string }) {
  return url ? (
    <img src={url} alt="" className="bg-muted aspect-[2/3] w-9 shrink-0 rounded-md object-cover" />
  ) : (
    <span aria-hidden className="bg-muted aspect-[2/3] w-9 shrink-0 rounded-md" />
  );
}

interface Row {
  id: string;
  /** The stored position, for a removal's Undo. */
  position?: number;
  info?: TitleInfo;
  /** Create mode: couldn't be added. */
  failed?: boolean;
}

function TitleRow({
  row,
  index,
  canReorder,
  onRemove,
}: {
  row: Row;
  index: number;
  canReorder: boolean;
  onRemove: () => void;
}) {
  const label = row.info?.title || row.id;
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: row.id,
    disabled: !canReorder,
  });
  return (
    <li
      ref={setNodeRef}
      data-title-id={row.id}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "border-border/70 grid grid-cols-[28px_24px_36px_minmax(0,1fr)_auto_36px] items-center gap-2.5 border-b py-2 last:border-b-0",
        isDragging && "bg-surface-raised relative z-10 rounded-xl shadow-lg",
      )}
    >
      {canReorder ? (
        <button
          type="button"
          aria-label={`Move ${label}`}
          className="text-muted-foreground/70 hover:text-foreground focus-visible:ring-ring/50 grid h-9 w-7 cursor-grab touch-none place-items-center rounded-lg outline-none focus-visible:ring-[3px]"
          {...attributes}
          {...listeners}
        >
          <GripVertical aria-hidden className="size-[18px]" />
        </button>
      ) : (
        <span />
      )}
      <span className="text-muted-foreground text-right text-[13px] tabular-nums">{index + 1}</span>
      <Poster url={row.info?.posterUrl} />
      <span className="min-w-0">
        <span className="block truncate text-[14.5px] font-semibold">{label}</span>
        <span className="text-muted-foreground block truncate text-[12.5px]">
          {metaOf(row.info)}
        </span>
      </span>
      {row.failed ? (
        <span className="text-destructive text-[12.5px] font-medium">Couldn't add</span>
      ) : (
        <span />
      )}
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="text-muted-foreground hover:text-destructive size-9 max-lg:size-11"
        aria-label={`Remove ${label}`}
        onClick={onRemove}
      >
        <X aria-hidden className="size-4" />
      </Button>
    </li>
  );
}

/**
 * "Removed X · Undo" for six seconds. The timer pauses while the pointer is
 * over it or focus is inside it, so the button can be reached.
 */
function UndoToast({
  message,
  onUndo,
  onDone,
}: {
  message: string;
  onUndo: () => void;
  onDone: () => void;
}) {
  const [paused, setPaused] = useState({ hover: false, focus: false });
  const remaining = useRef(UNDO_MS);
  const isPaused = paused.hover || paused.focus;
  useEffect(() => {
    if (isPaused) return;
    const started = Date.now();
    const timer = window.setTimeout(onDone, remaining.current);
    return () => {
      window.clearTimeout(timer);
      remaining.current -= Date.now() - started;
    };
  }, [isPaused, onDone]);
  return (
    <div
      role="status"
      onMouseEnter={() => setPaused((p) => ({ ...p, hover: true }))}
      onMouseLeave={() => setPaused((p) => ({ ...p, hover: false }))}
      onFocus={() => setPaused((p) => ({ ...p, focus: true }))}
      onBlur={() => setPaused((p) => ({ ...p, focus: false }))}
      className="border-border bg-popover flex w-fit items-center gap-3 rounded-xl border py-1.5 pr-1.5 pl-3 text-[13.5px] shadow-lg"
    >
      <span className="font-medium">{message}</span>
      <Button type="button" size="sm" variant="outline" className="h-8 gap-1.5" onClick={onUndo}>
        <Undo2 aria-hidden className="size-3.5" />
        Undo
      </Button>
    </div>
  );
}

export interface ManualContentsPanelProps {
  scope: CollectionScope;
  /** Unset until the collection exists: titles are staged instead. */
  collectionId?: string;
  /** The libraries the search covers: a server collection's own; none means all the viewer's. */
  searchLibraries: Array<{ id: number; name: string }>;
  /** The Libraries line, server only. */
  librariesLine?: ReactNode;
  /** Create mode: the staged titles. After create: the ones that couldn't be added. */
  staged: readonly string[];
  onStagedChange: (itemIds: string[]) => void;
  /** After create: try the titles that failed again, at this position onwards. */
  onRetryStaged?: (firstPosition: number) => void;
  /** The collection's title count, for appending when the list isn't all loaded. */
  itemCount?: number;
  /** After every title write, so the editor refreshes its save token. */
  onItemsChanged: () => void;
}

/**
 * A Manual collection's titles. Before the collection exists, picked titles
 * are staged and added on Create. Afterwards every add, remove and drag
 * saves at once, shows "Saved", and calls `onItemsChanged`.
 */
export function ManualContentsPanel({
  scope,
  collectionId,
  searchLibraries,
  librariesLine,
  staged,
  onStagedChange,
  onRetryStaged,
  itemCount = 0,
  onItemsChanged,
}: ManualContentsPanelProps) {
  const queryClient = useQueryClient();
  const headingId = useId();
  const source = scope.itemSource;
  const created = Boolean(collectionId);
  const [page, setPage] = useState({ collectionId, cursor: "" });
  const cursor = page.collectionId === collectionId ? page.cursor : "";
  const items = useCollectionItems(collectionId ?? "", cursor, source, created);
  const loaded = useMemo(() => items.data?.items ?? [], [items.data]);
  const personalCapabilities = useCollectionCapabilities();
  const adminCapabilities = useAdminCollectionCapabilities(scope.kind === "server");
  const capabilities =
    scope.kind === "personal" ? personalCapabilities.data : adminCapabilities.data;
  const orderSnapshot = useCollectionItemOrderSnapshot(
    collectionId ?? "",
    created && capabilities?.item_reorder === true,
    source,
  ).data;
  const reorder = useReorderCollectionItems(collectionId ?? "", source);
  const [known, setKnown] = useState<Record<string, TitleInfo>>({});
  const [saved, setSaved] = useState<"idle" | "saving" | "saved">("idle");
  const [expanded, setExpanded] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [undo, setUndo] = useState<{ id: string; title: string; position: number } | null>(null);
  const dragOrder = useRef<typeof orderSnapshot>(undefined);

  // Posters and years for titles saved before the editor opened.
  const catalogTitles = useQuery({
    queryKey: [...catalogKeys.all, "collectionTitles", source, collectionId],
    queryFn: ({ signal }) =>
      fetchCatalogItems(
        createCatalogSearchState(source === "user" ? "user_collection" : "library_collection", {
          collection_id: collectionId,
          uses_source_order: true,
        }),
        { max: COLLECTION_ITEMS_PAGE, signal },
      ),
    enabled: created,
    staleTime: 60 * 1000,
  });
  const catalogInfo = useMemo(
    () =>
      Object.fromEntries((catalogTitles.data ?? []).map((item) => [item.content_id, infoOf(item)])),
    [catalogTitles.data],
  );
  const infoFor = useCallback(
    (id: string, title?: string): TitleInfo | undefined => {
      const info = known[id] ?? catalogInfo[id];
      return info ?? (title ? { title } : undefined);
    },
    [catalogInfo, known],
  );

  // Before create: the staged titles. After: the saved ones, then any that couldn't be added.
  const rows = useMemo<Row[]>(
    () =>
      created
        ? [
            ...loaded.map((item) => ({
              id: item.media_item_id,
              position: item.position,
              info: infoFor(item.media_item_id, item.title),
            })),
            ...staged
              .filter((id) => !loaded.some((item) => item.media_item_id === id))
              .map((id) => ({ id, info: infoFor(id), failed: true })),
          ]
        : staged.map((id) => ({ id, info: infoFor(id) })),
    [created, infoFor, loaded, staged],
  );
  const existing = new Set(rows.map((row) => row.id));

  const fullyLoaded = !cursor && items.data?.page?.has_more === false;
  const sameOrder =
    !!orderSnapshot &&
    orderSnapshot.ordered_ids.length === loaded.length &&
    orderSnapshot.ordered_ids.every((id, index) => id === loaded[index]?.media_item_id);
  const canReorder = created
    ? capabilities?.item_reorder === true && sameOrder && !orderSnapshot?.has_more && fullyLoaded
    : staged.length > 1;
  const nextPosition =
    fullyLoaded && loaded.length > 0
      ? Math.max(...loaded.map((item) => item.position)) + 1
      : Math.max(itemCount, loaded.length);
  // The position after the last add sent: the server stores what it's given,
  // so an add picked before the list refreshes goes after the one before it.
  const claimed = useRef(0);

  async function afterWrite() {
    await scope.invalidate(queryClient, collectionId);
    setSaved("saved");
    onItemsChanged();
  }

  async function add(item: BrowseItem) {
    setKnown((current) => ({ ...current, [item.content_id]: infoOf(item) }));
    setProblem(null);
    if (!collectionId) {
      if (!staged.includes(item.content_id)) onStagedChange([...staged, item.content_id]);
      return;
    }
    setSaved("saving");
    const position = Math.max(nextPosition, claimed.current);
    claimed.current = position + 1;
    try {
      await putCollectionItem(scope, collectionId, item.content_id, position);
      await afterWrite();
    } catch (error) {
      setSaved("idle");
      setProblem(
        `Couldn't add ${item.title}. ${error instanceof Error ? error.message : "Try again."}`,
      );
    }
  }

  async function remove(row: Row) {
    setProblem(null);
    if (!collectionId || row.failed) {
      onStagedChange(staged.filter((id) => id !== row.id));
      return;
    }
    setSaved("saving");
    try {
      await v2(
        source === "user"
          ? "DELETE /api/v2/collections/{id}/items/{item_id}"
          : "DELETE /api/v2/admin/collections/{id}/items/{item_id}",
        { path: { id: collectionId, item_id: row.id } },
      );
      setUndo({ id: row.id, title: row.info?.title || row.id, position: row.position ?? 0 });
      if (row.info) setKnown((current) => ({ ...current, [row.id]: row.info! }));
      setPage({ collectionId, cursor: "" });
      await afterWrite();
    } catch (error) {
      setSaved("idle");
      setProblem(
        `Couldn't remove ${row.info?.title || row.id}. ${error instanceof Error ? error.message : ""}`.trim(),
      );
    }
  }

  async function restore(removed: NonNullable<typeof undo>) {
    setUndo(null);
    if (!collectionId) return;
    setSaved("saving");
    try {
      await putCollectionItem(scope, collectionId, removed.id, removed.position);
      await afterWrite();
    } catch (error) {
      setSaved("idle");
      setProblem(
        `Couldn't put back ${removed.title}. ${error instanceof Error ? error.message : ""}`.trim(),
      );
    }
  }

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const lastOver = useRef<UniqueIdentifier | null>(null);
  const announcements = useMemo<Announcements>(() => {
    const titleOf = (id: UniqueIdentifier) =>
      rows.find((row) => row.id === id)?.info?.title ?? "Title";
    const positionOf = (id: UniqueIdentifier) =>
      `position ${rows.findIndex((row) => row.id === id) + 1} of ${rows.length}`;
    return {
      onDragStart: ({ active }) => {
        lastOver.current = active.id;
        return `Picked up ${titleOf(active.id)}, ${positionOf(active.id)}.`;
      },
      onDragOver: ({ active, over }) => {
        if (!over || over.id === lastOver.current) return undefined;
        lastOver.current = over.id;
        return `${titleOf(active.id)} is now at ${positionOf(over.id)}.`;
      },
      onDragEnd: ({ active, over }) =>
        over
          ? `Moved ${titleOf(active.id)} to ${positionOf(over.id)}.`
          : `${titleOf(active.id)} dropped.`,
      onDragCancel: ({ active }) => `Moving ${titleOf(active.id)} was canceled.`,
    };
  }, [rows]);

  function onDragEnd({ active, over }: DragEndEvent) {
    if (!over || active.id === over.id) return;
    // Titles that couldn't be added aren't in the collection; they stay after the saved ones.
    const ids = rows.filter((row) => !row.failed).map((row) => row.id);
    const from = ids.indexOf(String(active.id));
    if (from < 0) return;
    const to = ids.indexOf(String(over.id));
    const next = arrayMove(ids, from, to < 0 ? ids.length - 1 : to);
    if (!collectionId) {
      onStagedChange(next);
      return;
    }
    const token = dragOrder.current;
    if (!canReorder || !token) return;
    setSaved("saving");
    reorder.mutate(
      { orderedIds: next, etag: token.etag },
      {
        onSuccess: () => {
          setSaved("saved");
          onItemsChanged();
        },
        onError: () => setSaved("idle"),
      },
    );
  }

  const visible = expanded ? rows : rows.slice(0, COLLAPSED_COUNT);
  const failedCount = created ? staged.length : 0;

  return (
    <section
      aria-labelledby={headingId}
      className="surface-panel grid content-start gap-4 rounded-[22px] p-5 sm:p-6"
    >
      <header>
        {/* The tag and the tick sit by the heading, not in it, so its name stays "Titles". */}
        <div className="flex items-center gap-2.5">
          <h2 id={headingId} className="text-[17px] font-semibold">
            Titles
          </h2>
          {!created ? (
            <span className="bg-muted text-muted-foreground rounded-md px-2 py-0.5 text-[12px] font-medium">
              {NOT_CREATED_YET}
            </span>
          ) : null}
          <span
            role="status"
            className="text-muted-foreground flex items-center gap-1 text-[13px] font-normal"
          >
            {saved === "saving" ? (
              <>
                <Loader2 aria-hidden className="size-3.5 animate-spin" />
                Saving…
              </>
            ) : saved === "saved" ? (
              <>
                <Check aria-hidden className="text-success size-3.5" />
                {TITLES_SAVED}
              </>
            ) : null}
          </span>
        </div>
        <p className="text-muted-foreground mt-1 text-[13.5px]">
          {!created ? TITLES_CAPTION.create : TITLES_CAPTION[scope.kind]}
        </p>
      </header>

      {librariesLine}

      <TitleSearch
        libraries={searchLibraries}
        existing={existing}
        onAdd={(item) => void add(item)}
      />

      {problem ? (
        <p role="alert" className="text-destructive text-[13px]">
          {problem}
        </p>
      ) : null}

      {failedCount > 0 ? (
        <div
          role="alert"
          className="border-destructive/40 bg-destructive/10 flex flex-wrap items-center gap-3 rounded-xl border px-3 py-2 text-[13.5px]"
        >
          <span>{createdButNotAdded(failedCount)}</span>
          {onRetryStaged ? (
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="h-8"
              onClick={() => onRetryStaged(nextPosition)}
            >
              Try again
            </Button>
          ) : null}
        </div>
      ) : null}

      {undo ? (
        <UndoToast
          key={undo.id}
          message={removedTitle(undo.title)}
          onUndo={() => void restore(undo)}
          onDone={() => setUndo(null)}
        />
      ) : null}

      {created && items.isLoading ? (
        <div className="grid gap-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : created && items.error ? (
        <div role="alert" className="grid justify-items-start gap-2 text-[13.5px]">
          <p>{items.error instanceof Error ? items.error.message : "Couldn't load the titles."}</p>
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              setPage({ collectionId, cursor: "" });
              void items.refetch();
            }}
          >
            Reload from start
          </Button>
        </div>
      ) : rows.length === 0 ? (
        <p className="border-border text-muted-foreground rounded-xl border border-dashed px-4 py-5 text-[13.5px]">
          {TITLES_EMPTY}
        </p>
      ) : (
        <DndContext
          sensors={sensors}
          collisionDetection={closestCenter}
          accessibility={{ announcements }}
          onDragStart={() => {
            dragOrder.current = orderSnapshot;
          }}
          onDragEnd={onDragEnd}
        >
          <SortableContext
            items={visible.map((row) => row.id)}
            strategy={verticalListSortingStrategy}
          >
            <ol aria-labelledby={headingId} className="m-0 list-none p-0">
              {visible.map((row, index) => (
                <TitleRow
                  key={row.id}
                  row={row}
                  index={index}
                  canReorder={canReorder && !row.failed}
                  onRemove={() => void remove(row)}
                />
              ))}
            </ol>
          </SortableContext>
        </DndContext>
      )}

      {rows.length > COLLAPSED_COUNT || canReorder ? (
        <div className="flex flex-wrap items-center justify-between gap-3">
          {rows.length > COLLAPSED_COUNT ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setExpanded((value) => !value)}
            >
              {expanded ? "Show fewer" : `Show all ${rows.length}`}
            </Button>
          ) : (
            <span />
          )}
          {canReorder ? (
            <p className="text-muted-foreground text-[12.5px]">
              Drag, or focus a handle and press Space, then ↑ or ↓.
            </p>
          ) : null}
        </div>
      ) : null}

      {created && (cursor || items.data?.page?.has_more) ? (
        <div className="grid gap-2">
          <p className="text-muted-foreground text-[13px]">
            Showing up to 200 titles. Reordering is available when the whole collection fits on one
            page.
          </p>
          <div className="flex gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={!cursor}
              onClick={() => setPage({ collectionId, cursor: "" })}
            >
              First page
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={!items.data?.page?.has_more || !items.data.page?.next_cursor}
              onClick={() => setPage({ collectionId, cursor: items.data?.page?.next_cursor ?? "" })}
            >
              Next page
            </Button>
          </div>
        </div>
      ) : null}

      <OrderBlock />
    </section>
  );
}
