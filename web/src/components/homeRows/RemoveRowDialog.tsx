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
import { profilePageKind } from "@/lib/homeRows/describe";
import type { PageRef } from "@/lib/homeRows/types";
import { useReturnFocus } from "./useReturnFocus";

/**
 * Settings > Home Screen: confirms taking a row off this profile's page. A
 * server row is removed (Reset brings it back); a row the profile added is
 * deleted.
 */
export function RemoveRowDialog({
  row,
  page,
  pageLabel,
  onConfirm,
  onOpenChange,
  skipReturnFocus,
}: {
  /** The row to take off, or null when the dialog is closed. */
  row: { title: string; own: boolean } | null;
  page: PageRef;
  /** The library's name on a library page. */
  pageLabel: string;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
  /** True once the row is gone, so focus doesn't go back to its menu. */
  skipReturnFocus?: () => boolean;
}) {
  const t = useT();
  const own = row?.own ?? false;
  const title = row?.title ?? "";
  const returnFocus = useReturnFocus(skipReturnFocus);
  const where = { page_kind: profilePageKind(page, pageLabel), page: pageLabel };
  return (
    <Dialog open={row !== null} onOpenChange={onOpenChange}>
      <DialogContent {...returnFocus}>
        <DialogHeader>
          <DialogTitle>
            {own
              ? t("web.home.rows.deleteTitle", { row: title })
              : t("web.home.rows.profilePage.removeTitle", { row: title, ...where })}
          </DialogTitle>
          <DialogDescription>
            {own
              ? t("web.home.rows.profilePage.removeDescriptionOwn", where)
              : t("web.home.rows.profilePage.removeDescription", where)}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">
            {own ? t("web.home.rows.cantUndo") : t("web.home.rows.remove.onlyThisProfile")}
          </p>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={onConfirm}>
              {own ? t("web.home.rows.deleteRow") : t("web.home.rows.remove.confirm")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
