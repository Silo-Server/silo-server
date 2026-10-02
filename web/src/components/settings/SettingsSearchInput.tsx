import { useEffect, useId, useRef } from "react";
import { Search, X } from "lucide-react";

import { Input } from "@/components/ui/input";
import { SEARCH_SHORTCUT_LABEL } from "@/lib/keyboardShortcut";
import { cn } from "@/lib/utils";

interface SettingsSearchInputProps {
  value: string;
  onChange: (value: string) => void;
  resultCount: number;
  placeholder?: string;
  emptyLabel?: string;
  className?: string;
  shortcutMediaQuery?: string;
  showShortcutHint?: boolean;
  /**
   * Focus the input on ⌘K / Ctrl-K. Turn off where another surface owns that
   * shortcut (the admin area's command palette).
   */
  captureShortcut?: boolean;
}

export function SettingsSearchInput({
  value,
  onChange,
  resultCount,
  placeholder = "Search settings",
  emptyLabel = "No matching settings",
  className,
  shortcutMediaQuery,
  showShortcutHint = false,
  captureShortcut = true,
}: SettingsSearchInputProps) {
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const hasQuery = value.trim().length > 0;
  // Idle shows nothing: a "12 settings pages" style count under an untouched
  // box is noise. The line only speaks while a query is filtering.
  const status = hasQuery
    ? resultCount === 0
      ? emptyLabel
      : `${resultCount} ${resultCount === 1 ? "match" : "matches"}`
    : null;

  useEffect(() => {
    if (!captureShortcut) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || !(event.metaKey || event.ctrlKey)) return;
      if (event.key.toLowerCase() !== "k") return;
      if (shortcutMediaQuery && !window.matchMedia(shortcutMediaQuery).matches) return;

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
  }, [captureShortcut, shortcutMediaQuery]);

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
          className={cn("h-11 rounded-xl pr-10 pl-9", showShortcutHint && !hasQuery && "sm:pr-16")}
          autoComplete="off"
        />
        {hasQuery ? (
          <button
            type="button"
            aria-label="Clear settings search"
            onClick={() => onChange("")}
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 absolute inset-y-0 right-0 inline-flex w-11 items-center justify-center rounded-xl transition-colors focus-visible:ring-[3px] focus-visible:outline-none"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        ) : showShortcutHint ? (
          <kbd
            aria-hidden="true"
            className="border-border/80 bg-surface text-muted-foreground pointer-events-none absolute top-1/2 right-2 hidden h-6 -translate-y-1/2 items-center rounded-md border px-1.5 font-sans text-[10px] font-medium sm:inline-flex"
          >
            {SEARCH_SHORTCUT_LABEL}
          </kbd>
        ) : null}
      </div>
      {/* Always mounted so the live region reliably announces count changes;
          visually collapses to nothing while idle. */}
      <p className={cn("text-muted-foreground text-xs", status && "mt-2")} aria-live="polite">
        {status}
      </p>
    </div>
  );
}
