import * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { Popover as PopoverPrimitive } from "radix-ui";
import { Check, ChevronsUpDown } from "lucide-react";

import { fetchCatalogFacetSearch, type CatalogFacetName } from "@/hooks/queries/catalog";
import { useDebounce } from "@/hooks/useDebounce";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";
import { cn } from "@/lib/utils";
import { PortalContainerContext } from "./portal-container-context";

interface FacetSearchSelectProps {
  /** The catalog facet to typeahead-search (e.g. "author", "series"). */
  facet: CatalogFacetName;
  /** The catalog scope — usually the same state passed to CatalogFiltersPanel. */
  state: CatalogSearchState;
  /** Currently selected value (or empty for "any"). */
  value: string;
  onChange: (next: string) => void;
  placeholder?: string;
  disabled?: boolean;
  /** Max matches to fetch per query. Defaults to 20. */
  limit?: number;
}

/**
 * FacetSearchSelect is the typeahead counterpart to SearchableSelect for
 * high-cardinality catalog facets (Author, Narrator, Series). It debounces
 * the user's input then queries /api/v1/catalog/filters/search with the
 * given facet + prefix. The dropdown lists matches in alphabetical order
 * with the currently selected value pinned first so users can clear it
 * without re-searching.
 *
 * The component intentionally does not render an "All N items" indicator —
 * the typeahead pattern relies on the user narrowing rather than scrolling.
 * When more matches are available than were returned, a small "Keep typing
 * to narrow further" hint shows below the list.
 */
export function FacetSearchSelect({
  facet,
  state,
  value,
  onChange,
  placeholder = "Search...",
  disabled = false,
  limit = 20,
}: FacetSearchSelectProps) {
  const [open, setOpen] = React.useState(false);
  const [search, setSearch] = React.useState("");
  const [focusedIndex, setFocusedIndex] = React.useState(-1);
  const listboxId = React.useId();
  const portalContainer = React.useContext(PortalContainerContext);
  const debouncedQuery = useDebounce(search.trim(), 200);

  const query = useQuery({
    queryKey: ["catalogFacetSearch", facet, debouncedQuery, limit, state] as const,
    queryFn: ({ signal }) =>
      fetchCatalogFacetSearch(state, facet, debouncedQuery, limit, { signal }),
    enabled: open && debouncedQuery.length > 0,
    staleTime: 60 * 1000,
  });

  const matches = query.data?.matches ?? [];
  const hasMore = query.data?.has_more ?? false;

  const options = React.useMemo(() => {
    const names = new Set<string>();
    if (value) {
      names.add(value);
    }
    for (const name of matches) {
      if (name) {
        names.add(name);
      }
    }
    return Array.from(names);
  }, [matches, value]);

  const allItems = React.useMemo(() => ["", ...options], [options]);

  React.useEffect(() => {
    if (!open) {
      setFocusedIndex(-1);
      setSearch("");
    }
  }, [open]);

  function handleSearchKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setFocusedIndex((prev) => (prev < allItems.length - 1 ? prev + 1 : prev));
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setFocusedIndex((prev) => (prev > 0 ? prev - 1 : prev));
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      if (focusedIndex >= 0 && focusedIndex < allItems.length) {
        const nextValue = allItems[focusedIndex];
        if (nextValue !== undefined) {
          onChange(nextValue);
          setOpen(false);
        }
      }
    }
  }

  return (
    <PopoverPrimitive.Root open={open} onOpenChange={setOpen} modal={false}>
      <PopoverPrimitive.Trigger asChild disabled={disabled}>
        <button
          type="button"
          role="combobox"
          aria-expanded={open}
          aria-controls={listboxId}
          className={cn(
            "border-input bg-background ring-offset-background placeholder:text-muted-foreground focus:ring-ring flex h-9 w-full items-center justify-between rounded-md border px-3 py-2 text-sm shadow-xs focus:ring-1 focus:outline-none disabled:cursor-not-allowed disabled:opacity-50",
            !value && "text-muted-foreground",
          )}
        >
          <span className="truncate">{value || placeholder}</span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </button>
      </PopoverPrimitive.Trigger>

      <PopoverPrimitive.Portal container={portalContainer ?? undefined}>
        <PopoverPrimitive.Content
          align="start"
          sideOffset={4}
          collisionPadding={8}
          className="bg-popover text-popover-foreground z-50 w-[var(--radix-popover-trigger-width)] rounded-md border shadow-md"
          onOpenAutoFocus={(event) => event.preventDefault()}
        >
          <div className="p-2">
            <input
              value={search}
              onChange={(event) => {
                setSearch(event.target.value);
                setFocusedIndex(-1);
              }}
              onKeyDown={handleSearchKeyDown}
              placeholder="Type to search..."
              aria-label="Search"
              aria-controls={listboxId}
              aria-activedescendant={
                focusedIndex >= 0 ? `${listboxId}-opt-${focusedIndex}` : undefined
              }
              className="border-input bg-background placeholder:text-muted-foreground flex h-8 w-full rounded-md border px-2 text-sm outline-none"
              autoFocus
            />
          </div>

          <div
            id={listboxId}
            role="listbox"
            className="max-h-60 overflow-y-auto overscroll-contain p-1"
          >
            {query.isLoading ? (
              <p className="text-muted-foreground py-4 text-center text-sm">Loading...</p>
            ) : options.length === 0 ? (
              <p className="text-muted-foreground py-4 text-center text-sm">
                {debouncedQuery ? "No matches" : "Start typing to search"}
              </p>
            ) : (
              <>
                <button
                  id={`${listboxId}-opt-0`}
                  type="button"
                  role="option"
                  aria-selected={!value}
                  className={cn(
                    "hover:bg-accent hover:text-accent-foreground relative flex w-full cursor-pointer items-center rounded-sm px-2 py-1.5 text-sm select-none",
                    !value && "font-medium",
                    focusedIndex === 0 && "bg-accent text-accent-foreground",
                  )}
                  onClick={() => {
                    onChange("");
                    setOpen(false);
                  }}
                >
                  <Check
                    className={cn("mr-2 h-4 w-4 shrink-0", value ? "opacity-0" : "opacity-100")}
                  />
                  <span className="text-muted-foreground italic">Any</span>
                </button>

                {options.map((option, index) => (
                  <button
                    id={`${listboxId}-opt-${index + 1}`}
                    key={option}
                    type="button"
                    role="option"
                    aria-selected={value === option}
                    className={cn(
                      "hover:bg-accent hover:text-accent-foreground relative flex w-full cursor-pointer items-center rounded-sm px-2 py-1.5 text-sm select-none",
                      value === option && "font-medium",
                      focusedIndex === index + 1 && "bg-accent text-accent-foreground",
                    )}
                    onClick={() => {
                      onChange(option);
                      setOpen(false);
                    }}
                  >
                    <Check
                      className={cn(
                        "mr-2 h-4 w-4 shrink-0",
                        value === option ? "opacity-100" : "opacity-0",
                      )}
                    />
                    {option}
                  </button>
                ))}
                {hasMore ? (
                  <p className="text-muted-foreground px-2 py-1.5 text-xs italic">
                    Keep typing to narrow further...
                  </p>
                ) : null}
              </>
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}
