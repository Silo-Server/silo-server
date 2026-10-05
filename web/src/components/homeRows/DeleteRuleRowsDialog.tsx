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
 * Deletes every rule row this profile added on a page, in one save. While
 * rule rows are off the server refuses any save that still holds one, so
 * they can only go together.
 */
export function DeleteRuleRowsDialog({
  open,
  titles,
  pageName,
  onConfirm,
  onOpenChange,
  skipReturnFocus,
}: {
  open: boolean;
  /** Every rule row the delete removes. */
  titles: string[];
  /** "Home" or "Movies page". */
  pageName: string;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
  /** True once the rows are gone with the button or menu that opened this. */
  skipReturnFocus?: () => boolean;
}) {
  const one = titles.length === 1;
  const returnFocus = useReturnFocus(skipReturnFocus);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent {...returnFocus}>
        <DialogHeader>
          <DialogTitle>{one ? "Delete your rule row?" : "Delete your rule rows?"}</DialogTitle>
          <DialogDescription>
            Rule rows are turned off on this server. Once{" "}
            {one ? "this row goes" : `these ${titles.length} rows go`}, you can change your{" "}
            {pageName} again.
          </DialogDescription>
        </DialogHeader>
        <ul aria-label="Rows to delete" className="m-0 grid list-disc gap-1 pl-5 text-sm">
          {titles.map((title, index) => (
            <li key={`${title}-${index}`}>{title}</li>
          ))}
        </ul>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">This can&apos;t be undone.</p>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={onConfirm}>
              {one ? "Delete rule row" : "Delete rule rows"}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
