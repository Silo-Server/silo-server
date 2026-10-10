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

function otherPagesLine(page: PageRef, otherLibraryLabels: string[]): string | null {
  const others = otherLibraryLabels.join(", ");
  if (page.kind === "home")
    return otherLibraryLabels.length > 0 ? `Library pages (${others}) don't change.` : null;
  return otherLibraryLabels.length > 0
    ? `Home and the other library pages (${others}) don't change.`
    : "Home doesn't change.";
}

/**
 * Confirms putting back Silo's default rows on one page. It says what goes,
 * that collections stay, which pages are untouched, and what the optional
 * reset of every profile clears. The page version was read when it opened; a
 * 412 keeps the dialog and asks for an explicit reload.
 */
export function RestoreDialog({
  open,
  page,
  pageLabel,
  otherLibraryLabels,
  rowCount,
  collectionRowTitles,
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
  const bullets: Array<[LucideIcon, string | null]> = [
    [Rows3, rowsLine(rowCount, name)],
    [Trash2, collectionLine(collectionRowTitles)],
    [Library, otherPagesLine(page, otherLibraryLabels)],
  ];
  const reset = resetSupported && resetProfiles;
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent className="gap-5 sm:max-w-[640px]">
        <DialogHeader>
          <DialogTitle>Restore {name} to the default rows?</DialogTitle>
          <DialogDescription>
            {capitalize(name)} goes back to the rows Silo starts with.
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
        <div className="border-border flex items-center justify-between gap-4 rounded-[14px] border px-4 py-3.5">
          <div className="min-w-0">
            <Label htmlFor={`${id}-reset`} className="text-sm font-medium">
              Also reset every profile&apos;s {page.kind === "home" ? "Home" : `${pageLabel} page`}
            </Label>
            <p id={`${id}-reset-help`} className="text-muted-foreground mt-1 text-[13px]">
              {reset
                ? `Clears what each profile hid, renamed, moved or added on ${name}.`
                : "Profiles keep rows they added. What they hid or renamed on the old rows no longer applies."}
            </p>
            {resetSupported ? null : (
              <p className="text-muted-foreground mt-1 text-[13px]">
                Resetting profiles isn&apos;t available on this server.
              </p>
            )}
          </div>
          <Switch
            id={`${id}-reset`}
            aria-describedby={`${id}-reset-help`}
            checked={reset}
            disabled={!resetSupported || busy}
            onCheckedChange={(checked) => onResetProfilesChange(checked === true)}
          />
        </div>
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
