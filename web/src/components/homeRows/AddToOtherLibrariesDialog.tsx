import { useRef, useState } from "react";
import { Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { BULK_LIBRARY_LIMIT } from "@/lib/homeRows/bulkCopy";
import { RowChangedError, type HomeRow, type LibraryPage } from "@/lib/homeRows/types";
import { LibraryPageChips } from "./addRow/LibraryPageChips";

const listFormat = new Intl.ListFormat("en", { style: "long", type: "conjunction" });

function confirmLabel(count: number) {
  if (count === 0) return "Add to pages";
  return count === 1 ? "Add to 1 page" : `Add to ${count} pages`;
}

/**
 * ⋯ Add to other libraries…: copies an existing row to other library pages.
 * The page it is on shows as "already here" and gets nothing new, so
 * "Add to 2 pages" means two new copies.
 */
export function AddToOtherLibrariesDialog({
  row,
  pages,
  currentId,
  onCopy,
  onClose,
}: {
  row: HomeRow;
  pages: readonly LibraryPage[];
  currentId: number;
  /** Rejects with RowChangedError when the row changed since the page loaded. */
  onCopy: (libraryIds: number[]) => Promise<unknown>;
  /** After the dialog closes, copied or not; the caller puts focus back on the row. */
  onClose: () => void;
}) {
  const [selectedIds, setSelectedIds] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [changed, setChanged] = useState(false);
  const [open, setOpen] = useState(true);
  const busyRef = useRef(false);

  async function confirm() {
    if (busyRef.current || selectedIds.length === 0) return;
    busyRef.current = true;
    setBusy(true);
    try {
      await onCopy(selectedIds);
      const names = pages.filter((page) => selectedIds.includes(page.id)).map((page) => page.label);
      toast.success(`Added to ${listFormat.format(names)}.`);
      setOpen(false);
    } catch (error) {
      if (error instanceof RowChangedError) setChanged(true);
      else
        toast.error(
          error instanceof Error && error.message ? error.message : "Could not add this row",
        );
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !busy && setOpen(next)}>
      <DialogContent
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          onClose();
        }}
      >
        <DialogHeader>
          <DialogTitle>Add {row.title} to other libraries</DialogTitle>
          <DialogDescription>
            Each page gets its own copy at the bottom, so you can change or remove it there later.
          </DialogDescription>
        </DialogHeader>
        <LibraryPageChips
          pages={pages}
          currentId={currentId}
          currentNote="already here"
          selectedIds={selectedIds}
          onChange={setSelectedIds}
          maxSelected={BULK_LIBRARY_LIMIT}
          help={
            row.hero
              ? "The copies aren't hero banners; this page keeps its own."
              : "Nothing changes on this page."
          }
        />
        {changed ? (
          <p role="alert" className="text-sm">
            {row.title} changed since you opened this page. Close this, reload the rows, and try
            again.
          </p>
        ) : null}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button disabled={busy || changed || selectedIds.length === 0} onClick={confirm}>
            <Copy aria-hidden className="size-4" />
            {confirmLabel(selectedIds.length)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
