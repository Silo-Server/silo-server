import { useId } from "react";

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { SAVE_FIRST_TITLE } from "@/lib/collections/copy";

/**
 * Asks before leaving the editor for Home rows with unsaved changes: save
 * them and carry on, drop them and carry on, or stay. Save and continue
 * keeps the dialog open until the save answers. `discardBlockedReason`
 * turns Discard off, saying why, when only saving would let Home rows add it;
 * `saveBlockedReason` turns Save and continue off when Save couldn't save.
 */
export function SaveFirstDialog({
  open,
  description,
  isSaving,
  discardBlockedReason = null,
  saveBlockedReason = null,
  onCancel,
  onDiscard,
  onSave,
}: {
  open: boolean;
  description: string;
  isSaving: boolean;
  discardBlockedReason?: string | null;
  saveBlockedReason?: string | null;
  onCancel: () => void;
  onDiscard: () => void;
  onSave: () => void;
}) {
  const reasonId = useId();
  const saveReasonId = useId();
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next && !isSaving) onCancel();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{SAVE_FIRST_TITLE}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
          {discardBlockedReason ? (
            <p id={reasonId} className="text-muted-foreground text-sm">
              {discardBlockedReason}
            </p>
          ) : null}
          {saveBlockedReason ? (
            <p id={saveReasonId} className="text-muted-foreground text-sm">
              {saveBlockedReason}
            </p>
          ) : null}
        </AlertDialogHeader>
        <AlertDialogFooter className="sm:justify-between">
          <AlertDialogCancel variant="ghost" disabled={isSaving}>
            Cancel
          </AlertDialogCancel>
          <div className="flex flex-col-reverse gap-2 sm:flex-row">
            <Button
              variant="outline"
              disabled={isSaving || Boolean(discardBlockedReason)}
              aria-describedby={discardBlockedReason ? reasonId : undefined}
              onClick={onDiscard}
            >
              Discard changes
            </Button>
            <Button
              disabled={isSaving || Boolean(saveBlockedReason)}
              aria-describedby={saveBlockedReason ? saveReasonId : undefined}
              onClick={onSave}
            >
              {isSaving ? (
                <span
                  aria-hidden
                  className="mr-2 inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"
                />
              ) : null}
              Save and continue
            </Button>
          </div>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
