import { useState, type FormEvent } from "react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { deleteShelfDescription, deleteShelfTitle } from "@/lib/collections/copy";

/**
 * Names a new shelf, or renames one. Save stays off until the name has text
 * (and, when renaming, differs); `onSave` resolves once the server took it.
 */
export function ShelfNameDialog({
  mode,
  libraryName,
  initialName = "",
  onSave,
  onClose,
}: {
  mode: "create" | "rename";
  libraryName: string;
  initialName?: string;
  onSave: (name: string) => Promise<void>;
  onClose: () => void;
}) {
  const [name, setName] = useState(initialName);
  const [saving, setSaving] = useState(false);
  const trimmed = name.trim();
  const unchanged = mode === "rename" && trimmed === initialName.trim();

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!trimmed || unchanged || saving) return;
    setSaving(true);
    try {
      await onSave(trimmed);
      onClose();
    } catch {
      // The mutation reports the failure; keep the dialog and the typed name.
      setSaving(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={(event) => void submit(event)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{mode === "create" ? "New shelf" : "Rename shelf"}</DialogTitle>
            <DialogDescription>
              A heading on {libraryName} › Collections. Only {libraryName} changes.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="shelf-name">Name</Label>
            <Input
              id="shelf-name"
              value={name}
              autoFocus
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!trimmed || unchanged || saving}>
              {mode === "create" ? "Add shelf" : "Save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Asks before deleting a shelf: its collections move to No heading, nothing
 * else changes. It closes as Delete shelf is pressed; the delete reports a failure.
 */
export function DeleteShelfDialog({
  name,
  collectionCount,
  libraryName,
  onConfirm,
  onClose,
}: {
  name: string;
  collectionCount: number;
  libraryName: string;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={deleteShelfTitle(name)}
      description={deleteShelfDescription(collectionCount, libraryName)}
      confirmLabel="Delete shelf"
      variant="destructive"
      onConfirm={onConfirm}
    />
  );
}
