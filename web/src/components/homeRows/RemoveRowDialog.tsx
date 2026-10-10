import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useReturnFocus } from "./useReturnFocus";

/**
 * Settings > Home Screen: confirms taking a row off this profile's page. A
 * server row is removed (Reset brings it back); a row the profile added is
 * deleted.
 */
export function RemoveRowDialog({
  row,
  pageName,
  onConfirm,
  onOpenChange,
  skipReturnFocus,
}: {
  /** The row to take off, or null when the dialog is closed. */
  row: { title: string; own: boolean } | null;
  /** "Home" or "Movies page". */
  pageName: string;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
  /** True once the row is gone, so focus doesn't go back to its menu. */
  skipReturnFocus?: () => boolean;
}) {
  const own = row?.own ?? false;
  const returnFocus = useReturnFocus(skipReturnFocus);
  return (
    <Dialog open={row !== null} onOpenChange={onOpenChange}>
      <DialogContent {...returnFocus}>
        <DialogHeader>
          <DialogTitle>
            {own ? `Delete ${row?.title}?` : `Remove ${row?.title} from your ${pageName}?`}
          </DialogTitle>
          <DialogDescription>
            {own
              ? `The row you added goes away from your ${pageName}.`
              : `You won't see this row on your ${pageName}. To keep it but hide it for now, turn its switch off instead. Reset in More brings it back.`}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">
            {own ? "This can't be undone." : "Only this profile changes."}
          </p>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={onConfirm}>
              {own ? "Delete row" : "Remove row"}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
