import type { ReactNode } from "react";
import { ChevronDown } from "lucide-react";

import { cn } from "@/lib/utils";

const DISCLOSURE =
  "text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 inline-flex items-start gap-1 rounded-sm text-start text-xs outline-none focus-visible:ring-[3px]";

/**
 * A text button that opens or closes a region it names. The chevron sits in a
 * box one line tall, so it stays on the first line when the label wraps.
 */
export function Disclosure({
  open,
  onOpenChange,
  controls,
  children,
  className,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  controls: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={() => onOpenChange(!open)}
      aria-expanded={open}
      aria-controls={controls}
      className={cn(DISCLOSURE, className)}
    >
      <span className="flex h-lh shrink-0 items-center">
        <ChevronDown
          aria-hidden="true"
          className={cn("size-3.5 motion-safe:transition-transform", open && "rotate-180")}
        />
      </span>
      {children}
    </button>
  );
}

/** A row's chevron icon button that opens or closes the row's details. */
export function RowDisclosure({
  open,
  onOpenChange,
  controls,
  label,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  controls: string;
  /** The accessible name, which says what opening shows. */
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={() => onOpenChange(!open)}
      aria-expanded={open}
      aria-controls={controls}
      aria-label={label}
      className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 grid size-7 shrink-0 place-items-center rounded-md outline-none focus-visible:ring-[3px]"
    >
      <ChevronDown
        aria-hidden="true"
        className={cn("size-4 motion-safe:transition-transform", open && "rotate-180")}
      />
    </button>
  );
}
