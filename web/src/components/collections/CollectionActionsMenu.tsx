import type { Ref } from "react";
import { Eye, House, Library as LibraryIcon, Pencil, RefreshCw, Trash2, Users } from "lucide-react";

import { ActionMenu, type ActionMenuItem } from "@/components/calm/ActionMenu";
import {
  ADD_TO_HOME,
  ADD_TO_HOME_HELP,
  ADD_TO_LIBRARY_PAGE,
  ADD_TO_LIBRARY_PAGE_HELP,
  ADD_TO_MY_HOME_HELP,
  ADD_TO_MY_HOME_ITEM,
  SHOW_TO_OTHER_PROFILES_LABEL,
  SHOW_TO_OTHER_PROFILES_SHORT_HELP,
} from "@/lib/collections/copy";
import type { PageRef } from "@/lib/homeRows/types";

/** Where a collection can be opened: one item, or a submenu when there are several. */
export interface OpenInLibraries {
  libraries: ReadonlyArray<{ id: number; name: string }>;
  onOpen: (libraryId: number) => void;
  /** Why it can't be opened now, shown under the item. */
  disabledReason?: string;
}

function openInItem({ libraries, onOpen, disabledReason }: OpenInLibraries): ActionMenuItem {
  const shared = { icon: Eye, disabled: Boolean(disabledReason), help: disabledReason };
  if (libraries.length === 1) {
    const [library] = libraries;
    return {
      ...shared,
      key: "open",
      label: `Open in ${library!.name}`,
      onSelect: () => onOpen(library!.id),
    };
  }
  return {
    ...shared,
    key: "open",
    label: "Open in",
    items: libraries.map((library) => ({
      key: `open-${library.id}`,
      label: library.name,
      icon: Eye,
      onSelect: () => onOpen(library.id),
    })),
  };
}

/**
 * Adding a collection as a Home row or a library page row: a server
 * collection on everyone's pages, or (`mine`) one on the viewer's own Home.
 */
export interface AddAsRow {
  /** The library pages offered, the collection's own libraries first. */
  libraries: ReadonlyArray<{ id: number; name: string }>;
  onAdd: (page: PageRef) => void;
  /** Why it can't be added now, shown under each item. */
  disabledReason?: string;
  /** The viewer's own Home ("Add to my Home…"). */
  mine?: boolean;
}

function addAsRowItems({ libraries, onAdd, disabledReason, mine }: AddAsRow): ActionMenuItem[] {
  const disabled = Boolean(disabledReason);
  const home: ActionMenuItem = {
    key: "add-home",
    label: mine ? ADD_TO_MY_HOME_ITEM : ADD_TO_HOME,
    help: disabledReason ?? (mine ? ADD_TO_MY_HOME_HELP : ADD_TO_HOME_HELP),
    icon: House,
    // Your own card's menu runs on from Edit and Sync now (spec §5.3).
    group: !mine,
    disabled,
    onSelect: () => onAdd({ kind: "home" }),
  };
  if (libraries.length === 0) return [home];
  const page = {
    key: "add-library",
    label: ADD_TO_LIBRARY_PAGE,
    help: disabledReason ?? ADD_TO_LIBRARY_PAGE_HELP,
    icon: LibraryIcon,
    disabled,
  };
  const pick = (libraryId: number) => () => onAdd({ kind: "library", libraryId });
  return [
    home,
    libraries.length === 1
      ? { ...page, onSelect: pick(libraries[0]!.id) }
      : {
          ...page,
          items: libraries.map((library) => ({
            key: `add-library-${library.id}`,
            label: library.name,
            icon: LibraryIcon,
            onSelect: pick(library.id),
          })),
        },
  ];
}

/**
 * The ⋯ on a collection the viewer may change: Edit collection, Open in for a
 * server collection, Sync now for a synced list, Add to Home and Add to a
 * library page for a server collection (Add to my Home… for your own), the
 * sharing switch on a multi-profile account, and Delete…. Leave out a handler
 * and its item is left out. `placement="poster"` draws the trigger over a
 * card's artwork.
 */
export function CollectionActionsMenu({
  name,
  onEdit,
  openIn,
  sync,
  addRow,
  share,
  onDelete,
  triggerRef,
  placement = "poster",
}: {
  name: string;
  onEdit: () => void;
  openIn?: OpenInLibraries;
  sync?: { onSync: () => void; syncing: boolean };
  addRow?: AddAsRow;
  share?: { shared: boolean; onChange: (shared: boolean) => void; disabled?: boolean };
  onDelete: () => void;
  triggerRef?: Ref<HTMLButtonElement>;
  placement?: "poster" | "row";
}) {
  const items: ActionMenuItem[] = [
    { key: "edit", label: "Edit collection", icon: Pencil, onSelect: onEdit },
  ];
  if (openIn && openIn.libraries.length > 0) items.push(openInItem(openIn));
  if (sync) {
    items.push({
      key: "sync",
      label: sync.syncing ? "Syncing…" : "Sync now",
      icon: RefreshCw,
      disabled: sync.syncing,
      onSelect: sync.onSync,
    });
  }
  if (addRow) items.push(...addAsRowItems(addRow));
  if (share) {
    items.push({
      key: "share",
      label: SHOW_TO_OTHER_PROFILES_LABEL,
      help: SHOW_TO_OTHER_PROFILES_SHORT_HELP,
      icon: Users,
      group: true,
      checked: share.shared,
      disabled: share.disabled,
      onCheckedChange: share.onChange,
    });
  }
  items.push({
    key: "delete",
    label: "Delete…",
    icon: Trash2,
    destructive: true,
    group: true,
    onSelect: onDelete,
  });
  return (
    <ActionMenu
      label={`More for ${name}`}
      items={items}
      triggerRef={triggerRef}
      triggerClassName={
        placement === "poster"
          ? // Over a poster: a dark chip that stays readable on any artwork.
            "size-8 max-lg:size-9 bg-black/55 text-white backdrop-blur-sm hover:bg-black/70 hover:text-white data-[state=open]:bg-black/80 data-[state=open]:text-white"
          : undefined
      }
    />
  );
}
