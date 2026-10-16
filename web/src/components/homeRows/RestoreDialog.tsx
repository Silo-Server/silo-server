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
import type { PageRef } from "@/lib/homeRows/types";

/** Collection rows named in the dialog before it says "and N more". */
const NAMED_COLLECTION_ROWS = 3;

/** "Home" or "the Movies page", for use inside a sentence. */
function pageName(page: PageRef, label: string): string {
  return page.kind === "home" ? "Home" : `the ${label} page`;
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** "A", "A and B", "A, B and C", "A, B, C and 2 more". */
function listTitles(titles: string[]): string {
  const named = titles.slice(0, NAMED_COLLECTION_ROWS);
  const rest = titles.length - named.length;
  const parts = rest > 0 ? [...named, `${rest} more`] : named;
  return parts.length === 1 ? parts[0]! : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;
}

function rowsLine(rowCount: number, name: string): string {
  if (rowCount === 0) return `${capitalize(name)} gets Silo's default rows.`;
  return rowCount === 1
    ? `The 1 row on ${name} is replaced by Silo's default rows.`
    : `The ${rowCount} rows on ${name} are replaced by Silo's default rows.`;
}

function collectionLine(titles: string[]): string | null {
  if (titles.length === 0) return null;
  if (titles.length === 1)
    return `${titles[0]} shows a collection and is removed. The collection itself stays in Collections.`;
  return `Rows that show collections, like ${listTitles(titles)}, are removed. The collections themselves stay in Collections.`;
}

/** Every page the restore changes, for use inside a sentence. */
function targetName(page: PageRef, label: string, restoreAll: boolean): string {
  if (!restoreAll) return pageName(page, label);
  return page.kind === "home" ? "Home and every library page" : "every library page";
}

/** The profile pages the reset clears: "Home", "Movies page", "library pages"… */
function profilePagesName(page: PageRef, label: string, restoreAll: boolean): string {
  if (restoreAll) return page.kind === "home" ? "Home and library pages" : "library pages";
  return page.kind === "home" ? "Home" : `${label} page`;
}

function otherPagesLine(
  page: PageRef,
  otherLibraryLabels: string[],
  restoreAll: boolean,
): string | null {
  const others = otherLibraryLabels.join(", ");
  if (restoreAll) {
    const restored = `also go back to Silo's default rows, and their collection rows are removed.`;
    return page.kind === "home"
      ? `Library pages (${others}) ${restored}`
      : `The other library pages (${others}) ${restored} Home doesn't change.`;
  }
  if (page.kind === "home")
    return otherLibraryLabels.length > 0 ? `Library pages (${others}) don't change.` : null;
  return otherLibraryLabels.length > 0
    ? `Home and the other library pages (${others}) don't change.`
    : "Home doesn't change.";
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
  const id = useId();
  const name = pageName(page, pageLabel);
  const all = restoreAll && otherLibraryLabels.length > 0;
  const target = targetName(page, pageLabel, all);
  const bullets: Array<[LucideIcon, string | null]> = [
    [Rows3, rowsLine(rowCount, name)],
    [Trash2, collectionLine(collectionRowTitles)],
    [Library, otherPagesLine(page, otherLibraryLabels, all)],
  ];
  const reset = resetSupported && resetProfiles;
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent className="gap-5 sm:max-w-[640px]">
        {/* Keeps the title clear of the close button. */}
        <DialogHeader className="px-6 sm:pr-6 sm:pl-0">
          <DialogTitle>Restore {target} to the default rows?</DialogTitle>
          <DialogDescription>
            {capitalize(target)} {all && page.kind === "home" ? "go" : "goes"} back to the rows Silo
            starts with.
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
        {otherLibraryLabels.length > 0 ? (
          <OptionRow
            id={`${id}-all`}
            label={
              page.kind === "home"
                ? "Also restore every library page"
                : "Also restore the other library pages"
            }
            help={`${otherLibraryLabels.join(", ")} go back to Silo's default rows too.`}
            checked={all}
            disabled={busy}
            onCheckedChange={onRestoreAllChange}
          />
        ) : null}
        <OptionRow
          id={`${id}-reset`}
          label={`Also reset every profile's ${profilePagesName(page, pageLabel, all)}`}
          help={
            reset
              ? `Clears what each profile hid, renamed, moved or added on ${target}.`
              : "Profiles keep rows they added. What they hid or renamed on the old rows no longer applies."
          }
          note={resetSupported ? undefined : "Resetting profiles isn't available on this server."}
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
            <p className="min-w-0 flex-1">
              These rows changed since you opened this. Reload to see the current rows, then try
              again.
            </p>
            <Button size="sm" variant="outline" disabled={busy} onClick={onReload}>
              Reload rows
            </Button>
          </div>
        ) : null}
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-[13px]">This can&apos;t be undone.</p>
          <div className="flex gap-2">
            <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            {conflict ? null : (
              <Button variant="destructive" disabled={busy || !canConfirm} onClick={onConfirm}>
                <RotateCcw />
                Restore defaults
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
