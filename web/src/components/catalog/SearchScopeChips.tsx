import { cn } from "@/lib/utils";
import type { SearchMediaScope } from "@/hooks/useSearchMediaScope";

const SCOPE_OPTIONS: Array<{ value: SearchMediaScope; label: string }> = [
  { value: "video", label: "Media" },
  { value: "audiobook", label: "Audiobooks" },
  { value: "all", label: "All" },
];

export interface SearchScopeChipsProps {
  activeScope: SearchMediaScope;
  onScopeChange: (scope: SearchMediaScope) => void;
}

/**
 * Coarse search-scope toggle shown under the search bar: Media (movies &
 * series), Audiobooks, or All. Selecting a chip both filters the current
 * results and saves the choice as the user's default search scope.
 */
export default function SearchScopeChips({ activeScope, onScopeChange }: SearchScopeChipsProps) {
  return (
    <div
      role="radiogroup"
      aria-label="Search scope"
      className="surface-panel inline-flex items-center gap-1 rounded-full p-1"
    >
      {SCOPE_OPTIONS.map((option) => {
        const isActive = option.value === activeScope;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={isActive}
            onClick={() => {
              if (!isActive) {
                onScopeChange(option.value);
              }
            }}
            className={cn(
              "rounded-full px-4 py-1.5 text-sm font-medium transition-colors",
              isActive
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
