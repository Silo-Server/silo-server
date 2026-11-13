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
import { useT } from "@/i18n";
import { appLanguage } from "@/lib/appLocale";
import { profilePageKind } from "@/lib/homeRows/describe";
import type { PageRef } from "@/lib/homeRows/types";
import { useReturnFocus } from "./useReturnFocus";

/**
 * Settings > Home Screen: confirms dropping everything this profile changed
 * on one page, and says what that brings back and what goes.
 */
export function ResetProfileDialog({
  open,
  page,
  pageLabel,
  ownTitles,
  onConfirm,
  onOpenChange,
}: {
  open: boolean;
  page: PageRef;
  /** The library's name on a library page. */
  pageLabel: string;
  /** Rows this profile added on the page, which the reset deletes. */
  ownTitles: string[];
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const t = useT();
  const returnFocus = useReturnFocus();
  const where = { page_kind: profilePageKind(page, pageLabel), page: pageLabel };
  // "A, B, C" in English, the language's own list punctuation elsewhere.
  const ownList = new Intl.ListFormat(appLanguage(), { type: "unit", style: "short" });
  const bullets: Array<[LucideIcon, string | null]> = [
    [EyeOff, t("web.home.rows.reset.hidden")],
    [Pencil, t("web.home.rows.reset.changed")],
    [
      Trash2,
      ownTitles.length > 0
        ? t("web.home.rows.reset.added", { rows: ownList.format(ownTitles) })
        : null,
    ],
    [Library, t("web.home.rows.reset.otherPages")],
  ];
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="gap-5 sm:max-w-[560px]" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>{t("web.home.rows.profilePage.resetTitle", where)}</DialogTitle>
          <DialogDescription>
            {t("web.home.rows.profilePage.resetDescription", where)}
          </DialogDescription>
        </DialogHeader>
        <ul
          aria-label={t("web.home.rows.whatChanges")}
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
          <p className="text-muted-foreground text-[13px]">{t("web.home.rows.cantUndo")}</p>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={onConfirm}>
              <RotateCcw />
              {t("web.home.rows.reset.confirm")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
