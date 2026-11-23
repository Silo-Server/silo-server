import { useId, useMemo, useRef, type ReactNode } from "react";
import { DndContext, closestCenter, type Announcements, type DragEndEvent } from "@dnd-kit/core";
import { SortableContext, arrayMove, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { AlertTriangle, ArrowDown, ArrowUp, Check, GripVertical, Plus, X } from "lucide-react";
import { PosterArt } from "@/components/calm/PosterTile";
import { LineName, MetaLine } from "@/components/MetaLine";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useProfiles } from "@/hooks/queries/profiles";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useGripDragSensors, useMotionSafeSortable } from "@/hooks/useSortableList";
import { useT } from "@/i18n";
import { ownerName } from "@/lib/collections/personalOwnership";
import {
  MAX_ROW_COLLECTIONS,
  collectionRefKey,
  optionRefKey,
  type CollectionRef,
} from "@/lib/homeRows/collectionList";
import type { RowCollections } from "@/lib/homeRows/types";
import { cn } from "@/lib/utils";
import { CollectionOptionBody, CollectionSearchBar } from "./collectionChooser";
import { collectionEmptyText, collectionOptionMeta, useCollectionSearch } from "./collectionSearch";

/**
 * Step 2 of a row of collections: the collections the row shows, in order,
 * then a list to add more from. The chosen list is reordered by dragging a
 * grip (mouse, touch or keyboard) or with Move up / Move down, one step at a
 * time. Adding keeps the search, so several can be added in a run; a chosen
 * collection this surface no longer offers stays, marked, until removed.
 */
export function CollectionsRowEditor({
  collections,
  refs,
  onChange,
}: {
  collections: RowCollections;
  refs: CollectionRef[];
  onChange: (refs: CollectionRef[]) => void;
}) {
  const t = useT();
  const chosenId = useId();
  const chooserId = useId();
  const byKey = useMemo(
    () => new Map(collections.options.map((option) => [optionRefKey(option), option])),
    [collections.options],
  );
  const chosen = useMemo(() => new Set(refs.map(collectionRefKey)), [refs]);
  const owner = useOwnerLine();
  const full = refs.length >= MAX_ROW_COLLECTIONS;
  const settled = !collections.loading && !collections.failed;

  const describe = (ref: CollectionRef): ChosenLine => {
    const option = byKey.get(collectionRefKey(ref));
    if (option) {
      return {
        title: option.title,
        option,
        meta: <MetaLine parts={[collectionOptionMeta(option), owner(option)]} />,
      };
    }
    return { title: settled ? t("web.home.addRow.collections.unavailable") : "", missing: settled };
  };

  function toggle(option: CollectionOption) {
    const key = optionRefKey(option);
    if (chosen.has(key)) {
      onChange(refs.filter((ref) => collectionRefKey(ref) !== key));
    } else if (!full) {
      onChange([...refs, { source: option.source, id: option.id }]);
    }
  }

  return (
    <div className="grid gap-4">
      {/* grid-cols-1 holds the column to the editor's width. An implicit auto
          column grows to fit the longest name instead of cutting it. */}
      <div className="grid grid-cols-1 gap-2">
        <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
          <span id={chosenId} className="text-sm font-medium">
            {t("web.home.addRow.collections.chosenLabel")}
          </span>
          {refs.length > 0 ? (
            <span className="text-muted-foreground text-[0.8125rem]">
              {t("web.home.addRow.collections.count", {
                count: refs.length,
                count_text: refs.length,
              })}
            </span>
          ) : null}
        </div>
        {refs.length === 0 ? (
          <p className="border-border text-muted-foreground rounded-xl border border-dashed px-3 py-3 text-[0.8125rem]">
            {t("web.home.addRow.collections.empty")}
          </p>
        ) : (
          <ChosenList labelId={chosenId} refs={refs} describe={describe} onChange={onChange} />
        )}
        {full ? (
          <p role="status" className="text-muted-foreground text-[0.8125rem]">
            {t("web.home.addRow.collections.limit", {
              count: MAX_ROW_COLLECTIONS,
              count_text: MAX_ROW_COLLECTIONS,
            })}
          </p>
        ) : null}
      </div>

      <Chooser
        labelId={chooserId}
        collections={collections}
        chosen={chosen}
        full={full}
        owner={owner}
        onToggle={toggle}
      />
    </div>
  );
}

interface ChosenLine {
  title: string;
  option?: CollectionOption;
  meta?: ReactNode;
  /** The collection is gone, hidden or no longer shared. */
  missing?: boolean;
}

/** "Yours" or "by Name" on a personal collection; nothing on a server one. */
function useOwnerLine(): (option: CollectionOption) => ReactNode {
  const t = useT();
  const { data: profiles = [] } = useProfiles();
  const { profile } = useCurrentProfile();
  return (option) => {
    if (option.source !== "user" || !option.creator_profile_id) return undefined;
    if (option.creator_profile_id === profile?.id) return t("web.home.addRow.collections.yours");
    return t("web.collections.posterCard.byOwner", {
      owner: <LineName name={ownerName(profiles, option.creator_profile_id)} />,
    });
  };
}

function ChosenList({
  labelId,
  refs,
  describe,
  onChange,
}: {
  labelId: string;
  refs: CollectionRef[];
  describe: (ref: CollectionRef) => ChosenLine;
  onChange: (refs: CollectionRef[]) => void;
}) {
  const t = useT();
  const sensors = useGripDragSensors();
  const keys = refs.map(collectionRefKey);
  // dnd-kit reports the item as over itself right after pickup; announcing
  // that would talk over "Picked up". Only a change of position is announced.
  const lastOver = useRef<string | null>(null);
  const titleOf = (key: unknown) => {
    const ref = refs.find((entry) => collectionRefKey(entry) === key);
    return (ref && describe(ref).title) || t("web.home.addRow.collections.unavailable");
  };
  const at = (key: unknown) => ({ position: keys.indexOf(String(key)) + 1, total: keys.length });
  const announcements: Announcements = {
    onDragStart: ({ active }) => {
      lastOver.current = String(active.id);
      return t("web.home.addRow.collections.drag.pickedUp", {
        ...at(active.id),
        collection: titleOf(active.id),
      });
    },
    onDragOver: ({ active, over }) => {
      if (!over || String(over.id) === lastOver.current) return undefined;
      lastOver.current = String(over.id);
      return t("web.home.addRow.collections.drag.over", {
        ...at(over.id),
        collection: titleOf(active.id),
      });
    },
    onDragEnd: ({ active, over }) =>
      over
        ? t("web.home.addRow.collections.drag.droppedAt", {
            ...at(over.id),
            collection: titleOf(active.id),
          })
        : t("web.home.addRow.collections.drag.dropped", { collection: titleOf(active.id) }),
    onDragCancel: ({ active }) =>
      t("web.home.addRow.collections.drag.canceled", { collection: titleOf(active.id) }),
  };

  function move(from: number, to: number) {
    if (to < 0 || to >= refs.length) return;
    onChange(arrayMove(refs, from, to));
  }

  function handleDragEnd({ active, over }: DragEndEvent) {
    if (!over || active.id === over.id) return;
    move(keys.indexOf(String(active.id)), keys.indexOf(String(over.id)));
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      accessibility={{
        announcements,
        screenReaderInstructions: {
          draggable: t("web.home.addRow.collections.drag.instructions"),
        },
      }}
      onDragEnd={handleDragEnd}
    >
      <SortableContext items={keys} strategy={verticalListSortingStrategy}>
        <ol
          aria-labelledby={labelId}
          className="border-border divide-border m-0 list-none divide-y rounded-xl border p-0"
        >
          {refs.map((ref, index) => {
            const line = describe(ref);
            return (
              <ChosenItem key={keys[index]} id={keys[index] ?? ""} line={line}>
                <MoveButtons
                  title={line.title}
                  first={index === 0}
                  last={index === refs.length - 1}
                  onUp={() => move(index, index - 1)}
                  onDown={() => move(index, index + 1)}
                  onRemove={() => onChange(refs.filter((_, at) => at !== index))}
                />
              </ChosenItem>
            );
          })}
        </ol>
      </SortableContext>
    </DndContext>
  );
}

function ChosenItem({ id, line, children }: { id: string; line: ChosenLine; children: ReactNode }) {
  const t = useT();
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useMotionSafeSortable({ id });
  return (
    <li
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "bg-background relative flex items-center gap-2 py-1.5 ps-1.5 pe-1.5 first:rounded-t-xl last:rounded-b-xl",
        isDragging && "ring-ring/40 z-10 shadow-lg ring-2",
      )}
    >
      <button
        type="button"
        ref={setActivatorNodeRef}
        {...attributes}
        {...listeners}
        aria-label={t("web.home.addRow.collections.dragHandle", { collection: line.title })}
        className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 grid size-8 shrink-0 cursor-grab touch-none place-items-center rounded-md outline-none focus-visible:ring-[3px] active:cursor-grabbing"
      >
        <GripVertical aria-hidden className="size-4" />
      </button>
      {line.missing ? (
        <span
          aria-hidden
          className="bg-warning/10 text-warning ring-warning/30 grid aspect-[2/3] w-9 shrink-0 place-items-center rounded-md ring-1 ring-inset"
        >
          <AlertTriangle className="size-4" />
        </span>
      ) : (
        <PosterArt
          posterUrl={line.option?.poster_url || undefined}
          thumbhash={line.option?.poster_thumbhash}
          className="ring-border/60 aspect-[2/3] w-9 shrink-0 rounded-md ring-1 ring-inset"
        />
      )}
      <span className="min-w-0 flex-1">
        {line.title ? (
          // A cut name keeps its start in either script; w-fit keeps a short
          // one on the page's start side.
          <span
            dir="auto"
            className={cn(
              "block w-fit max-w-full truncate text-sm font-semibold",
              line.missing && "text-warning font-medium",
            )}
          >
            {line.title}
          </span>
        ) : (
          <Skeleton className="h-4 w-32 max-w-full" />
        )}
        {line.meta ? (
          <span className="text-muted-foreground block truncate text-[0.8125rem]">{line.meta}</span>
        ) : null}
      </span>
      {children}
    </li>
  );
}

function MoveButtons({
  title,
  first,
  last,
  onUp,
  onDown,
  onRemove,
}: {
  title: string;
  first: boolean;
  last: boolean;
  onUp: () => void;
  onDown: () => void;
  onRemove: () => void;
}) {
  const t = useT();
  return (
    <span className="flex shrink-0 items-center">
      <Button
        type="button"
        size="icon"
        variant="ghost"
        className="size-8"
        aria-label={t("web.home.addRow.collections.moveUp", { collection: title })}
        disabled={first}
        onClick={onUp}
      >
        <ArrowUp aria-hidden className="size-4" />
      </Button>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        className="size-8"
        aria-label={t("web.home.addRow.collections.moveDown", { collection: title })}
        disabled={last}
        onClick={onDown}
      >
        <ArrowDown aria-hidden className="size-4" />
      </Button>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        className="text-muted-foreground hover:text-destructive size-8"
        aria-label={t("web.home.addRow.collections.remove", { collection: title })}
        onClick={onRemove}
      >
        <X aria-hidden className="size-4" />
      </Button>
    </span>
  );
}

function Chooser({
  labelId,
  collections,
  chosen,
  full,
  owner,
  onToggle,
}: {
  labelId: string;
  collections: RowCollections;
  chosen: ReadonlySet<string>;
  full: boolean;
  owner: (option: CollectionOption) => ReactNode;
  onToggle: (option: CollectionOption) => void;
}) {
  const t = useT();
  const search = useCollectionSearch(collections.options);
  return (
    <div className="grid gap-2">
      <span id={labelId} className="text-sm font-medium">
        {t("web.home.addRow.collections.addLabel")}
      </span>
      <div className="border-border overflow-hidden rounded-2xl border">
        <CollectionSearchBar
          count={collections.options.length}
          query={search.query}
          onQueryChange={search.setQuery}
          filter={search.filter}
          onFilterChange={search.setFilter}
          libraries={search.libraries}
          library={search.library}
          onLibraryChange={search.setLibrary}
        />
        {search.shown.length > 0 ? (
          <ul
            aria-labelledby={labelId}
            className="m-0 grid max-h-[280px] list-none gap-0.5 overflow-y-auto p-1.5"
          >
            {search.shown.map((option) => (
              <ChooserOption
                key={optionRefKey(option)}
                option={option}
                added={chosen.has(optionRefKey(option))}
                disabled={full}
                ownerLine={owner(option)}
                onToggle={() => onToggle(option)}
              />
            ))}
          </ul>
        ) : (
          <p role="status" className="text-muted-foreground px-4 py-6 text-sm">
            {collectionEmptyText(t, collections, search.query.trim(), search.library !== "all")}
          </p>
        )}
      </div>
    </div>
  );
}

function ChooserOption({
  option,
  added,
  disabled,
  ownerLine,
  onToggle,
}: {
  option: CollectionOption;
  added: boolean;
  /** The row is full: only a chosen collection can be pressed, to take it out. */
  disabled: boolean;
  ownerLine?: ReactNode;
  onToggle: () => void;
}) {
  const t = useT();
  const metaId = useId();
  const meta = <MetaLine parts={[collectionOptionMeta(option), ownerLine]} />;
  return (
    <li>
      <button
        type="button"
        aria-pressed={added}
        aria-label={option.title}
        aria-describedby={metaId}
        disabled={disabled && !added}
        onClick={onToggle}
        className="hover:bg-accent/60 focus-visible:ring-ring/50 aria-pressed:bg-accent grid w-full grid-cols-[36px_minmax(0,1fr)_auto] items-center gap-3 rounded-xl px-3 py-2 text-start outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-60"
      >
        <CollectionOptionBody
          title={option.title}
          posterUrl={option.poster_url}
          thumbhash={option.poster_thumbhash}
          meta={meta}
          metaId={metaId}
        />
        <span
          aria-hidden
          className={cn(
            "inline-flex h-7 items-center gap-1 rounded-full px-2.5 text-xs font-semibold ring-1 ring-inset",
            added
              ? "bg-background/70 ring-border text-foreground"
              : "text-muted-foreground ring-border/70",
          )}
        >
          {added ? <Check className="text-success size-3.5" /> : <Plus className="size-3.5" />}
          {added ? t("web.home.addRow.collections.added") : t("web.home.addRow.collections.add")}
        </span>
      </button>
    </li>
  );
}
