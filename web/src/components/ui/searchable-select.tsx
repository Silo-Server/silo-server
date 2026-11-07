import * as React from "react";
import { Popover as PopoverPrimitive } from "radix-ui";
import { Check, ChevronsUpDown } from "lucide-react";

import { useT } from "@/i18n";
import { cn } from "@/lib/utils";
import { PortalContainerContext } from "./portal-container-context";

interface SearchableSelectProps {
  /** The full list of available options. */
  options: string[];
  /** Currently selected value (empty string = nothing selected). */
  value: string;
  /** Called when the user picks an option or clears the selection. */
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  /** Show a loading skeleton while options are being fetched. */
  isLoading?: boolean;
  /** Optional mapper from raw value to display label; used for both the
   *  trigger text, list rendering, and substring search. */
  getLabel?: (value: string) => string;
}

export function SearchableSelect({
  options,
  value,
  onChange,
  placeholder,
  disabled = false,
  isLoading = false,
  getLabel,
}: SearchableSelectProps) {
  const t = useT();
  const [open, setOpen] = React.useState(false);
  const [search, setSearch] = React.useState("");
  const [focusedIndex, setFocusedIndex] = React.useState(-1);
  const listboxId = React.useId();
  const portalContainer = React.useContext(PortalContainerContext);
  const labelOf = React.useCallback((opt: string) => getLabel?.(opt) ?? opt, [getLabel]);

  const filtered = React.useMemo(() => {
    if (!search) return options;
    const lower = search.toLowerCase();
    return options.filter(
      (opt) => opt.toLowerCase().includes(lower) || labelOf(opt).toLowerCase().includes(lower),
    );
  }, [options, search, labelOf]);

  const allItems = React.useMemo(() => ["", ...filtered], [filtered]);

  React.useEffect(() => {
    if (!open) setFocusedIndex(-1);
  }, [open]);

  function handleSearchKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setFocusedIndex((prev) => (prev < allItems.length - 1 ? prev + 1 : prev));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setFocusedIndex((prev) => (prev > 0 ? prev - 1 : prev));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (focusedIndex >= 0 && focusedIndex < allItems.length) {
        const item = allItems[focusedIndex];
        if (item !== undefined) onChange(item);
        setOpen(false);
        setSearch("");
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
          <span className="truncate">
            {value ? labelOf(value) : (placeholder ?? t("web.ui.select.placeholder"))}
          </span>
          <ChevronsUpDown className="ms-2 h-4 w-4 shrink-0 opacity-50" />
        </button>
      </PopoverPrimitive.Trigger>

      <PopoverPrimitive.Portal container={portalContainer ?? undefined}>
        <PopoverPrimitive.Content
          align="start"
          sideOffset={4}
          collisionPadding={8}
          className="bg-popover text-popover-foreground z-50 w-[var(--radix-popover-trigger-width)] rounded-md border shadow-md"
          onOpenAutoFocus={(e) => e.preventDefault()}
        >
          <div className="p-2">
            <input
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                setFocusedIndex(-1);
              }}
              onKeyDown={handleSearchKeyDown}
              placeholder={t("web.ui.select.searchPlaceholder")}
              aria-label={t("web.ui.select.searchOptions")}
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
            {isLoading ? (
              <p className="text-muted-foreground py-4 text-center text-sm">
                {t("web.ui.loading")}
              </p>
            ) : filtered.length === 0 ? (
              <p className="text-muted-foreground py-4 text-center text-sm">
                {t("web.ui.select.noResults")}
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
                    setSearch("");
                  }}
                >
                  <Check
                    className={cn("me-2 h-4 w-4 shrink-0", value ? "opacity-0" : "opacity-100")}
                  />
                  <span className="text-muted-foreground italic">{t("web.ui.select.any")}</span>
                </button>

                {filtered.map((opt, i) => (
                  <button
                    id={`${listboxId}-opt-${i + 1}`}
                    type="button"
                    role="option"
                    aria-selected={value === opt}
                    key={opt}
                    className={cn(
                      "hover:bg-accent hover:text-accent-foreground relative flex w-full cursor-pointer items-center rounded-sm px-2 py-1.5 text-sm select-none",
                      value === opt && "font-medium",
                      focusedIndex === i + 1 && "bg-accent text-accent-foreground",
                    )}
                    onClick={() => {
                      onChange(opt);
                      setOpen(false);
                      setSearch("");
                    }}
                  >
                    <Check
                      className={cn(
                        "me-2 h-4 w-4 shrink-0",
                        value === opt ? "opacity-100" : "opacity-0",
                      )}
                    />
                    {labelOf(opt)}
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

interface SearchableMultiSelectProps {
  /** The full list of available options. */
  options: string[];
  /** Currently selected values. */
  value: string[];
  /** Called when the selection changes. */
  onChange: (value: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  /** Show a loading skeleton while options are being fetched. */
  isLoading?: boolean;
  /** Optional mapper from raw value to display label; used for trigger
   *  text, list rendering, and substring search. */
  getLabel?: (value: string) => string;
}

export function SearchableMultiSelect({
  options,
  value,
  onChange,
  placeholder,
  disabled = false,
  isLoading = false,
  getLabel,
}: SearchableMultiSelectProps) {
  const t = useT();
  const [open, setOpen] = React.useState(false);
  const [search, setSearch] = React.useState("");
  const [focusedIndex, setFocusedIndex] = React.useState(-1);
  const listboxId = React.useId();
  const portalContainer = React.useContext(PortalContainerContext);
  const labelOf = React.useCallback((opt: string) => getLabel?.(opt) ?? opt, [getLabel]);

  const selected = React.useMemo(() => new Set(value), [value]);

  const filtered = React.useMemo(() => {
    if (!search) return options;
    const lower = search.toLowerCase();
    return options.filter(
      (opt) => opt.toLowerCase().includes(lower) || labelOf(opt).toLowerCase().includes(lower),
    );
  }, [options, search, labelOf]);

  const hasClear = value.length > 0;
  const allItems = React.useMemo(
    () => (hasClear ? ["__clear__", ...filtered] : filtered),
    [hasClear, filtered],
  );

  React.useEffect(() => {
    if (!open) setFocusedIndex(-1);
  }, [open]);

  function toggle(opt: string) {
    if (selected.has(opt)) {
      onChange(value.filter((v) => v !== opt));
    } else {
      onChange([...value, opt]);
    }
  }

  function handleSearchKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setFocusedIndex((prev) => (prev < allItems.length - 1 ? prev + 1 : prev));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setFocusedIndex((prev) => (prev > 0 ? prev - 1 : prev));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (focusedIndex >= 0 && focusedIndex < allItems.length) {
        const item = allItems[focusedIndex];
        if (item === "__clear__") {
          onChange([]);
        } else if (item !== undefined) {
          toggle(item);
        }
      }
    }
  }

  const displayText =
    value.length === 0
      ? (placeholder ?? t("web.ui.select.placeholder"))
      : value.length <= 3
        ? value.map(labelOf).join(", ")
        : t("web.ui.selectedCount", { count: value.length, shown: value.length });

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
            value.length === 0 && "text-muted-foreground",
          )}
        >
          <span className="truncate">{displayText}</span>
          <ChevronsUpDown className="ms-2 h-4 w-4 shrink-0 opacity-50" />
        </button>
      </PopoverPrimitive.Trigger>

      <PopoverPrimitive.Portal container={portalContainer ?? undefined}>
        <PopoverPrimitive.Content
          align="start"
          sideOffset={4}
          collisionPadding={8}
          className="bg-popover text-popover-foreground z-50 w-[var(--radix-popover-trigger-width)] rounded-md border shadow-md"
          onOpenAutoFocus={(e) => e.preventDefault()}
        >
          <div className="p-2">
            <input
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                setFocusedIndex(-1);
              }}
              onKeyDown={handleSearchKeyDown}
              placeholder={t("web.ui.select.searchPlaceholder")}
              aria-label={t("web.ui.select.searchOptions")}
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
            aria-multiselectable="true"
            className="max-h-60 overflow-y-auto overscroll-contain p-1"
          >
            {isLoading ? (
              <p className="text-muted-foreground py-4 text-center text-sm">
                {t("web.ui.loading")}
              </p>
            ) : filtered.length === 0 ? (
              <p className="text-muted-foreground py-4 text-center text-sm">
                {t("web.ui.select.noResults")}
              </p>
            ) : (
              <>
                {value.length > 0 ? (
                  <button
                    id={`${listboxId}-opt-0`}
                    type="button"
                    role="option"
                    aria-selected={false}
                    className={cn(
                      "hover:bg-accent hover:text-accent-foreground relative flex w-full cursor-pointer items-center rounded-sm px-2 py-1.5 text-sm select-none",
                      focusedIndex === 0 && "bg-accent text-accent-foreground",
                    )}
                    onClick={() => onChange([])}
                  >
                    <Check className="me-2 h-4 w-4 shrink-0 opacity-0" />
                    <span className="text-muted-foreground italic">
                      {t("web.ui.select.clearAll")}
                    </span>
                  </button>
                ) : null}

                {filtered.map((opt, i) => {
                  const optIndex = hasClear ? i + 1 : i;
                  return (
                    <button
                      id={`${listboxId}-opt-${optIndex}`}
                      type="button"
                      role="option"
                      aria-selected={selected.has(opt)}
                      key={opt}
                      className={cn(
                        "hover:bg-accent hover:text-accent-foreground relative flex w-full cursor-pointer items-center rounded-sm px-2 py-1.5 text-sm select-none",
                        selected.has(opt) && "font-medium",
                        focusedIndex === optIndex && "bg-accent text-accent-foreground",
                      )}
                      onClick={() => toggle(opt)}
                    >
                      <Check
                        className={cn(
                          "me-2 h-4 w-4 shrink-0",
                          selected.has(opt) ? "opacity-100" : "opacity-0",
                        )}
                      />
                      {labelOf(opt)}
                    </button>
                  );
                })}
              </>
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}
