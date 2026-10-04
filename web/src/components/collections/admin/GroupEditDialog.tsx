import { useState } from "react";
import type { GroupSortMode, LibraryCollectionGroup } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";

export interface GroupEditDialogProps {
  group: LibraryCollectionGroup | null; // null = create new
  mode: "create" | "edit";
  onSubmit: (input: { name: string; default_sort_mode: GroupSortMode }) => void;
  onDelete?: () => void;
  onCancel: () => void;
}

export function GroupEditDialog({
  group,
  mode,
  onSubmit,
  onDelete,
  onCancel,
}: GroupEditDialogProps) {
  const [name, setName] = useState(group?.name ?? "");
  const [sortMode, setSortMode] = useState<GroupSortMode>(group?.default_sort_mode ?? "manual");
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const isUserKind = group?.kind === "user_collections";

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="bg-background w-full max-w-md rounded-lg p-6 shadow-xl">
        <h2 className="mb-4 text-lg font-semibold">
          {mode === "create" ? "New group" : "Edit group"}
        </h2>

        <label className="block text-sm font-medium">
          Name
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="border-input bg-background text-foreground mt-1 w-full rounded-md border px-2 py-1.5"
            autoFocus
          />
        </label>

        <label className="mt-3 block text-sm font-medium">
          Default sort (end-user view)
          <select
            value={sortMode}
            onChange={(e) => setSortMode(e.target.value as GroupSortMode)}
            className="border-input bg-background text-foreground mt-1 w-full rounded-md border px-2 py-1.5"
          >
            <option value="manual">Manual (drag-drop order)</option>
            <option value="name_asc">Name A–Z</option>
            <option value="name_desc">Name Z–A</option>
            <option value="recent">Recently Updated</option>
            <option value="most_items">Most Items</option>
          </select>
        </label>

        <div className="mt-6 flex items-center gap-2">
          {mode === "edit" && !isUserKind && onDelete && (
            <>
              <button
                onClick={() => setConfirmingDelete(true)}
                className="text-destructive text-sm hover:underline"
                type="button"
              >
                Delete group
              </button>
              <ConfirmDialog
                open={confirmingDelete}
                onOpenChange={setConfirmingDelete}
                title="Delete group"
                description={`Delete group "${group?.name}"? Its collections move to Ungrouped. No collections are deleted.`}
                confirmLabel="Delete group"
                variant="destructive"
                onConfirm={onDelete}
              />
            </>
          )}
          <div className="flex-1" />
          <button onClick={onCancel} className="rounded border px-3 py-1.5 text-sm" type="button">
            Cancel
          </button>
          <button
            onClick={() => onSubmit({ name, default_sort_mode: sortMode })}
            disabled={!name.trim()}
            className="bg-primary text-primary-foreground rounded px-3 py-1.5 text-sm disabled:opacity-50"
            type="button"
          >
            {mode === "create" ? "Create" : "Save"}
          </button>
        </div>
      </div>
    </div>
  );
}
