import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useDebounce } from "@/hooks/useDebounce";
import { Input } from "@/components/ui/input";
import { buildQueryCatalogHref } from "@/pages/catalogSearchParams";
import { Search, X } from "lucide-react";
import type { FormEvent } from "react";

const SEARCH_NAVIGATION_DEBOUNCE_MS = 200;

interface SearchBarProps {
  initialQuery?: string;
  autoFocus?: boolean;
  prominent?: boolean;
  buildSearchHref?: (query: string) => string;
}

export default function SearchBar({
  initialQuery = "",
  autoFocus = false,
  prominent = false,
  buildSearchHref = buildQueryCatalogHref,
}: SearchBarProps) {
  const [query, setQuery] = useState(initialQuery);
  const navigate = useViewTransitionNavigate();
  const navigateWithoutTransition = useNavigate();
  const inputRef = useRef<HTMLInputElement>(null);
  const isInitialMount = useRef(true);
  const buildSearchHrefRef = useRef(buildSearchHref);
  const lastNavigatedQueryRef = useRef(initialQuery.trim());
  const debouncedQuery = useDebounce(query, SEARCH_NAVIGATION_DEBOUNCE_MS);

  useEffect(() => {
    buildSearchHrefRef.current = buildSearchHref;
  }, [buildSearchHref]);

  // Browser history, scope changes, and external navigation can update the
  // canonical query without remounting this component. Keep the input in sync
  // instead of leaving it attached to a stale request key.
  useEffect(() => {
    setQuery(initialQuery);
    lastNavigatedQueryRef.current = initialQuery.trim();
  }, [initialQuery]);

  useEffect(() => {
    if (autoFocus && inputRef.current) {
      inputRef.current.focus();
    }
  }, [autoFocus]);

  // Live search-as-you-type for the prominent variant
  useEffect(() => {
    if (!prominent) return;
    if (isInitialMount.current) {
      isInitialMount.current = false;
      return;
    }
    const normalizedQuery = query.trim();
    const normalizedDebouncedQuery = debouncedQuery.trim();
    // A newer keystroke (including clear) invalidates the previous debounce.
    // The hook already clears its timer; this comparison also closes the race
    // where an expired callback and the input update reach React together.
    if (
      normalizedDebouncedQuery !== normalizedQuery ||
      lastNavigatedQueryRef.current === normalizedDebouncedQuery
    ) {
      return;
    }
    // Updating only the query string is not a page transition. Animating a
    // full route snapshot for every debounced keystroke makes the search page
    // visibly wobble and adds compositor work to its hottest interaction.
    lastNavigatedQueryRef.current = normalizedDebouncedQuery;
    navigateWithoutTransition(buildSearchHrefRef.current(normalizedDebouncedQuery), {
      replace: true,
    });
  }, [debouncedQuery, prominent, navigateWithoutTransition, query]);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (query.trim()) {
      if (prominent) {
        lastNavigatedQueryRef.current = query.trim();
        navigateWithoutTransition(buildSearchHref(query.trim()));
      } else {
        navigate(buildSearchHref(query.trim()));
      }
    }
  }

  function handleClear() {
    setQuery("");
    if (!prominent) return;

    // Clear is an explicit action, not typeahead. Remove the active route and
    // abort its request immediately instead of waiting for the debounce.
    lastNavigatedQueryRef.current = "";
    navigateWithoutTransition(buildSearchHrefRef.current(""), { replace: true });
  }

  if (prominent) {
    return (
      <form onSubmit={handleSubmit} className="relative w-full max-w-xl">
        <Search className="text-muted-foreground absolute top-4 left-4 h-5 w-5" />
        <Input
          ref={inputRef}
          placeholder="Search movies, series..."
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="search-paint-surface h-14 rounded-[1.4rem] border pr-10 pl-12 text-base shadow-none"
        />
        {query && (
          <button
            type="button"
            onClick={handleClear}
            aria-label="Clear search"
            className="text-muted-foreground hover:text-foreground absolute top-1/2 right-4 -translate-y-1/2 p-1"
          >
            <X className="h-4 w-4" />
          </button>
        )}
      </form>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="relative">
      <Search className="text-muted-foreground absolute top-2.5 left-2.5 h-4 w-4" />
      <Input
        ref={inputRef}
        placeholder="Search..."
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        className="pl-9"
      />
    </form>
  );
}
