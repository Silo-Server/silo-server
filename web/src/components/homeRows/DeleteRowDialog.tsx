import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useT } from "@/i18n";

/**
 * Confirms deleting one row for everyone. The row was read (with its version)
 * before this opened; a 412 keeps the dialog and asks for an explicit reload.
 */
export function DeleteRowDialog({
  rowTitle,
  pageLabel,
  description,
  open,
  conflict,
  busy,
  canConfirm,
  onConfirm,
  onReload,
  onOpenChange,
  onCloseAutoFocus,
}: {
  rowTitle: string;
  pageLabel: string;
  /** Replaces the line about the row and its collection. */
  description?: string;
  open: boolean;
  conflict: boolean;
  busy: boolean;
  canConfirm: boolean;
  onConfirm: () => void;
  onReload: () => void;
  onOpenChange: (open: boolean) => void;
  onCloseAutoFocus?: (event: Event) => void;
}) {
  const t = useT();
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent onCloseAutoFocus={onCloseAutoFocus}>
        <DialogHeader>
          <DialogTitle>{t("web.home.rows.deleteTitle", { row: rowTitle })}</DialogTitle>
          <DialogDescription>
            {description ?? t("web.home.rows.delete.description", { page: pageLabel })}
          </DialogDescription>
        </DialogHeader>
        {conflict ? (
          <p role="alert" className="text-sm">
            {t("web.home.rows.delete.conflict")}
          </p>
        ) : null}
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">{t("web.home.rows.cantUndo")}</p>
          <div className="flex gap-2">
            <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            {conflict ? (
              <Button disabled={busy} onClick={onReload}>
                {t("web.home.rows.delete.reloadRow")}
              </Button>
            ) : (
              <Button variant="destructive" disabled={busy || !canConfirm} onClick={onConfirm}>
                {t("web.home.rows.deleteRow")}
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Confirms deleting the rows picked in select mode. Their versions were read
 * before this opened, so rows that change meanwhile fail instead of going.
 */
export function DeleteRowsDialog({
  count,
  pageLabel,
  open,
  busy,
  progress,
  onConfirm,
  onOpenChange,
}: {
  count: number;
  pageLabel: string;
  open: boolean;
  busy: boolean;
  /** "Deleting 1 of 3…" while the deletes run. */
  progress?: string;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const t = useT();
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("web.home.rows.deleteMany.title", { count })}</DialogTitle>
          <DialogDescription>
            {t("web.home.rows.deleteMany.description", { count, page: pageLabel })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">{t("web.home.rows.cantUndo")}</p>
          <div className="flex gap-2">
            <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" disabled={busy} onClick={onConfirm}>
              {busy && progress ? progress : t("web.home.rows.deleteMany.confirm", { count })}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
