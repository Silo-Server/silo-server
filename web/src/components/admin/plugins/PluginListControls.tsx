import { Search, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export function PluginSearchField({
  query,
  placeholder,
  onQueryChange,
}: {
  query: string;
  placeholder: string;
  onQueryChange: (query: string) => void;
}) {
  return (
    <div className="relative w-full sm:max-w-xs">
      <Search
        className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2"
        aria-hidden
      />
      <Input
        type="search"
        value={query}
        onChange={(event) => onQueryChange(event.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
        className="pr-9 pl-9"
      />
      {query ? (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={() => onQueryChange("")}
          aria-label="Clear plugin search"
          className="absolute top-1/2 right-1 -translate-y-1/2"
        >
          <X className="h-3.5 w-3.5" />
        </Button>
      ) : null}
    </div>
  );
}

export interface FilterChip<T extends string> {
  value: T;
  label: string;
  count?: number;
}

/** Single-choice filter as a row of pressed/unpressed buttons. */
export function FilterChips<T extends string>({
  label,
  chips,
  value,
  onChange,
}: {
  label: string;
  chips: FilterChip<T>[];
  value: T;
  onChange: (value: T) => void;
}) {
  return (
    <div role="group" aria-label={label} className="flex flex-wrap gap-1.5">
      {chips.map((chip) => {
        const selected = chip.value === value;
        return (
          <button
            key={chip.value}
            type="button"
            aria-pressed={selected}
            onClick={() => onChange(chip.value)}
            className={cn(
              "focus-visible:ring-ring inline-flex h-[30px] items-center gap-1.5 rounded-full border px-3 text-[13px] transition-colors focus-visible:ring-2 focus-visible:outline-none",
              selected
                ? "bg-foreground text-background border-foreground"
                : "text-muted-foreground hover:text-foreground border-border",
            )}
          >
            {chip.label}
            {chip.count !== undefined ? (
              <span className="tabular-nums opacity-70">{chip.count}</span>
            ) : null}
          </button>
        );
      })}
    </div>
  );
}
