import { useEffect, useId, useRef } from "react";
import { Search, X } from "lucide-react";

import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

interface SettingsSearchInputProps {
  value: string;
  onChange: (value: string) => void;
  resultCount: number;
  totalCount: number;
  placeholder?: string;
  itemLabel?: string;
  emptyLabel?: string;
  className?: string;
}

export function SettingsSearchInput({
  value,
  onChange,
  resultCount,
  totalCount,
  placeholder = "Search settings",
  itemLabel = "settings sections",
  emptyLabel = "No matching settings",
  className,
}: SettingsSearchInputProps) {
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const hasQuery = value.trim().length > 0;
  const status = hasQuery
    ? resultCount === 0
      ? emptyLabel
      : `${resultCount} ${resultCount === 1 ? "match" : "matches"}`
    : `${totalCount} ${itemLabel}`;

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || !(event.metaKey || event.ctrlKey)) return;
      if (event.key.toLowerCase() !== "k") return;

      event.preventDefault();
      event.stopPropagation();
      event.stopImmediatePropagation();
      inputRef.current?.focus();
      inputRef.current?.select();
    };

    window.addEventListener("keydown", onKeyDown, { capture: true });
    document.addEventListener("keydown", onKeyDown, { capture: true });
    return () => {
      window.removeEventListener("keydown", onKeyDown, { capture: true });
      document.removeEventListener("keydown", onKeyDown, { capture: true });
    };
  }, []);

  return (
    <div className={cn("w-full", className)}>
      <label htmlFor={inputId} className="sr-only">
        {placeholder}
      </label>
      <div className="relative">
        <Search
          className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2"
          aria-hidden="true"
        />
        <Input
          ref={inputRef}
          id={inputId}
          type="search"
          value={value}
          placeholder={placeholder}
          onChange={(event) => onChange(event.target.value)}
          className="h-10 rounded-xl pr-10 pl-9"
          autoComplete="off"
        />
        {hasQuery ? (
          <button
            type="button"
            aria-label="Clear settings search"
            onClick={() => onChange("")}
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 absolute top-1/2 right-2 inline-flex h-7 w-7 -translate-y-1/2 items-center justify-center rounded-md transition-colors focus-visible:ring-[3px] focus-visible:outline-none"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        ) : null}
      </div>
      <p className="text-muted-foreground mt-2 text-xs" aria-live="polite">
        {status}
      </p>
    </div>
  );
}
