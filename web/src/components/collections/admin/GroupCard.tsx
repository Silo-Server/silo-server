import { useId, type ReactNode } from "react";
import { useDroppable } from "@dnd-kit/core";
import { SortableContext, rectSortingStrategy, useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Info, MinusCircle, Pin, Users } from "lucide-react";

import type { GroupSortMode, LibraryCollection } from "@/api/types";
import { Grip, type ListRowProps } from "@/components/calm/ListRow";
import {
  MY_COLLECTIONS_NOTE,
  MY_COLLECTIONS_NO_DROP,
  MY_COLLECTIONS_TAG,
  NO_HEADING_HELP,
  PINNED_BAND,
} from "@/lib/collections/copy";
import {
  SHELF_ORDER_LABEL,
  pinnedBand,
  shelfCountLine,
  shownCollections,
  type Shelf,
} from "@/lib/collections/shelves";
import { cn } from "@/lib/utils";

/** What a shelf hands each card so it can be dragged by its grip only. */
export interface SortableCardProps {
  handleProps?: ListRowProps["handleProps"];
  ref: (node: HTMLLIElement | null) => void;
  style: React.CSSProperties;
  dragging: boolean;
}

/** Drag data, read by the board to tell what moved and where it landed. */
export type ArrangeDragData =
  | { kind: "shelf"; id: string }
  /**
   * `banded`: in its shelf's pinned band, which only pinned collections may
   * join. `pinned`: the collection is pinned, so it may join a band even when
   * its own shelf sorts itself and has none.
   */
  | { kind: "collection"; id: string; shelfId: string; banded: boolean; pinned: boolean }
  | { kind: "body"; shelfId: string };

const SORT_MODES = Object.keys(SHELF_ORDER_LABEL) as GroupSortMode[];

function SortableCard({
  collection,
  shelfId,
  banded,
  disabled,
  children,
}: {
  collection: LibraryCollection;
  shelfId: string;
  banded: boolean;
  disabled: boolean;
  children: (props: SortableCardProps) => ReactNode;
}) {
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: `col:${collection.id}`,
    disabled,
    data: {
      kind: "collection",
      id: collection.id,
      shelfId,
      banded,
      pinned: collection.featured,
    } satisfies ArrangeDragData,
  });
  return children({
    ref: setNodeRef,
    style: { transform: CSS.Translate.toString(transform), transition },
    dragging: isDragging,
    handleProps: { ...attributes, ...listeners, ref: setActivatorNodeRef },
  });
}

/**
 * One shelf on Arrange: grip, name and count, its Order (saved for viewers)
 * and ⋯, then its collections as cards in the order viewers see them. On a
 * Your order shelf, pinned collections sit in a band at the start.
 * My collections holds each viewer's own collections, so it shows a note
 * instead of cards and refuses server collections while one is dragged.
 * No heading has neither Order nor ⋯.
 */
export function GroupCard({
  shelf,
  collapsed,
  dragDisabled,
  showGrips,
  noDrop,
  orderDisabled,
  onSortChange,
  menu,
  renderCard,
}: {
  shelf: Shelf;
  /** While a shelf is dragged, every shelf shrinks to its header. */
  collapsed: boolean;
  dragDisabled: boolean;
  /** Phones have no drag, so no grips. */
  showGrips: boolean;
  /** A server collection is being dragged: My collections can't take it. */
  noDrop: boolean;
  orderDisabled: boolean;
  onSortChange: (mode: GroupSortMode) => void;
  menu: ReactNode;
  renderCard: (collection: LibraryCollection, sortable: SortableCardProps) => ReactNode;
}) {
  const mine = shelf.kind === "user_collections";
  const loose = shelf.kind === "ungrouped";
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: `shelf:${shelf.id}`,
    disabled: dragDisabled,
    data: { kind: "shelf", id: shelf.id } satisfies ArrangeDragData,
  });
  // My collections' body stays a drop target so a card dragged there is
  // announced as refused; the board never moves a server collection onto it.
  const { setNodeRef: setBodyRef, isOver } = useDroppable({
    id: `body:${shelf.id}`,
    data: { kind: "body", shelfId: shelf.id } satisfies ArrangeDragData,
  });
  const bandLabelId = useId();
  const cards = shownCollections(shelf);
  const bandIds = new Set(pinnedBand(shelf).map((entry) => entry.id));
  const banded = cards.filter((collection) => bandIds.has(collection.id));
  const rest = cards.filter((collection) => !bandIds.has(collection.id));
  const cardsDisabled = dragDisabled || !showGrips;
  const sortableCards = (list: LibraryCollection[]) => (
    <ol className="m-0 grid list-none gap-2 p-0 sm:grid-cols-2">
      {list.map((collection) => (
        <SortableCard
          key={collection.id}
          collection={collection}
          shelfId={shelf.id}
          banded={bandIds.has(collection.id)}
          disabled={cardsDisabled}
        >
          {(sortable) =>
            renderCard(collection, showGrips ? sortable : { ...sortable, handleProps: undefined })
          }
        </SortableCard>
      ))}
    </ol>
  );
  // The grip reads "Move shelf Studios".
  const gripName = loose ? "the collections with no heading" : `shelf ${shelf.name}`;

  return (
    <section
      ref={setNodeRef}
      aria-label={loose ? shelf.name : `Shelf ${shelf.name}`}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(
        "surface-panel grid gap-2.5 rounded-[22px] p-3",
        isDragging && "z-10 shadow-lg",
        mine && noDrop && "border-destructive/70 bg-destructive/5 border border-dashed",
      )}
    >
      <div
        className={cn(
          "grid items-center gap-2",
          showGrips
            ? "grid-cols-[28px_minmax(0,1fr)_auto_auto]"
            : "grid-cols-[minmax(0,1fr)_auto_auto] pl-1",
        )}
      >
        {showGrips ? (
          <Grip
            title={gripName}
            collapsed={false}
            handleProps={{ ...attributes, ...listeners, ref: setActivatorNodeRef }}
          />
        ) : null}
        <h3 className="m-0 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[15px] font-semibold tracking-[-0.01em]">
          <span className="min-w-0 break-words">{shelf.name}</span>
          {mine ? (
            <span className="bg-info/15 text-info ring-info/30 inline-flex h-[22px] items-center gap-1 rounded-full px-2 text-[11.5px] font-semibold ring-1 ring-inset">
              <Users aria-hidden className="size-3" />
              {MY_COLLECTIONS_TAG}
            </span>
          ) : (
            <small className="text-muted-foreground text-[12.5px] font-normal">
              {loose ? NO_HEADING_HELP : shelfCountLine(shelf)}
            </small>
          )}
        </h3>
        {mine && noDrop ? (
          <span className="text-destructive inline-flex items-center gap-1.5 text-[12.5px] font-medium">
            <MinusCircle aria-hidden className="size-3.5" />
            {MY_COLLECTIONS_NO_DROP}
          </span>
        ) : loose || mine ? (
          <span />
        ) : (
          <label className="text-muted-foreground inline-flex items-center gap-2 text-[12.5px]">
            {/* Phones keep the select and drop the word, which the select's label still says. */}
            <span className="max-lg:sr-only">Order</span>
            <select
              value={shelf.sortMode}
              disabled={orderDisabled}
              aria-label={`Order of ${shelf.name}`}
              onChange={(event) => onSortChange(event.target.value as GroupSortMode)}
              className="border-input bg-background text-foreground h-8 rounded-[9px] border px-2 text-[13px] max-lg:h-11 max-lg:max-w-[140px]"
            >
              {SORT_MODES.map((mode) => (
                <option key={mode} value={mode}>
                  {SHELF_ORDER_LABEL[mode]}
                </option>
              ))}
            </select>
          </label>
        )}
        {menu ?? <span />}
      </div>

      {collapsed ? null : mine ? (
        <p
          ref={setBodyRef}
          className="bg-muted/30 text-muted-foreground m-0 flex items-start gap-2.5 rounded-[14px] px-3.5 py-3 text-[13px] leading-normal"
        >
          <Info aria-hidden className="mt-0.5 size-4 shrink-0" />
          {MY_COLLECTIONS_NOTE}
        </p>
      ) : (
        <div ref={setBodyRef} className={cn("rounded-[14px]", isOver && "bg-accent/40")}>
          {cards.length === 0 ? (
            <p className="border-border text-muted-foreground m-0 rounded-[14px] border border-dashed px-4 py-4 text-center text-[13px]">
              {loose
                ? "Collections on no shelf show here, without a title."
                : "Drag a collection here, or use a collection's ⋯ › Move to shelf."}
            </p>
          ) : (
            <SortableContext
              items={cards.map((collection) => `col:${collection.id}`)}
              strategy={rectSortingStrategy}
            >
              {banded.length > 0 ? (
                <div
                  role="group"
                  aria-labelledby={bandLabelId}
                  className={cn(
                    "border-border/70 bg-muted/20 grid gap-2 rounded-[16px] border p-2",
                    rest.length > 0 && "mb-2",
                  )}
                >
                  <span
                    id={bandLabelId}
                    className="text-muted-foreground flex items-center gap-1.5 px-1 text-[12px]"
                  >
                    <Pin aria-hidden className="size-3" />
                    {PINNED_BAND}
                  </span>
                  {sortableCards(banded)}
                </div>
              ) : null}
              {rest.length > 0 ? sortableCards(rest) : null}
            </SortableContext>
          )}
        </div>
      )}
    </section>
  );
}
