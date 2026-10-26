import * as React from "react";
import { Popover as PopoverPrimitive } from "radix-ui";
import { Check, ChevronsUpDown } from "lucide-react";

import { usePersonSearch } from "@/hooks/queries/personSearch";
import { useDebounce } from "@/hooks/useDebounce";
import { cn } from "@/lib/utils";
import { PortalContainerContext } from "./portal-container-context";

interface PersonSearchSelectProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
}

export function PersonSearchSelect({
  value,
  onChange,
  placeholder = "Search people...",
  disabled = false,
}: PersonSearchSelectProps) {
  const [open, setOpen] = React.useState(false);
  const [search, setSearch] = React.useState("");
  const [focusedIndex, setFocusedIndex] = React.useState(-1);
  const listboxId = React.useId();
  const portalContainer = React.useContext(PortalContainerContext);
  const debouncedQuery = useDebounce(search.trim(), 200);
  const resultsQuery = usePersonSearch(debouncedQuery, 20, open);

  const options = React.useMemo(() => {
    const names = new Set<string>();
    if (value) {
      names.add(value);
    }
    for (const person of resultsQuery.data ?? []) {
      if (person.name) {
        names.add(person.name);
      }
    }
    return Array.from(names);
  }, [resultsQuery.data, value]);

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
              placeholder="Search people..."
              aria-label="Search people"
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
            {resultsQuery.isLoading ? (
              <p className="text-muted-foreground py-4 text-center text-sm">Loading...</p>
            ) : options.length === 0 ? (
              <p className="text-muted-foreground py-4 text-center text-sm">
                {debouncedQuery ? "No people found" : "Start typing to search"}
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
              </>
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}
