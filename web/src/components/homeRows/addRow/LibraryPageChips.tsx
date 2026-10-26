import { useId } from "react";
import { Checkbox } from "@/components/ui/checkbox";
import { BULK_LIBRARY_LIMIT } from "@/lib/homeRows/bulkCopy";
import type { LibraryPage } from "@/lib/homeRows/types";
import { cn } from "@/lib/utils";

/**
 * "Add to these library pages": one checkbox chip per library page. The page
 * being edited is always on and can't be turned off; `currentNote` says why
 * ("this page" while adding a row, "already here" for an existing one).
 */
export function LibraryPageChips({
  pages,
  currentId,
  currentNote,
  selectedIds,
  onChange,
  disabled = false,
  maxSelected,
  help,
}: {
  pages: readonly LibraryPage[];
  currentId: number;
  currentNote: string;
  /** The other pages picked; the current page is never in it. */
  selectedIds: readonly number[];
  onChange: (ids: number[]) => void;
  /** Every chip but the current page's is off and can't be picked. */
  disabled?: boolean;
  /** How many other pages one request can take; past it, the rest can't be picked. */
  maxSelected: number;
  help: string;
}) {
  const id = useId();
  const full = selectedIds.length >= maxSelected;
  const showLimit = full && !disabled;
  return (
    <div
      role="group"
      aria-labelledby={`${id}-label`}
      aria-describedby={showLimit ? `${id}-help ${id}-limit` : `${id}-help`}
    >
      <span id={`${id}-label`} className="mb-2 block text-sm leading-none font-medium">
        Add to these library pages
      </span>
      <div className="flex flex-wrap gap-2">
        {pages.map((page) => {
          const current = page.id === currentId;
          const checked = current || (!disabled && selectedIds.includes(page.id));
          const locked = disabled || (full && !checked);
          return (
            <label
              key={page.id}
              className={cn(
                "border-border has-focus-visible:ring-ring/50 inline-flex h-10 items-center gap-2.5 rounded-[12px] border pr-3.5 pl-3 text-sm font-medium transition-colors has-focus-visible:ring-[3px]",
                checked && "bg-accent border-foreground/55",
                current || locked
                  ? "cursor-default opacity-80"
                  : "hover:bg-accent/60 cursor-pointer",
              )}
            >
              <Checkbox
                checked={checked}
                // The current page stays in the tab order so keyboard users
                // hear that it is included; it just never toggles.
                aria-disabled={current || undefined}
                disabled={!current && locked}
                className="size-[18px] rounded-[5px] focus-visible:ring-0 aria-disabled:opacity-50"
                onCheckedChange={(next) => {
                  if (current) return;
                  onChange(
                    next === true
                      ? [...selectedIds, page.id]
                      : selectedIds.filter((selected) => selected !== page.id),
                  );
                }}
              />
              {page.label}
              {current ? (
                // Screen readers hear "Movies (this page)"; flex drops the space visually.
                <>
                  {" "}
                  <span className="text-muted-foreground text-xs font-normal">
                    <span className="sr-only">(</span>
                    {currentNote}
                    <span className="sr-only">)</span>
                  </span>
                </>
              ) : null}
            </label>
          );
        })}
      </div>
      <p id={`${id}-help`} className="text-muted-foreground mt-2 text-[13px]">
        {help}
      </p>
      {showLimit ? (
        <p id={`${id}-limit`} className="text-muted-foreground mt-1 text-[13px]">
          You can add a row to up to {BULK_LIBRARY_LIMIT} pages at once.
        </p>
      ) : null}
    </div>
  );
}
