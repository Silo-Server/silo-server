import { useId } from "react";
import { CircleAlert, Library, RotateCcw, Rows3, Trash2, type LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useT, type Translate } from "@/i18n";
import { appLanguage } from "@/lib/appLocale";
import type { PageRef } from "@/lib/homeRows/types";

/** Collection rows named in the dialog before it says "and N more". */
const NAMED_COLLECTION_ROWS = 3;

/**
 * Which pages a sentence names, for the messages' `target` select: Home, one
 * library page, Home and every library page, or every library page (`other`).
 */
type Target = "home" | "library" | "homeAll" | "other";

function targetOf(page: PageRef, restoreAll: boolean): Target {
  if (!restoreAll) return page.kind === "home" ? "home" : "library";
  return page.kind === "home" ? "homeAll" : "other";
}

/** "A", "A and B", "A, B and C", "A, B, C and 2 more". */
function listTitles(t: Translate, titles: string[]): string {
  const [first = "", second = "", third = ""] = titles;
  const rest = titles.length - NAMED_COLLECTION_ROWS;
  if (rest > 0)
    return t("web.home.rows.restore.listMore", { first, second, third, more_count: rest });
  if (titles.length === 3) return t("web.home.rows.restore.listTriple", { first, second, third });
  if (titles.length === 2) return t("web.home.rows.restore.listPair", { first, second });
  return first;
}

function rowsLine(t: Translate, rowCount: number, page: PageRef, label: string): string {
  const values = { target: targetOf(page, false), page: label };
  if (rowCount === 0) return t("web.home.rows.restore.rowsEmpty", values);
  return t("web.home.rows.restore.rows", { ...values, row_count: rowCount });
}

function collectionLine(t: Translate, titles: string[]): string | null {
  if (titles.length === 0) return null;
  if (titles.length === 1) return t("web.home.rows.restore.collectionOne", { row: titles[0]! });
  return t("web.home.rows.restore.collectionMany", { rows: listTitles(t, titles) });
}

function otherPagesLine(
  t: Translate,
  page: PageRef,
  otherLibraryLabels: string[],
  restoreAll: boolean,
): string | null {
  const libraries = listNames(otherLibraryLabels);
  if (restoreAll) {
    return page.kind === "home"
      ? t("web.home.rows.restore.allFromHome", { libraries })
      : t("web.home.rows.restore.allFromLibrary", { libraries });
  }
  if (page.kind === "home")
    return otherLibraryLabels.length > 0
      ? t("web.home.rows.restore.librariesUnchanged", { libraries })
      : null;
  return otherLibraryLabels.length > 0
    ? t("web.home.rows.restore.homeAndLibrariesUnchanged", { libraries })
    : t("web.home.rows.restore.homeUnchanged");
}

/** Library names as a list: "Movies, TV Shows" in English. */
function listNames(names: string[]): string {
  return new Intl.ListFormat(appLanguage(), { type: "unit", style: "short" }).format(names);
}

function OptionRow({
  id,
  label,
  help,
  note,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string;
  label: string;
  help: string;
  note?: string;
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="border-border flex items-center justify-between gap-4 rounded-[14px] border px-4 py-3.5">
      <div className="min-w-0">
        <Label htmlFor={id} className="text-sm font-medium">
          {label}
        </Label>
        <p id={`${id}-help`} className="text-muted-foreground mt-1 text-[13px]">
          {help}
        </p>
        {note ? <p className="text-muted-foreground mt-1 text-[13px]">{note}</p> : null}
      </div>
      <Switch
        id={id}
        aria-describedby={`${id}-help`}
        checked={checked}
        disabled={disabled}
        onCheckedChange={(next) => onCheckedChange(next === true)}
      />
    </div>
  );
}

/**
 * Confirms putting back Silo's default rows on one page, and optionally on the
 * other library pages too. It says what goes, that collections stay, which
 * pages are untouched, and what the optional reset of every profile clears.
 * This page's version was read when it opened; a 412 keeps the dialog and asks
 * for an explicit reload. The other library pages are restored over the version
 * they have when the restore reaches them.
 */
export function RestoreDialog({
  open,
  page,
  pageLabel,
  otherLibraryLabels,
  rowCount,
  collectionRowTitles,
  restoreAll,
  onRestoreAllChange,
  resetSupported,
  resetProfiles,
  onResetProfilesChange,
  conflict,
  busy,
  canConfirm,
  onConfirm,
  onReload,
  onOpenChange,
}: {
  open: boolean;
  page: PageRef;
  pageLabel: string;
  /** The library pages other than this one, in switcher order. */
  otherLibraryLabels: string[];
  /** Rows on the page when the dialog opened. */
  rowCount: number;
  /** Titles of the page's collection rows. */
  collectionRowTitles: string[];
  /** Also restore every library page in `otherLibraryLabels`. */
  restoreAll: boolean;
  onRestoreAllChange: (restoreAll: boolean) => void;
  resetSupported: boolean;
  resetProfiles: boolean;
  onResetProfilesChange: (reset: boolean) => void;
  conflict: boolean;
  busy: boolean;
  canConfirm: boolean;
  onConfirm: () => void;
  onReload: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const t = useT();
  const id = useId();
  const all = restoreAll && otherLibraryLabels.length > 0;
  const target = { target: targetOf(page, all), page: pageLabel };
  const bullets: Array<[LucideIcon, string | null]> = [
    [Rows3, rowsLine(t, rowCount, page, pageLabel)],
    [Trash2, collectionLine(t, collectionRowTitles)],
    [Library, otherPagesLine(t, page, otherLibraryLabels, all)],
  ];
  const reset = resetSupported && resetProfiles;
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent className="gap-5 sm:max-w-[640px]">
        {/* Keeps the title clear of the close button. */}
        <DialogHeader className="px-6 sm:pr-6 sm:pl-0">
          <DialogTitle>{t("web.home.rows.restore.title", target)}</DialogTitle>
          <DialogDescription>{t("web.home.rows.restore.description", target)}</DialogDescription>
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
        {otherLibraryLabels.length > 0 ? (
          <OptionRow
            id={`${id}-all`}
            label={
              page.kind === "home"
                ? t("web.home.rows.restore.alsoEveryLibrary")
                : t("web.home.rows.restore.alsoOtherLibraries")
            }
            help={t("web.home.rows.restore.alsoHelp", {
              libraries: listNames(otherLibraryLabels),
            })}
            checked={all}
            disabled={busy}
            onCheckedChange={onRestoreAllChange}
          />
        ) : null}
        <OptionRow
          id={`${id}-reset`}
          label={t("web.home.rows.restore.resetLabel", target)}
          help={
            reset
              ? t("web.home.rows.restore.resetHelpOn", target)
              : t("web.home.rows.restore.resetHelpOff")
          }
          note={resetSupported ? undefined : t("web.home.rows.restore.resetUnsupported")}
          checked={reset}
          disabled={!resetSupported || busy}
          onCheckedChange={onResetProfilesChange}
        />
        {conflict ? (
          <div
            role="alert"
            className="border-warning/40 bg-warning/10 flex items-center gap-3 rounded-[14px] border px-4 py-3 text-sm"
          >
            <CircleAlert aria-hidden className="text-warning size-[18px] shrink-0" />
            <p className="min-w-0 flex-1">{t("web.home.rows.restore.conflict")}</p>
            <Button size="sm" variant="outline" disabled={busy} onClick={onReload}>
              {t("web.home.rows.reloadRows")}
            </Button>
          </div>
        ) : null}
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-[13px]">{t("web.home.rows.cantUndo")}</p>
          <div className="flex gap-2">
            <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            {conflict ? null : (
              <Button variant="destructive" disabled={busy || !canConfirm} onClick={onConfirm}>
                <RotateCcw />
                {t("web.home.rows.restore.confirm")}
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
