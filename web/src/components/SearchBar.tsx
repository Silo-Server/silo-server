import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useDebounce } from "@/hooks/useDebounce";
import { Input } from "@/components/ui/input";
import { buildQueryCatalogHref } from "@/pages/catalogSearchParams";
import { Search, X } from "lucide-react";
import type { FormEvent } from "react";

const SEARCH_NAVIGATION_DEBOUNCE_MS = 200;
const SEARCH_NAVIGATION_STATE_KEY = "searchBarNavigation";

let searchNavigationCount = 0;

// History state survives a reload, which restarts the counter, so the time
// keeps an older entry's id from matching a new navigation.
function createSearchNavigationId() {
  searchNavigationCount += 1;
  return `${Date.now().toString(36)}-${searchNavigationCount}`;
}

function readSearchNavigationId(state: unknown): string | null {
  if (typeof state !== "object" || state === null) return null;
  const id = (state as Record<string, unknown>)[SEARCH_NAVIGATION_STATE_KEY];
  return typeof id === "string" ? id : null;
}

interface LiveSearchDraft {
  query: string;
  pendingNavigations: string[];
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
  const location = useLocation();
  const landedNavigationId = readSearchNavigationId(location.state);
  const inputRef = useRef<HTMLInputElement>(null);
  const isInitialMount = useRef(true);
  const buildSearchHrefRef = useRef(buildSearchHref);
  const lastNavigatedQueryRef = useRef(initialQuery.trim());
  // Navigations this bar has started that the router has not committed yet,
  // oldest first, by the id each carries in its history state. Router
  // navigations commit as transitions, so each one lands after a delay, and
  // the person may have typed more by then.
  const pendingNavigationsRef = useRef<string[]>([]);
  const syncedQueryRef = useRef(initialQuery);
  const queryRef = useRef(query);
  const mountNavigationIdRef = useRef(landedNavigationId);
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
    const mountNavigationId = mountNavigationIdRef.current;
    if (prominent && mountNavigationId && draft?.pendingNavigations.includes(mountNavigationId)) {
      pendingNavigationsRef.current = draft.pendingNavigations;
      lastNavigatedQueryRef.current = draft.lastNavigatedQuery;
      // eslint-disable-next-line react-hooks/set-state-in-effect -- adopts the unmounted bar's text before paint
      setQuery(draft.query);
    }
    return () => {
      if (pendingNavigationsRef.current.length === 0) return;
      const outgoing = {
        query: queryRef.current,
        pendingNavigations: [...pendingNavigationsRef.current],
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
  // instead of leaving it attached to a stale request key. A navigation this
  // bar started is only the router catching up: the input already holds that
  // text or newer typing, and the URL query is trimmed, so copying it back
  // would drop whatever was typed since, trailing spaces included. Any other
  // navigation that changes the query replaces the text, even when it opens a
  // query this bar is still loading.
  useEffect(() => {
    const pendingNavigations = pendingNavigationsRef.current;
    const landedIndex = landedNavigationId ? pendingNavigations.indexOf(landedNavigationId) : -1;
    if (landedIndex >= 0) {
      pendingNavigations.splice(0, landedIndex + 1);
      syncedQueryRef.current = initialQuery;
      return;
    }
    if (initialQuery === syncedQueryRef.current) return;
    syncedQueryRef.current = initialQuery;
    pendingNavigationsRef.current = [];
    // eslint-disable-next-line react-hooks/set-state-in-effect -- the URL changed outside this bar
    setQuery(initialQuery);
    lastNavigatedQueryRef.current = initialQuery.trim();
  }, [initialQuery, landedNavigationId]);

  const navigateToQuery = useCallback(
    (normalizedQuery: string, options?: { replace: true }) => {
      const navigationId = createSearchNavigationId();
      lastNavigatedQueryRef.current = normalizedQuery;
      pendingNavigationsRef.current.push(navigationId);
      navigateWithoutTransition(buildSearchHrefRef.current(normalizedQuery), {
        ...options,
        state: { [SEARCH_NAVIGATION_STATE_KEY]: navigationId },
      });
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
