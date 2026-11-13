import type { ReactNode } from "react";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  ChevronUp,
  Search,
} from "lucide-react";

import { PAGE_SIZE, type SortDir } from "@/components/admin/libraries/diagnosticsListState";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { TableHead } from "@/components/ui/table";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

/*
 * Pieces shared by the diagnostics sections under the admin libraries list:
 * the table chrome they use from `lg` up and the card chrome below it.
 */

/** Controls at least 44px tall below `lg`, where the sections show cards. */
export const TOUCH_CONTROL_CLASS = "max-lg:h-11!";

/** A full-width labeled action at the bottom of a card. */
export const CARD_ACTION_CLASS = "h-11 min-w-0 flex-1";

export function PaginationBar({
  total,
  rangeStart,
  rangeEnd,
  canPrev,
  canNext,
  first,
  prev,
  next,
  last,
}: {
  total: number;
  rangeStart: number;
  rangeEnd: number;
  canPrev: boolean;
  canNext: boolean;
  first: () => void;
  prev: () => void;
  next: () => void;
  last: () => void;
}) {
  const t = useT();
  if (total <= PAGE_SIZE) return null;
  const buttonClass = "h-7 w-7 max-lg:size-11";
  return (
    <div className="border-border/40 flex flex-wrap items-center justify-between gap-x-2 gap-y-1 border-t px-1 pt-3">
      <span className="text-muted-foreground text-xs tracking-tight tabular-nums">
        {t("web.admin.libraries.pagination.range", {
          start: rangeStart,
          end: rangeEnd,
          total: total,
        })}
      </span>
      <div className="flex items-center gap-0.5">
        <Button
          variant="ghost"
          size="icon"
          className={buttonClass}
          disabled={!canPrev}
          onClick={first}
          title={t("web.admin.libraries.pagination.first")}
          aria-label={t("web.admin.libraries.pagination.first")}
        >
          <ChevronsLeft className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className={buttonClass}
          disabled={!canPrev}
          onClick={prev}
          title={t("web.admin.libraries.pagination.previous")}
          aria-label={t("web.admin.libraries.pagination.previous")}
        >
          <ChevronLeft className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className={buttonClass}
          disabled={!canNext}
          onClick={next}
          title={t("web.admin.libraries.pagination.next")}
          aria-label={t("web.admin.libraries.pagination.next")}
        >
          <ChevronRight className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className={buttonClass}
          disabled={!canNext}
          onClick={last}
          title={t("web.admin.libraries.pagination.last")}
          aria-label={t("web.admin.libraries.pagination.last")}
        >
          <ChevronsRight className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  );
}

export function SortableHead<K extends string>({
  field,
  activeField,
  activeDir,
  onSort,
  children,
  className,
}: {
  field: K;
  activeField: K;
  activeDir: SortDir;
  onSort: (field: K) => void;
  children: ReactNode;
  className?: string;
}) {
  const active = field === activeField;
  return (
    <TableHead className={cn("select-none", className)}>
      <button
        type="button"
        className="hover:text-foreground inline-flex items-center gap-1 transition-colors"
        onClick={() => onSort(field)}
      >
        {children}
        {active ? (
          activeDir === "asc" ? (
            <ChevronUp className="h-3 w-3" />
          ) : (
            <ChevronDown className="h-3 w-3" />
          )
        ) : (
          <ChevronDown className="h-3 w-3 opacity-0 group-hover:opacity-30" />
        )}
      </button>
    </TableHead>
  );
}

/**
 * The card layout's stand-in for sortable table headers: a field picker and a
 * direction toggle bound to the same `useSort` state as the headers.
 */
export function SortControl<K extends string>({
  options,
  sortField,
  sortDir,
  onFieldChange,
  onDirChange,
}: {
  options: { value: K; label: string }[];
  sortField: K;
  sortDir: SortDir;
  onFieldChange: (field: K) => void;
  onDirChange: (dir: SortDir) => void;
}) {
  const t = useT();
  const ascending = sortDir === "asc";
  return (
    <div className="mb-2 flex items-center gap-2">
      <Select
        value={sortField}
        onValueChange={(value) => {
          const option = options.find((entry) => entry.value === value);
          if (option) onFieldChange(option.value);
        }}
      >
        <SelectTrigger className="h-11! min-w-0 flex-1 text-sm">
          <span className="text-muted-foreground shrink-0">{t("web.catalog.sort.sortBy")}</span>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem key={option.value} value={option.value} className="min-h-11">
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button
        type="button"
        variant="outline"
        className="h-11 shrink-0"
        onClick={() => onDirChange(ascending ? "desc" : "asc")}
      >
        {ascending ? <ArrowUp aria-hidden="true" /> : <ArrowDown aria-hidden="true" />}
        {ascending ? t("web.catalog.sort.ascending") : t("web.catalog.sort.descending")}
      </Button>
    </div>
  );
}

/** A section's search box; 44px tall below `lg`. */
export function DiagnosticsSearch({
  value,
  onValueChange,
  placeholder,
  className,
}: {
  value: string;
  onValueChange: (value: string) => void;
  placeholder: string;
  className?: string;
}) {
  return (
    <div className={cn("relative", className)}>
      <Search className="text-muted-foreground pointer-events-none absolute start-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2" />
      <Input
        placeholder={placeholder}
        value={value}
        onChange={(e) => onValueChange(e.target.value)}
        // 16px below sm so focusing it does not zoom the page on iOS.
        className="h-8 ps-8 text-xs max-lg:h-11 max-lg:text-sm max-sm:text-base"
      />
    </div>
  );
}

/** The card layout's list, one card per table row. */
export function DiagnosticsCardList({ children }: { children: ReactNode }) {
  return <ul className="space-y-3">{children}</ul>;
}

/** One row of a diagnostics table as a card, on the libraries list's card surface. */
export function DiagnosticsCard({ children }: { children: ReactNode }) {
  return (
    <li>
      <article className="surface-panel space-y-3 rounded-2xl border-0 p-4">{children}</article>
    </li>
  );
}

/** Short label/value lines inside a card. */
export function CardFields({ children }: { children: ReactNode }) {
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">{children}</dl>
  );
}

export function CardField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  );
}

/** A filesystem path in a card: monospace, wrapping anywhere, never scrolling. */
export function CardPath({ path, className }: { path: string; className?: string }) {
  return (
    <code
      className={cn("text-muted-foreground block font-mono text-xs break-all", className)}
      dir="ltr"
    >
      {path}
    </code>
  );
}

/** The labeled actions at the bottom of a card. */
export function CardActions({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap gap-2">{children}</div>;
}

/** An empty, loading, or error line in place of the card list. */
export function CardListMessage({
  tone = "muted",
  children,
}: {
  tone?: "muted" | "error";
  children: ReactNode;
}) {
  return (
    <p
      className={cn(
        "border-border/40 bg-background/40 rounded-xl border px-4 py-6 text-center text-sm break-words",
        tone === "error" ? "text-destructive" : "text-muted-foreground",
      )}
    >
      {children}
    </p>
  );
}
