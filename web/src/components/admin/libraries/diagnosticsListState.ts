import { useCallback, useMemo, useState } from "react";

import { useMediaQuery } from "@/hooks/useMediaQuery";

/** Rows per page of a diagnostics list. */
export const PAGE_SIZE = 10;

// Tailwind's lg breakpoint: tables from here up, cards below.
const DESKTOP_QUERY = "(min-width: 64rem)";

/**
 * Whether the admin libraries page shows its tables, for the library list and
 * the diagnostics sections alike. Tests have no matchMedia and get the tables.
 */
export function useLibrariesTableLayout(): boolean {
  return useMediaQuery(DESKTOP_QUERY, true);
}

export type SortDir = "asc" | "desc";

/** The last segment of a filesystem path, which names a root in the lists. */
export function pathLeaf(path: string): string | undefined {
  return path.split("/").filter(Boolean).pop();
}

export function usePagination<T>(items: T[], pageSize = PAGE_SIZE) {
  const [page, setPage] = useState(0);
  const totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  const clamped = Math.min(page, totalPages - 1);

  const slice = useMemo(
    () => items.slice(clamped * pageSize, clamped * pageSize + pageSize),
    [items, clamped, pageSize],
  );

  return {
    page: clamped,
    totalPages,
    total: items.length,
    rows: slice,
    setPage,
    canPrev: clamped > 0,
    canNext: clamped < totalPages - 1,
    first: () => setPage(0),
    prev: () => setPage((p) => Math.max(0, p - 1)),
    next: () => setPage((p) => Math.min(totalPages - 1, p + 1)),
    last: () => setPage(totalPages - 1),
    // 1-indexed display range
    rangeStart: items.length === 0 ? 0 : clamped * pageSize + 1,
    rangeEnd: Math.min((clamped + 1) * pageSize, items.length),
  };
}

export function useSort<K extends string>(defaultField: K, defaultDir: SortDir = "desc") {
  const [sortField, setSortField] = useState<K>(defaultField);
  const [sortDir, setSortDir] = useState<SortDir>(defaultDir);

  // A table header click: the active column flips direction, another column
  // becomes active in ascending order.
  const toggle = useCallback(
    (field: K) => {
      if (field === sortField) {
        setSortDir((d) => (d === "asc" ? "desc" : "asc"));
      } else {
        setSortField(field);
        setSortDir("asc");
      }
    },
    [sortField],
  );

  return { sortField, sortDir, toggle, setSortField, setSortDir };
}
