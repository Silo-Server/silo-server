import { useCallback, useMemo, useState } from "react";
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCorners,
  useDroppable,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import type { DragEndEvent } from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  rectSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Check, GripVertical, Pencil, Plus, Trash2, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { UNGROUPED_LABEL, buildGroupedSections } from "@/lib/collectionGroups";
import type { GroupedSection } from "@/lib/collectionGroups";

const GROUP_DROPPABLE_PREFIX = "group:";
const GROUP_BODY_DROPPABLE_PREFIX = "group-body:";

export interface GroupedItem {
  id: string;
  group_id?: string | null;
}

export interface GroupDef {
  id: string;
  name: string;
  sort_order: number;
}

type PreparedRename = { title: string; commit: (title: string) => void };

export interface GroupedCollectionsBoardProps<T extends GroupedItem> {
  items: T[];
  groups: GroupDef[];
  renderItem: (item: T) => React.ReactNode;
  getItemId?: (item: T) => string;
  readOnly?: boolean;
  hideEmptyGroups?: boolean;
  ungroupedTitle?: string;
  onBeginDrag?: (id: string) => void;
  onReorderInGroup?: (groupId: string | null, orderedIds: string[]) => void;
  onMoveItemAcross?: (itemId: string, toGroupId: string | null) => void;
  onReorderGroups?: (orderedIds: string[]) => void;
  onAddGroup?: (title: string) => void;
  onRenameGroup?: (id: string, title: string) => void;
  onDeleteGroup?: (id: string) => void;
  onPrepareRenameGroup?: (id: string) => Promise<PreparedRename | undefined>;
}

export function GroupedCollectionsBoard<T extends GroupedItem>({
  items,
  groups,
  renderItem,
  getItemId = (item) => item.id,
  readOnly = false,
  hideEmptyGroups = false,
  ungroupedTitle = "Ungrouped",
  onReorderInGroup,
  onMoveItemAcross,
  onReorderGroups,
  onAddGroup,
  onRenameGroup,
  onDeleteGroup,
  onPrepareRenameGroup,
  onBeginDrag,
}: GroupedCollectionsBoardProps<T>) {
  const sections: GroupedSection<T>[] = useMemo(
    () =>
      buildGroupedSections(items, groups, {
        hideEmpty: hideEmptyGroups,
        ungroupedTitle,
      }),
    [items, groups, hideEmptyGroups, ungroupedTitle],
  );

  const itemIdToGroup = useMemo(() => {
    const map = new Map<string, string>();
    for (const item of items) map.set(getItemId(item), item.group_id ?? UNGROUPED_LABEL);
    return map;
  }, [items, getItemId]);

  const explicitIdsInOrder = useMemo(
    () =>
      sections
        .map((s) => s.id)
        .filter((id): id is string => id !== null && groups.some((g) => g.id === id)),
    [sections, groups],
  );

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      const { active, over } = event;
      if (!over) return;
      const activeId = String(active.id);
      const overId = String(over.id);
      if (activeId === overId) return;

      if (
        activeId.startsWith(GROUP_DROPPABLE_PREFIX) &&
        overId.startsWith(GROUP_DROPPABLE_PREFIX)
      ) {
        if (!onReorderGroups) return;
        const fromId = activeId.slice(GROUP_DROPPABLE_PREFIX.length);
        const toId = overId.slice(GROUP_DROPPABLE_PREFIX.length);
        const oldIndex = explicitIdsInOrder.indexOf(fromId);
        const newIndex = explicitIdsInOrder.indexOf(toId);
        if (oldIndex === -1 || newIndex === -1) return;
        onReorderGroups(arrayMove(explicitIdsInOrder, oldIndex, newIndex));
        return;
      }

      const fromGroup = itemIdToGroup.get(activeId);
      if (fromGroup === undefined) return;
      const toGroup = overId.startsWith(GROUP_BODY_DROPPABLE_PREFIX)
        ? overId.slice(GROUP_BODY_DROPPABLE_PREFIX.length)
        : overId.startsWith(GROUP_DROPPABLE_PREFIX)
          ? overId.slice(GROUP_DROPPABLE_PREFIX.length)
          : itemIdToGroup.get(overId);
      if (toGroup === undefined) return;

      if (fromGroup === toGroup) {
        if (!onReorderInGroup) return;
        const groupItems =
          sections.find((s) => (s.id ?? UNGROUPED_LABEL) === fromGroup)?.items ?? [];
        const ids = groupItems.map(getItemId);
        const oldIndex = ids.indexOf(activeId);
        const newIndex = ids.indexOf(overId);
        if (oldIndex === -1 || newIndex === -1) return;
        onReorderInGroup(
          fromGroup === UNGROUPED_LABEL ? null : fromGroup,
          arrayMove(ids, oldIndex, newIndex),
        );
        return;
      }

      onMoveItemAcross?.(activeId, toGroup === UNGROUPED_LABEL ? null : toGroup);
    },
    [
      sections,
      itemIdToGroup,
      explicitIdsInOrder,
      onReorderInGroup,
      onMoveItemAcross,
      onReorderGroups,
      getItemId,
    ],
  );

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCorners}
      onDragStart={(event) => onBeginDrag?.(String(event.active.id))}
      onDragEnd={handleDragEnd}
    >
      <SortableContext
        items={explicitIdsInOrder.map((id) => `${GROUP_DROPPABLE_PREFIX}${id}`)}
        strategy={verticalListSortingStrategy}
      >
        <div className="space-y-10">
          {sections.map((section) => {
            const sectionKey = section.id ?? UNGROUPED_LABEL;
            const isUngrouped = section.id === null;
            const isExplicit = section.id !== null && groups.some((g) => g.id === section.id);
            return (
              <SectionBlock
                key={sectionKey || "__ungrouped__"}
                section={section}
                renderItem={renderItem}
                getItemId={getItemId}
                readOnly={readOnly}
                isExplicit={isExplicit}
                isUngrouped={isUngrouped}
                onRenameGroup={onRenameGroup}
                onPrepareRenameGroup={onPrepareRenameGroup}
                onDeleteGroup={onDeleteGroup}
              />
            );
          })}
        </div>
      </SortableContext>

      {!readOnly && onAddGroup ? <AddGroupRow onAddGroup={onAddGroup} /> : null}
    </DndContext>
  );
}

function SectionBlock<T extends GroupedItem>({
  section,
  renderItem,
  getItemId,
  readOnly,
  isExplicit,
  isUngrouped,
  onRenameGroup,
  onDeleteGroup,
  onPrepareRenameGroup,
}: {
  section: GroupedSection<T>;
  renderItem: (item: T) => React.ReactNode;
  getItemId: (item: T) => string;
  readOnly: boolean;
  isExplicit: boolean;
  isUngrouped: boolean;
  onRenameGroup?: (id: string, title: string) => void;
  onDeleteGroup?: (id: string) => void;
  onPrepareRenameGroup?: (id: string) => Promise<PreparedRename | undefined>;
}) {
  // Whole section is droppable so empty groups still accept drops.
  const sectionKey = section.id ?? UNGROUPED_LABEL;
  const droppableId = `${GROUP_DROPPABLE_PREFIX}${sectionKey}`;
  const bodyDroppableId = `${GROUP_BODY_DROPPABLE_PREFIX}${sectionKey}`;
  const { setNodeRef: setDroppableRef, isOver } = useDroppable({ id: bodyDroppableId });

  const sortableGroup = useSortable({
    id: droppableId,
    disabled: readOnly || !isExplicit,
  });

  const headerStyle: React.CSSProperties = {
    transform: CSS.Transform.toString(sortableGroup.transform),
    transition: sortableGroup.transition,
    opacity: sortableGroup.isDragging ? 0.5 : 1,
  };

  return (
    <section ref={sortableGroup.setNodeRef} style={headerStyle} className="space-y-4">
      <CollectionGroupHeader
        title={section.name}
        count={section.items.length}
        readOnly={readOnly}
        canManage={isExplicit}
        muted={isUngrouped}
        dragAttributes={sortableGroup.attributes}
        dragListeners={sortableGroup.listeners}
        onPrepareRename={
          isExplicit && onPrepareRenameGroup && section.id
            ? () => onPrepareRenameGroup(section.id!)
            : undefined
        }
        onRename={
          isExplicit && onRenameGroup && section.id
            ? (title) => onRenameGroup(section.id!, title)
            : undefined
        }
        onDelete={
          isExplicit && onDeleteGroup && section.id ? () => onDeleteGroup(section.id!) : undefined
        }
      />
      <div
        ref={setDroppableRef}
        className={`rounded-2xl transition-colors ${
          isOver && section.items.length === 0
            ? "border-primary/40 border border-dashed bg-white/[0.02] p-4"
            : ""
        }`}
      >
        <SortableContext items={section.items.map(getItemId)} strategy={rectSortingStrategy}>
          {section.items.length === 0 ? (
            <EmptyGroupPlaceholder />
          ) : (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {section.items.map((item) => (
                <div key={getItemId(item)}>{renderItem(item)}</div>
              ))}
            </div>
          )}
        </SortableContext>
      </div>
    </section>
  );
}

function CollectionGroupHeader({
  title,
  count,
  readOnly,
  canManage,
  muted,
  dragAttributes,
  dragListeners,
  onRename,
  onPrepareRename,
  onDelete,
}: {
  title: string;
  count: number;
  readOnly: boolean;
  canManage: boolean;
  muted: boolean;
  dragAttributes: ReturnType<typeof useSortable>["attributes"];
  dragListeners: ReturnType<typeof useSortable>["listeners"];
  onRename?: (title: string) => void;
  onPrepareRename?: () => Promise<PreparedRename | undefined>;
  onDelete?: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [preparing, setPreparing] = useState(false);
  const [renameSnapshot, setRenameSnapshot] = useState({ commit: onRename });
  const [draft, setDraft] = useState(title);

  const commit = () => {
    const next = draft.trim();
    if (next && next !== title) renameSnapshot.commit?.(next);
    setEditing(false);
  };
  const cancel = () => {
    setDraft(title);
    setEditing(false);
  };

  return (
    <header className="group/header flex items-center gap-3 border-b border-white/5 pb-2">
      {!readOnly && canManage ? (
        <button
          type="button"
          aria-label={`Drag group ${title}`}
          className="hover:bg-surface-hover -ml-1 cursor-grab touch-none rounded-md p-1 opacity-0 transition group-hover/header:opacity-100 [@media(pointer:coarse)]:opacity-100"
          {...dragAttributes}
          {...dragListeners}
        >
          <GripVertical className="text-muted-foreground h-4 w-4" />
        </button>
      ) : null}

      {editing ? (
        <Input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") commit();
            if (e.key === "Escape") cancel();
          }}
          className="h-8 max-w-xs"
        />
      ) : (
        <h2
          className={`text-2xl font-light tracking-tight ${muted ? "text-muted-foreground" : ""}`}
        >
          {title}
        </h2>
      )}
      <span className="text-muted-foreground text-xs tabular-nums">{count}</span>
      <div className="ml-auto flex items-center gap-1">
        {editing ? (
          <>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              aria-label="Save"
              onClick={commit}
            >
              <Check className="h-3.5 w-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              aria-label="Cancel"
              onClick={cancel}
            >
              <X className="h-3.5 w-3.5" />
            </Button>
          </>
        ) : !readOnly && canManage ? (
          <div className="opacity-0 transition group-hover/header:opacity-100 [@media(pointer:coarse)]:opacity-100">
            {onRename || onPrepareRename ? (
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                aria-label="Rename group"
                disabled={preparing}
                onClick={async () => {
                  setPreparing(true);
                  try {
                    const prepared = onPrepareRename
                      ? await onPrepareRename()
                      : { title, commit: onRename! };
                    if (!prepared) return;
                    setRenameSnapshot({ commit: prepared.commit });
                    setDraft(prepared.title);
                    setEditing(true);
                  } finally {
                    setPreparing(false);
                  }
                }}
              >
                <Pencil className="h-3.5 w-3.5" />
              </Button>
            ) : null}
            {onDelete ? (
              <Button
                variant="ghost"
                size="icon"
                className="text-destructive hover:bg-destructive/10 hover:text-destructive h-7 w-7"
                aria-label="Delete group"
                onClick={onDelete}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
    </header>
  );
}

function EmptyGroupPlaceholder() {
  return (
    <div className="text-muted-foreground/60 rounded-xl border border-dashed border-white/10 px-4 py-6 text-center text-xs">
      Drop a collection here
    </div>
  );
}

function AddGroupRow({ onAddGroup }: { onAddGroup: (title: string) => void }) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");

  if (!open) {
    return (
      <div className="pt-4">
        <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
          <Plus className="mr-1 h-4 w-4" />
          Add group
        </Button>
      </div>
    );
  }

  const commit = () => {
    const next = draft.trim();
    if (next) onAddGroup(next);
    setDraft("");
    setOpen(false);
  };

  return (
    <div className="flex items-center gap-2 pt-4">
      <Input
        autoFocus
        value={draft}
        placeholder="Group title"
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit();
          if (e.key === "Escape") {
            setDraft("");
            setOpen(false);
          }
        }}
        className="h-8 max-w-xs"
      />
      <Button variant="ghost" size="sm" onClick={commit}>
        <Check className="mr-1 h-3.5 w-3.5" /> Add
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => {
          setDraft("");
          setOpen(false);
        }}
      >
        Cancel
      </Button>
    </div>
  );
}

/** Helper hook that exposes a sortable for a collection card so callers can
 * keep using their existing card markup without depending on the board's
 * internals. The board only requires that each item be draggable with its id. */
export function useGroupedCollectionCard(id: string, disabled = false) {
  return useSortable({ id, disabled });
}
