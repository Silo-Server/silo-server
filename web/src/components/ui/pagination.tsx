import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from "lucide-react";

import { pageWindow } from "@/components/ui/pagination.utils";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

const DEFAULT_PAGE_SIZE_OPTIONS = [25, 50, 100];

export interface TablePaginationProps {
  /** Zero-indexed current page. */
  page: number;
  pageSize: number;
  /** Total number of matching rows across all pages. */
  total: number;
  onPageChange: (page: number) => void;
  /** When provided, renders a rows-per-page selector. */
  onPageSizeChange?: (size: number) => void;
  pageSizeOptions?: number[];
  /** Singular noun for the summary, e.g. "scan" → "Showing 1–25 of 240 scans". */
  itemNoun?: string;
  /** Dims the summary while a background refetch is in flight. */
  isFetching?: boolean;
  className?: string;
}

export function TablePagination({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = DEFAULT_PAGE_SIZE_OPTIONS,
  itemNoun = "item",
  isFetching = false,
  className,
}: TablePaginationProps) {
  if (total <= 0) return null;

  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(Math.max(page, 0), pageCount - 1);
  const from = safePage * pageSize + 1;
  const to = Math.min((safePage + 1) * pageSize, total);
  const window = pageWindow(safePage, pageCount);
  const noun = total === 1 ? itemNoun : `${itemNoun}s`;

  const goTo = (next: number) => {
    const clamped = Math.min(Math.max(next, 0), pageCount - 1);
    if (clamped !== safePage) onPageChange(clamped);
  };

  return (
    <div
      className={cn(
        "flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between",
        className,
      )}
    >
      <div className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-2 text-xs">
        <span className={cn("transition-opacity", isFetching && "opacity-60")}>
          Showing <span className="text-foreground font-medium tabular-nums">{from}</span>–
          <span className="text-foreground font-medium tabular-nums">{to}</span> of{" "}
          <span className="text-foreground font-medium tabular-nums">{total}</span> {noun}
        </span>
        {onPageSizeChange ? (
          <label className="flex items-center gap-2">
            <span className="hidden sm:inline">Rows</span>
            <Select
              value={String(pageSize)}
              onValueChange={(value) => onPageSizeChange(Number(value))}
            >
              <SelectTrigger size="sm" className="h-8 w-[4.5rem] tabular-nums">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {pageSizeOptions.map((size) => (
                  <SelectItem key={size} value={String(size)} className="tabular-nums">
                    {size}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>
        ) : null}
      </div>

      {pageCount > 1 ? (
        <nav aria-label="Pagination" className="flex items-center gap-1">
          <Button
            variant="outline"
            size="icon-sm"
            onClick={() => goTo(0)}
            disabled={safePage === 0}
            aria-label="First page"
          >
            <ChevronsLeft />
          </Button>
          <Button
            variant="outline"
            size="icon-sm"
            onClick={() => goTo(safePage - 1)}
            disabled={safePage === 0}
            aria-label="Previous page"
          >
            <ChevronLeft />
          </Button>

          {/* Numbered window on roomy viewports, compact counter on phones. */}
          <div className="hidden items-center gap-1 sm:flex">
            {window.map((entry, index) =>
              entry === "ellipsis" ? (
                <span
                  key={`ellipsis-${index}`}
                  className="text-muted-foreground/60 px-1 text-sm select-none"
                  aria-hidden="true"
                >
                  …
                </span>
              ) : (
                <Button
                  key={entry}
                  variant={entry === safePage ? "default" : "ghost"}
                  size="icon-sm"
                  className="tabular-nums"
                  onClick={() => goTo(entry)}
                  aria-label={`Page ${entry + 1}`}
                  aria-current={entry === safePage ? "page" : undefined}
                >
                  {entry + 1}
                </Button>
              ),
            )}
          </div>
          <span className="text-muted-foreground px-2 text-xs tabular-nums sm:hidden">
            {safePage + 1} / {pageCount}
          </span>

          <Button
            variant="outline"
            size="icon-sm"
            onClick={() => goTo(safePage + 1)}
            disabled={safePage >= pageCount - 1}
            aria-label="Next page"
          >
            <ChevronRight />
          </Button>
          <Button
            variant="outline"
            size="icon-sm"
            onClick={() => goTo(pageCount - 1)}
            disabled={safePage >= pageCount - 1}
            aria-label="Last page"
          >
            <ChevronsRight />
          </Button>
        </nav>
      ) : null}
    </div>
  );
}
