import { useState, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";
import "@/styles/admin-settings.css";

const STORAGE_PREFIX = "silo.admin.advanced.";

function storageKey(id: string) {
  return `${STORAGE_PREFIX}${id}`;
}

function readPersisted(id: string): boolean | null {
  try {
    const raw = localStorage.getItem(storageKey(id));
    if (raw === "true") return true;
    if (raw === "false") return false;
    return null;
  } catch {
    return null;
  }
}

function writePersisted(id: string, open: boolean): void {
  try {
    localStorage.setItem(storageKey(id), open ? "true" : "false");
  } catch {
    // Storage full or unavailable: the disclosure still works this session.
  }
}

export interface AdvancedSectionProps {
  /** Stable id for the persisted open state, e.g. `playback.transcoding`. */
  id: string;
  /** Number of settings inside, rendered as a muted count on the right. */
  count?: number;
  title?: string;
  /** Open state used when nothing is persisted yet. */
  defaultOpen?: boolean;
  /**
   * Forces the section open regardless of the persisted state — pass the
   * section's dirty/invalid/search-match state so a hidden field can never be
   * the reason a save bar refuses to save.
   */
  forceOpen?: boolean;
  children: ReactNode;
}

/**
 * The single disclosure primitive for advanced admin settings: an inline row at
 * the end of a group rather than a nested card. Collapsed by default, remembers
 * the admin's choice per section in localStorage, and auto-expands while
 * `forceOpen` is set.
 */
export function AdvancedSection({
  id,
  count,
  title = "Advanced",
  defaultOpen = false,
  forceOpen = false,
  children,
}: AdvancedSectionProps) {
  // Persisted choice, read once: a section's id is fixed for the life of the
  // instance (give the component a `key` if a caller ever swaps ids).
  const [persistedOpen, setPersistedOpen] = useState(() => readPersisted(id) ?? defaultOpen);
  // Explicit toggle this session, which also wins over `forceOpen` so an
  // auto-expanded section can still be collapsed.
  const [override, setOverride] = useState<boolean | null>(null);
  const [wasForcedOpen, setWasForcedOpen] = useState(forceOpen);

  // A manual collapse only outranks the *current* reason to force the section
  // open. When a new one arrives (a field inside just went dirty or invalid, or
  // a search started matching), drop the override so the save bar can never
  // block on a field the admin cannot see. Adjusting state during render is
  // cheaper than an effect: React re-renders before committing.
  if (forceOpen !== wasForcedOpen) {
    setWasForcedOpen(forceOpen);
    if (forceOpen) setOverride(null);
  }

  const open = override ?? (persistedOpen || forceOpen);

  function toggle() {
    const next = !open;
    setOverride(next);
    setPersistedOpen(next);
    writePersisted(id, next);
  }

  // The number reads as a bare count on screen; the accessible name spells out
  // what it counts, since it is visually pushed to the right of the row.
  const accessibleLabel =
    typeof count === "number" ? `${title} · ${count} setting${count === 1 ? "" : "s"}` : title;

  return (
    <div className="min-w-0">
      <button
        type="button"
        aria-label={accessibleLabel}
        aria-expanded={open}
        onClick={toggle}
        className={cn(
          "text-muted-foreground hover:text-foreground flex w-full items-center gap-2.5",
          "py-3 text-left text-[13px] transition-colors",
          open && "text-foreground",
        )}
      >
        <ChevronRight
          className={cn(
            "size-3.5 shrink-0 transition-transform",
            open ? "rotate-90 text-[var(--settings-accent)]" : "text-muted-foreground",
          )}
          aria-hidden="true"
        />
        <span className="min-w-0 font-medium">{title}</span>
        {typeof count === "number" ? (
          <span aria-hidden="true" className="text-muted-foreground ml-auto shrink-0 text-[11.5px]">
            {count}
          </span>
        ) : null}
      </button>
      {open ? (
        <div
          className={cn(
            "ml-[6px] border-l-2 border-[var(--settings-accent-line)] pl-[15px]",
            "settings-field-list",
          )}
        >
          {children}
        </div>
      ) : null}
    </div>
  );
}
