import { useEffect, useRef, useState } from "react";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useDebounce } from "@/hooks/useDebounce";
import { Input } from "@/components/ui/input";
import { buildQueryCatalogHref } from "@/pages/catalogSearchParams";
import { Search, X } from "lucide-react";
import type { FormEvent } from "react";

const SEARCH_NAVIGATION_DEBOUNCE_MS = 100;

interface SearchBarProps {
  initialQuery?: string;
  autoFocus?: boolean;
  prominent?: boolean;
}

export default function SearchBar({
  initialQuery = "",
  autoFocus = false,
  prominent = false,
}: SearchBarProps) {
  const [query, setQuery] = useState(initialQuery);
  const navigate = useViewTransitionNavigate();
  const inputRef = useRef<HTMLInputElement>(null);
  const isInitialMount = useRef(true);
  const debouncedQuery = useDebounce(query, SEARCH_NAVIGATION_DEBOUNCE_MS);

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
    if (debouncedQuery.trim()) {
      navigate(buildQueryCatalogHref(debouncedQuery.trim()), { replace: true });
    }
  }, [debouncedQuery, prominent, navigate]);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (query.trim()) {
      navigate(buildQueryCatalogHref(query.trim()));
    }
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
          className="surface-panel h-14 rounded-[1.4rem] border-0 pr-10 pl-12 text-base shadow-none"
        />
        {query && (
          <button
            type="button"
            onClick={() => setQuery("")}
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
