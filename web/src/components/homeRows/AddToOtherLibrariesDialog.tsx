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
import { useT } from "@/i18n";
import { appLanguage } from "@/lib/appLocale";
import { useHomeRowTitle } from "@/lib/sectionTitle";

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
  const t = useT();
  const rowTitle = useHomeRowTitle();

  async function confirm() {
    if (busyRef.current || selectedIds.length === 0) return;
    busyRef.current = true;
    setBusy(true);
    try {
      await onCopy(selectedIds);
      const names = pages.filter((page) => selectedIds.includes(page.id)).map((page) => page.label);
      const list = new Intl.ListFormat(appLanguage(), { style: "long", type: "conjunction" });
      toast.success(t("web.home.rows.addToOtherLibraries.added", { pages: list.format(names) }));
      setOpen(false);
    } catch (error) {
      if (error instanceof RowChangedError) setChanged(true);
      else
        toast.error(
          error instanceof Error && error.message
            ? error.message
            : t("web.home.rows.addToOtherLibraries.failed"),
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
          <DialogTitle>
            {t("web.home.rows.addToOtherLibraries.title", { row: rowTitle(row) })}
          </DialogTitle>
          <DialogDescription>
            {t("web.home.rows.addToOtherLibraries.description")}
          </DialogDescription>
        </DialogHeader>
        <LibraryPageChips
          pages={pages}
          currentId={currentId}
          currentNote={t("web.home.rows.addToOtherLibraries.alreadyHere")}
          selectedIds={selectedIds}
          onChange={setSelectedIds}
          maxSelected={BULK_LIBRARY_LIMIT}
          help={t(
            row.hero
              ? "web.home.rows.addToOtherLibraries.helpHero"
              : "web.home.rows.addToOtherLibraries.helpRow",
          )}
        />
        {changed ? (
          <p role="alert" className="text-sm">
            {t("web.home.rows.addToOtherLibraries.changed", { row: rowTitle(row) })}
          </p>
        ) : null}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => setOpen(false)}>
            {t("common.cancel")}
          </Button>
          <Button disabled={busy || changed || selectedIds.length === 0} onClick={confirm}>
            <Copy aria-hidden className="size-4" />
            {selectedIds.length === 0
              ? t("web.home.rows.addToOtherLibraries.confirmNone")
              : t("web.home.rows.addToOtherLibraries.confirm", { count: selectedIds.length })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
