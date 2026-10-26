import { useId } from "react";
import { Link } from "react-router";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  CHECKING_ROWS,
  KEEP_ROWS_HINT,
  OPEN_ROW,
  ROWS_FAILED,
  TITLES_STAY,
  deleteWithRowsDescription,
  deleteWithRowsLabel,
  serverDeleteDescription,
} from "@/lib/collections/copy";
import { openRowPath, type RowsState } from "@/lib/collections/rows";

import { CollectionRowSummary } from "./CollectionRowSummary";

/**
 * Confirms deleting one server collection. With no rows showing it (or when
 * the server doesn't report rows: `rows` null) it's the plain confirm naming
 * every library it leaves. When rows show it, it lists them, each with Open
 * row, and its button deletes the rows first, then the collection. That
 * button doesn't close the dialog: the caller closes it once the delete is
 * done, or keeps it open with `error`.
 */
export function DeleteCollectionDialog({
  open,
  onOpenChange,
  title,
  libraryNames,
  rows,
  rowLibraryNames,
  isPending,
  error,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  libraryNames: readonly string[];
  rows: RowsState | null;
  /** Names for the rows' library pages. */
  rowLibraryNames: ReadonlyMap<number, string>;
  isPending: boolean;
  error?: string | null;
  onConfirm: () => void;
}) {
  const id = useId();
  if (!rows || (rows.status === "ready" && rows.rows.length === 0)) {
    return (
      <ConfirmDialog
        open={open}
        onOpenChange={onOpenChange}
        title={title}
        description={serverDeleteDescription(libraryNames)}
        confirmLabel="Delete"
        variant="destructive"
        isPending={isPending}
        error={error}
        onConfirm={onConfirm}
      />
    );
  }
  const listed = rows.status === "ready" ? rows.rows : [];
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next && !isPending) onOpenChange(false);
      }}
    >
      <AlertDialogContent aria-describedby={`${id}-description`} className="sm:max-w-[560px]">
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription id={`${id}-description`}>
            {rows.status === "ready"
              ? deleteWithRowsDescription(libraryNames, listed.length)
              : serverDeleteDescription(libraryNames)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {rows.status === "loading" ? (
          <p role="status" className="text-muted-foreground text-sm">
            {CHECKING_ROWS}
          </p>
        ) : null}
        {rows.status === "error" ? (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
            <span>{ROWS_FAILED}</span>
            <Button variant="outline" size="sm" onClick={rows.onRetry}>
              Retry
            </Button>
          </div>
        ) : null}
        {listed.length > 0 ? (
          <>
            <ul className="border-border/80 divide-border/70 grid divide-y rounded-xl border">
              {listed.map((row) => (
                <li key={row.id} className="flex items-center gap-3 px-3 py-2.5">
                  <CollectionRowSummary row={row} libraryNames={rowLibraryNames} />
                  <Link
                    to={openRowPath(row)}
                    aria-label={`${OPEN_ROW}: ${row.title}`}
                    className="shrink-0 text-[13px] font-medium underline underline-offset-4"
                  >
                    {OPEN_ROW}
                  </Link>
                </li>
              ))}
            </ul>
            <p className="text-muted-foreground text-sm">{KEEP_ROWS_HINT}</p>
          </>
        ) : null}
        {error ? (
          <p role="alert" className="text-destructive text-sm font-medium">
            {error}
          </p>
        ) : null}
        <AlertDialogFooter className="items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">{TITLES_STAY} This can&apos;t be undone.</p>
          <div className="flex gap-2">
            <AlertDialogCancel disabled={isPending}>Cancel</AlertDialogCancel>
            <Button
              variant="destructive"
              disabled={isPending || rows.status !== "ready"}
              onClick={onConfirm}
            >
              {isPending ? (
                <span
                  aria-hidden
                  className="mr-2 inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"
                />
              ) : null}
              {rows.status === "ready" ? deleteWithRowsLabel(listed.length) : "Delete"}
            </Button>
          </div>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
