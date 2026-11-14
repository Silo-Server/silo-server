import {
  ArrowDownToLine,
  ArrowUpToLine,
  Eye,
  EyeOff,
  Move,
  Pencil,
  Pin,
  PinOff,
  Trash2,
} from "lucide-react";

import { ActionMenu, type ActionMenuItem } from "@/components/calm/ActionMenu";
import { pinLabel, showOnTabLabel, unpinLabel, pinHelp, unpinHelp } from "@/lib/collections/copy";
import { sortedBy, type Shelf } from "@/lib/collections/shelves";
import { useT } from "@/i18n";

/**
 * A shelf's ⋯: Rename shelf, Move to top, Move to bottom and Delete shelf….
 * My collections can be renamed and moved but never deleted. With `disabled`
 * (no shelf support, or the order still loading) every item says so by being off.
 */
export function ShelfMenu({
  shelf,
  isFirst,
  isLast,
  disabled,
  onRename,
  onMove,
  onDelete,
}: {
  shelf: Shelf;
  isFirst: boolean;
  isLast: boolean;
  disabled: boolean;
  onRename: () => void;
  onMove: (to: "top" | "bottom") => void;
  onDelete: () => void;
}) {
  const t = useT();
  const items: ActionMenuItem[] = [
    {
      key: "rename",
      label: t("web.collections.shelfDialogs.renameShelf"),
      icon: Pencil,
      disabled,
      onSelect: onRename,
    },
    {
      key: "top",
      label: t("web.collections.admin.moveToTop"),
      icon: ArrowUpToLine,
      disabled: disabled || isFirst,
      onSelect: () => onMove("top"),
    },
    {
      key: "bottom",
      label: t("web.collections.admin.moveToBottom"),
      icon: ArrowDownToLine,
      disabled: disabled || isLast,
      onSelect: () => onMove("bottom"),
    },
  ];
  if (shelf.kind === "regular")
    items.push({
      key: "delete",
      label: t("web.collections.admin.deleteShelfItem"),
      icon: Trash2,
      destructive: true,
      group: true,
      disabled,
      onSelect: onDelete,
    });
  return (
    <ActionMenu
      label={t("web.collections.admin.moreForShelf", { name: shelf.name })}
      items={items}
    />
  );
}

/**
 * A collection's ⋯ on an Arrange shelf: Edit collection, Move to shelf ▸
 * (every shelf that takes server collections, the current one checked), Pin
 * to the start of its shelf or Unpin, with a line on what that does on this
 * shelf, and the Collections tab: Hide from Collections tab, or Show on it
 * when hidden.
 */
export function ArrangeCardMenu({
  name,
  shelves,
  currentShelfId,
  canMove,
  visible,
  pinned,
  canPin,
  inSeveralLibraries,
  onEdit,
  onMove,
  onPinChange,
  onVisibleChange,
}: {
  name: string;
  shelves: readonly Shelf[];
  currentShelfId: string;
  canMove: boolean;
  visible: boolean;
  pinned: boolean;
  canPin: boolean;
  /** In more than one library, where Pin applies too. */
  inSeveralLibraries: boolean;
  onEdit: () => void;
  onMove: (shelfId: string) => void;
  onPinChange: (pinned: boolean) => void;
  onVisibleChange: (visible: boolean) => void;
}) {
  const t = useT();
  const current = shelves.find((shelf) => shelf.id === currentShelfId);
  const by = current ? sortedBy(current.sortMode) : null;
  const items: ActionMenuItem[] = [
    {
      key: "edit",
      label: t("web.collections.admin.editCollection"),
      icon: Pencil,
      onSelect: onEdit,
    },
    {
      key: "move",
      label: t("web.collections.admin.moveToShelf"),
      icon: Move,
      disabled: !canMove,
      selectedKey: currentShelfId,
      items: shelves.map((shelf) => ({
        key: shelf.id,
        label: shelf.name,
        icon: Move,
        onSelect: () => onMove(shelf.id),
      })),
    },
    {
      key: "pin",
      label: pinned ? unpinLabel() : pinLabel(),
      icon: pinned ? PinOff : Pin,
      help: pinned ? unpinHelp(by, inSeveralLibraries) : pinHelp(by, inSeveralLibraries),
      disabled: !canPin,
      onSelect: () => onPinChange(!pinned),
    },
    visible
      ? {
          key: "hide",
          label: t("web.collections.admin.hideFromTab"),
          icon: EyeOff,
          group: true,
          onSelect: () => onVisibleChange(false),
        }
      : {
          key: "show",
          label: showOnTabLabel(),
          icon: Eye,
          group: true,
          onSelect: () => onVisibleChange(true),
        },
  ];
  return <ActionMenu label={t("web.collections.admin.moreFor", { name })} items={items} />;
}
