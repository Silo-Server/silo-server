import { EyeOff, Library, Pencil, RotateCcw, Trash2, type LucideIcon } from "lucide-react";
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
 * Settings > Home Screen: confirms dropping everything this profile changed
 * on one page, and says what that brings back and what goes.
 */
export function ResetProfileDialog({
  open,
  pageName,
  ownTitles,
  onConfirm,
  onOpenChange,
}: {
  open: boolean;
  /** "Home" or "Movies page". */
  pageName: string;
  /** Rows this profile added on the page, which the reset deletes. */
  ownTitles: string[];
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const returnFocus = useReturnFocus();
  const bullets: Array<[LucideIcon, string | null]> = [
    [EyeOff, "Rows you hid or removed come back, in the server's order."],
    [Pencil, "Rows you renamed or changed go back to how the server has them."],
    [Trash2, ownTitles.length > 0 ? `Rows you added (${ownTitles.join(", ")}) are deleted.` : null],
    [Library, "Your other pages and other profiles don't change."],
  ];
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="gap-5 sm:max-w-[560px]" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Reset your {pageName} to the server&apos;s rows?</DialogTitle>
          <DialogDescription>
            Your {pageName} goes back to the rows the server shows everyone.
          </DialogDescription>
        </DialogHeader>
        <ul
          aria-label="What changes"
          className="text-foreground/85 m-0 grid list-none gap-2.5 p-0 text-sm leading-normal"
        >
          {bullets.map(([Icon, text]) =>
            text ? (
              <li key={text} className="grid grid-cols-[18px_1fr] gap-2.5">
                <Icon aria-hidden className="text-muted-foreground mt-[3px] size-4" />
                <span>{text}</span>
              </li>
            ) : null,
          )}
        </ul>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-[13px]">This can&apos;t be undone.</p>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={onConfirm}>
              <RotateCcw />
              Reset
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
