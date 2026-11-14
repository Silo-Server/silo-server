import { useT, type MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";
import type { SearchMediaScope } from "@/hooks/useSearchMediaScope";

const SCOPE_OPTIONS: Array<{ value: SearchMediaScope; label: MessageKey }> = [
  { value: "video", label: "web.catalog.mediaType.media" },
  { value: "audiobook", label: "web.catalog.mediaType.audiobooks" },
  { value: "all", label: "web.catalog.mediaType.all" },
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
  const t = useT();
  return (
    <div
      role="radiogroup"
      aria-label={t("web.catalog.search.scopeLabel")}
      className="search-paint-surface inline-flex items-center gap-1 rounded-full border p-1"
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
            {t(option.label)}
          </button>
        );
      })}
    </div>
  );
}
