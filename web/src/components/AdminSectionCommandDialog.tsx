import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Search } from "lucide-react";
import { VisuallyHidden } from "radix-ui";
import { useNavigate } from "react-router";

import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import {
  countSettingsSearchItems,
  filterSettingsSearchEntries,
  filterSettingsSearchGroups,
} from "@/components/settings/settingsSearch";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import type { AdminNavGroup, AdminNavItem } from "@/lib/adminNavigation";
import { cn } from "@/lib/utils";

interface AdminSectionCommandDialogProps {
  sections: readonly AdminNavGroup[];
}

export function AdminSectionCommandDialog({ sections }: AdminSectionCommandDialogProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const navigate = useNavigate();

  const filteredSections = useMemo(
    () => filterSettingsSearchGroups(sections, query),
    [query, sections],
  );
  const results = useMemo(
    () => filteredSections.flatMap((section) => section.items),
    [filteredSections],
  );
  const resultIndexByHref = useMemo(
    () => new Map(results.map((item, index) => [item.href, index])),
    [results],
  );
  const totalCount = countSettingsSearchItems(sections);
  const resultCount = results.length;
  const selectedResult = selectedIndex >= 0 ? results[selectedIndex] : undefined;

  const focusSearch = useCallback(() => {
    const focus = () => {
      inputRef.current?.focus();
      inputRef.current?.select();
    };
    if (typeof window.requestAnimationFrame === "function") {
      window.requestAnimationFrame(focus);
      return;
    }
    window.setTimeout(focus, 0);
  }, []);

  const closeDialog = useCallback(() => {
    setOpen(false);
    setQuery("");
    setSelectedIndex(0);
  }, []);

  const openDialog = useCallback(() => {
    setOpen(true);
    focusSearch();
  }, [focusSearch]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || !(event.metaKey || event.ctrlKey)) return;
      if (event.key.toLowerCase() !== "k") return;

      event.preventDefault();
      event.stopPropagation();
      event.stopImmediatePropagation();
      openDialog();
    };

    window.addEventListener("keydown", onKeyDown, { capture: true });
    return () => window.removeEventListener("keydown", onKeyDown, { capture: true });
  }, [openDialog]);

  const pickResult = useCallback(
    (item: AdminNavItem) => {
      closeDialog();
      if (item.external) {
        void navigateToPluginRoute(item.href);
        return;
      }
      navigate(item.href);
    },
    [closeDialog, navigate],
  );

  function handleInputKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setSelectedIndex((current) => (resultCount === 0 ? -1 : (current + 1) % resultCount));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setSelectedIndex((current) =>
        resultCount === 0 ? -1 : current <= 0 ? resultCount - 1 : current - 1,
      );
    } else if (event.key === "Enter") {
      if (!selectedResult) return;
      event.preventDefault();
      pickResult(selectedResult);
    } else if (event.key === "Escape") {
      event.preventDefault();
      closeDialog();
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (nextOpen) {
          openDialog();
          return;
        }
        closeDialog();
      }}
    >
      <DialogContent
        className="top-[18%] max-h-[min(34rem,calc(100dvh-4rem))] translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-xl"
        showCloseButton={false}
      >
        <VisuallyHidden.Root>
          <DialogTitle>Search admin sections</DialogTitle>
          <DialogDescription>Search and open admin sections.</DialogDescription>
        </VisuallyHidden.Root>
        <div className="border-border flex h-12 items-center border-b px-4">
          <Search className="text-muted-foreground mr-3 h-4 w-4 shrink-0" aria-hidden="true" />
          <input
            ref={inputRef}
            type="search"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setSelectedIndex(0);
            }}
            onKeyDown={handleInputKeyDown}
            placeholder="Search admin sections..."
            aria-label="Search admin sections"
            aria-activedescendant={selectedResult ? resultId(selectedResult.href) : undefined}
            className="placeholder:text-muted-foreground h-full min-w-0 flex-1 bg-transparent text-sm outline-none"
            autoComplete="off"
            autoFocus
          />
          <kbd className="bg-muted text-muted-foreground pointer-events-none ml-3 hidden rounded border px-1.5 py-0.5 text-[10px] font-medium select-none sm:inline-flex">
            ESC
          </kbd>
        </div>

        <div className="max-h-[min(25rem,58vh)] overflow-y-auto overscroll-contain p-2">
          {filteredSections.length > 0 ? (
            <div role="listbox" aria-label="Admin sections" className="space-y-3">
              {filteredSections.map((section) => (
                <div key={section.label}>
                  <div className="text-muted-foreground px-2 pb-1 text-xs font-medium">
                    {section.label}
                  </div>
                  <div className="space-y-1">
                    {section.items.map((item) => {
                      const index = resultIndexByHref.get(item.href) ?? -1;
                      return (
                        <AdminCommandResultRow
                          key={item.href}
                          item={item}
                          query={query}
                          selected={index === selectedIndex}
                          onMouseEnter={() => setSelectedIndex(index)}
                          onPick={() => pickResult(item)}
                        />
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-muted-foreground px-3 py-6 text-center text-sm">
              No matching admin sections
            </p>
          )}
        </div>

        <div className="text-muted-foreground border-border border-t px-4 py-2 text-xs">
          {query.trim()
            ? `${resultCount} ${resultCount === 1 ? "match" : "matches"}`
            : `${totalCount} admin sections`}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function AdminCommandResultRow({
  item,
  query,
  selected,
  onMouseEnter,
  onPick,
}: {
  item: AdminNavItem;
  query: string;
  selected: boolean;
  onMouseEnter: () => void;
  onPick: () => void;
}) {
  const Icon = item.icon;
  const matchingSettings = filterSettingsSearchEntries(item.settings, query).slice(0, 3);

  return (
    <button
      id={resultId(item.href)}
      type="button"
      role="option"
      aria-selected={selected}
      data-selected={selected || undefined}
      onMouseEnter={onMouseEnter}
      onClick={onPick}
      className={cn(
        "focus-visible:ring-ring/50 flex w-full items-start gap-3 rounded-md px-3 py-2 text-left transition-colors focus-visible:ring-[3px] focus-visible:outline-none",
        selected ? "bg-accent text-accent-foreground" : "hover:bg-accent/70",
      )}
    >
      <span className="text-muted-foreground mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center">
        <Icon className="h-4 w-4" aria-hidden="true" />
      </span>
      <span className="min-w-0">
        <span className="text-foreground block text-sm font-medium">{item.label}</span>
        {item.description ? (
          <span className="text-muted-foreground mt-0.5 block text-xs leading-relaxed">
            {item.description}
          </span>
        ) : null}
        {matchingSettings.length > 0 ? (
          <span className="text-muted-foreground mt-1 block text-xs">
            {matchingSettings.map((setting) => setting.label).join(", ")}
          </span>
        ) : null}
      </span>
    </button>
  );
}

function resultId(href: string) {
  return `admin-command-${href.replace(/[^a-z0-9]+/gi, "-")}`;
}
