import type { CSSProperties, ReactNode, Ref } from "react";
import { Check, EyeOff, Layers, Loader2, Pin } from "lucide-react";

import type { LibraryCollection } from "@/api/types";
import {
  Grip,
  ListRow,
  MetaDot,
  type ListRowProps,
  type ListRowSelection,
} from "@/components/calm/ListRow";
import { PosterPeek } from "@/components/calm/PosterPeek";
import { PosterArt } from "@/components/calm/PosterTile";
import type { PeekRequest } from "@/components/calm/usePeekLimiter";
import { formatRelativeTime } from "@/lib/date";
import { rowTag, showOnTabsLabel } from "@/lib/collections/adminList";
import { HIDDEN_FROM_TAB, HIDDEN_TAG, ON_HOME, PINNED } from "@/lib/collections/copy";
import { COLLECTION_KIND_LABEL, collectionKindOf } from "@/lib/collections/types";
import { cn } from "@/lib/utils";

const TAG =
  "inline-flex h-[22px] shrink-0 items-center gap-1 rounded-full px-2 text-[11.5px] font-semibold";

function Tag({ collection, visible }: { collection: LibraryCollection; visible: boolean }) {
  const tag = rowTag({ ...collection, visibility: visible ? "visible" : "hidden" });
  if (tag === "home")
    return (
      <span className={cn(TAG, "bg-success/15 text-success ring-success/30 ring-1 ring-inset")}>
        <Check aria-hidden className="size-3" />
        {ON_HOME}
      </span>
    );
  if (tag === "hidden")
    return (
      <span
        className={cn(TAG, "text-muted-foreground ring-border ring-1 ring-inset max-sm:hidden")}
      >
        <EyeOff aria-hidden className="size-3" />
        {HIDDEN_FROM_TAB}
      </span>
    );
  return null;
}

/** Failed or syncing lists say so at the end of the meta line; a healthy sync stays quiet. */
function SyncStatus({ collection, syncing }: { collection: LibraryCollection; syncing: boolean }) {
  if (syncing || collection.last_sync_status === "running")
    return (
      <>
        <MetaDot />
        <Loader2 aria-hidden className="mr-1 inline size-3 animate-spin align-[-1px]" />
        Syncing now
      </>
    );
  if (collection.last_sync_status !== "failed") return null;
  const when = formatRelativeTime(collection.last_sync_at);
  return (
    <>
      <MetaDot />
      <span
        title={collection.last_sync_message || undefined}
        className="text-destructive font-medium"
      >
        Sync failed{when ? ` ${when}` : ""}
        {collection.last_sync_message ? (
          <span className="sr-only">: {collection.last_sync_message}</span>
        ) : null}
      </span>
    </>
  );
}

interface ListItemProps {
  variant?: "row";
  collection: LibraryCollection;
  /** Every library it's in, by name. */
  libraryNames: readonly string[];
  /** False when one library is selected: every row is in it, so the meta line leaves it out. */
  showLibraries: boolean;
  peek: PeekRequest | null;
  /** The switch, which may run ahead of the saved `visibility` while a change saves. */
  visible: boolean;
  syncing: boolean;
  switchDisabled?: boolean;
  onVisibleChange: (visible: boolean) => void;
  onOpen: () => void;
  menu: ReactNode;
  /** Select mode: a checkbox at the start of the row. */
  selection?: ListRowSelection;
}

/**
 * One server collection on the List: its peek, name and at most one tag, the
 * meta line "type · libraries · N titles", the Collections tab switch and ⋯.
 * A hidden collection keeps a full row with a dimmed peek, because Home rows
 * can still show it. Clicking the row opens the editor.
 */
function ListItem({
  collection,
  libraryNames,
  showLibraries,
  peek,
  visible,
  syncing,
  switchDisabled,
  onVisibleChange,
  onOpen,
  menu,
  selection,
}: ListItemProps) {
  const titles = collection.item_count;
  return (
    <ListRow
      id={collection.id}
      selection={selection}
      title={collection.title}
      collapsed={false}
      collapsedText={null}
      art={
        <div className={cn(!visible && "opacity-45")}>
          <PosterPeek icon={Layers} request={peek} />
        </div>
      }
      tags={<Tag collection={collection} visible={visible} />}
      meta={
        <>
          {COLLECTION_KIND_LABEL[collectionKindOf(collection.collection_type)]}
          {showLibraries && libraryNames.length > 0 ? (
            <>
              <MetaDot />
              {libraryNames.join(", ")}
            </>
          ) : null}
          <MetaDot />
          {titles} title{titles === 1 ? "" : "s"}
          <SyncStatus collection={collection} syncing={syncing} />
        </>
      }
      shown={{
        checked: visible,
        disabled: switchDisabled,
        label: showOnTabsLabel(collection.title, libraryNames),
        onChange: onVisibleChange,
      }}
      menu={menu}
      onOpen={onOpen}
    />
  );
}

/** The pin glyph that marks a pinned collection: pinned state is a glyph, never a tag. */
export function PinGlyph({ className }: { className?: string }) {
  return (
    <span role="img" aria-label={PINNED} title={PINNED} className={cn("inline-flex", className)}>
      <Pin aria-hidden className="size-full" />
    </span>
  );
}

interface CompactItemProps {
  variant: "compact";
  collection: LibraryCollection;
  visible: boolean;
  /** Pinned to the start of its shelf, which may run ahead of the saved `featured`. */
  pinned?: boolean;
  /** The grip, from the sortable shelf. None on phones, where ⋯ moves it instead. */
  handleProps?: ListRowProps["handleProps"];
  menu: ReactNode;
  onOpen: () => void;
  ref?: Ref<HTMLLIElement>;
  style?: CSSProperties;
  dragging?: boolean;
}

/**
 * One collection on an Arrange shelf: grip, its poster, name and "type · N
 * titles", the pin when it's pinned, a Hidden tag when it's off the
 * Collections tab, and ⋯. A hidden collection stays on its shelf, dimmed,
 * because Home rows can still show it.
 */
function CompactItem({
  collection,
  visible,
  pinned = false,
  handleProps,
  menu,
  onOpen,
  ref,
  style,
  dragging,
}: CompactItemProps) {
  const titles = collection.item_count;
  return (
    <li
      ref={ref}
      style={style}
      data-collection-id={collection.id}
      className={cn(
        "bg-card/60 border-border/80 hover:bg-accent/50 relative grid min-h-[54px] items-center gap-2.5 rounded-[14px] border py-1.5 pr-1.5",
        handleProps
          ? "grid-cols-[28px_30px_minmax(0,1fr)_auto_auto] pl-1"
          : "grid-cols-[30px_minmax(0,1fr)_auto_auto] pl-3",
        dragging && "bg-surface-raised z-10 shadow-lg",
      )}
    >
      {handleProps ? (
        <Grip title={collection.title} collapsed={false} handleProps={handleProps} />
      ) : null}
      <PosterArt
        posterUrl={collection.poster_url}
        thumbhash={collection.poster_thumbhash}
        className={cn(
          "ring-border/60 h-[42px] w-[30px] rounded-[5px] ring-1",
          !visible && "opacity-45",
        )}
      />
      <div className={cn("min-w-0 cursor-pointer", !visible && "opacity-60")} onClick={onOpen}>
        <p className="truncate text-[14px] font-semibold tracking-[-0.01em]">{collection.title}</p>
        <p className="text-muted-foreground truncate text-[12.5px]">
          {COLLECTION_KIND_LABEL[collectionKindOf(collection.collection_type)]}
          <MetaDot />
          {titles} title{titles === 1 ? "" : "s"}
        </p>
      </div>
      <span className="inline-flex items-center gap-2">
        {pinned ? <PinGlyph className="text-muted-foreground size-3.5" /> : null}
        {visible ? null : (
          <span
            className={cn(TAG, "text-muted-foreground ring-border font-medium ring-1 ring-inset")}
          >
            <EyeOff aria-hidden className="size-3" />
            {HIDDEN_TAG}
          </span>
        )}
      </span>
      {menu}
    </li>
  );
}

/**
 * A server collection on the List (`variant` "row", the default), or as a
 * card on an Arrange shelf (`variant="compact"`).
 */
export function CollectionListItem(props: ListItemProps | CompactItemProps) {
  return props.variant === "compact" ? <CompactItem {...props} /> : <ListItem {...props} />;
}

/** The heads of the List's two columns: how many collections, and what the switch means. */
export function CollectionColumnHeader({ count }: { count: number }) {
  return (
    <div className="text-muted-foreground flex items-center justify-between px-4 pt-1 pb-2 text-[12.5px]">
      <span role="status">
        {count} collection{count === 1 ? "" : "s"}
      </span>
      <span aria-hidden className="pr-14 max-lg:pr-16">
        On Collections tab
      </span>
    </div>
  );
}
