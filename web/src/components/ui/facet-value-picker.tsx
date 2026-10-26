import * as React from "react";
import { Popover as PopoverPrimitive } from "radix-ui";
import { Check, ChevronsUpDown } from "lucide-react";

import type { CatalogFacetName } from "@/hooks/queries/catalog";
import { useFacetValues, type FacetValueScope } from "@/hooks/queries/facetValues";
import { useDebounce } from "@/hooks/useDebounce";
import { cn } from "@/lib/utils";
import { PortalContainerContext } from "./portal-container-context";

interface FacetValuePickerProps {
  facet: CatalogFacetName;
  value: string;
  onChange: (value: string) => void;
  /** Where to look for values; none searches all of the viewer's titles. */
  scope?: FacetValueScope;
  /** The trigger's text while no value is picked: "Pick a studio". */
  placeholder: string;
  /** Names the trigger. */
  label?: string;
  /** Names the search box: "Search studios". */
  searchLabel: string;
  className?: string;
}

type PickerOption =
  | { kind: "value"; value: string; count?: number }
  | { kind: "typed"; value: string };

const titleCount = new Intl.NumberFormat();

/**
 * A value for an exact-match rule, picked from the values titles carry. It
 * opens on the scope's most common values with their title counts, and typing
 * searches them. Typed text no title has yet can still be used, with a note
 * saying so; a saved value missing from the list stays pinned first.
 *
 * The popover is modal, so it scrolls and takes focus inside a dialog too, and
 * it portals into a PortalContainerContext element when one is provided.
 */
export function FacetValuePicker({
  facet,
  value,
  onChange,
  scope,
  placeholder,
  label = "Value",
  searchLabel,
  className,
}: FacetValuePickerProps) {
  const [open, setOpen] = React.useState(false);
  const [search, setSearch] = React.useState("");
  const listboxId = React.useId();
  const portalContainer = React.useContext(PortalContainerContext);

  function setOpenState(next: boolean) {
    setOpen(next);
    if (!next) setSearch("");
  }

  function pick(next: string) {
    onChange(next);
    setOpenState(false);
  }

  return (
    <PopoverPrimitive.Root open={open} onOpenChange={setOpenState} modal>
      <PopoverPrimitive.Trigger asChild>
        <button
          type="button"
          role="combobox"
          aria-label={label}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-controls={open ? listboxId : undefined}
          className={cn(
            "border-border bg-background hover:bg-accent focus-visible:border-ring focus-visible:ring-ring/50 flex h-9 min-w-0 items-center justify-between gap-2 rounded-md border px-3 text-left text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px]",
            className,
          )}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              setOpen(true);
            } else if (
              event.key.length === 1 &&
              event.key !== " " &&
              !event.ctrlKey &&
              !event.metaKey &&
              !event.altKey
            ) {
              // Typing on the closed picker starts a search with that letter.
              event.preventDefault();
              setSearch(event.key);
              setOpen(true);
            }
          }}
        >
          <span className={cn("truncate", !value && "text-muted-foreground")}>
            {value || placeholder}
          </span>
          <ChevronsUpDown aria-hidden className="size-4 shrink-0 opacity-50" />
        </button>
      </PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal container={portalContainer ?? undefined}>
        <PopoverPrimitive.Content
          align="start"
          sideOffset={4}
          collisionPadding={8}
          className="border-border bg-popover text-popover-foreground z-50 w-[min(max(var(--radix-popover-trigger-width),18rem),calc(100vw_-_1rem))] rounded-md border shadow-md"
        >
          <FacetValueList
            facet={facet}
            value={value}
            scope={scope}
            search={search}
            onSearchChange={setSearch}
            onPick={pick}
            onDismiss={() => setOpenState(false)}
            listboxId={listboxId}
            searchLabel={searchLabel}
          />
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}

/** The search box and list; mounted only while the picker is open, so a closed one asks nothing. */
function FacetValueList({
  facet,
  value,
  scope,
  search,
  onSearchChange,
  onPick,
  onDismiss,
  listboxId,
  searchLabel,
}: {
  facet: CatalogFacetName;
  value: string;
  scope: FacetValueScope | undefined;
  search: string;
  onSearchChange: (search: string) => void;
  onPick: (value: string) => void;
  onDismiss: () => void;
  listboxId: string;
  searchLabel: string;
}) {
  const typed = search.trim();
  const debounced = useDebounce(typed, 150);
  const result = useFacetValues(facet, debounced, scope ?? {});
  const data = result.data;
  // The list on screen answers what is typed now, not an earlier search.
  const current = data !== undefined && !result.isPlaceholderData && debounced === typed;

  const options = React.useMemo(() => {
    const list: PickerOption[] = [];
    const values = data?.values ?? [];
    const lowered = typed.toLowerCase();
    if (
      value &&
      !values.some((entry) => entry.value === value) &&
      value.toLowerCase().includes(lowered)
    ) {
      list.push({ kind: "value", value });
    }
    for (const entry of values) list.push({ kind: "value", ...entry });
    // Rules match exactly, so typed text that isn't a listed value is offered as itself.
    if (typed && !list.some((option) => option.value === typed)) {
      list.push({ kind: "typed", value: typed });
    }
    return list;
  }, [data, typed, value]);

  // A new list starts at its first option, so Enter picks the best match.
  const [highlight, setHighlight] = React.useState({ options, index: 0 });
  const active = highlight.options === options ? highlight.index : 0;
  const setActive = (index: number) => setHighlight({ options, index });
  const activeId = options[active] ? `${listboxId}-${active}` : undefined;
  React.useEffect(() => {
    if (activeId) document.getElementById(activeId)?.scrollIntoView?.({ block: "nearest" });
  }, [activeId]);

  function handleKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    const last = options.length - 1;
    const moves: Record<string, () => number> = {
      ArrowDown: () => Math.min(active + 1, last),
      ArrowUp: () => Math.max(active - 1, 0),
      Home: () => 0,
      End: () => last,
    };
    const move = moves[event.key];
    if (move && last >= 0) {
      event.preventDefault();
      setActive(move());
    } else if (event.key === "Enter") {
      event.preventDefault();
      // Until the list answers what is typed, its first option belongs to an
      // earlier search, so Enter uses the typed text unless an option was chosen.
      const chosen = current || highlight.options === options ? options[active] : undefined;
      const next = chosen?.value ?? typed;
      if (next) onPick(next);
    } else if (event.key === "Tab") {
      event.preventDefault();
      onDismiss();
    }
  }

  let status: string | null = null;
  if (data === null) status = "Type to search";
  else if (data === undefined) status = result.isError ? "Couldn’t load values" : "Loading…";
  else if (current && data.values.length === 0) status = typed ? "No matches" : "Nothing here yet";

  return (
    <>
      <div className="border-border border-b p-2">
        <input
          role="combobox"
          aria-label={searchLabel}
          aria-expanded
          aria-controls={listboxId}
          aria-autocomplete="list"
          aria-activedescendant={activeId}
          autoFocus
          value={search}
          placeholder="Search…"
          onChange={(event) => onSearchChange(event.target.value)}
          onKeyDown={handleKeyDown}
          className="placeholder:text-muted-foreground h-8 w-full bg-transparent px-1 text-sm outline-none"
        />
      </div>
      {status ? (
        <p role="status" className="text-muted-foreground px-3 pt-3 pb-1 text-sm">
          {status}
        </p>
      ) : null}
      <div
        id={listboxId}
        role="listbox"
        aria-label={searchLabel}
        aria-busy={!current && data !== null}
        className="max-h-64 overflow-y-auto overscroll-contain p-1"
      >
        {options.map((option, index) => {
          const selected = option.kind === "value" && option.value === value;
          const count = option.kind === "value" ? option.count : undefined;
          return (
            <div
              key={`${option.kind}:${option.value}`}
              id={`${listboxId}-${index}`}
              role="option"
              aria-selected={selected}
              aria-label={
                count === undefined
                  ? undefined
                  : `${option.value}, ${titleCount.format(count)} ${count === 1 ? "title" : "titles"}`
              }
              className={cn(
                "flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm select-none",
                index === active && "bg-accent text-accent-foreground",
              )}
              onMouseDown={(event) => event.preventDefault()}
              onMouseMove={() => setActive(index)}
              onClick={() => onPick(option.value)}
            >
              <Check
                aria-hidden
                className={cn("size-4 shrink-0", selected ? "opacity-100" : "opacity-0")}
              />
              {option.kind === "typed" ? (
                <span className="min-w-0 flex-1">
                  <span className="block truncate">Use “{option.value}”</span>
                  {current && !data?.hasMore ? (
                    <span className="text-muted-foreground block text-xs">
                      No titles have this yet
                    </span>
                  ) : null}
                </span>
              ) : (
                <>
                  <span className="min-w-0 flex-1 truncate">{option.value}</span>
                  {count !== undefined ? (
                    <span className="text-muted-foreground shrink-0 text-xs tabular-nums">
                      {titleCount.format(count)}
                    </span>
                  ) : null}
                </>
              )}
            </div>
          );
        })}
      </div>
      {data?.hasMore ? (
        <p className="text-muted-foreground border-border border-t px-3 py-2 text-xs">
          Keep typing to narrow
        </p>
      ) : null}
    </>
  );
}
