import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useDebounce } from "@/hooks/useDebounce";
import { Input } from "@/components/ui/input";
import { buildQueryCatalogHref } from "@/pages/catalogSearchParams";
import { Search, X } from "lucide-react";
import type { FormEvent } from "react";

const SEARCH_NAVIGATION_DEBOUNCE_MS = 200;

interface LiveSearchDraft {
  query: string;
  sentQueries: string[];
  lastNavigatedQuery: string;
}

// The Search page renders one bar in its empty state and another above the
// results, so its first live search unmounts the bar being typed in. Anything
// typed while that navigation rendered exists only in the outgoing bar. It
// leaves its text here as it unmounts, and the incoming bar takes it in the
// same commit. A draft nobody took is dropped once that commit ends.
let liveSearchHandoff: LiveSearchDraft | null = null;

interface SearchBarProps {
  initialQuery?: string;
  autoFocus?: boolean;
  prominent?: boolean;
  buildSearchHref?: (query: string) => string;
  /** Hint for the compact variant's input, which also names it. */
  placeholder?: string;
}

export default function SearchBar({
  initialQuery = "",
  autoFocus = false,
  prominent = false,
  buildSearchHref = buildQueryCatalogHref,
  placeholder = "Search...",
}: SearchBarProps) {
  const [query, setQuery] = useState(initialQuery);
  const navigate = useViewTransitionNavigate();
  const navigateWithoutTransition = useNavigate();
  const inputRef = useRef<HTMLInputElement>(null);
  const isInitialMount = useRef(true);
  const buildSearchHrefRef = useRef(buildSearchHref);
  const lastNavigatedQueryRef = useRef(initialQuery.trim());
  // Queries this bar has put in the URL that the URL has not shown yet, oldest
  // first. Router navigations commit as transitions, so each one lands after
  // a delay, and the person may have typed more by then.
  const sentQueriesRef = useRef<string[]>([]);
  const queryRef = useRef(query);
  const mountQueryRef = useRef(initialQuery);
  const debouncedQuery = useDebounce(query, SEARCH_NAVIGATION_DEBOUNCE_MS);

  useEffect(() => {
    buildSearchHrefRef.current = buildSearchHref;
  }, [buildSearchHref]);

  useLayoutEffect(() => {
    queryRef.current = query;
  }, [query]);

  useLayoutEffect(() => {
    const draft = liveSearchHandoff;
    liveSearchHandoff = null;
    if (prominent && draft?.sentQueries.includes(mountQueryRef.current.trim())) {
      sentQueriesRef.current = draft.sentQueries;
      lastNavigatedQueryRef.current = draft.lastNavigatedQuery;
      // eslint-disable-next-line react-hooks/set-state-in-effect -- adopts the unmounted bar's text before paint
      setQuery(draft.query);
    }
    return () => {
      if (sentQueriesRef.current.length === 0) return;
      const outgoing = {
        query: queryRef.current,
        sentQueries: [...sentQueriesRef.current],
        lastNavigatedQuery: lastNavigatedQueryRef.current,
      };
      liveSearchHandoff = outgoing;
      queueMicrotask(() => {
        if (liveSearchHandoff === outgoing) liveSearchHandoff = null;
      });
    };
  }, [prominent]);

  // Browser history, scope changes, and external navigation can update the
  // canonical query without remounting this component. Keep the input in sync
  // instead of leaving it attached to a stale request key. A URL query this
  // bar sent itself is only the router catching up: the input already holds
  // that text or newer typing, and the URL query is trimmed, so copying it
  // back would drop whatever was typed since, trailing spaces included.
  useEffect(() => {
    const urlQuery = initialQuery.trim();
    const sentQueries = sentQueriesRef.current;
    const sentIndex = sentQueries.indexOf(urlQuery);
    if (sentIndex >= 0) {
      sentQueries.splice(0, sentIndex + 1);
      return;
    }
    sentQueriesRef.current = [];
    // eslint-disable-next-line react-hooks/set-state-in-effect -- the URL changed outside this bar
    setQuery(initialQuery);
    lastNavigatedQueryRef.current = urlQuery;
  }, [initialQuery]);

  const navigateToQuery = useCallback(
    (normalizedQuery: string, options?: { replace: true }) => {
      lastNavigatedQueryRef.current = normalizedQuery;
      sentQueriesRef.current.push(normalizedQuery);
      navigateWithoutTransition(buildSearchHrefRef.current(normalizedQuery), options);
    },
    [navigateWithoutTransition],
  );

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
    navigateToQuery(normalizedDebouncedQuery, { replace: true });
  }, [debouncedQuery, prominent, navigateToQuery, query]);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (query.trim()) {
      if (prominent) {
        navigateToQuery(query.trim());
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
    navigateToQuery("", { replace: true });
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
    <form onSubmit={handleSubmit} role="search" className="relative">
      <Search
        className="text-muted-foreground pointer-events-none absolute top-2.5 left-2.5 h-4 w-4"
        aria-hidden
      />
      <Input
        ref={inputRef}
        placeholder={placeholder}
        aria-label={placeholder}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        className="pl-9"
      />
    </form>
  );
}
