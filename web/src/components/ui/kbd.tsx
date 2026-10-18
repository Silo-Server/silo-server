import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

/** A keyboard key chip, such as ESC or ↵ in a shortcut hint. */
export function Kbd({ className, ...props }: ComponentProps<"kbd">) {
  return (
    <kbd
      className={cn(
        "bg-muted text-muted-foreground pointer-events-none inline-flex items-center rounded border px-1.5 py-0.5 text-[0.625rem] font-medium select-none",
        className,
      )}
      {...props}
    />
  );
}

/**
 * Marks what Enter acts on in a list with virtual focus, such as the row a
 * search combobox has selected. Hidden below the sm breakpoint, where a
 * keyboard is unlikely, and from assistive tech, since the selected option is
 * already announced.
 */
export function EnterKeyHint({ className }: { className?: string }) {
  return (
    <Kbd aria-hidden className={cn("hidden shrink-0 sm:inline-flex", className)}>
      ↵
    </Kbd>
  );
}
